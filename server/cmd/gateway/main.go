// 模型网关入口(镜像 apps/gateway/src/index.ts):
// 内存模式为「无共享数据库,仅本地冒烟(无可用凭据)」——与 TS 一致,不灌演示数据。
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/gwapp"
	"github.com/vam12375/shucode-platform/server/internal/platformenv"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func main() {
	platformenv.LoadEnvFilesUpwards(".")
	logger := platformenv.NewLogger("gateway")

	env, err := gwapp.LoadEnv()
	if err != nil {
		logger.Error("启动失败", "error", err)
		os.Exit(1)
	}

	ctx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	var platformStore store.PlatformStore
	if env.MemoryDB {
		logger.Warn("MEMORY_DB 模式：无共享数据库，仅用于本地冒烟（无可用凭据）")
		mem, err := store.NewMemoryStore(ctx, store.MemoryOptions{})
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		platformStore = mem
	} else {
		pg, err := store.NewPostgres(ctx, env.DatabaseURL)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		platformStore = pg
	}
	if env.UpstreamAPIKey == "" && env.DatabaseURL != "" {
		logger.Warn("未配置 UPSTREAM_API_KEY，未绑定供应商的请求转发到上游会返回 401")
	}

	var secretBox *crypto.SecretBox
	if env.PlatformSecretKey != "" {
		secretBox, err = crypto.NewSecretBox(env.PlatformSecretKey)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
	}

	var redisClient *redis.Client
	if env.RedisURL != "" {
		options, err := redis.ParseURL(env.RedisURL)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		redisClient = redis.NewClient(options)
		logger.Info("已启用 Redis 共享限流")
	}

	deps := gwapp.Deps{
		Store:       platformStore,
		Env:         env,
		Now:         time.Now,
		Logger:      logger,
		SecretBox:   secretBox,
		Trusted:     gwapp.NewTrustedProxies(env.TrustedProxyIPs),
		RateLimiter: ratelimit.NewGatewayLimiter(redisClient),
		HTTPClient: &http.Client{
			// TS fetch redirect:"error":上游重定向视为连接失败,不跟随。
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("上游重定向被禁止")
			},
		},
	}
	engine := gwapp.New(deps)

	// 预留回收：进程异常/客户端长时间挂起时，避免 reserved_credits 永久占用。
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				count, err := platformStore.ExpireStaleReservations(ctx, now.Add(-env.ReservationTTL))
				if err != nil {
					logger.Warn("过期预留回收失败", "error", err)
					continue
				}
				if count > 0 {
					logger.Info("已回收过期预留", "count", count)
				}
			}
		}
	}()
	gwapp.StartProviderProber(ctx, deps, env.ProviderProbeInterval)

	addr := fmt.Sprintf("%s:%d", env.Host, env.Port)
	server := &http.Server{Addr: addr, Handler: engine}
	go func() {
		logger.Info("Mendao 模型网关已启动",
			"listen", "http://"+addr,
			"upstream", env.UpstreamBaseURL,
			"memoryDb", env.MemoryDB)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("监听失败", "error", err)
			os.Exit(1)
		}
	}()

	shutdown := make(chan os.Signal, 2)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	sig := <-shutdown
	logger.Info("收到停止信号，正在退出", "signal", sig.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	stopBackground()
	if redisClient != nil {
		_ = redisClient.Close()
	}
	_ = platformStore.Dispose(shutdownCtx)
}

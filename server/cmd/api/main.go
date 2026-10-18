// 控制面 API 入口(镜像 apps/api/src/index.ts):
// 内存模式 = 演示种子 + 临时密钥;Postgres 模式启动即迁移且必须配置 PLATFORM_SECRET_KEY。
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/vam12375/shucode-platform/server/internal/apiapp"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/platformenv"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/seed"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func main() {
	migrationsDir := flag.String("migrations-dir", "packages/db/migrations", "迁移 SQL 目录")
	seedFile := flag.String("seed-file", "apps/api/seeds/shucode-builtin.seed.json", "种子内置配置 JSON")
	flag.Parse()

	platformenv.LoadEnvFilesUpwards(".")
	logger := platformenv.NewLogger("api")

	env, err := apiapp.LoadPlatformEnv()
	if err != nil {
		logger.Error("启动失败", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()
	var platformStore store.PlatformStore
	if env.MemoryDB {
		logger.Warn("MEMORY_DB 模式：数据仅存于进程内，重启丢失（仅用于本地体验）")
		mem, err := store.NewMemoryStore(ctx, store.MemoryOptions{DemoSeed: true})
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		platformStore = mem
	} else {
		conn, err := pgx.Connect(ctx, env.DatabaseURL)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		if err := store.RunMigrations(ctx, conn, *migrationsDir, func(message string) {
			logger.Info(message)
		}); err != nil {
			_ = conn.Close(ctx)
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		_ = conn.Close(ctx)
		pg, err := store.NewPostgres(ctx, env.DatabaseURL)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		platformStore = pg
	}

	var secretBox *crypto.SecretBox
	if env.PlatformSecretKey != "" {
		secretBox, err = crypto.NewSecretBox(env.PlatformSecretKey)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
	} else {
		if !env.MemoryDB {
			logger.Error("启动失败", "error", errors.New(
				"生产模式必须配置 PLATFORM_SECRET_KEY（32 字节 base64，生成方式见 .env.example）"))
			os.Exit(1)
		}
		randomKey := make([]byte, 32)
		if _, err := rand.Read(randomKey); err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		secretBox, err = crypto.NewSecretBox(base64.StdEncoding.EncodeToString(randomKey))
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		logger.Warn("未配置 PLATFORM_SECRET_KEY，已生成临时密钥（重启后旧设备 token 无法恢复）")
	}

	// 事件总线与限流:REDIS_URL 配置时用 Redis(跨实例),否则进程内(单实例语义)。
	var bus events.Bus
	var consoleLimiter, platformLimiter ratelimit.Limiter
	var redisClient *redis.Client
	window := ratelimit.Window{Window: 5 * time.Minute, Max: 10}
	if env.RedisURL != "" {
		options, err := redis.ParseURL(env.RedisURL)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		redisClient = redis.NewClient(options)
		redisBus, err := events.NewRedis(ctx, redisClient)
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		bus = redisBus
		consoleLimiter = ratelimit.NewRedis(redisClient, window)
		platformLimiter = ratelimit.NewRedis(redisClient, window)
		logger.Info("已启用 Redis 共享限流与事件总线")
	} else {
		bus = events.NewInProc()
		consoleLimiter = ratelimit.NewInProc(window)
		platformLimiter = ratelimit.NewInProc(window)
	}

	deps := &apiapp.Deps{
		Store:                platformStore,
		Env:                  env,
		Now:                  time.Now,
		SecretBox:            secretBox,
		ConfigEvents:         bus,
		ConsoleLoginLimiter:  consoleLimiter,
		PlatformLoginLimiter: platformLimiter,
		Logger:               logger,
		HTTPClient:           &http.Client{},
	}
	if err := seed.EnsureSeedRelease(ctx, platformStore, *seedFile); err != nil {
		logger.Error("种子失败", "error", err)
	}
	if err := seed.EnsureDefaultModels(ctx, platformStore); err != nil {
		logger.Error("种子失败", "error", err)
	}

	engine := apiapp.New(deps)
	addr := fmt.Sprintf("%s:%d", env.Host, env.Port)
	server := &http.Server{Addr: addr, Handler: engine}
	go func() {
		logger.Info("Mendao 平台 API 已启动",
			"listen", "http://"+addr,
			"publicApiOrigin", env.PublicAPIOrigin,
			"gatewayBaseUrl", env.GatewayBaseURL,
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
	bus.Close()
	if redisClient != nil {
		_ = redisClient.Close()
	}
	_ = platformStore.Dispose(shutdownCtx)
}

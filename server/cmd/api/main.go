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
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/vam12375/shucode-platform/server/internal/apiapp"
	"github.com/vam12375/shucode-platform/server/internal/audit"
	"github.com/vam12375/shucode-platform/server/internal/compensation"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/events"
	"github.com/vam12375/shucode-platform/server/internal/netguard"
	"github.com/vam12375/shucode-platform/server/internal/netutil"
	"github.com/vam12375/shucode-platform/server/internal/platformenv"
	"github.com/vam12375/shucode-platform/server/internal/ratelimit"
	"github.com/vam12375/shucode-platform/server/internal/seed"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/turnstile"
)

func main() {
	migrationsDir := flag.String("migrations-dir", "migrations", "迁移 SQL 目录")
	seedFile := flag.String("seed-file", "seeds/shucode-builtin.seed.json", "种子内置配置 JSON")
	flag.Parse()

	platformenv.LoadEnvFilesUpwards(".")
	logger := platformenv.NewLogger("api")

	env, err := apiapp.LoadPlatformEnv()
	if err != nil {
		logger.Error("启动失败", "error", err)
		os.Exit(1)
	}
	if parsed, parseErr := url.Parse(env.PublicAPIOrigin); parseErr == nil && parsed.Scheme == "http" {
		switch parsed.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			logger.Warn("PUBLIC_API_ORIGIN 使用 HTTP：管理员口令、会话 cookie 与录入的上游 key 可能被链路攻击者读取；请尽快启用 HTTPS")
		}
	}
	if env.TurnstileSiteKey == "" {
		logger.Warn("未配置 TURNSTILE_SITE_KEY/TURNSTILE_SECRET_KEY，登录页不启用人机验证")
	}
	if env.RootMFARequired {
		logger.Info("已启用 ROOT_MFA_REQUIRED：root 未绑定 TOTP 时仅能访问安全设置")
	}
	if !env.MemoryDB && strings.HasPrefix(env.PublicAPIOrigin, "https:") && len(env.TrustedProxyIPs) == 0 {
		logger.Warn("API_TRUSTED_PROXY_IPS 未配置：经 Nginx 回源时所有请求可能折叠到 Docker 网桥 IP，登录限流分桶会失效")
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
	var consoleLimiter, platformLimiter, loginIPLimiter, loginIPBurstLimiter, rootLoginLimiter, modelProbeLimiter, publicReadLimiter ratelimit.Limiter
	var redisClient *redis.Client
	window := ratelimit.Window{Window: 5 * time.Minute, Max: 10}
	ipWindow := ratelimit.Window{Window: 24 * time.Hour, Max: 100}
	ipBurstWindow := ratelimit.Window{Window: time.Minute, Max: 20}
	rootWindow := ratelimit.Window{Window: time.Hour, Max: 5}
	// 用户端"测试"会真实调用上游；按用户限速，既允许复查延迟又避免持续成本。
	probeWindow := ratelimit.Window{Window: 10 * time.Minute, Max: 30}
	// setup/status、public-config 等匿名只读端点也限速，避免被无限探测。
	publicReadWindow := ratelimit.Window{Window: time.Minute, Max: 120}
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
		loginIPLimiter = ratelimit.NewRedis(redisClient, ipWindow)
		loginIPBurstLimiter = ratelimit.NewRedis(redisClient, ipBurstWindow)
		rootLoginLimiter = ratelimit.NewRedis(redisClient, rootWindow)
		modelProbeLimiter = ratelimit.NewRedis(redisClient, probeWindow)
		publicReadLimiter = ratelimit.NewRedis(redisClient, publicReadWindow)
		logger.Info("已启用 Redis 共享限流与事件总线")
	} else {
		bus = events.NewInProc()
		consoleLimiter = ratelimit.NewInProc(window)
		platformLimiter = ratelimit.NewInProc(window)
		loginIPLimiter = ratelimit.NewInProc(ipWindow)
		loginIPBurstLimiter = ratelimit.NewInProc(ipBurstWindow)
		rootLoginLimiter = ratelimit.NewInProc(rootWindow)
		modelProbeLimiter = ratelimit.NewInProc(probeWindow)
		publicReadLimiter = ratelimit.NewInProc(publicReadWindow)
	}

	var turnstileVerifier apiapp.TurnstileVerifier
	if env.TurnstileSecretKey != "" {
		verifier, err := turnstile.NewClient(turnstile.Options{
			SecretKey:      env.TurnstileSecretKey,
			Endpoint:       env.TurnstileVerifyURL,
			ExpectedAction: turnstile.DefaultAction,
			HTTPClient: &http.Client{
				Timeout: 5 * time.Second,
				// siteverify 不需要重定向；禁止重定向避免 secret 被带到意外目标。
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					return errors.New("Turnstile 校验重定向被禁止")
				},
			},
		})
		if err != nil {
			logger.Error("启动失败", "error", err)
			os.Exit(1)
		}
		turnstileVerifier = verifier
	}

	deps := &apiapp.Deps{
		Store:                platformStore,
		Env:                  env,
		Now:                  time.Now,
		SecretBox:            secretBox,
		ConfigEvents:         bus,
		ConsoleLoginLimiter:  consoleLimiter,
		PlatformLoginLimiter: platformLimiter,
		LoginIPLimiter:       loginIPLimiter,
		LoginIPBurstLimiter:  loginIPBurstLimiter,
		RootLoginLimiter:     rootLoginLimiter,
		PublicReadLimiter:    publicReadLimiter,
		LoginGate:            apiapp.NewLoginGate(env.LoginMaxConcurrent),
		ModelProbeLimiter:    modelProbeLimiter,
		Turnstile:            turnstileVerifier,
		Logger:               logger,
		Trusted:              netutil.NewTrustedProxies(env.TrustedProxyIPs),
		AuditSink:            audit.NewSink(platformStore, env.AuditOutboxDir, logger),
		HTTPClient: &http.Client{
			Transport: netguard.NewTransport(env.AllowPrivateUpstreamNetworks),
			// 管理端探测同样禁止跟随重定向，避免解密后的上游密钥被带到新目标。
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("上游重定向被禁止")
			},
			Timeout: 60 * time.Second,
		},
	}
	if err := seed.EnsureSeedRelease(ctx, platformStore, *seedFile); err != nil {
		logger.Error("种子失败", "error", err)
	}
	if err := seed.EnsureDefaultModels(ctx, platformStore); err != nil {
		logger.Error("种子失败", "error", err)
	}

	runCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	deps.Media = apiapp.NewMediaService(deps)
	go deps.Media.Run(runCtx)
	apiapp.StartAlertEvaluator(runCtx, deps, time.Minute)
	apiapp.StartReservationSweeper(runCtx, deps, time.Minute)
	apiapp.StartLoginFlowSweeper(runCtx, deps, 10*time.Minute)
	compensation.NewSettlementCompensator(platformStore, env.PendingSettlementDir, logger, time.Now).
		Run(runCtx, 30*time.Second)
	deps.AuditSink.Run(runCtx, 30*time.Second)

	engine := apiapp.New(deps)
	addr := fmt.Sprintf("%s:%d", env.Host, env.Port)
	server := &http.Server{
		Addr:    addr,
		Handler: engine,
		// SSE 需要长写连接，因此不设 WriteTimeout；读取入口仍有明确上限。
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
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
	stopBackground()
	bus.Close()
	if redisClient != nil {
		_ = redisClient.Close()
	}
	_ = platformStore.Dispose(shutdownCtx)
}

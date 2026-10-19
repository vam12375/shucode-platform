package apiapp

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/netutil"
	"github.com/vam12375/shucode-platform/server/internal/platformenv"
)

// env.ts(控制面)的镜像:变量名/默认值/派生规则逐项一致。
type Env struct {
	Port                int
	Host                string
	DatabaseURL         string
	MemoryDB            bool
	PublicAPIOrigin     string
	PublicAppOrigin     string
	PlatformSecretKey   string
	CookieDomain        string
	SecureCookies       bool
	GatewayBaseURL      string
	GatewayAPIType      string // "openai-chat-completions" | "anthropic-messages"
	GatewayAPIBaseURL   string
	ReleaseBaseURL      string
	ConsoleDistDir      string
	PublicConsoleOrigin string
	RedisURL            string
	AlertWebhookURL     string
	ReservationTTL      time.Duration
	// TrustedProxyIPs:只有 TCP 对端命中这些精确地址时才解析转发头。
	TrustedProxyIPs []string
	// BootstrapSecret:首次 root 初始化的一次性口令；生产模式未配置时拒绝初始化。
	BootstrapSecret string
	// AllowInsecureUpstreamURLs:仅本地开发/测试允许供应商地址使用 http。
	AllowInsecureUpstreamURLs bool
	// AllowPrivateUpstreamNetworks:允许出站访问私网/回环目标（自建内网上游）。
	AllowPrivateUpstreamNetworks bool
	// TrustCFConnectingIP:仅在可信代理就是 Cloudflare 边缘/cloudflared 且源站
	// 无法被直连时启用；否则优先使用经可信代理追加的 XFF。
	TrustCFConnectingIP bool
	// DeviceTokenTTL:设备凭据最长寿命；0 = 不按年龄过期（仍可吊销）。
	DeviceTokenTTL time.Duration
	// ConsoleAllowedHosts:非空时 /console 仅接受这些 Host（配合边缘 Access 按域名隔离管理面）。
	ConsoleAllowedHosts []string
}

func LoadPlatformEnv() (*Env, error) {
	port, err := strconv.Atoi(platformenv.EnvOr("PORT", "8787"))
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("PORT 非法: %s", os.Getenv("PORT"))
	}
	memoryDB := platformenv.Truthy(os.Getenv("MEMORY_DB"))
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if !memoryDB && databaseURL == "" {
		return nil, fmt.Errorf("必须提供 DATABASE_URL，或设置 MEMORY_DB=1 进入本地体验模式")
	}

	publicAPIOrigin, err := normalizeOrigin(platformenv.EnvOr("PUBLIC_API_ORIGIN", fmt.Sprintf("http://localhost:%d", port)))
	if err != nil {
		return nil, fmt.Errorf("PUBLIC_API_ORIGIN 非法: %v", err)
	}
	publicAppOrigin, err := normalizeOrigin(platformenv.EnvOr("PUBLIC_APP_ORIGIN", publicAPIOrigin))
	if err != nil {
		return nil, fmt.Errorf("PUBLIC_APP_ORIGIN 非法: %v", err)
	}
	gatewayBaseURL := strings.TrimRight(platformenv.EnvOr("GATEWAY_BASE_URL", "https://gw.mendao.chat"), "/")
	gatewayAPIType := "openai-chat-completions"
	if strings.TrimSpace(os.Getenv("GATEWAY_API_TYPE")) == "anthropic-messages" {
		gatewayAPIType = "anthropic-messages"
	}
	gatewayAPIBaseURL := gatewayBaseURL
	if gatewayAPIType == "openai-chat-completions" {
		gatewayAPIBaseURL = gatewayBaseURL + "/v1"
	}
	reservationTTLSeconds, err := strconv.Atoi(platformenv.EnvOr("GATEWAY_RESERVATION_TTL_SEC", "1800"))
	if err != nil || reservationTTLSeconds <= 0 {
		return nil, fmt.Errorf("GATEWAY_RESERVATION_TTL_SEC 非法")
	}
	trustedProxies, err := netutil.ParseTrustedProxyIPsNamed(
		os.Getenv("API_TRUSTED_PROXY_IPS"), "API_TRUSTED_PROXY_IPS")
	if err != nil {
		return nil, err
	}
	publicConsoleOrigin := strings.TrimSpace(os.Getenv("PUBLIC_CONSOLE_ORIGIN"))
	if publicConsoleOrigin != "" {
		publicConsoleOrigin, err = normalizeOrigin(publicConsoleOrigin)
		if err != nil {
			return nil, fmt.Errorf("PUBLIC_CONSOLE_ORIGIN 非法: %v", err)
		}
	}
	var consoleAllowedHosts []string
	for _, raw := range strings.Split(os.Getenv("CONSOLE_ALLOWED_HOSTS"), ",") {
		if host := strings.ToLower(strings.TrimSpace(raw)); host != "" {
			consoleAllowedHosts = append(consoleAllowedHosts, host)
		}
	}
	deviceTokenTTLDays, err := strconv.Atoi(platformenv.EnvOr("DEVICE_TOKEN_TTL_DAYS", "0"))
	if err != nil || deviceTokenTTLDays < 0 {
		return nil, fmt.Errorf("DEVICE_TOKEN_TTL_DAYS 非法")
	}
	return &Env{
		Port:                port,
		Host:                platformenv.EnvOr("HOST", "0.0.0.0"),
		DatabaseURL:         databaseURL,
		MemoryDB:            memoryDB,
		PublicAPIOrigin:     publicAPIOrigin,
		PublicAppOrigin:     publicAppOrigin,
		PlatformSecretKey:   strings.TrimSpace(os.Getenv("PLATFORM_SECRET_KEY")),
		CookieDomain:        strings.TrimSpace(os.Getenv("COOKIE_DOMAIN")),
		SecureCookies:       strings.HasPrefix(publicAPIOrigin, "https:"),
		GatewayBaseURL:      gatewayBaseURL,
		GatewayAPIType:      gatewayAPIType,
		GatewayAPIBaseURL:   gatewayAPIBaseURL,
		ReleaseBaseURL:      strings.TrimRight(platformenv.EnvOr("RELEASE_BASE_URL", publicAPIOrigin), "/"),
		ConsoleDistDir:      strings.TrimSpace(os.Getenv("CONSOLE_DIST_DIR")),
		PublicConsoleOrigin: publicConsoleOrigin,
		RedisURL:            strings.TrimSpace(os.Getenv("REDIS_URL")),
		AlertWebhookURL:     strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_URL")),
		ReservationTTL:      time.Duration(reservationTTLSeconds) * time.Second,
		TrustedProxyIPs:     trustedProxies,
		BootstrapSecret:     strings.TrimSpace(os.Getenv("BOOTSTRAP_SECRET")),
		AllowInsecureUpstreamURLs: platformenv.Truthy(
			os.Getenv("ALLOW_INSECURE_UPSTREAM_URLS")),
		AllowPrivateUpstreamNetworks: platformenv.Truthy(
			os.Getenv("UPSTREAM_ALLOW_PRIVATE_NETWORKS")),
		TrustCFConnectingIP: platformenv.Truthy(os.Getenv("TRUST_CF_CONNECTING_IP")),
		ConsoleAllowedHosts: consoleAllowedHosts,
		DeviceTokenTTL:      time.Duration(deviceTokenTTLDays) * 24 * time.Hour,
	}, nil
}

// normalizeOrigin:等价 new URL(value).origin(scheme + host[:port],丢弃路径等)。
func normalizeOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("不是合法的绝对地址")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("只支持 http/https origin")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("origin 不允许包含 userinfo")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

package apiapp

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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
		PublicConsoleOrigin: strings.TrimSpace(os.Getenv("PUBLIC_CONSOLE_ORIGIN")),
		RedisURL:            strings.TrimSpace(os.Getenv("REDIS_URL")),
		AlertWebhookURL:     strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_URL")),
		ReservationTTL:      time.Duration(reservationTTLSeconds) * time.Second,
	}, nil
}

// normalizeOrigin:等价 new URL(value).origin(scheme + host[:port],丢弃路径等)。
func normalizeOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("不是合法的绝对地址")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

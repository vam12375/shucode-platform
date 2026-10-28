package gwapp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/platformenv"
)

// env.ts 的镜像:变量名/默认值/校验逐项一致。
type Env struct {
	Port            int
	Host            string
	TrustedProxyIPs []string
	DatabaseURL     string
	MemoryDB        bool
	UpstreamBaseURL string
	UpstreamAPIKey  string
	// DeepSeekTools* 为网页搜索/抓取的独立上游与按次价格；API key 只从运行时环境读取。
	DeepSeekToolsBaseURL         string
	DeepSeekToolsAPIKey          string
	DeepSeekSearchFeeMicro       int64
	DeepSeekFetchFeeMicro        int64
	AnthropicVersion             string
	UpstreamTimeoutMs            int64
	UpstreamIdleTimeoutMs        int64
	ProviderAutoDisableThreshold int64
	PlatformSecretKey            string
	RedisURL                     string
	MaxAttempts                  int64
	ReservationTTL               time.Duration
	ProviderProbeInterval        time.Duration
	// AllowPrivateUpstreamNetworks：允许出站访问私网/回环目标（自建内网上游）。
	AllowPrivateUpstreamNetworks bool
	// DeviceTokenTTL：设备凭据最长寿命；0 = 不按年龄过期（仍可吊销）。
	DeviceTokenTTL time.Duration
	// PeakHoursHolidays：北京时间法定节假日（YYYY-MM-DD），高峰窗口排除这些日期。
	PeakHoursHolidays map[string]bool
	// PendingSettlementDir：数据库不可用时结算补偿的本地 spool 目录。
	PendingSettlementDir string
}

func LoadEnv() (*Env, error) {
	portRaw := firstNonEmpty(strings.TrimSpace(os.Getenv("GATEWAY_PORT")), strings.TrimSpace(os.Getenv("PORT")), "8788")
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("GATEWAY_PORT 非法: %s", portRaw)
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	trusted, err := ParseTrustedProxyIPs(os.Getenv("GATEWAY_TRUSTED_PROXY_IPS"))
	if err != nil {
		return nil, err
	}
	timeout, err := readPositiveIntEnv("UPSTREAM_TIMEOUT_MS", 120_000)
	if err != nil {
		return nil, err
	}
	idleTimeout, err := readPositiveIntEnv("UPSTREAM_IDLE_TIMEOUT_MS", 60_000)
	if err != nil {
		return nil, err
	}
	threshold, err := readNonNegativeIntEnv("PROVIDER_AUTO_DISABLE_THRESHOLD", 3)
	if err != nil {
		return nil, err
	}
	maxAttempts, err := readPositiveIntEnv("GATEWAY_MAX_ATTEMPTS", 3)
	if err != nil {
		return nil, err
	}
	reservationTTL, err := readPositiveIntEnv("GATEWAY_RESERVATION_TTL_SEC", 1800)
	if err != nil {
		return nil, err
	}
	probeInterval, err := readNonNegativeIntEnv("PROVIDER_PROBE_INTERVAL_SEC", 120)
	if err != nil {
		return nil, err
	}
	deviceTokenTTLDays, err := readNonNegativeIntEnv("DEVICE_TOKEN_TTL_DAYS", 0)
	if err != nil {
		return nil, err
	}
	searchFee, err := readPositiveIntEnv("DEEPSEEK_SEARCH_FEE_MICRO", 5_000)
	if err != nil {
		return nil, err
	}
	fetchFee, err := readPositiveIntEnv("DEEPSEEK_FETCH_FEE_MICRO", 5_000)
	if err != nil {
		return nil, err
	}
	holidays := map[string]bool{}
	for _, raw := range strings.Split(os.Getenv("PEAK_HOURS_HOLIDAYS"), ",") {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return nil, fmt.Errorf("PEAK_HOURS_HOLIDAYS 非法日期: %s", value)
		}
		holidays[value] = true
	}
	return &Env{
		Port:                         port,
		Host:                         platformenv.EnvOr("HOST", "0.0.0.0"),
		TrustedProxyIPs:              trusted,
		DatabaseURL:                  databaseURL,
		MemoryDB:                     platformenv.Truthy(os.Getenv("MEMORY_DB")) || databaseURL == "",
		UpstreamBaseURL:              strings.TrimRight(platformenv.EnvOr("UPSTREAM_BASE_URL", "https://api.anthropic.com"), "/"),
		UpstreamAPIKey:               strings.TrimSpace(os.Getenv("UPSTREAM_API_KEY")),
		DeepSeekToolsBaseURL:         strings.TrimRight(platformenv.EnvOr("DEEPSEEK_TOOLS_BASE_URL", "https://api.deepseek.com"), "/"),
		DeepSeekToolsAPIKey:          strings.TrimSpace(os.Getenv("DEEPSEEK_TOOLS_API_KEY")),
		DeepSeekSearchFeeMicro:       searchFee,
		DeepSeekFetchFeeMicro:        fetchFee,
		AnthropicVersion:             platformenv.EnvOr("ANTHROPIC_VERSION", "2023-06-01"),
		UpstreamTimeoutMs:            timeout,
		UpstreamIdleTimeoutMs:        idleTimeout,
		ProviderAutoDisableThreshold: threshold,
		PlatformSecretKey:            strings.TrimSpace(os.Getenv("PLATFORM_SECRET_KEY")),
		RedisURL:                     strings.TrimSpace(os.Getenv("REDIS_URL")),
		MaxAttempts:                  maxAttempts,
		ReservationTTL:               time.Duration(reservationTTL) * time.Second,
		ProviderProbeInterval:        time.Duration(probeInterval) * time.Second,
		AllowPrivateUpstreamNetworks: platformenv.Truthy(os.Getenv("UPSTREAM_ALLOW_PRIVATE_NETWORKS")),
		DeviceTokenTTL:               time.Duration(deviceTokenTTLDays) * 24 * time.Hour,
		PeakHoursHolidays:            holidays,
		PendingSettlementDir:         platformenv.EnvOr("PENDING_SETTLEMENT_DIR", "./data/pending-settlements"),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func readPositiveIntEnv(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s 非法: %s", name, raw)
	}
	return parsed, nil
}

func readNonNegativeIntEnv(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s 非法: %s", name, raw)
	}
	return parsed, nil
}

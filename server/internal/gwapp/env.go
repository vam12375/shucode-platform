package gwapp

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/vam12375/shucode-platform/server/internal/platformenv"
)

// env.ts 的镜像:变量名/默认值/校验逐项一致。
type Env struct {
	Port                         int
	Host                         string
	TrustedProxyIPs              []string
	DatabaseURL                  string
	MemoryDB                     bool
	UpstreamBaseURL              string
	UpstreamAPIKey               string
	AnthropicVersion             string
	UpstreamTimeoutMs            int64
	UpstreamIdleTimeoutMs        int64
	ProviderAutoDisableThreshold int64
	PlatformSecretKey            string
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
	return &Env{
		Port:                         port,
		Host:                         platformenv.EnvOr("HOST", "0.0.0.0"),
		TrustedProxyIPs:              trusted,
		DatabaseURL:                  databaseURL,
		MemoryDB:                     platformenv.Truthy(os.Getenv("MEMORY_DB")) || databaseURL == "",
		UpstreamBaseURL:              strings.TrimRight(platformenv.EnvOr("UPSTREAM_BASE_URL", "https://api.anthropic.com"), "/"),
		UpstreamAPIKey:               strings.TrimSpace(os.Getenv("UPSTREAM_API_KEY")),
		AnthropicVersion:             platformenv.EnvOr("ANTHROPIC_VERSION", "2023-06-01"),
		UpstreamTimeoutMs:            timeout,
		UpstreamIdleTimeoutMs:        idleTimeout,
		ProviderAutoDisableThreshold: threshold,
		PlatformSecretKey:            strings.TrimSpace(os.Getenv("PLATFORM_SECRET_KEY")),
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

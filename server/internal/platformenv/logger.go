package platformenv

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger:与 TS createConsoleLogger 对齐的最小语义——带进程标签的日志输出。
// 格式非契约(不入 API 响应),采用 slog JSON;凭据永不入日志(调用方约束)。
func NewLogger(proc string) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).
		With("proc", proc)
}

// Truthy:与 TS 的 MEMORY_DB 口径一致(1/true/yes)。
func Truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// EnvOr:取环境变量,空值回落默认值(TS env 解析的常用形态)。
func EnvOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

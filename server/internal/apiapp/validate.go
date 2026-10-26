package apiapp

import (
	"strconv"
	"strings"
	"time"
)

// 校验辅助:zod 的常用组合(trim/min/max/enum/int/uuid)在此统一实现。

// parseLeadingInt 复刻 Number.parseInt 的「前缀解析」语义:允许符号与数字前缀,
// 无数字则失败("12abc" → 12 + true)。
func parseLeadingInt(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0, false
	}
	value, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseFiniteFloat(raw any) (float64, bool) {
	switch typed := raw.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// parsePriceString:zod 的 priceString(数字/字符串 → 非负 → toFixed(6))。
func parsePriceString(raw any) (string, bool) {
	numeric, ok := parseFiniteFloat(raw)
	if !ok || numeric < 0 {
		return "", false
	}
	return strconv.FormatFloat(numeric, 'f', 6, 64), true
}

func trimmedRequired(value any, min, max int) (string, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < min || len(trimmed) > max {
		return "", false
	}
	return trimmed, true
}

func trimmedOptional(value any, max int) (string, bool) {
	if value == nil {
		return "", true
	}
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) > max {
		return "", false
	}
	return trimmed, true
}

func uuidShape(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, ch := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		isHex := (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

func intInRange(value any, min, max int64) (int64, bool) {
	numeric, ok := value.(float64)
	if !ok || numeric != float64(int64(numeric)) {
		return 0, false
	}
	parsed := int64(numeric)
	if parsed < min || parsed > max {
		return 0, false
	}
	return parsed, true
}

func boolValue(value any) (bool, bool) {
	b, ok := value.(bool)
	return b, ok
}

func emailShape(value string) bool {
	// z.string().email() 的常用形态:非空本地部 + @ + 非空域名(不追求 RFC 全量)。
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return false
	}
	domain := value[at+1:]
	return strings.Contains(domain, ".") && !strings.ContainsAny(value, " \t\n")
}

// parseURLCursor:usage/audit 的 (createdAt, id) 游标,对客户端不透明。
const cursorSeparator = "~"

func encodeUsageCursor(createdAt time.Time, id string) string {
	// 展示时间沿用毫秒格式，游标必须保留数据库的完整精度；截断微秒会漏掉同毫秒内的剩余记录。
	return createdAt.UTC().Format(time.RFC3339Nano) + cursorSeparator + id
}

func decodeUsageCursor(cursor string) (string, string, bool) {
	if cursor == "" {
		return "", "", false
	}
	index := strings.LastIndex(cursor, cursorSeparator)
	if index <= 0 || index == len(cursor)-1 {
		return "", "", false
	}
	createdAt, id := cursor[:index], cursor[index+1:]
	// ISO 形状前缀 + UUID 形状;时间交给解析层严格解析。
	if len(createdAt) < 19 || createdAt[4] != '-' || createdAt[10] != 'T' || !uuidShape(id) {
		return "", "", false
	}
	return createdAt, id, true
}

// parseQueryTime:把 usage 的 from/to 解析为即时点。
// 与现有生产行为一致(naive 串经 PG UTC 会话解释):无时区后缀按 UTC 处理;
// 支持 RFC3339(带偏移)、"YYYY-MM-DDTHH:MM:SS"、"YYYY-MM-DD HH:MM:SS" 与日期串。
func parseQueryTime(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return parsed, true
	}
	layouts := []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

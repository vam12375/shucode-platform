package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// CreditLedgerPage 镜像客户端 shared/platform-credit-ledger.ts 的只读响应。
// 金额保留十进制字符串；本包不计算余额，也不拥有账本写入路径。
type CreditLedgerPage struct {
	Range      string               `json:"range"`
	Entries    []CreditLedgerEntry  `json:"entries"`
	NextCursor *string              `json:"nextCursor"`
	Summary    *CreditLedgerSummary `json:"summary"`
}

type CreditLedgerEntry struct {
	ID                    string             `json:"id"`
	CreatedAt             string             `json:"createdAt"`
	Kind                  string             `json:"kind"`
	ChangeReason          string             `json:"changeReason"`
	Direction             string             `json:"direction"`
	Status                string             `json:"status"`
	Credits               string             `json:"credits"`
	ModelName             *string            `json:"modelName"`
	ActorName             *string            `json:"actorName"`
	SessionID             *string            `json:"sessionId"`
	RequestID             *string            `json:"requestId"`
	Quantity              *int64             `json:"quantity"`
	ReasoningEffort       *string            `json:"reasoningEffort"`
	ReasoningBudgetTokens *int64             `json:"reasoningBudgetTokens"`
	Usage                 *CreditLedgerUsage `json:"usage"`
}

type CreditLedgerUsage struct {
	InputTokens         *int64 `json:"inputTokens"`
	OutputTokens        *int64 `json:"outputTokens"`
	CachedInputTokens   *int64 `json:"cachedInputTokens"`
	CacheCreationTokens *int64 `json:"cacheCreationTokens"`
	ReasoningTokens     *int64 `json:"reasoningTokens"`
}

type CreditLedgerSummary struct {
	TotalCreditsIn  string `json:"totalCreditsIn"`
	TotalCreditsOut string `json:"totalCreditsOut"`
	NetCredits      string `json:"netCredits"`
	EntryCount      int64  `json:"entryCount"`
}

var (
	creditLedgerDecimal        = regexp.MustCompile(`^(?:0|[1-9]\d{0,11})(?:\.\d{1,6})?$`)
	creditLedgerSummaryDecimal = regexp.MustCompile(`^(?:0|[1-9]\d{0,99})(?:\.\d{1,6})?$`)
	creditLedgerSignedDecimal  = regexp.MustCompile(`^-?(?:0|[1-9]\d{0,99})(?:\.\d{1,6})?$`)
	creditLedgerNonzero        = regexp.MustCompile(`[1-9]`)
	creditLedgerDateTime       = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)
)

// ParseCreditLedgerPage 校验实际 wire data，并保留历史 nullable 默认。
// 根因：Go 的普通 JSON 解码会接受字段大小写变体并把缺失视作零值，
// 与客户端 strict/required 不同；先逐层校验原始字段后才构造投影。
func ParseCreditLedgerPage(raw []byte) (CreditLedgerPage, error) {
	var out CreditLedgerPage
	fields, err := creditLedgerObject(raw, "range entries nextCursor summary", "")
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(fields["range"], &out.Range); err != nil || !creditLedgerEnum(out.Range, "all 7d 30d") {
		return out, creditLedgerInvalid("range")
	}
	if creditLedgerNull(fields["entries"]) {
		return out, creditLedgerInvalid("entries")
	}
	var entries []json.RawMessage
	if json.Unmarshal(fields["entries"], &entries) != nil {
		return out, creditLedgerInvalid("entries")
	}
	out.Entries = make([]CreditLedgerEntry, 0, len(entries))
	for _, entry := range entries {
		value, err := ParseCreditLedgerEntry(entry)
		if err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, value)
	}
	out.NextCursor, err = creditLedgerString(fields["nextCursor"], true, 1, 0, false)
	if err != nil {
		return out, creditLedgerInvalid("nextCursor")
	}
	if !creditLedgerNull(fields["summary"]) {
		summary, err := parseCreditLedgerSummary(fields["summary"])
		if err != nil {
			return out, err
		}
		out.Summary = &summary
	}
	return out, nil
}

func ParseCreditLedgerEntry(raw []byte) (CreditLedgerEntry, error) {
	var out CreditLedgerEntry
	fields, err := creditLedgerObject(raw, "id createdAt kind changeReason direction status credits modelName sessionId requestId quantity reasoningEffort usage", "actorName reasoningBudgetTokens")
	if err != nil {
		return out, err
	}
	for _, field := range []struct {
		key  string
		dest *string
	}{
		{"id", &out.ID}, {"createdAt", &out.CreatedAt}, {"kind", &out.Kind}, {"changeReason", &out.ChangeReason}, {"direction", &out.Direction}, {"status", &out.Status}, {"credits", &out.Credits},
	} {
		value, err := creditLedgerString(fields[field.key], false, 1, 0, false)
		if err != nil {
			return out, creditLedgerInvalid(field.key)
		}
		*field.dest = *value
	}
	if !creditLedgerDateTime.MatchString(out.CreatedAt) {
		return out, creditLedgerInvalid("createdAt")
	}
	if _, err := time.Parse(time.RFC3339Nano, out.CreatedAt); err != nil {
		return out, creditLedgerInvalid("createdAt")
	}
	if !creditLedgerEnum(out.Kind, "chat image video audio media search fetch analysis import topup adjustment") {
		return out, creditLedgerInvalid("kind")
	}
	if !creditLedgerEnum(out.ChangeReason, "usage initial_grant alipay_recharge manual_add quota_request_approved quota_adjustment other_adjustment") {
		return out, creditLedgerInvalid("changeReason")
	}
	if !creditLedgerEnum(out.Direction, "debit credit") || out.Status != "settled" {
		return out, creditLedgerInvalid("settlement")
	}
	if !creditLedgerDecimal.MatchString(out.Credits) || !creditLedgerNonzero.MatchString(out.Credits) {
		return out, creditLedgerInvalid("credits")
	}
	for _, field := range []struct {
		key      string
		dest     **string
		min, max int
		trim     bool
	}{
		{"modelName", &out.ModelName, 0, 120, false}, {"actorName", &out.ActorName, 1, 255, true}, {"sessionId", &out.SessionID, 1, 0, false}, {"requestId", &out.RequestID, 1, 0, false}, {"reasoningEffort", &out.ReasoningEffort, 1, 64, false},
	} {
		value, err := creditLedgerString(fields[field.key], true, field.min, field.max, field.trim)
		if err != nil {
			return out, creditLedgerInvalid(field.key)
		}
		*field.dest = value
	}
	for _, field := range []struct {
		key  string
		dest **int64
	}{{"quantity", &out.Quantity}, {"reasoningBudgetTokens", &out.ReasoningBudgetTokens}} {
		value, err := creditLedgerInteger(fields[field.key], true, 1)
		if err != nil {
			return out, creditLedgerInvalid(field.key)
		}
		*field.dest = value
	}
	if !creditLedgerNull(fields["usage"]) {
		usage, err := parseCreditLedgerUsage(fields["usage"])
		if err != nil {
			return out, err
		}
		out.Usage = &usage
	}
	return out, nil
}

func parseCreditLedgerUsage(raw []byte) (CreditLedgerUsage, error) {
	var out CreditLedgerUsage
	fields, err := creditLedgerObject(raw, "inputTokens outputTokens cachedInputTokens reasoningTokens", "cacheCreationTokens")
	if err != nil {
		return out, err
	}
	for _, field := range []struct {
		key  string
		dest **int64
	}{{"inputTokens", &out.InputTokens}, {"outputTokens", &out.OutputTokens}, {"cachedInputTokens", &out.CachedInputTokens}, {"cacheCreationTokens", &out.CacheCreationTokens}, {"reasoningTokens", &out.ReasoningTokens}} {
		value, err := creditLedgerInteger(fields[field.key], true, 0)
		if err != nil {
			return out, creditLedgerInvalid(field.key)
		}
		*field.dest = value
	}
	return out, nil
}

func parseCreditLedgerSummary(raw []byte) (CreditLedgerSummary, error) {
	var out CreditLedgerSummary
	fields, err := creditLedgerObject(raw, "totalCreditsIn totalCreditsOut netCredits entryCount", "")
	if err != nil {
		return out, err
	}
	for _, field := range []struct {
		key     string
		dest    *string
		pattern *regexp.Regexp
	}{{"totalCreditsIn", &out.TotalCreditsIn, creditLedgerSummaryDecimal}, {"totalCreditsOut", &out.TotalCreditsOut, creditLedgerSummaryDecimal}, {"netCredits", &out.NetCredits, creditLedgerSignedDecimal}} {
		value, err := creditLedgerString(fields[field.key], false, 1, 0, false)
		if err != nil || !field.pattern.MatchString(*value) {
			return out, creditLedgerInvalid(field.key)
		}
		*field.dest = *value
	}
	count, err := creditLedgerInteger(fields["entryCount"], false, 0)
	if err != nil {
		return out, creditLedgerInvalid("entryCount")
	}
	out.EntryCount = *count
	return out, nil
}

func creditLedgerObject(raw []byte, required, optional string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, creditLedgerInvalid("object")
	}
	allowed := strings.Fields(required + " " + optional)
	for key := range fields {
		if !creditLedgerEnum(key, strings.Join(allowed, " ")) {
			return nil, creditLedgerInvalid(key)
		}
	}
	for _, key := range strings.Fields(required) {
		if _, exists := fields[key]; !exists {
			return nil, creditLedgerInvalid(key)
		}
	}
	return fields, nil
}

func creditLedgerString(raw []byte, nullable bool, min, max int, trim bool) (*string, error) {
	if nullable && (len(raw) == 0 || creditLedgerNull(raw)) {
		return nil, nil
	}
	var value string
	if creditLedgerNull(raw) || json.Unmarshal(raw, &value) != nil {
		return nil, creditLedgerInvalid("string")
	}
	if trim {
		value = strings.TrimFunc(value, creditLedgerJSWhitespace)
	}
	length := len([]rune(value))
	if length < min || (max > 0 && length > max) {
		return nil, creditLedgerInvalid("string_length")
	}
	return &value, nil
}

func creditLedgerInteger(raw []byte, nullable bool, min float64) (*int64, error) {
	if nullable && (len(raw) == 0 || creditLedgerNull(raw)) {
		return nil, nil
	}
	var value float64
	if creditLedgerNull(raw) || json.Unmarshal(raw, &value) != nil || value < min || value > 9_007_199_254_740_991 || math.Trunc(value) != value {
		return nil, creditLedgerInvalid("integer")
	}
	result := int64(value)
	return &result, nil
}

func creditLedgerJSWhitespace(r rune) bool {
	return (r >= '\t' && r <= '\r') || r == ' ' || r == '\u00a0' || r == '\u1680' || (r >= '\u2000' && r <= '\u200a') || r == '\u2028' || r == '\u2029' || r == '\u202f' || r == '\u205f' || r == '\u3000' || r == '\ufeff'
}

func creditLedgerNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }
func creditLedgerEnum(value, options string) bool {
	for _, option := range strings.Fields(options) {
		if value == option {
			return true
		}
	}
	return false
}
func creditLedgerInvalid(field string) error { return fmt.Errorf("invalid_credit_ledger_%s", field) }

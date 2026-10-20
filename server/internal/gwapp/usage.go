package gwapp

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// usage.ts 的镜像:三协议的 usage 解析与 SSE 采集。
// 计费口径:OpenAI/Responses 的 prompt/input 是「含缓存」总量,
// fresh input = 总量 - 缓存读;Anthropic 的 input_tokens 不含缓存,直接采用。

const maxSSEBufferChars = 1_000_000

type UsageCounts struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// 供应商计数必须是安全非负整数；非法/缺失 usage 交给预留估算，不能折成零。
func validCount(record map[string]any, key string, required bool) bool {
	value, ok := record[key]
	if !ok {
		return !required
	}
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case string:
		var err error
		n, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return false
		}
	default:
		return false
	}
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 9_007_199_254_740_991 && n == math.Trunc(n)
}

func validOpenAIUsage(usage map[string]any) bool {
	if !validCount(usage, "prompt_tokens", true) || !validCount(usage, "completion_tokens", true) || !validCount(usage, "prompt_cache_hit_tokens", false) {
		return false
	}
	if raw, exists := usage["prompt_tokens_details"]; exists && raw != nil {
		details, ok := raw.(map[string]any)
		if !ok || !validCount(details, "cached_tokens", false) {
			return false
		}
		if readCount(details, "cached_tokens") > readCount(usage, "prompt_tokens") {
			return false
		}
	}
	return readCount(usage, "prompt_cache_hit_tokens") <= readCount(usage, "prompt_tokens")
}

// readCount:复刻 Number(value) → floor → >0 的口径。
func readCount(record map[string]any, key string) int64 {
	value, ok := record[key]
	if !ok {
		return 0
	}
	var numeric float64
	switch typed := value.(type) {
	case float64:
		numeric = typed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0
		}
		numeric = parsed
	default:
		return 0
	}
	if math.IsNaN(numeric) || math.IsInf(numeric, 0) || numeric <= 0 || numeric > 9_007_199_254_740_991 {
		return 0
	}
	return int64(math.Floor(numeric))
}

func countsFromUsageRecord(record map[string]any) *UsageCounts {
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		if !validCount(record, key, false) {
			return nil
		}
	}
	_, hasInput := record["input_tokens"]
	_, hasOutput := record["output_tokens"]
	_, hasCacheRead := record["cache_read_input_tokens"]
	_, hasCacheCreate := record["cache_creation_input_tokens"]
	if !hasInput && !hasOutput && !hasCacheRead && !hasCacheCreate {
		return nil
	}
	return &UsageCounts{
		InputTokens:         readCount(record, "input_tokens"),
		OutputTokens:        readCount(record, "output_tokens"),
		CacheReadTokens:     readCount(record, "cache_read_input_tokens"),
		CacheCreationTokens: readCount(record, "cache_creation_input_tokens"),
	}
}

func ParseUsageFromAnthropicJSON(body any) *UsageCounts {
	record, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := record["usage"].(map[string]any)
	if !ok {
		return nil
	}
	if !validCount(usage, "input_tokens", true) || !validCount(usage, "output_tokens", true) {
		return nil
	}
	return countsFromUsageRecord(usage)
}

func countsFromOpenAIUsage(usage map[string]any) UsageCounts {
	prompt := readCount(usage, "prompt_tokens")
	completion := readCount(usage, "completion_tokens")
	var cached int64
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		cached = readCount(details, "cached_tokens")
	}
	if cached == 0 {
		cached = readCount(usage, "prompt_cache_hit_tokens")
	}
	fresh := prompt - cached
	if fresh < 0 {
		fresh = 0
	}
	return UsageCounts{InputTokens: fresh, OutputTokens: completion, CacheReadTokens: cached}
}

func ParseUsageFromOpenAIJSON(body any) *UsageCounts {
	record, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := record["usage"].(map[string]any)
	if !ok {
		return nil
	}
	if !validOpenAIUsage(usage) {
		return nil
	}
	counts := countsFromOpenAIUsage(usage)
	return &counts
}

func countsFromResponsesUsage(usage map[string]any) *UsageCounts {
	if !validCount(usage, "input_tokens", true) || !validCount(usage, "output_tokens", true) {
		return nil
	}
	if raw, exists := usage["input_tokens_details"]; exists && raw != nil {
		details, ok := raw.(map[string]any)
		if !ok || !validCount(details, "cached_tokens", false) || readCount(details, "cached_tokens") > readCount(usage, "input_tokens") {
			return nil
		}
	}
	_, hasInput := usage["input_tokens"]
	_, hasOutput := usage["output_tokens"]
	if !hasInput && !hasOutput {
		return nil
	}
	input := readCount(usage, "input_tokens")
	output := readCount(usage, "output_tokens")
	var cached int64
	if details, ok := usage["input_tokens_details"].(map[string]any); ok {
		cached = readCount(details, "cached_tokens")
	}
	fresh := input - cached
	if fresh < 0 {
		fresh = 0
	}
	return &UsageCounts{InputTokens: fresh, OutputTokens: output, CacheReadTokens: cached}
}

// ParseUsageFromResponsesJSON:非流式响应体或流式 terminal 事件(response.usage)归一处理。
func ParseUsageFromResponsesJSON(body any) *UsageCounts {
	record, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := record["usage"].(map[string]any)
	if !ok {
		response, responseOK := record["response"].(map[string]any)
		if !responseOK {
			return nil
		}
		usage, ok = response["usage"].(map[string]any)
	}
	if !ok {
		return nil
	}
	return countsFromResponsesUsage(usage)
}

// ---- SSE 事件切分(公共:按空行切帧,buffer 上限 1MB,溢出计 dropped) ----

var sseEventBoundary = regexp.MustCompile(`\r?\n\r?\n`)

type sseSplitter struct {
	buffer     strings.Builder
	overflowed int64
	onEvent    func(raw string)
}

func newSSESplitter(onEvent func(raw string)) *sseSplitter {
	return &sseSplitter{onEvent: onEvent}
}

func (s *sseSplitter) push(chunk string) {
	s.buffer.WriteString(chunk)
	if s.buffer.Len() > maxSSEBufferChars {
		s.overflowed++
		s.buffer.Reset()
	}
	for {
		buf := s.buffer.String()
		loc := sseEventBoundary.FindStringIndex(buf)
		if loc == nil {
			return
		}
		raw := buf[:loc[0]]
		rest := buf[loc[1]:]
		s.buffer.Reset()
		s.buffer.WriteString(rest)
		s.onEvent(raw)
	}
}

func (s *sseSplitter) dropped() int64 { return s.overflowed }

func splitLines(raw string) []string {
	return regexp.MustCompile(`\r?\n`).Split(raw, -1)
}

// ---- OpenAI chat 扫描器(取最后一条 usage chunk) ----

type OpenAIScanner struct {
	usage     UsageCounts
	observed  bool
	terminal  bool
	malformed int64
	splitter  *sseSplitter
}

func NewOpenAIScanner() *OpenAIScanner {
	scanner := &OpenAIScanner{}
	scanner.splitter = newSSESplitter(func(rawEvent string) {
		for _, line := range splitLines(rawEvent) {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(line[5:])
			if data == "[DONE]" {
				scanner.terminal = true
				continue
			}
			if data == "" {
				continue
			}
			var parsed any
			if err := json.Unmarshal([]byte(data), &parsed); err != nil {
				scanner.malformed++
				continue
			}
			record, ok := parsed.(map[string]any)
			if !ok {
				continue
			}
			usageValue, exists := record["usage"]
			if !exists {
				continue
			}
			usage, ok := usageValue.(map[string]any)
			if !ok {
				continue
			}
			if !validOpenAIUsage(usage) {
				scanner.malformed++
				continue
			}
			counts := countsFromOpenAIUsage(usage)
			scanner.usage = counts
			scanner.observed = true
		}
	})
	return scanner
}

func (s *OpenAIScanner) Push(chunk string)   { s.splitter.push(chunk) }
func (s *OpenAIScanner) Usage() UsageCounts  { return s.usage }
func (s *OpenAIScanner) UsageObserved() bool { return s.observed }
func (s *OpenAIScanner) UsageComplete() bool { return s.observed && s.terminal }
func (s *OpenAIScanner) Dropped() int64      { return s.malformed + s.splitter.dropped() }

// ---- Anthropic 扫描器(message_start 输入侧 + message_delta 累计输出) ----

type AnthropicScanner struct {
	inputObserved  bool
	outputObserved bool
	usage          UsageCounts
	observed       bool
	terminal       bool
	malformed      int64
	splitter       *sseSplitter
}

func NewAnthropicScanner() *AnthropicScanner {
	scanner := &AnthropicScanner{}
	scanner.splitter = newSSESplitter(func(rawEvent string) {
		scanner.handleRawEvent(rawEvent)
	})
	return scanner
}

func (s *AnthropicScanner) handleRawEvent(rawEvent string) {
	eventName := ""
	var dataLines []string
	for _, line := range splitLines(rawEvent) {
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			data := line[5:]
			dataLines = append(dataLines, strings.TrimLeft(data, " \t"))
		}
	}
	dataText := strings.Join(dataLines, "\n")
	if dataText == "" || dataText == "[DONE]" {
		return
	}
	var parsed any
	if err := json.Unmarshal([]byte(dataText), &parsed); err != nil {
		s.malformed++
		return
	}
	if record, ok := parsed.(map[string]any); ok && eventName != "" {
		if _, hasType := record["type"]; !hasType {
			record["type"] = eventName
		}
	}
	s.observe(parsed)
}

func (s *AnthropicScanner) observe(message any) {
	record, ok := message.(map[string]any)
	if !ok {
		return
	}
	switch record["type"] {
	case "message_stop":
		s.terminal = true
	case "message_start":
		startMessage, ok := record["message"].(map[string]any)
		if !ok {
			return
		}
		usage, ok := startMessage["usage"].(map[string]any)
		if !ok {
			return
		}
		counts := countsFromUsageRecord(usage)
		if counts != nil {
			s.usage = *counts
			s.observed = true
			s.inputObserved = validCount(usage, "input_tokens", true)
		}
	case "message_delta":
		usage, ok := record["usage"].(map[string]any)
		if !ok {
			return
		}
		counts := countsFromUsageRecord(usage)
		if counts == nil {
			return
		}
		s.observed = true
		s.outputObserved = validCount(usage, "output_tokens", true)
		// message_delta 的 output_tokens 是累计值,直接覆盖;输入侧仅非零时更新。
		s.usage.OutputTokens = counts.OutputTokens
		if counts.InputTokens > 0 {
			s.usage.InputTokens = counts.InputTokens
		}
		if counts.CacheReadTokens > 0 {
			s.usage.CacheReadTokens = counts.CacheReadTokens
		}
		if counts.CacheCreationTokens > 0 {
			s.usage.CacheCreationTokens = counts.CacheCreationTokens
		}
	}
}

func (s *AnthropicScanner) Push(chunk string)   { s.splitter.push(chunk) }
func (s *AnthropicScanner) Usage() UsageCounts  { return s.usage }
func (s *AnthropicScanner) UsageObserved() bool { return s.observed }
func (s *AnthropicScanner) UsageComplete() bool {
	return s.inputObserved && s.outputObserved && s.terminal
}
func (s *AnthropicScanner) Dropped() int64 { return s.malformed + s.splitter.dropped() }

// ---- Responses 扫描器(terminal 事件携带累计 usage) ----

type ResponsesScanner struct {
	responseID string
	usage      UsageCounts
	observed   bool
	terminal   bool
	malformed  int64
	splitter   *sseSplitter
}

func NewResponsesScanner() *ResponsesScanner {
	scanner := &ResponsesScanner{}
	scanner.splitter = newSSESplitter(func(rawEvent string) {
		for _, line := range splitLines(rawEvent) {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(line[5:])
			if data == "" || data == "[DONE]" {
				continue
			}
			var parsed any
			if err := json.Unmarshal([]byte(data), &parsed); err != nil {
				scanner.malformed++
				continue
			}
			if record, ok := parsed.(map[string]any); ok {
				if response, ok := record["response"].(map[string]any); ok {
					if id, ok := response["id"].(string); ok && id != "" {
						scanner.responseID = id
					}
				}
				switch record["type"] {
				case "response.completed", "response.incomplete", "response.failed":
					scanner.terminal = true
				}
			}
			counts := ParseUsageFromResponsesJSON(parsed)
			if counts != nil {
				scanner.usage = *counts
				scanner.observed = true
			}
		}
	})
	return scanner
}

func (s *ResponsesScanner) Push(chunk string)   { s.splitter.push(chunk) }
func (s *ResponsesScanner) ResponseID() string  { return s.responseID }
func (s *ResponsesScanner) Usage() UsageCounts  { return s.usage }
func (s *ResponsesScanner) UsageObserved() bool { return s.observed }
func (s *ResponsesScanner) UsageComplete() bool { return s.observed && s.terminal }
func (s *ResponsesScanner) Dropped() int64      { return s.malformed + s.splitter.dropped() }

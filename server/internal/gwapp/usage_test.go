package gwapp

import (
	"encoding/json"
	"testing"
)

func countsEqual(a *UsageCounts, input, output, cacheRead, cacheCreate int64) bool {
	return a != nil && a.InputTokens == input && a.OutputTokens == output &&
		a.CacheReadTokens == cacheRead && a.CacheCreationTokens == cacheCreate
}

func TestParseUsageJSON(t *testing.T) {
	parse := func(text string) any {
		var body any
		if err := json.Unmarshal([]byte(text), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	// Anthropic:input 不含缓存,直接采用。
	if got := ParseUsageFromAnthropicJSON(parse(`{"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":5}}`)); !countsEqual(got, 100, 50, 20, 5) {
		t.Fatalf("anthropic 解析不符: %+v", got)
	}
	if got := ParseUsageFromAnthropicJSON(parse(`{"usage":{}}`)); got != nil {
		t.Fatal("无已知字段应返回 nil")
	}
	// OpenAI:prompt 含缓存,fresh = prompt - cached。
	if got := ParseUsageFromOpenAIJSON(parse(`{"usage":{"prompt_tokens":100,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":40}}}`)); !countsEqual(got, 60, 50, 40, 0) {
		t.Fatalf("openai 解析不符: %+v", got)
	}
	if got := ParseUsageFromOpenAIJSON(parse(`{"usage":{"prompt_tokens":100,"completion_tokens":50,"prompt_cache_hit_tokens":30}}`)); !countsEqual(got, 70, 50, 30, 0) {
		t.Fatalf("deepseek 缓存字段解析不符: %+v", got)
	}
	// Responses:支持顶层 usage 与 response.usage 两种形态。
	if got := ParseUsageFromResponsesJSON(parse(`{"usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"cached_tokens":40}}}`)); !countsEqual(got, 60, 50, 40, 0) {
		t.Fatalf("responses 顶层解析不符: %+v", got)
	}
	if got := ParseUsageFromResponsesJSON(parse(`{"response":{"usage":{"input_tokens":10,"output_tokens":2}}}`)); !countsEqual(got, 10, 2, 0, 0) {
		t.Fatalf("responses 嵌套解析不符: %+v", got)
	}
	if got := ParseUsageFromResponsesJSON(parse(`{"response":{}}`)); got != nil {
		t.Fatal("无 usage 应返回 nil")
	}
}

func TestAnthropicScanner(t *testing.T) {
	scanner := NewAnthropicScanner()
	// 事件跨 chunk 切分:message_start 提供输入侧,message_delta 累计输出。
	scanner.Push("event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":2}}}\r\n\r\n")
	scanner.Push("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n")
	scanner.Push("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n")
	got := scanner.Usage()
	if !countsEqual(&got, 10, 9, 3, 2) {
		t.Fatalf("Anthropic 流式采集不符: %+v", got)
	}
	scanner.Push("data: not-json\n\n")
	if scanner.Dropped() != 1 {
		t.Fatalf("坏帧应计 dropped=1: %d", scanner.Dropped())
	}
}

func TestOpenAIScanner(t *testing.T) {
	scanner := NewOpenAIScanner()
	scanner.Push("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n")
	// usage 是绝对值:最后一条覆盖。
	scanner.Push("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":50,\"prompt_tokens_details\":{\"cached_tokens\":40}}}\n\ndata: [DONE]\n\n")
	got := scanner.Usage()
	if !countsEqual(&got, 60, 50, 40, 0) {
		t.Fatalf("OpenAI 流式采集不符: %+v", got)
	}
	scanner.Push("data: {broken\n\n")
	if scanner.Dropped() != 1 {
		t.Fatalf("坏帧计数不符: %d", scanner.Dropped())
	}
}

func TestResponsesScanner(t *testing.T) {
	scanner := NewResponsesScanner()
	scanner.Push("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":50,\"input_tokens_details\":{\"cached_tokens\":40}}}}\n\n")
	got := scanner.Usage()
	if !countsEqual(&got, 60, 50, 40, 0) {
		t.Fatalf("Responses 流式采集不符: %+v", got)
	}
}

func TestSSEBufferOverflow(t *testing.T) {
	scanner := NewOpenAIScanner()
	big := make([]byte, maxSSEBufferChars+1)
	for i := range big {
		big[i] = 'x'
	}
	scanner.Push(string(big))
	if scanner.Dropped() < 1 {
		t.Fatal("超限 buffer 应计 dropped")
	}
	// 溢出后仍可继续解析后续正常帧。
	scanner.Push("data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
	got := scanner.Usage()
	if got.InputTokens != 1 {
		t.Fatalf("溢出后应继续工作: %+v", got)
	}
}

func TestUsageRequiresValidCompleteCounts(t *testing.T) {
	for _, usage := range []string{`{}`, `{"prompt_tokens":10}`, `{"prompt_tokens":-1,"completion_tokens":1}`, `{"prompt_tokens":1e30,"completion_tokens":1}`, `{"prompt_tokens":1.5,"completion_tokens":1}`, `{"prompt_tokens":1,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":2}}`} {
		var body any
		_ = json.Unmarshal([]byte(`{"usage":`+usage+`}`), &body)
		if got := ParseUsageFromOpenAIJSON(body); got != nil {
			t.Fatalf("不完整/非法 usage 不能当作真实零用量: %s %+v", usage, got)
		}
	}
	s := NewAnthropicScanner()
	s.Push("event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
	if s.UsageComplete() {
		t.Fatal("仅有输入用量不能证明已终止")
	}
	s.Push("event: message_delta\ndata: {\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {}\n\n")
	if !s.UsageComplete() {
		t.Fatal("最终累计用量与终止事件应完成计量")
	}
}

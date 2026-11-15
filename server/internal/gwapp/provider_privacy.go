package gwapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

const publicProviderFailure = "模型服务暂时不可用，请稍后重试"

// 仅删除已识别的协议错误信息，成功内容和合法 usage 保持原样。
// 上游错误可能包含渠道名称、地址或凭据，替换密钥子串不足以保护这些信息。
func publicProviderPayload(raw []byte, errorEvent bool) []byte {
	var body map[string]any
	valid := json.Unmarshal(raw, &body) == nil && body != nil
	response, _ := body["response"].(map[string]any)
	kind, _ := body["type"].(string)
	failed := body["error"] != nil || kind == "error" || kind == "response.failed" || response["error"] != nil
	if !errorEvent && (!valid || !failed) {
		return raw
	}
	errBody := map[string]any{"type": "api_error", "code": "upstream_error", "message": publicProviderFailure}
	result := map[string]any{"error": errBody}
	if kind == "error" {
		result["type"] = "error"
	}
	if response != nil || body["object"] == "response" {
		source := response
		if source == nil {
			source = body
		}
		clean := map[string]any{"error": errBody}
		for _, key := range []string{"id", "object", "created_at", "status", "model", "output", "usage"} {
			if value, ok := source[key]; ok {
				clean[key] = value
			}
		}
		if response != nil {
			result = map[string]any{"type": kind, "response": clean}
			if sequence, ok := body["sequence_number"]; ok {
				result["sequence_number"] = sequence
			}
		} else {
			result = clean
		}
	}
	encoded, _ := json.Marshal(result)
	return encoded
}

type providerEventFilter struct{ pending []byte }

func (f *providerEventFilter) push(raw []byte) ([]byte, error) {
	f.pending = append(f.pending, raw...)
	var out []byte
	for {
		end, width := -1, 0
		for _, sep := range [][]byte{[]byte("\n\n"), []byte("\r\n\r\n"), []byte("\r\r")} {
			if pos := bytes.Index(f.pending, sep); pos >= 0 && (end < 0 || pos < end) {
				end, width = pos, len(sep)
			}
		}
		if end < 0 {
			if len(f.pending) > maxSSEBufferChars {
				return nil, errors.New("upstream_event_too_large")
			}
			return out, nil
		}
		if end > maxSSEBufferChars {
			return nil, errors.New("upstream_event_too_large")
		}
		frame := f.pending[:end+width]
		text := strings.ReplaceAll(strings.ReplaceAll(string(frame), "\r\n", "\n"), "\r", "\n")
		var data []string
		event := ""
		var visibleLines []string
		hasComment := false
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, ":") {
				hasComment = true
				continue
			}
			visibleLines = append(visibleLines, line)
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
		}
		payload := []byte(strings.Join(data, "\n"))
		clean := publicProviderPayload(payload, event == "error" || event == "response.failed")
		if bytes.Equal(clean, payload) {
			if hasComment {
				// SSE 注释可能携带渠道 banner；只保留中性心跳和实际协议字段。
				visible := strings.Join(visibleLines, "\n")
				if strings.TrimSpace(visible) == "" {
					visible = ": keep-alive\n\n"
				}
				out = append(out, []byte(visible)...)
			} else {
				out = append(out, frame...)
			}
		} else {
			if event != "" {
				if event != "response.failed" {
					event = "error"
				}
				out = append(out, []byte("event: "+event+"\n")...)
			}
			out = append(out, []byte("data: ")...)
			out = append(out, clean...)
			out = append(out, '\n', '\n')
		}
		f.pending = f.pending[end+width:]
	}
}

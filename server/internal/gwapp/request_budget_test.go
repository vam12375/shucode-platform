package gwapp

import (
	"strings"
	"testing"
)

func TestRequestBudgetRecognizesProtocolImagesWithoutCountingBase64(t *testing.T) {
	payload := strings.Repeat("x", 1024*1024)
	for _, test := range []struct {
		name  string
		value map[string]any
	}{
		{"chat", map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64," + payload}}},
		{"responses", map[string]any{"type": "input_image", "image_url": "data:image/jpeg;base64," + payload}},
		{"responses-file", map[string]any{"type": "input_image", "file_id": "file_fixture"}},
		{"anthropic", map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/jpeg", "data": payload}}},
		{"anthropic-url", map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://cdn.example.test/frame.jpg"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			textBytes, imageTokens := estimateMultimodalInput(test.value)
			if imageTokens != imageInputTokenAllowance || textBytes > 100 {
				t.Fatalf("image payload counted as text: text=%d images=%d", textBytes, imageTokens)
			}
		})
	}
}

func TestRequestBudgetDoesNotHideOrdinaryTextOrImageSiblingText(t *testing.T) {
	text := strings.Repeat("large prompt ", 10000)
	for _, value := range []map[string]any{
		{"type": "text", "text": "data:image/jpeg;base64," + text},
		{"type": "image_generation", "prompt": text},
		{"type": "image_url", "image_url": map[string]any{"url": "https://cdn.example.test/frame.jpg"}, "caption": text},
		{"type": "image_url", "image_url": map[string]any{"url": "https://cdn.example.test/frame.jpg", "extra": text}},
	} {
		textBytes, _ := estimateMultimodalInput(value)
		if textBytes < int64(len(text)) {
			t.Fatalf("ordinary text disappeared from budget: %d < %d", textBytes, len(text))
		}
	}
}

func TestPromptCostEstimateRetainsConservativeTextSemantics(t *testing.T) {
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "中文输入保持原保守字节估算"}}}
	if got, want := estimatePromptTokens(body), int64(len(mustJSON(body)))+128; got != want {
		t.Fatalf("prompt cost estimate changed: got=%d want=%d", got, want)
	}
}

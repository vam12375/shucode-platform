package upstreamurl

import "testing"

func TestJoin(t *testing.T) {
	for _, tc := range []struct {
		name, base, path, want string
	}{
		{"origin", "https://upstream.test", "/v1/chat/completions", "https://upstream.test/v1/chat/completions"},
		{"trailing slash", "https://upstream.test/", "/v1/responses", "https://upstream.test/v1/responses"},
		{"explicit v1", "https://upstream.test/v1/", "/v1/messages", "https://upstream.test/v1/messages"},
		{"ark chat", "https://upstream.test/api/v3", "/v1/chat/completions", "https://upstream.test/api/v3/chat/completions"},
		{"ark responses", "https://upstream.test/api/v3/", "/v1/responses", "https://upstream.test/api/v3/responses"},
		{"ark models", "https://upstream.test/api/v3", "/v1/models", "https://upstream.test/api/v3/models"},
		{"background response", "https://upstream.test/api/v3", "/v1/responses/resp_123", "https://upstream.test/api/v3/responses/resp_123"},
		{"cancel response", "https://upstream.test/api/v3", "/v1/responses/resp_123/cancel", "https://upstream.test/api/v3/responses/resp_123/cancel"},
		{"proxy root", "https://upstream.test/proxy", "/v1/models", "https://upstream.test/proxy/v1/models"},
		{"proxy prefix", "https://upstream.test/proxy/v1", "/v1/models", "https://upstream.test/proxy/v1/models"},
		{"other version", "https://upstream.test/v4", "/v1/chat/completions", "https://upstream.test/v4/chat/completions"},
		{"non version suffix", "https://upstream.test/preview1", "/v1/models", "https://upstream.test/preview1/v1/models"},
		{"hostname is not a path", "https://v1", "/v1/models", "https://v1/v1/models"},
		{"non protocol path", "https://upstream.test/api/v3", "/images/generations", "https://upstream.test/api/v3/images/generations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Join(tc.base, tc.path); got != tc.want {
				t.Fatalf("Join(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
}

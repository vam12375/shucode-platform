package netguard

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestTransportBlocksLoopbackUnlessAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	blocked := &http.Client{Transport: NewTransport(false), Timeout: 2 * time.Second}
	if resp, err := blocked.Get(server.URL); err == nil {
		resp.Body.Close()
		t.Fatal("默认应拒绝回环目标")
	}

	allowed := &http.Client{Transport: NewTransport(true), Timeout: 2 * time.Second}
	resp, err := allowed.Get(server.URL)
	if err != nil {
		t.Fatalf("显式允许私网后应可连接: %v", err)
	}
	resp.Body.Close()
}

func TestBlockedAddress(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":            true,
		"10.0.0.1":             true,
		"172.16.0.1":           true,
		"192.168.1.1":          true,
		"100.64.0.1":           true,
		"169.254.169.254":      true,
		"0.0.0.0":              true,
		"::1":                  true,
		"fe80::1":              true,
		"fc00::1":              true,
		"8.8.8.8":              false,
		"1.1.1.1":              false,
		"2606:4700:4700::1111": false,
		// 保留段与文档段：不应被当成公网可达目标。
		"240.0.0.1":       true,
		"255.255.255.255": true,
		"192.0.2.1":       true,
		"198.51.100.1":    true,
		"203.0.113.1":     true,
		"2001:db8::1":     true,
		// NAT64：内嵌 v4 必须解包后按 v4 规则判定，否则可绕过元数据黑名单。
		"64:ff9b::a9fe:a9fe": true,  // → 169.254.169.254
		"64:ff9b::7f00:1":    true,  // → 127.0.0.1
		"64:ff9b::a00:1":     true,  // → 10.0.0.1
		"64:ff9b::808:808":   false, // → 8.8.8.8，公网仍放行
	}
	for raw, want := range cases {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("解析 %s: %v", raw, err)
		}
		if got := blockedAddress(addr); got != want {
			t.Fatalf("%s: blocked=%v want=%v", raw, got, want)
		}
	}
}

// NAT64 解包必须与 v4 规则一致：同一目标经 v6 字面量表达时不得放宽。
func TestEmbeddedV4NormalizesNat64(t *testing.T) {
	cases := map[string]string{
		"64:ff9b::a9fe:a9fe": "169.254.169.254",
		"64:ff9b::7f00:1":    "127.0.0.1",
		"64:ff9b:1::a00:1":   "10.0.0.1",
	}
	for raw, want := range cases {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("解析 %s: %v", raw, err)
		}
		got, ok := embeddedV4(addr)
		if !ok {
			t.Fatalf("%s 应识别为 NAT64 地址", raw)
		}
		if got.String() != want {
			t.Fatalf("%s 解出 %s，期望 %s", raw, got, want)
		}
	}
	// 普通 v6 与 v4 不应被误判。
	for _, raw := range []string{"2606:4700:4700::1111", "2001:db8::1", "8.8.8.8"} {
		addr, _ := netip.ParseAddr(raw)
		if _, ok := embeddedV4(addr); ok {
			t.Fatalf("%s 不应被识别为 NAT64 地址", raw)
		}
	}
}

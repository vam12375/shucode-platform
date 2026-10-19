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

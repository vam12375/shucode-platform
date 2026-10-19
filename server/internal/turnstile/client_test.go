package turnstile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	client, err := NewClient(Options{
		SecretKey:      "test-secret",
		Endpoint:       endpoint,
		ExpectedAction: "login",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestVerifySendsTokenAndRemoteIP(t *testing.T) {
	var gotSecret, gotToken, gotIP string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotSecret = r.Form.Get("secret")
		gotToken = r.Form.Get("response")
		gotIP = r.Form.Get("remoteip")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"action":"login"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if err := client.Verify(context.Background(), " token-value ", "203.0.113.7"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if gotSecret != "test-secret" || gotToken != "token-value" || gotIP != "203.0.113.7" {
		t.Fatalf("siteverify 表单不符: secret=%q token=%q ip=%q", gotSecret, gotToken, gotIP)
	}
}

func TestVerifyMissingTokenDoesNotCallSiteverify(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if err := client.Verify(context.Background(), "   ", ""); !errors.Is(err, ErrMissingToken) {
		t.Fatalf("期望 ErrMissingToken，得到 %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("空 token 不应请求 siteverify")
	}
}

func TestVerifyFailedChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	err := client.Verify(context.Background(), "bad-token", "")
	if !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("期望 ErrVerificationFailed，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "invalid-input-response") {
		t.Fatalf("错误应包含 Cloudflare 错误码: %v", err)
	}
	if strings.Contains(err.Error(), "bad-token") {
		t.Fatalf("错误不得包含 token: %v", err)
	}
}

func TestVerifyRejectsActionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"action":"other"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if err := client.Verify(context.Background(), "token", ""); !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("期望 action 不匹配失败，得到 %v", err)
	}
}

func TestVerifyUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	if err := client.Verify(context.Background(), "token", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("期望 ErrUnavailable，得到 %v", err)
	}
}

func TestNewClientRejectsInsecureRemoteEndpoint(t *testing.T) {
	if _, err := NewClient(Options{SecretKey: "secret", Endpoint: "http://example.com/siteverify"}); err == nil {
		t.Fatal("非回环 http endpoint 应被拒绝")
	}
}

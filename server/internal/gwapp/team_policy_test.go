package gwapp

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestGatewayTeamPolicyCannotRaisePlatformRPM(t *testing.T) {
	for _, test := range []struct {
		platform int64
		policy   int64
		allowed  int
	}{{10, 20, 10}, {20, 10, 10}, {0, 3, 3}, {3, 0, 3}} {
		t.Run(fmt.Sprintf("platform-%d-policy-%d", test.platform, test.policy), func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			ctx := context.Background()
			if _, err := f.store.UpdateTeam(ctx, f.teamID, store.UpdateTeamPatch{RPMLimit: &test.platform}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.teamID, RPMLimit: test.policy}); err != nil {
				t.Fatal(err)
			}
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			})
			for i := range test.allowed {
				rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`)
				if rec.Code != 200 {
					t.Fatalf("第%d个请求应在有效上限内: %d %s", i+1, rec.Code, rec.Body.String())
				}
			}
			assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`), 429, "rate_limit_error")
			if len(f.usageRecords()) != test.allowed {
				t.Fatal("超过有效上限的请求不能触达上游或扣费")
			}
		})
	}
}

func TestGatewayTeamPolicyCannotRaisePlatformTPM(t *testing.T) {
	for _, test := range []struct{ platform, policy int64 }{{10, 20}, {20, 10}, {0, 10}, {10, 0}} {
		t.Run(fmt.Sprintf("platform-%d-policy-%d", test.platform, test.policy), func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			ctx := context.Background()
			if _, err := f.store.UpdateTeam(ctx, f.teamID, store.UpdateTeamPatch{TPMLimit: &test.platform}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.teamID, TPMLimit: test.policy}); err != nil {
				t.Fatal(err)
			}
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":0,"completion_tokens":1}}`))
			})
			body := `{"model":"claude-sonnet-4-5","max_tokens":1}`
			first := f.do("POST", "/v1/chat/completions", f.token, body)
			if first.Code != 200 {
				t.Fatalf("首个请求应通过: %d %s", first.Code, first.Body.String())
			}
			assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, body), 429, "rate_limit_error")
		})
	}
}

func TestGatewayTeamPolicyCannotRaisePlatformConcurrency(t *testing.T) {
	for _, test := range []struct {
		platform int64
		policy   int64
		allowed  int
	}{{10, 20, 10}, {20, 10, 10}, {0, 2, 2}, {2, 0, 2}} {
		t.Run(fmt.Sprintf("platform-%d-policy-%d", test.platform, test.policy), func(t *testing.T) {
			f := newGatewayFixture(t, nil)
			ctx := context.Background()
			if _, err := f.store.UpdateTeam(ctx, f.teamID, store.UpdateTeamPatch{ConcurrencyLimit: &test.platform}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.SaveTeamPolicy(ctx, store.TeamPolicyRecord{TeamID: f.teamID, ConcurrencyLimit: test.policy}); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{}, test.allowed)
			release := make(chan struct{})
			releaseAll := sync.OnceFunc(func() { close(release) })
			defer releaseAll()
			f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				started <- struct{}{}
				<-release
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
			})
			done := make(chan int, test.allowed)
			for range test.allowed {
				go func() { done <- f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`).Code }()
			}
			for range test.allowed {
				select {
				case <-started:
				case code := <-done:
					t.Fatalf("有效上限内的请求过早结束: %d", code)
				case <-time.After(2 * time.Second):
					t.Fatal("有效上限内的请求未触达上游")
				}
			}
			assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`), 429, "rate_limit_error")
			releaseAll()
			for range test.allowed {
				if code := <-done; code != 200 {
					t.Fatalf("有效并发请求失败: %d", code)
				}
			}
		})
	}
}

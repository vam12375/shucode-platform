package gwapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestConcurrentStreamsKeepBalanceUntilFullSettlement(t *testing.T) {
	f := newGatewayFixture(t, nil)
	if _, err := f.store.UpdateTeam(context.Background(), f.teamID, store.UpdateTeamPatch{QuotaCredits: ptr("0.001000")}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	finish := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(finish) })
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":100}}\n\ndata: [DONE]\n\n")
	})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			responses <- f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100}`)
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("两笔请求必须均能在正余额时准入，不能按最大输出预占余额")
		}
	}
	if team := f.team(); team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("两笔流式请求执行中余额必须保持不变: %+v", team)
	}
	release.Do(func() { close(finish) })
	for range 2 {
		select {
		case rec := <-responses:
			if rec.Code != http.StatusOK {
				t.Fatalf("已准入请求正常完成: %d %s", rec.Code, rec.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("请求未完成结算")
		}
	}
	if team := f.team(); team.UsedCredits != "0.003120" || team.ReservedCredits != "0.000000" {
		t.Fatalf("两笔实际费用必须完整入账，不受余额或估算封顶: %+v", team)
	}
	if rows := f.usageRecords(); len(rows) != 2 || rows[0].CreditsMicro != 1560 || rows[1].CreditsMicro != 1560 {
		t.Fatalf("每笔完整 SSE 只结算一次: %+v", rows)
	}
	assertOpenAIError(t, f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5"}`), 429, "rate_limit_error")
}

func TestUnknownBackgroundSubmissionPersistsReviewWithoutDebitOrRetry(t *testing.T) {
	f := newGatewayFixture(t, nil)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("未知受理应返回可核对的错误: %d %s", rec.Code, rec.Body.String())
	}
	pending, err := f.store.ListPendingSettlements(context.Background(), store.PendingSettlementFilter{Status: store.PendingSettlementPending, Limit: 10})
	if err != nil || len(pending) != 1 {
		t.Fatalf("未知后台受理必须持久化供核对: %+v %v", pending, err)
	}
	record := pending[0].Record
	if record.ErrorType != "background_submission_unknown" || record.TeamID != f.teamID || record.UserID != f.userID || record.UpstreamModel == "" || record.PriceSnapshot == nil || record.PriceSnapshot.Sale.InputPerMtok != "3.000000" || record.CreditsMicro != 0 {
		t.Fatalf("核对保留归属、路由及锁价，不伪造用量: %+v", record)
	}
	if team := f.team(); team.ReservedCredits != "0.000000" || team.UsedCredits != "0.000000" || len(f.usageRecords()) != 0 {
		t.Fatalf("未知受理不占余额、不先结算: %+v", team)
	}
}

type failingBackgroundJobStore struct{ store.PlatformStore }

func (s *failingBackgroundJobStore) CreateBackgroundGeneration(context.Context, store.BackgroundGenerationJob) error {
	return fmt.Errorf("background state unavailable")
}

func TestBackgroundPersistenceFailureKeepsKnownResponseIdentityForReview(t *testing.T) {
	f := newGatewayFixture(t, func(_ *Env, f *gatewayFixture) {
		f.store = &failingBackgroundJobStore{PlatformStore: f.store}
	})
	f.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"resp_keep_for_review","status":"in_progress"}`)
	})
	rec := f.do("POST", "/v1/responses", f.token, `{"model":"claude-sonnet-4-5","background":true}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("后台状态保存失败必须显式返回: %d %s", rec.Code, rec.Body.String())
	}
	pending, err := f.store.ListPendingSettlements(context.Background(), store.PendingSettlementFilter{Status: store.PendingSettlementPending, Limit: 10})
	if err != nil || len(pending) != 1 || pending[0].Record.BackgroundResponseID != "resp_keep_for_review" || pending[0].Record.ErrorType != "background_submission_unknown" {
		t.Fatalf("不能丢失已取得的供应商任务 ID: %+v %v", pending, err)
	}
	if team := f.team(); team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("待核对不占余额或扣费: %+v", team)
	}
}

package gwapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

type lifetimeSettlementCheck struct {
	at           time.Time
	reservations []store.QuotaReservationRecord
	expired      int64
	err          error
	contextErr   error
}

type lifetimeCheckingStore struct {
	store.PlatformStore
	checks chan lifetimeSettlementCheck
}

func (s *lifetimeCheckingStore) SettleReservation(ctx context.Context, id string, record store.UsageRecord) error {
	check := lifetimeSettlementCheck{at: time.Now(), contextErr: ctx.Err()}
	check.reservations, check.err = s.PlatformStore.ListOpenReservations(ctx, 10)
	if check.err == nil {
		check.expired, check.err = s.PlatformStore.ExpireStaleReservations(ctx, time.Now())
	}
	s.checks <- check
	return s.PlatformStore.SettleReservation(ctx, id, record)
}

func assertLifetimeSettlement(t *testing.T, f *gatewayFixture, checked *lifetimeCheckingStore) {
	t.Helper()
	var check lifetimeSettlementCheck
	select {
	case check = <-checked.checks:
	case <-time.After(time.Second):
		t.Fatal("请求结束后没有进入结算")
	}
	if check.err != nil || check.contextErr != nil {
		t.Fatalf("总时限不能取消独立结算 context: check=%v context=%v", check.err, check.contextErr)
	}
	if check.expired != 0 || len(check.reservations) != 1 || !check.reservations[0].ExpiresAt.After(check.at) {
		t.Fatalf("必须在 sweeper 可释放预留前结算: %+v", check)
	}
	records := f.usageRecords()
	if len(records) != 1 || records[0].Status != store.UsageStatusError || records[0].ErrorType != "request_lifetime_timeout" || records[0].CreditsMicro != 0 || records[0].PriceSnapshot == nil || records[0].PriceSnapshot.Cost.UsageEstimated {
		t.Fatalf("总时限无 usage 应只记一次零费用终态: %+v", records)
	}
	if team := f.team(); team.ReservedCredits != "0.000000" {
		t.Fatalf("结算后不应残留预留: %+v", team)
	}
}

func TestGatewayReservationTTLBoundsContinuousStream(t *testing.T) {
	checked := &lifetimeCheckingStore{checks: make(chan lifetimeSettlementCheck, 3)}
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.ReservationTTL = 500 * time.Millisecond
		env.UpstreamIdleTimeoutMs = 80
		checked.PlatformStore, f.store = f.store, checked
	})
	var chunks atomic.Int32
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		safety := time.NewTimer(2 * time.Second)
		defer safety.Stop()
		for {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
			w.(http.Flusher).Flush()
			chunks.Add(1)
			select {
			case <-r.Context().Done():
				return
			case <-safety.C:
				return
			case <-ticker.C:
			}
		}
	})
	rec := f.do("POST", "/v1/chat/completions", f.token, `{"model":"claude-sonnet-4-5","max_tokens":50,"stream":true}`)
	if rec.Code != http.StatusOK || chunks.Load() < 10 {
		t.Fatalf("应已持续透传后由总时限终止，不能误走 idle timeout: status=%d chunks=%d", rec.Code, chunks.Load())
	}
	assertLifetimeSettlement(t, f, checked)
}

func TestGatewayReservationTTLSharedAcrossRetriesAndBody(t *testing.T) {
	checked := &lifetimeCheckingStore{checks: make(chan lifetimeSettlementCheck, 3)}
	f := newGatewayFixture(t, func(env *Env, f *gatewayFixture) {
		env.ReservationTTL = 500 * time.Millisecond
		checked.PlatformStore, f.store = f.store, checked
	})
	var calls atomic.Int32
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-time.After(250 * time.Millisecond):
				http.Error(w, "retry", http.StatusInternalServerError)
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
			Name: fmt.Sprintf("lifetime-%d", i), BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.CreateUpstreamRoute(ctx, store.CreateUpstreamRouteInput{
			ModelID: "claude-sonnet-4-5", ProviderID: provider.ID, Priority: int64(i), Weight: 1, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(f.engine)
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":50}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("authorization", "Bearer "+f.token)
	req.Header.Set("content-type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGatewayTimeout || calls.Load() != 2 {
		t.Fatalf("重试与非流式响应体必须共用总时限，不能再启动第三次请求: status=%d calls=%d body=%s", resp.StatusCode, calls.Load(), body)
	}
	assertLifetimeSettlement(t, f, checked)
}

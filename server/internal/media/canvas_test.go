package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func canvasRequest(t *testing.T, f *fixture) CanvasRequest {
	t.Helper()
	models, _ := f.s.Repo.Models(context.Background())
	models = append(models, Model{ID: "i", Name: "image", Kind: "image", Adapter: "ark", ProviderID: uuid.NewString(), UpstreamModel: "image", PriceMicro: 100000, PriceVersion: "v1"})
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(context.Background(), ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	return CanvasRequest{Kind: "image", ModelID: "i", Prompt: "a landscape", AssetIDs: []string{}, Resolution: "2K", AspectRatio: "16:9", Count: 2, ExpectedQuoteMicro: 200000, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture"}
}
func TestCanvasImageLifecycleAndReplay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	id := uuid.NewString()
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", id, r)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.CreateCanvas(ctx, f.team, f.user, "", id, r)
	if err != nil || again.ID != id {
		t.Fatalf("replay %v", err)
	}
	r.Prompt = "changed"
	if _, err = f.s.CreateCanvas(ctx, f.team, f.user, "", id, r); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	for range 16 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "completed" || p.ChargedMicro != 200000 || f.submits.Load() != 2 {
		t.Fatalf("status=%s charged=%d submits=%d", p.Status, p.ChargedMicro, f.submits.Load())
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.ReservedCredits != "0.000000" || team.UsedCredits != "0.200000" {
		t.Fatalf("ledger %+v", team)
	}
	if p.View().ReservedMicro != 0 || len(p.Shots) != 2 || p.Shots[0].OutputAssetID == "" {
		t.Fatal("missing image result")
	}
}
func TestCanvasQuoteValidationAndUnknownDoesNotHoldBalance(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	r.ExpectedQuoteMicro = 1
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("quote accepted")
	}
	r.ExpectedQuoteMicro = 200000
	r.AssetIDs = []string{"foreign"}
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("foreign asset accepted")
	}
	r.AssetIDs = []string{}
	f.unknown = true
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "reconciliation_required" || f.submits.Load() != 1 || p.View().ReservedMicro != 0 || p.Shots[0].PendingChargeMicro != 100000 || p.Shots[0].ReservationID == "" {
		t.Fatalf("unknown %+v", p.View())
	}
	if _, err = f.s.Action(ctx, f.team, p.ID, p.Version, "retry"); err == nil {
		t.Fatal("unknown retry admitted")
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("unknown task changed balance: %+v", team)
	}
}
func TestCanvasQueuedCancelNoCharge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Status != "cancelled" || p.ChargedMicro != 0 || f.submits.Load() != 0 {
		t.Fatalf("cancel %+v", p)
	}
}

func TestCanvasPartialFailureChargesOnlySuccessAndRetriesFailedOutput(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	var submissions atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if submissions.Add(1) == 2 {
			w.WriteHeader(422)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"url": f.server.URL + "/result.png"}}})
	}))
	defer upstream.Close()
	f.s.Provider = func(context.Context, Model) (Provider, error) {
		return Provider{BaseURL: upstream.URL, Client: upstream.Client()}, nil
	}
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "failed" || p.ChargedMicro != 100000 || p.View().ReleasedMicro != 0 || p.View().ReservedMicro != 0 {
		t.Fatalf("partial failure: %+v", p.View())
	}
	successfulAsset := p.Shots[0].OutputAssetID
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "retry")
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "completed" || p.ChargedMicro != 200000 || submissions.Load() != 3 || p.Shots[0].OutputAssetID != successfulAsset || p.Shots[0].Attempt != 1 || p.Shots[1].Attempt != 2 || team.UsedCredits != "0.200000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("retry result: %+v ledger=%+v calls=%d", p.View(), team, submissions.Load())
	}
}

func TestCanvasInsufficientCreditsNeverCallsProvider(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	zero := "0.000000"
	if _, err := f.s.Repo.Store.UpdateTeam(ctx, f.team, store.UpdateTeamPatch{QuotaCredits: &zero}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "failed" || p.ChargedMicro != 0 || p.View().ReservedMicro != 0 || f.submits.Load() != 0 || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("insufficient: %+v ledger=%+v", p.View(), team)
	}
}

func TestCanvasVideoSettlesPerSecondAndCancellationStopsRemainingOutputs(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var err error
	f.video, err = os.ReadFile("testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	r := CanvasRequest{Kind: "video", ModelID: "v", Prompt: "slow pan", AssetIDs: []string{}, Resolution: "720p", AspectRatio: "16:9", Duration: 5, Count: 2, ExpectedQuoteMicro: 1000000, BoardID: "b", NodeID: "n", WorkspacePath: "/fixture"}
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.View().ReservedMicro != 0 || p.Shots[0].PendingChargeMicro != 500000 || p.Shots[0].ReservationID == "" || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("running task changed balance or lost price: %+v ledger=%+v", p.View(), team)
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	team, _ = f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "cancelled" || p.ChargedMicro != 500000 || p.View().ReservedMicro != 0 || f.submits.Load() != 1 || team.UsedCredits != "0.500000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("cancelled video: %+v ledger=%+v", p.View(), team)
	}
}

// 结算先于项目投影写入；崩溃后重放 saved 阶段仍必须只扣一次，且使用阶段锁价。
func TestCanvasSavedResultRecoverySettlesLockedPriceExactlyOnce(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			r := canvasRequest(t, f)
			r.Count = 1
			r.ExpectedQuoteMicro = 100000
			p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
			if err != nil {
				t.Fatal(err)
			}
			for range 8 {
				f.s.Tick(ctx)
				p = current(t, f, p)
				if p.Shots[0].Status == "image_saved" {
					break
				}
			}
			if p.Shots[0].Status != "image_saved" {
				t.Fatalf("not saved: %+v", p.Shots[0])
			}
			team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
			if team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
				t.Fatalf("premature debit: %+v", team)
			}
			originalID := p.Shots[0].ReservationID
			if legacy {
				p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
					v.Shots[0].ReservedMicro = v.Shots[0].PendingChargeMicro
					v.Shots[0].PendingChargeMicro = 0
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			m, err := FindModel(p.Models, r.ModelID, "image")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.Repo.Store.SettleReservation(ctx, originalID, f.s.usage(p, p.Shots[0], m)); err != nil {
				t.Fatal(err)
			}
			p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				for i := range v.Models {
					v.Models[i].PriceMicro = 900000
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for range 6 {
				f.s.Tick(ctx)
			}
			p = current(t, f, p)
			team, _ = f.s.Repo.Store.GetTeam(ctx, f.team)
			if p.Status != "completed" || p.ChargedMicro != 100000 || p.Shots[0].ChargedMicro != 100000 || p.Shots[0].PendingChargeMicro != 0 || p.Shots[0].ReservationID != "" || p.View().ReservedMicro != 0 || p.View().ReleasedMicro != 0 || team.UsedCredits != "0.100000" || team.ReservedCredits != "0.000000" || f.submits.Load() != 1 {
				t.Fatalf("recovery: %+v ledger=%+v calls=%d", p, team, f.submits.Load())
			}
		})
	}
}

func TestCanvasUnknownResultReconciliationKeepsIdentityAndSettlesAfterCancel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	f.unknown = true
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	identity := p.Shots[0].ReservationID
	if identity == "" || p.Shots[0].PendingChargeMicro != 100000 || p.Status != "reconciliation_required" {
		t.Fatalf("missing recovery state: %+v", p)
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Reconcile(ctx, f.team, p.ID, Reconciliation{Version: p.Version, Shot: 0, Outcome: "image_result", Value: f.server.URL + "/result.png", Reason: "已核实供应商生成成功"})
	if err != nil {
		t.Fatal(err)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Shots[0].ReservationID != identity || p.Shots[0].PendingChargeMicro != 100000 || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("reconciliation lost identity or changed balance: %+v %+v", p.Shots[0], team)
	}
	for range 10 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	team, _ = f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "cancelled" || p.ChargedMicro != 100000 || p.Shots[0].OutputAssetID == "" || p.Shots[1].Status != "pending" || f.submits.Load() != 1 || team.UsedCredits != "0.100000" || team.ReservedCredits != "0.000000" {
		t.Fatalf("recovered cancellation: %+v ledger=%+v calls=%d", p, team, f.submits.Load())
	}
}

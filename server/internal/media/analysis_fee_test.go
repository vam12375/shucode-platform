package media

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func analysisFeeFixture(t *testing.T) (*fixture, *Project, Plan) {
	t.Helper()
	f := setup(t)
	b, models, assets, plan := replicaFixture()
	for _, a := range assets {
		if err := f.s.Repo.AddAsset(context.Background(), f.team, a); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(context.Background(), ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Create(context.Background(), f.team, f.user, "", uuid.NewString(), b)
	if err != nil {
		t.Fatal(err)
	}
	return f, p, plan
}

func TestReferenceAnalysisChargesLockedTotalExactlyOnce(t *testing.T) {
	f, p, plan := analysisFeeFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.AnalysisFee == nil || p.AnalysisFee.Micro != 1000000 || team.UsedCredits != "0.000000" || team.ReservedCredits != "0.000000" {
		t.Fatal("analysis admission changed balance or did not freeze price")
	}
	oldVersion := p.Version
	if err := f.s.Repo.Store.SetConfigValue(ctx, ReferencePricingKey, []byte(`{"snapAnyImportMicro":0,"analysisMicro":3000000}`)); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input", plan)
	if err != nil {
		t.Fatal(err)
	}
	if p.AnalysisChargedMicro != 1000000 || p.ChargedMicro != 0 || p.Status != "review" {
		t.Fatalf("incorrect fixed fee projection: %+v", p)
	}
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, oldVersion, "input", plan); err != nil {
		t.Fatal("same accepted plan must replay safely", err)
	}
	team, _ = f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.UsedCredits != "1.000000" || team.ReservedCredits != "0.000000" {
		t.Fatal("analysis charged more than locked total")
	}
	changed := plan
	changed.Shots = append([]ShotPlan(nil), plan.Shots...)
	changed.Shots[0].Prompt = "different"
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input", changed); !errors.Is(err, ErrConflict) {
		t.Fatal("different result overwrote accepted plan")
	}
}

type failingAnalysisSettlementStore struct {
	store.PlatformStore
	failures atomic.Int32
}

func (s *failingAnalysisSettlementStore) SettleReservation(ctx context.Context, id string, record store.UsageRecord) error {
	if s.failures.Add(-1) >= 0 {
		return errors.New("temporary settlement failure")
	}
	return s.PlatformStore.SettleReservation(ctx, id, record)
}

func TestReferenceAnalysisFailureRetryAndSettlementRecovery(t *testing.T) {
	f, p, plan := analysisFeeFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task1", InputID: "input1", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	firstFee := p.AnalysisFee.ID
	invalid := plan
	invalid.Shots = nil
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input1", invalid); err == nil {
		t.Fatal("invalid plan accepted")
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "analysis-failed")
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, firstFee)
	if identity.Status != store.ReservationReleased {
		t.Fatal("failed analysis retained charge identity")
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task2", InputID: "input2", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input1", plan); !errors.Is(err, ErrConflict) {
		t.Fatal("stale analysis charged")
	}
	faults := &failingAnalysisSettlementStore{PlatformStore: f.s.Repo.Store}
	faults.failures.Store(1)
	f.s.Repo.Store = faults
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input2", plan); err == nil {
		t.Fatal("settlement error hidden")
	}
	pending, _ := f.s.Repo.Get(ctx, f.team, p.ID)
	if pending.Status != "analysis_settling" || pending.Plan == nil {
		t.Fatal("completed analysis lost recovery intent")
	}
	restarted := *f.s
	restarted.Tick(ctx)
	p, _ = f.s.Repo.Get(ctx, f.team, p.ID)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "review" || team.UsedCredits != "1.000000" || p.AnalysisChargedMicro != 1000000 {
		t.Fatalf("analysis did not recover: %s %s", p.Status, team.UsedCredits)
	}
}

func TestReferenceAnalysisRejectsInsufficientBalanceBeforeBinding(t *testing.T) {
	f, p, _ := analysisFeeFixture(t)
	quota := "0.500000"
	_, err := f.s.Repo.Store.UpdateTeam(context.Background(), f.team, store.UpdateTeamPatch{QuotaCredits: &quota})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Bind(context.Background(), f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"}); err == nil || err.Error() != "insufficient_credits" {
		t.Fatalf("analysis admission was not rejected: %v", err)
	}
	p, _ = f.s.Repo.Get(context.Background(), f.team, p.ID)
	if p.Status != "draft" || p.Analysis != nil {
		t.Fatal("denied analysis acquired binding")
	}
}

func TestReferenceAnalysisFreeAutomaticPlanReplaysAfterSettlement(t *testing.T) {
	f, p, plan := analysisFeeFixture(t)
	ctx := context.Background()
	if err := f.s.Repo.Store.SetConfigValue(ctx, ReferencePricingKey, []byte(`{"snapAnyImportMicro":0,"analysisMicro":0}`)); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.ReviewPlan = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	version := p.Version
	p, err = f.s.SetPlan(ctx, f.team, p.ID, version, "input", plan)
	if err != nil || p.Status != "queued" || p.AnalysisChargedMicro != 0 {
		t.Fatalf("free analysis did not settle: %+v %v", p, err)
	}
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, version, "input", plan); err != nil {
		t.Fatal("free accepted plan must replay safely", err)
	}
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, p.AnalysisFee.ID)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if identity.Status != store.ReservationSettled || team.UsedCredits != "0.000000" {
		t.Fatal("free analysis lost its zero-price settlement identity")
	}
}

func TestReferenceAnalysisCancelBeforePlanDoesNotCharge(t *testing.T) {
	f, p, plan := analysisFeeFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
	if err != nil || p.Status != "cancelled" {
		t.Fatalf("cancel failed: %+v %v", p, err)
	}
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input", plan); !errors.Is(err, ErrConflict) {
		t.Fatal("late plan accepted after cancellation")
	}
	f.s.Tick(ctx)
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, p.AnalysisFee.ID)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if identity.Status != store.ReservationReleased || team.UsedCredits != "0.000000" {
		t.Fatal("cancelled analysis charged")
	}
}

func TestReferenceAnalysisConcurrentSettlementRecoveryRespectsCancel(t *testing.T) {
	f, p, plan := analysisFeeFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	faults := &failingAnalysisSettlementStore{PlatformStore: f.s.Repo.Store}
	faults.failures.Store(1)
	f.s.Repo.Store = faults
	if _, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "input", plan); err == nil {
		t.Fatal("settlement failure hidden")
	}
	p, _ = f.s.Repo.Get(ctx, f.team, p.ID)
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
	if err != nil || p.Status != "analysis_settling" || !p.CancelRequested {
		t.Fatalf("accepted analysis lost settlement intent on cancel: %+v %v", p, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := f.s.finishAnalysisFee(ctx, p); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	p, _ = f.s.Repo.Get(ctx, f.team, p.ID)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "cancelled" || p.AnalysisChargedMicro != 1000000 || team.UsedCredits != "1.000000" {
		t.Fatalf("concurrent recovery did not settle accepted plan once: %+v %s", p, team.UsedCredits)
	}
}

func TestReferenceAnalysisHonorsMemberBudgetBeforeBinding(t *testing.T) {
	f, p, _ := analysisFeeFixture(t)
	ctx := context.Background()
	limit := "0.999999"
	if _, err := f.s.Repo.Store.UpdateTeamMember(ctx, f.team, f.user, store.TeamMemberPatch{MonthlyBudgetCredits: &limit}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"}); err == nil || err.Error() != "budget_exhausted" {
		t.Fatalf("analysis bypassed member budget: %v", err)
	}
	p, _ = f.s.Repo.Get(ctx, f.team, p.ID)
	if p.Status != "draft" || p.AnalysisFee != nil {
		t.Fatal("denied analysis acquired fee identity")
	}
}

type failingAnalysisReleaseStore struct {
	store.PlatformStore
	failures atomic.Int32
}

func (s *failingAnalysisReleaseStore) ReleaseReservationByID(ctx context.Context, id string, at time.Time) error {
	if s.failures.Add(-1) >= 0 {
		return errors.New("temporary release failure")
	}
	return s.PlatformStore.ReleaseReservationByID(ctx, id, at)
}

func TestReferenceAnalysisRecoversFailedIdentityRelease(t *testing.T) {
	for _, action := range []string{"cancel", "analysis-failed"} {
		t.Run(action, func(t *testing.T) {
			f, p, _ := analysisFeeFixture(t)
			ctx := context.Background()
			p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
			if err != nil {
				t.Fatal(err)
			}
			feeID := p.AnalysisFee.ID
			faults := &failingAnalysisReleaseStore{PlatformStore: f.s.Repo.Store}
			faults.failures.Store(1)
			f.s.Repo.Store = faults
			if _, err := f.s.Action(ctx, f.team, p.ID, p.Version, action); err == nil {
				t.Fatal("release failure hidden")
			}
			f.s.Tick(ctx)
			identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, feeID)
			if identity.Status != store.ReservationReleased {
				t.Fatal("abandoned analysis identity not recovered")
			}
		})
	}
}

func TestReferenceAnalysisRetryClosesPreviousIdentityBeforeReplacingIt(t *testing.T) {
	f, p, _ := analysisFeeFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{TaskID: "task", InputID: "input", WorkspacePath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	feeID := p.AnalysisFee.ID
	faults := &failingAnalysisReleaseStore{PlatformStore: f.s.Repo.Store}
	faults.failures.Store(2)
	f.s.Repo.Store = faults
	if _, err := f.s.Action(ctx, f.team, p.ID, p.Version, "analysis-failed"); err == nil {
		t.Fatal("release failure hidden")
	}
	p, _ = f.s.Repo.Get(ctx, f.team, p.ID)
	next := Analysis{TaskID: "task2", InputID: "input2", WorkspacePath: "/tmp"}
	if _, err := f.s.Bind(ctx, f.team, p.ID, p.Version, next); err == nil {
		t.Fatal("new attempt overwrote unclosed previous identity")
	}
	p, _ = f.s.Repo.Get(ctx, f.team, p.ID)
	if p.AnalysisFee.ID != feeID {
		t.Fatal("previous identity recovery intent lost")
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, next)
	if err != nil || p.AnalysisFee.ID == feeID {
		t.Fatalf("retry failed after identity release recovered: %+v %v", p, err)
	}
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, feeID)
	if identity.Status != store.ReservationReleased {
		t.Fatal("retry lost prior reserved identity")
	}
}

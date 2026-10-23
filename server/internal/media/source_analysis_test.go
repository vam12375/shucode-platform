package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func sourceFixture(t *testing.T) (*fixture, *Project, SourceAnalysis, Brief, Plan) {
	t.Helper()
	f := setup(t)
	b, models, assets, plan := replicaFixture()
	assets[0].Name = "source.mp4"
	assets[0].Checksum = strings.Repeat("a", 64)
	ctx := context.Background()
	for _, asset := range assets {
		if err := f.s.Repo.AddAsset(ctx, f.team, asset); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreateReferenceAnalysis(ctx, f.team, f.user, uuid.NewString(), uuid.NewString(), assets[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	report := SourceAnalysis{SourceChecksum: assets[0].Checksum, Summary: "人物舞蹈原片", References: plan.References, Shots: []SourceShot{{SourceURL: "", SourceStart: 0, SourceEnd: 1.3, Framing: "全景", Camera: "推进", Action: "舞蹈", Purpose: "开场", Transition: "cut"}}}
	return f, p, report, b, plan
}

func completeSource(t *testing.T, f *fixture, p *Project, report SourceAnalysis) *Project {
	t.Helper()
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "source", TaskID: "task", InputID: "source-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.SetSourceAnalysis(ctx, f.team, p.ID, p.Version, "source-input", report)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSourceFirstAnalysisConfiguresAdaptationAndChargesOnce(t *testing.T) {
	f, p, report, brief, plan := sourceFixture(t)
	ctx := context.Background()
	p = completeSource(t, f, p, report)
	feeID := p.AnalysisFee.ID
	if p.Status != "source_ready" || p.SourceAnalysis == nil || p.Plan != nil || len(p.Shots) != 0 || p.AnalysisChargedMicro != ReferenceAnalysisFeeMicro {
		t.Fatalf("source report requires no generation: %+v", p)
	}
	oldVersion := p.Version
	replay, err := f.s.SetSourceAnalysis(ctx, f.team, p.ID, oldVersion-1, "source-input", report)
	if err != nil || replay.Version != oldVersion {
		t.Fatalf("source terminal replay: %+v %v", replay, err)
	}
	configured, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Status != "draft" || configured.SourceAnalysis == nil || configured.Analysis == nil || *configured.Analysis != *p.Analysis || configured.Brief.SourceOnly {
		t.Fatalf("configuration lost source report: %+v", configured)
	}
	if _, err = f.s.SetSourceAnalysis(ctx, f.team, p.ID, configured.Version, "source-input", report); !errors.Is(err, ErrConflict) {
		t.Fatalf("draft reused a completed source result: %v", err)
	}
	replay, err = f.s.ConfigureReference(ctx, f.team, p.ID, oldVersion, brief)
	if err != nil || replay.Version != configured.Version {
		t.Fatalf("configure replay must not reset binding: %+v %v", replay, err)
	}
	adapted, err := f.s.Bind(ctx, f.team, p.ID, configured.Version, Analysis{Kind: "adaptation", TaskID: configured.Analysis.TaskID, InputID: "adapt-input", WorkspacePath: configured.Analysis.WorkspacePath, WorkspaceIdentity: configured.Analysis.WorkspaceIdentity})
	if err != nil {
		t.Fatal(err)
	}
	if adapted.AnalysisFee.ID != feeID || adapted.Analysis.TaskID != p.Analysis.TaskID || adapted.Analysis.InputID == p.Analysis.InputID {
		t.Fatal("adaptation created second billing identity")
	}
	if _, err = f.s.SetSourceAnalysis(ctx, f.team, p.ID, adapted.Version, "source-input", report); !errors.Is(err, ErrConflict) {
		t.Fatalf("old source result overwrote the same-chat adaptation: %v", err)
	}
	replay, err = f.s.ConfigureReference(ctx, f.team, p.ID, oldVersion, brief)
	if err != nil || replay.Analysis == nil || replay.Analysis.InputID != "adapt-input" {
		t.Fatal("configure replay destroyed current adaptation input", err)
	}
	p, err = f.s.SetPlan(ctx, f.team, p.ID, adapted.Version, "adapt-input", plan)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "review" || p.SourceAnalysis.Summary != report.Summary || p.AnalysisChargedMicro != ReferenceAnalysisFeeMicro {
		t.Fatalf("adaptation not ready: %+v", p)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.UsedCredits != "1.000000" || f.submits.Load() != 0 {
		t.Fatalf("analysis was charged twice or invoked generation: %+v", team)
	}
}

func TestSourceFirstRejectsInvalidFactsKindsStaleInputsAndChangedBoundaries(t *testing.T) {
	for _, kind := range []string{"checksum", "empty-summary", "gap", "missing-end", "wrong-source", "wrong-kind"} {
		t.Run(kind, func(t *testing.T) {
			f, p, report, _, _ := sourceFixture(t)
			ctx := context.Background()
			var err error
			p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "source", TaskID: "task", InputID: "source-input", WorkspacePath: "/fixture"})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "checksum":
				report.SourceChecksum = strings.Repeat("b", 64)
			case "empty-summary":
				report.Summary = ""
			case "gap":
				report.Shots[0].SourceStart = .1
			case "missing-end":
				report.Shots[0].SourceEnd = 1.2
			case "wrong-source":
				report.Shots[0].SourceURL = "https://example.test/other"
			case "wrong-kind":
				p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Analysis.Kind = "adaptation"; return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.s.SetSourceAnalysis(ctx, f.team, p.ID, p.Version, "source-input", report); err == nil {
				t.Fatal("invalid source report accepted")
			}
		})
	}
	f, p, report, brief, plan := sourceFixture(t)
	ctx := context.Background()
	if _, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "task", InputID: "input", WorkspacePath: "/fixture"}); err == nil {
		t.Fatal("adaptation before source accepted")
	}
	p = completeSource(t, f, p, report)
	changed := brief
	changed.ReferenceAssetID = "other"
	if _, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, changed); err == nil {
		t.Fatal("source changed during configure")
	}
	p, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "task", InputID: "adapt", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "source-input", plan); !errors.Is(err, ErrConflict) {
		t.Fatal("old source input submitted generation plan")
	}
	changedPlan := plan
	changedPlan.Shots = append([]ShotPlan(nil), plan.Shots...)
	changedPlan.Shots[0].SourceEnd = 1.2
	if _, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "adapt", changedPlan); err == nil {
		t.Fatal("adaptation changed source shot boundary")
	}
}

func TestSourceFirstScopeSharesFeeIdentityAndRequiresCorrectSettlementPhase(t *testing.T) {
	f, p, report, brief, _ := sourceFixture(t)
	ctx := context.Background()
	box, err := crypto.NewSecretBox(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("t", 32))))
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "source", TaskID: "task", InputID: "source-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ticket, err := IssueAnalysisScope(ctx, f.s.Repo, box, p, "deepseek-fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "deepseek-fixture", now, "high"); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.SetSourceAnalysis(ctx, f.team, p.ID, p.Version, "source-input", report)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "deepseek-fixture", now, "high"); err == nil {
		t.Fatal("completed source scope stayed valid")
	}
	p, err = f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "adapt", InputID: "adapt-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err = IssueAnalysisScope(ctx, f.s.Repo, box, p, "deepseek-fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "deepseek-fixture", now, "medium"); err != nil {
		t.Fatalf("adaptation must preserve its legal initialized reasoning: %v", err)
	}
	budgetKey := "media:analysis-scope-budget:" + p.TeamID + ":" + p.AnalysisFee.ID
	if err = f.s.Repo.Store.SetConfigValue(ctx, budgetKey, []byte(`{"requests":128,"tokens":1000000}`)); err != nil {
		t.Fatal(err)
	}
	if err = ValidateAnalysisScope(ctx, f.s.Repo, box, ticket, p.TeamID, p.UserID, p.DeviceID, "deepseek-fixture", now, "high"); err != nil {
		t.Fatalf("legacy fee budget blocked adaptation authorization: %v", err)
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "analysis-failed")
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, p.AnalysisFee.ID)
	if identity.Status != store.ReservationSettled || p.AnalysisChargedMicro != ReferenceAnalysisFeeMicro {
		t.Fatal("adaptation failure refunded successful source report")
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "retry", InputID: "retry-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if p.AnalysisFee.ID != identity.ID {
		t.Fatal("retry created new fee")
	}
}

func TestSourceReportSettlementRecoversWithoutGenerationPlan(t *testing.T) {
	f, p, report, _, _ := sourceFixture(t)
	ctx := context.Background()
	p, err := f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "source", TaskID: "source", InputID: "input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	faults := &failingAnalysisSettlementStore{PlatformStore: f.s.Repo.Store}
	faults.failures.Store(1)
	f.s.Repo.Store = faults
	if _, err = f.s.SetSourceAnalysis(ctx, f.team, p.ID, p.Version, "input", report); err == nil {
		t.Fatal("settlement failure hidden")
	}
	pending, err := f.s.Repo.Get(ctx, f.team, p.ID)
	if err != nil || pending.Status != "analysis_settling" || pending.SourceAnalysis == nil || pending.Plan != nil {
		t.Fatal("source report recovery intent lost", pending, err)
	}
	restarted := *f.s
	restarted.Tick(ctx)
	restored, err := f.s.Repo.Get(ctx, f.team, p.ID)
	if err != nil || restored.Status != "source_ready" || restored.SourceAnalysis.SourceChecksum != report.SourceChecksum {
		t.Fatal("source report settlement not recovered", restored, err)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.UsedCredits != "1.000000" {
		t.Fatal("source recovery charged incorrectly", team)
	}
}

func TestZeroPriceSourceAndAdaptationReuseSettledIdentity(t *testing.T) {
	f, p, report, brief, plan := sourceFixture(t)
	ctx := context.Background()
	if err := f.s.Repo.Store.SetConfigValue(ctx, ReferencePricingKey, []byte(`{"snapAnyImportMicro":0,"analysisMicro":0}`)); err != nil {
		t.Fatal(err)
	}
	p = completeSource(t, f, p, report)
	feeID := p.AnalysisFee.ID
	p, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "adapt", InputID: "adapt-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "analysis-failed")
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "retry", InputID: "retry-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "retry-input", plan)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, feeID)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if identity.Status != store.ReservationSettled || p.AnalysisFee.ID != feeID || p.Status != "review" || team.UsedCredits != "0.000000" {
		t.Fatal("zero price changed settled identity", p, identity, team)
	}
}

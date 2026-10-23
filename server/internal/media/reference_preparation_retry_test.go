package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func referencePreparationFixture(t *testing.T) (*fixture, *Project, Plan) {
	t.Helper()
	f := setup(t)
	brief, models, plan := wholeReferenceFixture()
	report := &SourceAnalysis{SourceChecksum: strings.Repeat("a", 64), Summary: "合成原片事实", References: plan.References}
	for _, shot := range plan.Shots {
		report.Shots = append(report.Shots, SourceShot{SourceURL: shot.SourceURL, SourceStart: shot.SourceStart, SourceEnd: shot.SourceEnd, Framing: shot.Framing, Camera: shot.Camera, Action: shot.Action, Purpose: shot.Purpose})
	}
	shots, quote, err := BuildShots(brief, plan, models)
	if err != nil {
		t.Fatal(err)
	}
	fee := newFixedFee(f.team, f.user, "synthetic-source-analysis", "media:analysis", ReferenceAnalysisFeeMicro)
	ctx := context.Background()
	if err = f.s.admitFixedFee(ctx, f.team, f.user, fee); err != nil {
		t.Fatal(err)
	}
	if err = f.s.settleFixedFee(ctx, f.team, f.user, "", fee); err != nil {
		t.Fatal(err)
	}
	p := &Project{ID: uuid.NewString(), Version: 1, Status: "analyzing", TeamID: f.team, UserID: f.user, Brief: brief, Models: models, SourceChecksum: report.SourceChecksum, SourceAnalysis: report, Analysis: &Analysis{Kind: "adaptation", TaskID: "adapt-task", InputID: "adapt-input", WorkspacePath: "/fixture"}, AnalysisFee: &fee, AnalysisChargedMicro: fee.Micro, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), QuoteMicro: quote, Shots: shots}
	if err = f.s.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.Index(ctx, ProjectKey(f.team, p.ID)); err != nil {
		t.Fatal(err)
	}
	return f, p, plan
}

func TestAnalysisPlanRemovesExecutionClipBindingsBeforeAcceptAndReplay(t *testing.T) {
	f, p, plan := referencePreparationFixture(t)
	for i := range plan.Shots {
		plan.Shots[i].ReferenceVideoAssetID = "model-original-id"
	}
	ctx := context.Background()
	accepted, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "adapt-input", plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, shot := range accepted.Plan.Shots {
		if shot.ReferenceVideoAssetID != "" {
			t.Fatal("model-owned analysis persisted an execution clip binding")
		}
	}
	if accepted.Shots[0].Plan.ReferenceVideoAssetID != "" || accepted.Shots[0].ReferenceVideoAssetID != "" || plan.Shots[0].ReferenceVideoAssetID != "model-original-id" {
		t.Fatal("analysis polluted execution or mutated the caller plan")
	}
	// 分析重放不拥有后续 Host 绑定；比较脚本时忽略执行字段，但不能清除已校验的绑定。
	accepted, err = f.s.Mutate(ctx, f.team, p.ID, accepted.Version, func(v *Project) error {
		for i := range v.Plan.Shots {
			v.Plan.Shots[i].ReferenceVideoAssetID = "host-uploaded-id"
		}
		v.Shots[0].Plan.ReferenceVideoAssetID = "host-uploaded-id"
		v.Shots[0].ReferenceVideoAssetID = "host-uploaded-id"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan.Shots {
		plan.Shots[i].ReferenceVideoAssetID = "different-model-id"
	}
	replay, err := f.s.SetPlan(ctx, f.team, p.ID, p.Version, "adapt-input", plan)
	if err != nil || !reflect.DeepEqual(replay, accepted) {
		t.Fatalf("normalized analysis replay changed platform clip facts: %v", err)
	}
	plan.Shots[0].Prompt = "changed script"
	if _, err = f.s.SetPlan(ctx, f.team, p.ID, accepted.Version, "adapt-input", plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("normalization admitted a changed script: %v", err)
	}
}

func failedReferencePreparation(t *testing.T, code string) (*fixture, *Project) {
	t.Helper()
	f, p, plan := referencePreparationFixture(t)
	for i := range plan.Shots {
		plan.Shots[i].ReferenceVideoAssetID = "public-original-id"
	}
	p.Plan = &plan
	p.Status, p.Error = "failed", code
	p.Shots[0].Status, p.Shots[0].Error, p.Shots[0].Attempt = "failed", code, 3
	p.Shots[0].Plan.ReferenceVideoAssetID, p.Shots[0].ReferenceVideoAssetID = "public-original-id", "public-original-id"
	if code == "video_reference_clip_required" {
		for i := range p.Plan.Shots {
			p.Plan.Shots[i].ReferenceVideoAssetID = ""
		}
		p.Shots[0].Plan.ReferenceVideoAssetID, p.Shots[0].ReferenceVideoAssetID = "", ""
	}
	previous, err := f.s.Repo.Get(context.Background(), f.team, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.Save(context.Background(), p, previous); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Repo.Get(context.Background(), f.team, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

func TestReferencePreparationRetryReturnsSameFrozenProjectToReview(t *testing.T) {
	for _, code := range []string{"video_reference_clip_invalid", "video_reference_clip_required"} {
		t.Run(code, func(t *testing.T) {
			f, p := failedReferencePreparation(t, code)
			ctx := context.Background()
			recovered, err := f.s.Action(ctx, f.team, p.ID, p.Version, "retry")
			if err != nil || recovered.Status != "review" || recovered.Error != "" || recovered.Shots[0].Status != "pending" || recovered.Shots[0].Error != "" || recovered.Shots[0].Attempt != 4 || recovered.Version != p.Version+1 {
				t.Fatalf("safe attempt 3 did not return to review: %+v %v", recovered, err)
			}
			if recovered.Shots[0].ReferenceVideoAssetID != "" || recovered.Shots[0].Plan.ReferenceVideoAssetID != "" {
				t.Fatal("execution binding was retained")
			}
			for _, shot := range recovered.Plan.Shots {
				if shot.ReferenceVideoAssetID != "" {
					t.Fatal("review binding was retained")
				}
			}
			if recovered.ID != p.ID || !reflect.DeepEqual(recovered.Brief, p.Brief) || !reflect.DeepEqual(recovered.SourceAnalysis, p.SourceAnalysis) || !reflect.DeepEqual(recovered.Analysis, p.Analysis) || !reflect.DeepEqual(recovered.AnalysisFee, p.AnalysisFee) || !reflect.DeepEqual(recovered.Models, p.Models) || recovered.SourceChecksum != p.SourceChecksum || recovered.QuoteMicro != p.QuoteMicro || recovered.Shots[0].QuoteMicro != p.Shots[0].QuoteMicro || recovered.AnalysisChargedMicro != p.AnalysisChargedMicro {
				t.Fatal("recovery changed frozen source, analysis, prices or models")
			}
			identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, p.AnalysisFee.ID)
			team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
			if identity.Status != store.ReservationSettled || team.UsedCredits != "1.000000" || team.ReservedCredits != "0.000000" || f.submits.Load() != 0 {
				t.Fatal("reference preparation recovery altered billing or submitted generation")
			}
			if _, err = f.s.Action(ctx, f.team, p.ID, p.Version, "retry"); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale retry replay mutated review: %v", err)
			}
		})
	}
}

func TestReferencePreparationRetryRejectsUnsafeOrIncompleteExecution(t *testing.T) {
	cases := map[string]func(*Project){
		"cancelled":             func(p *Project) { p.CancelRequested = true },
		"canvas":                func(p *Project) { p.Canvas = &CanvasRequest{} },
		"multiple-executions":   func(p *Project) { p.Shots = append(p.Shots, p.Shots[0]) },
		"wrong-mode":            func(p *Project) { p.Brief.GenerationMode = "storyboard" },
		"missing-source":        func(p *Project) { p.SourceAnalysis = nil },
		"missing-fee":           func(p *Project) { p.AnalysisFee = nil },
		"unknown-fee":           func(p *Project) { p.AnalysisFee.ID = "unsettled-fee" },
		"unsettled-fee":         func(p *Project) { p.AnalysisChargedMicro = 0 },
		"changed-source":        func(p *Project) { p.SourceChecksum = strings.Repeat("b", 64) },
		"incomplete-source":     func(p *Project) { p.SourceAnalysis.Shots[0].SourceStart = .1 },
		"missing-plan":          func(p *Project) { p.Plan = nil },
		"changed-plan-boundary": func(p *Project) { p.Plan.Shots[0].SourceEnd = 4 },
		"partial-execution":     func(p *Project) { p.Shots[0].Plan.SourceEnd = 4 },
		"changed-execution":     func(p *Project) { p.Shots[0].Plan.Prompt = "different input" },
		"task":                  func(p *Project) { p.Shots[0].VideoTaskID = "supplier-task" },
		"diagnostic":            func(p *Project) { p.Shots[0].ProviderError = &ProviderError{Code: "InvalidParameter"} },
		"image":                 func(p *Project) { p.Shots[0].ImageAssetID = "generated-image" },
		"output":                func(p *Project) { p.Shots[0].OutputAssetID = "output" },
		"result":                func(p *Project) { p.Shots[0].ResultURL = "https://example.test/result.mp4" },
		"project-output":        func(p *Project) { p.OutputAssetID = "output" },
		"charged":               func(p *Project) { p.Shots[0].ChargedMicro = 1 },
		"project-charged":       func(p *Project) { p.ChargedMicro = 1 },
		"reservation":           func(p *Project) { p.Shots[0].ReservationID = "admission" },
		"pending":               func(p *Project) { p.Shots[0].PendingChargeMicro = 1 },
		"reserved":              func(p *Project) { p.Shots[0].ReservedMicro = 1 },
		"released":              func(p *Project) { p.Shots[0].ReleasedMicro = 1 },
		"stage":                 func(p *Project) { p.Shots[0].Stage = "video" },
		"tokens":                func(p *Project) { p.Shots[0].ProviderUsageTokens = 1 },
		"token-charge":          func(p *Project) { p.Shots[0].TokenChargeMicro = 1 },
		"token-rate":            func(p *Project) { p.Shots[0].TokenRateMicroPerMillion = 1 },
		"token-cost":            func(p *Project) { p.Shots[0].TokenCostMicroPerMillion = 1 },
		"unknown":               func(p *Project) { p.Status, p.Shots[0].Status = "reconciliation_required", "reconciliation_required" },
		"running":               func(p *Project) { p.Status = "running" },
		"queued":                func(p *Project) { p.Status = "queued" },
		"mismatched-error":      func(p *Project) { p.Shots[0].Error = "provider_rejected" },
		"lease":                 func(p *Project) { p.LeaseOwner, p.LeaseUntil = "worker", time.Now().UTC().Add(time.Minute) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f, p := failedReferencePreparation(t, "video_reference_clip_invalid")
			ctx := context.Background()
			p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { mutate(v); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.Action(ctx, f.team, p.ID, p.Version, "retry"); !errors.Is(err, ErrConflict) {
				t.Fatalf("unsafe reference recovery accepted: %v", err)
			}
			latest, _ := f.s.Repo.Get(ctx, f.team, p.ID)
			if !reflect.DeepEqual(latest, p) {
				t.Fatal("rejected recovery changed persisted state")
			}
		})
	}
}

func TestReferencePreparationRetryCASAllowsOneRecovery(t *testing.T) {
	f, p := failedReferencePreparation(t, "video_reference_clip_invalid")
	var success, conflict atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, err := f.s.Action(context.Background(), f.team, p.ID, p.Version, "retry")
			if err == nil {
				success.Add(1)
			} else if errors.Is(err, ErrConflict) {
				conflict.Add(1)
			} else {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if success.Load() != 1 || conflict.Load() != 1 {
		t.Fatalf("recovery was not atomic: successes=%d conflicts=%d", success.Load(), conflict.Load())
	}
}

func TestSupplierFailureRetryStillQueuesExistingReferencePlan(t *testing.T) {
	f, p := failedReferencePreparation(t, "provider_rejected")
	retried, err := f.s.Action(context.Background(), f.team, p.ID, p.Version, "retry")
	if err != nil || retried.Status != "queued" || retried.Shots[0].Attempt != 4 || retried.Shots[0].ReferenceVideoAssetID != p.Shots[0].ReferenceVideoAssetID || !reflect.DeepEqual(retried.Plan, p.Plan) {
		t.Fatalf("ordinary supplier retry changed behavior: %+v %v", retried, err)
	}
}

func TestReferencePreparationRetryPreservesAttemptThroughClipBinding(t *testing.T) {
	for _, editScript := range []bool{false, true} {
		t.Run(map[bool]string{false: "binding-only", true: "edited-script"}[editScript], func(t *testing.T) {
			f, p := failedReferencePreparation(t, "video_reference_clip_invalid")
			ctx := context.Background()
			video, err := os.ReadFile("testdata/blue.mp4")
			if err != nil {
				t.Fatal(err)
			}
			sourceID := uuid.NewString()
			sourcePath := f.s.Blobs.Path(f.team, sourceID)
			if err = os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(sourcePath, video, 0600); err != nil {
				t.Fatal(err)
			}
			checksum, err := checksumFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: sourceID, Name: "synthetic-source.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len(video)), DurationSeconds: p.Brief.ReferenceDurationSeconds, Checksum: checksum}); err != nil {
				t.Fatal(err)
			}
			p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Brief.ReferenceAssetID, v.SourceChecksum, v.SourceAnalysis.SourceChecksum = sourceID, checksum, checksum
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "retry")
			if err != nil || p.Shots[0].Attempt != 4 {
				t.Fatalf("reference retry failed: %v", err)
			}
			f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
				return ReferenceMetadata{DurationSeconds: p.Brief.ReferenceDurationSeconds, Width: 160, Height: 90}, nil
			}
			clip, err := f.s.UploadReferenceClip(ctx, f.team, sourceID, checksum, 0, p.Brief.ReferenceDurationSeconds, bytes.NewReader(video))
			if err != nil {
				t.Fatal(err)
			}
			plan := *p.Plan
			plan.Shots = append([]ShotPlan(nil), plan.Shots...)
			for i := range plan.Shots {
				plan.Shots[i].ReferenceVideoAssetID = clip.ID
			}
			wantAttempt := 4
			if editScript {
				plan.Shots[0].Prompt = "显式编辑新脚本"
				wantAttempt = 1
			}
			p, err = f.s.Revise(ctx, f.team, p.ID, p.Version, plan)
			if err != nil || p.Shots[0].Attempt != wantAttempt || p.Shots[0].ReferenceVideoAssetID != clip.ID {
				t.Fatalf("clip binding reset attempt or skipped validation: got=%d want=%d err=%v", p.Shots[0].Attempt, wantAttempt, err)
			}
			p, err = f.s.Action(ctx, f.team, p.ID, p.Version, "generate")
			if err != nil || p.Status != "queued" || p.Shots[0].Attempt != wantAttempt || f.submits.Load() != 0 {
				t.Fatalf("generate lost attempt or submitted inline: %v", err)
			}
			f.s.Tick(ctx)
			p = current(t, f, p)
			if p.Shots[0].Status != "video_running" || p.Shots[0].Attempt != wantAttempt || f.submits.Load() != 1 {
				t.Fatalf("prepared reference did not submit once: status=%s attempt=%d submits=%d", p.Shots[0].Status, p.Shots[0].Attempt, f.submits.Load())
			}
		})
	}
}

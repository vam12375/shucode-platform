package media

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRetryModelSwitchPreservesDurationAndRequiresNewPlanReview(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	models, _ := f.s.Repo.Models(ctx)
	old := models[0]
	old.ID = "long"
	old.MinDuration = 4
	old.MaxDuration = 30
	models = append(models, old)
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p := f.project(t)
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "failed"
		v.Brief.Duration = 30
		v.Brief.VideoModel = "long"
		v.Analysis = &Analysis{TaskID: "task", InputID: "old-input", WorkspacePath: "/fixture"}
		v.Plan = &Plan{Shots: []ShotPlan{{Duration: 30, Prompt: "完整创意"}}}
		v.Shots = []Shot{{Plan: v.Plan.Shots[0], Status: "failed", Attempt: 1}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.s.RetryWithModel(ctx, f.team, p.ID, p.Version, RetrySettings{VideoModel: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "draft" || next.Brief.Duration != 30 || next.Plan != nil || len(next.Shots) != 0 || next.RetryContext == nil || next.Analysis.TaskID != "task" {
		t.Fatalf("invalid retry state %+v", next)
	}
	if got := planImageGenerationDurations(next.Brief, next.Models[0]); !reflect.DeepEqual(got, []int{15, 15}) {
		t.Fatal(got)
	}
	if next.Brief.GenerationPolicy != "" {
		t.Fatal("legacy generation contract was silently upgraded")
	}
	if _, err = f.s.RetryWithModel(ctx, f.team, p.ID, p.Version, RetrySettings{VideoModel: "v"}); err == nil {
		t.Fatal("stale version accepted")
	}
}

func TestRetryModelLegacyThirtySecondsKeepsContractAndRequiresAtLeastTwoSegments(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	original := *p.Plan
	first := original.Shots[0]
	first.Duration, first.SourceEnd = 30, 30
	original.Shots = []ShotPlan{first}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status, v.Brief.Duration = "failed", 30
		v.Plan = &original
		v.Shots = []Shot{{Plan: first, Status: "failed", Attempt: 1}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.s.RetryWithModel(ctx, f.team, p.ID, p.Version, RetrySettings{VideoModel: "v"})
	if err != nil {
		t.Fatal(err)
	}
	next, err = f.s.Bind(ctx, f.team, next.ID, next.Version, Analysis{Kind: "planning", TaskID: next.Analysis.TaskID, WorkspacePath: next.Analysis.WorkspacePath, InputID: "legacy-replan"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetPlan(ctx, f.team, next.ID, next.Version, "legacy-replan", original); err == nil {
		t.Fatal("new 15-second model accepted one 30-second generation")
	}
	plan := original
	first.Duration, first.SourceEnd, first.Prompt = 15, 15, "开场动作完整结束"
	second := first
	second.SourceStart, second.SourceEnd, second.Prompt = 15, 30, "承接起态并回应开场"
	plan.Shots = []ShotPlan{first, second}
	next, err = f.s.SetPlan(ctx, f.team, next.ID, next.Version, "legacy-replan", plan)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "review" || len(next.Shots) != 2 || next.Brief.Duration != 30 || next.Brief.GenerationPolicy != "" {
		t.Fatalf("legacy replan changed contract or delivery: %+v", next)
	}
}

func TestRetryModelPreservesSuccessfulShotAndRejectsMutation(t *testing.T) {
	success := Shot{Status: "succeeded", OutputAssetID: "saved", Attempt: 1, ChargedMicro: 100, QuoteMicro: 100, Plan: ShotPlan{Duration: 15, Prompt: "开场完整动作", Creative: &ShotCreative{InFrame: 0, OutFrame: 450}}}
	pending := ShotPlan{Duration: 15, Prompt: "结尾回应开场", Creative: &ShotCreative{InFrame: 0, OutFrame: 450}}
	p := &Project{RetryContext: &RetryContext{Plan: Plan{Shots: []ShotPlan{success.Plan, pending}}, SuccessfulShotIndices: []int{0}}, RetryState: &retryState{Shots: []Shot{success, {Plan: pending, Status: "failed", Attempt: 1}}}}
	plans := []ShotPlan{success.Plan, pending}
	shots := []Shot{{Plan: success.Plan, QuoteMicro: 200}, {Plan: pending, QuoteMicro: 200}}
	reused, total, err := preserveRetrySuccesses(p, Plan{Shots: plans}, shots)
	if err != nil || total != 300 || reused[0].OutputAssetID != "saved" || reused[0].ChargedMicro != 100 || reused[1].Attempt != 2 {
		t.Fatalf("not preserved: %+v %d %v", reused, total, err)
	}
	changed := append([]ShotPlan(nil), plans...)
	changed[0].Prompt = "changed"
	if _, _, err = preserveRetrySuccesses(p, Plan{Shots: changed}, shots); err == nil {
		t.Fatal("changed success accepted")
	}
}

func TestRetryModelRejectsUnresolvedIdentityAndActiveLease(t *testing.T) {
	for _, kind := range []string{"identity", "lease"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			p := f.project(t)
			p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Status = "failed"
				v.Analysis = &Analysis{TaskID: "task", InputID: "old", WorkspacePath: "/fixture"}
				if kind == "identity" {
					v.Shots[0].ReservationID = "unknown"
				} else {
					v.LeaseOwner = "worker"
					v.LeaseUntil = time.Now().Add(time.Minute)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.RetryWithModel(ctx, f.team, p.ID, p.Version, RetrySettings{VideoModel: "v"}); err == nil {
				t.Fatal("unsafe model switch accepted")
			}
		})
	}
}

func TestRetryModelNewPlanningInputWaitsForReviewAndReusesPaidOutput(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	brief, original, models := wholeDurationFixture([]int{30, 15}, 30)
	brief.Finishing = &FinishingOptions{Subtitles: "none"}
	brief.VideoModel = models[0].ID
	brief.AssetIDs = []string{"a"}
	models[0].ID = "new-short"
	models[0].MinDuration, models[0].MaxDuration = 4, 15
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	// Use the fixture's original complete asset/research metadata rather than manufacturing a second plan schema.
	fixtureBrief, _, _ := wholeDurationFixture([]int{30, 15}, 30)
	brief.AssetIDs = fixtureBrief.AssetIDs
	for _, id := range brief.AssetIDs {
		if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: id, Kind: "image", Name: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	success := Shot{Plan: original.Shots[0], Status: "succeeded", Attempt: 1, OutputAssetID: "paid-output", ChargedMicro: 300, QuoteMicro: 300}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "failed"
		v.Brief = brief
		v.Brief.VideoModel = "old-long"
		v.Plan = &original
		v.Shots = []Shot{success, {Plan: original.Shots[1], Status: "failed", Attempt: 1}}
		v.ChargedMicro = 300
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.s.RetryWithModel(ctx, f.team, p.ID, p.Version, RetrySettings{VideoModel: "new-short"})
	if err != nil {
		t.Fatal(err)
	}
	next, err = f.s.Bind(ctx, f.team, next.ID, next.Version, Analysis{Kind: "planning", TaskID: next.Analysis.TaskID, WorkspacePath: next.Analysis.WorkspacePath, InputID: "new-input"})
	if err != nil {
		t.Fatal(err)
	}
	next, err = f.s.FailAnalysis(ctx, f.team, next.ID, next.Version, "new-input", "analysis_failed")
	if err != nil {
		t.Fatal(err)
	}
	if next.RetryState == nil || next.RetryState.Shots[0].OutputAssetID != "paid-output" {
		t.Fatal("failed planning lost successful output")
	}
	next, err = f.s.Bind(ctx, f.team, next.ID, next.Version, Analysis{Kind: "planning", TaskID: next.Analysis.TaskID, WorkspacePath: next.Analysis.WorkspacePath, InputID: "recovered-input"})
	if err != nil {
		t.Fatal(err)
	}
	next, err = f.s.acceptAnalysisPlan(ctx, f.team, next.ID, next.Version, "recovered-input", original)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "review" || next.Shots[0].OutputAssetID != "paid-output" || next.Shots[0].ChargedMicro != 300 || next.Shots[1].Attempt != 2 || next.ChargedMicro != 300 {
		t.Fatalf("retry lost settlement/review %+v", next)
	}
	// A repeated current input is read recovery and does not reset the saved success or attempt.
	replay, err := f.s.acceptAnalysisPlan(ctx, f.team, next.ID, next.Version, "recovered-input", original)
	if err != nil || replay.Version != next.Version {
		t.Fatalf("replay %v %+v", err, replay)
	}
	// 编辑审阅时同样走 retry-aware 校验：旧成功 30 秒镜头不被新模型 15 秒能力误拒。
	revisedPlan := original
	revisedPlan.Shots = append([]ShotPlan(nil), original.Shots...)
	revisedPlan.Shots[1].Prompt = "编辑后的结尾回应开场"
	next, err = f.s.Revise(ctx, f.team, next.ID, next.Version, revisedPlan)
	if err != nil || next.Status != "review" || next.Shots[0].OutputAssetID != "paid-output" {
		t.Fatalf("retry revise rejected preserved success: %v %+v", err, next)
	}
	if os.Getenv("MENDAO_RETRY_GENERATE_TEST") == "1" {
		next, err = f.s.Action(ctx, f.team, next.ID, next.Version, "generate")
		if err != nil {
			t.Fatal(err)
		}
		if next.Status != "queued" || next.Shots[0].OutputAssetID != "paid-output" {
			t.Fatal("generate discarded paid output")
		}
	}
}

func TestRetryModelDirectThirtySecondsReplansAsTwoIndependentSegments(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	brief, original, models := wholeDurationFixture([]int{30}, 30)
	brief.ImageAnalysisFirst = true
	brief.PromptPolicy = "direct"
	brief.ReferenceMode = "multimodal"
	original.Shots[0].AssetID = ""
	original.Shots[0].ImagePrompt = ""
	models[0].MinDuration, models[0].MaxDuration = 4, 15
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Status = "failed"
		v.Brief = brief
		v.Brief.VideoModel = "long"
		v.Plan = &original
		v.Shots = []Shot{{Plan: original.Shots[0], Status: "failed", Attempt: 1}}
		v.Analysis.Kind = "direct"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.s.RetryWithModel(ctx, f.team, p.ID, p.Version, RetrySettings{VideoModel: models[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if next.Brief.PromptPolicy != "staged" || next.ImageAnalysis != nil {
		t.Fatal("did not convert direct")
	}
	next, err = f.s.Bind(ctx, f.team, next.ID, next.Version, Analysis{Kind: "planning", TaskID: next.Analysis.TaskID, WorkspacePath: next.Analysis.WorkspacePath, InputID: "short-input"})
	if err != nil {
		t.Fatal(err)
	}
	_, replanned, _ := wholeDurationFixture([]int{15, 15}, 15)
	for i := range replanned.Shots {
		replanned.Shots[i].AssetID = ""
		replanned.Shots[i].ImagePrompt = ""
	}
	replanned.Shots[0].Prompt = "开场建立目标，动作完整结束"
	replanned.Shots[1].Prompt = "承接完成态，结尾回应目标"
	next, err = f.s.acceptAnalysisPlan(ctx, f.team, next.ID, next.Version, "short-input", replanned)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "review" || len(next.Shots) != 2 || next.Shots[0].Plan.Duration != 15 || next.Shots[1].Plan.Duration != 15 || next.Shots[0].Plan.Prompt == next.Shots[1].Plan.Prompt {
		t.Fatalf("not independently replanned %+v", next)
	}
	if f.submits.Load() != 0 {
		t.Fatal("provider called before quote review")
	}
	if os.Getenv("MENDAO_RETRY_GENERATE_TEST") == "1" {
		next, err = f.s.Action(ctx, f.team, next.ID, next.Version, "generate")
		if err != nil {
			t.Fatal(err)
		}
		if next.Status != "queued" || f.submits.Load() != 0 {
			t.Fatal("generate admission changed")
		}
	}
}

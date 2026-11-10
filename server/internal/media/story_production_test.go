package media

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func storyFixture() (Brief, Plan, []Model) {
	b, p, models := adaptivePlanFixture()
	b.CreativeExpansion = "related"
	b.Finishing = &FinishingOptions{Subtitles: "none"}
	p.Story = &StoryPlan{Goal: "夜间茶饮氛围", Progression: "发现灯光后看见冰茶", Ending: "冰茶落点", AssetRoles: []StoryAssetRole{{AssetID: "a", Role: "product", Facts: "琥珀色茶汤", Retain: "杯形", Allow: "近景"}}, Gaps: []StoryGap{}, Review: &StoryPlanReview{Status: "passed", Issues: []string{}}}
	first := p.Shots[0]
	first.Duration = 6
	first.Creative = &ShotCreative{Source: "extended", AssetIDs: []string{"a"}, Retain: "杯形", Expand: "近景", Beat: "看见茶", Connection: "暖色承接", InFrame: 15, OutFrame: 165}
	second := first
	second.Creative = &ShotCreative{Source: "generated", AssetIDs: []string{}, Retain: "夜色", Expand: "非真实地点的水光过场", Beat: "情绪落点", Connection: "水光", OutFrame: 150}
	p.Shots = []ShotPlan{first, second}
	return b, p, models
}

func TestStoryGenerationSecondsAreSeparateFromDeliveryFrames(t *testing.T) {
	b, p, models := storyFixture()
	shots, quote, err := BuildShots(b, p, models)
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != 2 || quote != mediaVideoQuoteMicro(models[0], b.Resolution, b.AspectRatio, 6)*2 {
		t.Fatalf("generated quote incorrect: %d %+v", quote, shots)
	}
	project := &Project{Brief: b, Plan: &p, Shots: shots}
	segments, err := compositionTimeline(project)
	if err != nil || compositionTotalFrames(segments) != 300 {
		t.Fatalf("delivery clock: %+v %v", segments, err)
	}
	if clip := finishingSourceClip(project, 0); clip.InFrame != 15 || clip.OutFrame != 165 {
		t.Fatalf("source trim lost: %+v", clip)
	}
}

func TestStoryRejectsIllegalInputsAndFrames(t *testing.T) {
	for name, mutate := range map[string]func(*Brief, *Plan){
		"foreign-shot":             func(_ *Brief, p *Plan) { p.Shots[0].Creative.AssetIDs = []string{"foreign"} },
		"generated-with-reference": func(_ *Brief, p *Plan) { p.Shots[1].Creative.AssetIDs = []string{"a"} },
		"invalid-trim":             func(_ *Brief, p *Plan) { p.Shots[0].Creative.OutFrame = 181 },
		"wrong-clock":              func(_ *Brief, p *Plan) { p.Shots[0].Creative.OutFrame-- },
		"strict-extension":         func(b *Brief, _ *Plan) { b.CreativeExpansion = "strict" },
	} {
		t.Run(name, func(t *testing.T) {
			b, p, m := storyFixture()
			mutate(&b, &p)
			if _, _, err := BuildShots(b, p, m); err == nil {
				t.Fatal("invalid story accepted")
			}
		})
	}
}

func TestStoryReviewOnlyFollowsFrozenUserSelection(t *testing.T) {
	b, p, _ := storyFixture()
	if storyNeedsReview(b, p) {
		t.Fatal("passed plan forced review")
	}
	p.Story.Review = nil
	if storyNeedsReview(b, p) {
		t.Fatal("missing independent review blocked production")
	}
	p.Story.Review = &StoryPlanReview{Status: "passed", Issues: []string{}}
	p.Story.Gaps = []StoryGap{{Description: "缺真实布局", Suggestion: "上传店内照片", Resolution: "upload"}}
	if storyNeedsReview(b, p) {
		t.Fatal("upload advice blocked executable plan")
	}
	b.ReviewPlan = true
	if !storyNeedsReview(b, p) {
		t.Fatal("explicit user review lost")
	}
}

func TestStoryCreativeStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"source":"generated","assetIds":[],"retain":"","expand":"","beat":"b","connection":"c","outFrame":150}`, `{"source":"generated","assetIds":[],"retain":"","expand":"","beat":"b","connection":"c","inFrame":null,"outFrame":150}`} {
		var c ShotCreative
		if json.Unmarshal([]byte(raw), &c) == nil {
			t.Fatal("missing/null inFrame accepted")
		}
	}
}

func TestMachineReviewCASIdempotencyAndOutputBinding(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.CreativeExpansion = "related"
		v.Status = "completed"
		v.OutputAssetID = "final-output"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := MachineReviewRequest{Version: p.Version, RequestID: uuid.NewString(), OutputAssetID: p.OutputAssetID, Operation: "start"}
	next, started, err := f.s.MachineReview(ctx, f.team, p.ID, req)
	if err != nil || !started {
		t.Fatalf("start %v %v", started, err)
	}
	replay, started, err := f.s.MachineReview(ctx, f.team, p.ID, req)
	if err != nil || started || replay.Version != next.Version {
		t.Fatalf("replay %v %v", started, err)
	}
	req.Operation = "complete"
	req.Version = next.Version
	req.Review = &MachineReviewResult{Status: "warning", Summary: "动作不明显", Issues: []MachineReviewIssue{{ShotIndex: intPtr(0), Code: "action_missing", Detail: "未见动作", Repair: "regenerate"}}, AudioReview: "not_evaluated"}
	done, err := f.s.CompleteMachineReview(ctx, f.team, p.ID, req)
	if err != nil || done.MachineReview.Status != "warning" {
		t.Fatalf("complete %v", err)
	}
	req.OutputAssetID = "other-output"
	if _, err = f.s.CompleteMachineReview(ctx, f.team, p.ID, req); err == nil {
		t.Fatal("stale output accepted")
	}
}
func intPtr(v int) *int { return &v }

func TestPlanReviewBindsCandidateAndRetriesUnavailableOnlyWithNewRequest(t *testing.T) {
	requireFinishingComposition(t)
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	plan := *p.Plan
	plan.Shots = append([]ShotPlan(nil), p.Plan.Shots...)
	plan.Story = &StoryPlan{Goal: "夜间茶饮", Progression: "灯影到茶饮", Ending: "茶饮落点", AssetRoles: []StoryAssetRole{{AssetID: "a", Role: "product", Facts: "杯中茶", Retain: "杯形", Allow: "近景"}}, Gaps: []StoryGap{}, Review: nil}
	// 本用例依赖 ffmpeg，本地会跳过、只有 CI 真正执行；Brief 切到 ResearchPolicy=auto 后，
	// 候选计划必须自证为同一来源模式：原创研究不允许保留旧参考与镜头来源字段，
	// 否则 SetPlan 会先被 video_research_invalid 拒绝。
	plan.Research = &ResearchDecision{Mode: "original", Reason: "素材足够，按原创执行", IntentSummary: "夜间茶饮氛围", AssetObservations: []AssetObservation{{AssetID: "a", Description: "杯中茶"}}, CreativeGaps: []string{}}
	plan.References = []Reference{}
	for i := range plan.Shots {
		plan.Shots[i].SourceURL, plan.Shots[i].SourceStart, plan.Shots[i].SourceEnd = "", 0, 0
		plan.Shots[i].Creative = &ShotCreative{Source: "asset", AssetIDs: []string{"a"}, Beat: "推进故事", Connection: "承接上镜", InFrame: 0, OutFrame: 150}
	}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.CreativeExpansion = "related"
		v.Brief.ResearchPolicy = "auto"
		v.Brief.ReferenceURLs = []string{}
		v.Brief.Finishing = &FinishingOptions{Subtitles: "none"}
		v.Status = "analyzing"
		v.Plan = nil
		v.Shots = []Shot{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := candidatePlanHash(plan)
	if err != nil {
		t.Fatal(err)
	}
	req := PlanReviewRequest{Version: p.Version, RequestID: uuid.NewString(), InputID: p.Analysis.InputID, CandidateHash: hash, Operation: "start"}
	startedProject, started, err := f.s.PlanReview(ctx, f.team, p.ID, req)
	if err != nil || !started || startedProject.PlanReview == nil || startedProject.PlanReview.Status != "pending" {
		t.Fatalf("start=%v err=%v review=%+v", started, err, startedProject.PlanReview)
	}
	replay, started, err := f.s.PlanReview(ctx, f.team, p.ID, req)
	if err != nil || started || replay.Version != startedProject.Version {
		t.Fatalf("idempotent replay started=%v err=%v version=%d/%d", started, err, replay.Version, startedProject.Version)
	}
	req.Version = startedProject.Version
	req.Operation = "complete"
	req.Review = &PlanReviewResult{Status: "warning", Issues: []string{"桥段缺少真实证据"}}
	completed, _, err := f.s.PlanReview(ctx, f.team, p.ID, req)
	if err != nil || completed.PlanReview.Status != "warning" {
		t.Fatalf("complete err=%v review=%+v", err, completed.PlanReview)
	}
	accepted, err := f.s.SetPlan(ctx, f.team, p.ID, completed.Version, p.Analysis.InputID, plan)
	if err != nil || accepted.Plan == nil || accepted.Plan.Story.Review != nil || accepted.PlanReview == nil || accepted.PlanReview.Status != "warning" {
		t.Fatalf("plan acceptance err=%v", err)
	}
}

func TestPlanReviewUnavailableNeedsExplicitNewRequest(t *testing.T) {
	requireFinishingComposition(t)
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	plan := *p.Plan
	plan.Story = &StoryPlan{Goal: "goal", Progression: "progress", Ending: "ending", AssetRoles: []StoryAssetRole{{AssetID: "a", Role: "product", Facts: "fact"}}, Gaps: []StoryGap{}}
	for i := range plan.Shots {
		plan.Shots[i].Creative = &ShotCreative{Source: "asset", AssetIDs: []string{"a"}, Beat: "beat", InFrame: 0, OutFrame: 150}
	}
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.CreativeExpansion = "related"
		v.Brief.ResearchPolicy = "auto"
		v.Brief.ReferenceURLs = []string{}
		v.Brief.Finishing = &FinishingOptions{Subtitles: "none"}
		v.Status = "analyzing"
		v.Plan, v.Shots = nil, []Shot{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := candidatePlanHash(plan)
	if err != nil {
		t.Fatal(err)
	}
	req := PlanReviewRequest{Version: p.Version, RequestID: uuid.NewString(), InputID: p.Analysis.InputID, CandidateHash: hash, Operation: "start"}
	p, started, err := f.s.PlanReview(ctx, f.team, p.ID, req)
	if err != nil || !started {
		t.Fatalf("start=%v err=%v", started, err)
	}
	req.Version, req.Operation = p.Version, "complete"
	req.Review = &PlanReviewResult{Status: "unavailable", Issues: []string{}}
	p, _, err = f.s.PlanReview(ctx, f.team, p.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Version, req.Operation, req.Review = p.Version, "start", nil
	if _, started, err = f.s.PlanReview(ctx, f.team, p.ID, req); err == nil || started {
		t.Fatalf("same request reused after unknown result: started=%v err=%v", started, err)
	}
	req.RequestID, req.Version = uuid.NewString(), p.Version
	if _, started, err = f.s.PlanReview(ctx, f.team, p.ID, req); err != nil || !started {
		t.Fatalf("explicit retry failed: started=%v err=%v", started, err)
	}
}

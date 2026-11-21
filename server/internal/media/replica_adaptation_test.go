package media

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func replicaPurposeBrief(t *testing.T, brief Brief, purpose string) Brief {
	t.Helper()
	raw, _ := json.Marshal(brief)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["referenceVideoPurpose"] = purpose
	raw, _ = json.Marshal(fields)
	if err := json.Unmarshal(raw, &brief); err != nil {
		t.Fatal(err)
	}
	return brief
}

func replicaAdaptationPlan(t *testing.T, plan Plan, state any) Plan {
	t.Helper()
	raw, _ := json.Marshal(plan)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["adaptation"] = state
	raw, _ = json.Marshal(fields)
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func blockedReplicaPlan(t *testing.T, plan Plan) Plan {
	return replicaAdaptationPlan(t, plan, map[string]any{"status": "blocked", "blockers": []any{map[string]any{"code": "missing_target_structure", "message": "缺少目标内部结构证据", "shotIndices": []int{0}}}})
}

func readyReplicaPlan(t *testing.T, plan Plan) Plan {
	return replicaAdaptationPlan(t, plan, map[string]any{"status": "ready", "blockers": []any{}})
}

func TestReplicaPurposeRequiresExplicitReadinessAndValidScope(t *testing.T) {
	b, models, assets, plan := replicaFixture()
	for _, purpose := range []string{"motion", "preserve"} {
		brief := replicaPurposeBrief(t, b, purpose)
		if err := ValidateBrief(brief, models, assets); err != nil {
			t.Fatal(err)
		}
		if _, _, err := BuildShots(brief, plan, models); err == nil {
			t.Fatal("new purpose accepted missing readiness")
		}
		if _, _, err := BuildShots(brief, readyReplicaPlan(t, plan), models); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"unknown", "auto", "source-only", "no-source", "duplicate-images"} {
		brief := replicaPurposeBrief(t, b, "motion")
		switch kind {
		case "unknown":
			brief = replicaPurposeBrief(t, b, "edit")
		case "auto":
			brief.Mode = "auto"
		case "source-only":
			brief.SourceOnly = true
		case "no-source":
			brief.ReferenceAssetID = ""
		case "duplicate-images":
			brief.AssetIDs = []string{"replacement", "replacement"}
		}
		if ValidateBrief(brief, models, assets) == nil {
			t.Fatalf("invalid purpose scope accepted: %s", kind)
		}
	}
}

func TestReplicaAdaptationStrictWireAndState(t *testing.T) {
	b, models, _, plan := replicaFixture()
	b = replicaPurposeBrief(t, b, "motion")
	raw, _ := json.Marshal(plan)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, state := range []string{
		`{"status":"ready","blockers":[{"code":"missing_target_copy","message":"缺文案"}]}`,
		`{"status":"blocked","blockers":[]}`,
		`{"status":"ready","blockers":null}`,
		`{"status":"ready","blockers":[],"unknown":true}`,
		`{"status":"ready","Blockers":[]}`,
		`{"status":"unknown","blockers":[]}`,
		`{"status":"blocked","blockers":[{"code":"unknown","message":"缺口"}]}`,
		`{"status":"blocked","blockers":[{"code":"missing_target_copy","message":" ","shotIndices":[0]}]}`,
		`{"status":"blocked","blockers":[{"code":"missing_target_copy","message":"缺口","shotIndices":[1]}]}`,
		`{"status":"blocked","blockers":[{"code":"missing_target_copy","message":"缺口","shotIndices":[0,0]}]}`,
		`{"status":"blocked","blockers":[{"code":"missing_target_copy","message":"缺口","shotIndices":null}]}`,
	} {
		fields["adaptation"] = json.RawMessage(state)
		encoded, _ := json.Marshal(fields)
		var current Plan
		if err := json.Unmarshal(encoded, &current); err != nil {
			continue
		}
		if _, _, err := BuildShots(b, current, models); err == nil {
			t.Fatalf("invalid readiness accepted: %s", state)
		}
	}
}

func acceptedBlockedReplica(t *testing.T) (*fixture, *Project, Brief, Plan) {
	t.Helper()
	f, p, report, brief, plan := sourceFixture(t)
	p = completeSource(t, f, p, report)
	brief = replicaPurposeBrief(t, brief, "motion")
	var err error
	p, err = f.s.ConfigureReference(context.Background(), f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(context.Background(), f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "task", InputID: "adapt-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	plan = blockedReplicaPlan(t, plan)
	plan.Shots[0].Prompt = "等待补充素材，暂不执行"
	p, err = f.s.SetPlan(context.Background(), f.team, p.ID, p.Version, "adapt-input", plan)
	if err != nil {
		t.Fatal(err)
	}
	return f, p, brief, plan
}

func TestReplicaBlockedPersistsReviewAndCannotGenerateOrFlipReadiness(t *testing.T) {
	f, p, _, plan := acceptedBlockedReplica(t)
	raw, _ := json.Marshal(p.View())
	if p.Status != "review" || !strings.Contains(string(raw), `"adaptation":{"status":"blocked"`) || len(p.Shots) != 1 || p.QuoteMicro == 0 {
		t.Fatal("blocked review lost plan, readiness or quote")
	}
	ctx := context.Background()
	if _, err := f.s.Action(ctx, f.team, p.ID, p.Version, "generate"); err == nil || err.Error() != "video_replica_adaptation_blocked" {
		t.Fatalf("blocked admitted: %v", err)
	}
	if _, err := f.s.Revise(ctx, f.team, p.ID, p.Version, readyReplicaPlan(t, plan)); err == nil {
		t.Fatal("review changed readiness without reanalysis")
	}
	current, _ := f.s.Repo.Get(ctx, f.team, p.ID)
	if current.Version != p.Version || current.Status != "review" || f.submits.Load() != 0 || current.ChargedMicro != 0 {
		t.Fatal("blocked mutated or submitted")
	}
}

func TestReplicaBlockedReconfigurePreservesSourceTaskAndFee(t *testing.T) {
	f, p, brief, _ := acceptedBlockedReplica(t)
	ctx := context.Background()
	feeID, source, task := p.AnalysisFee.ID, p.SourceAnalysis, p.Analysis.TaskID
	brief.ReplacementInstructions = "改为只展示已有外观，保留运镜"
	configured, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Status != "draft" || configured.Plan != nil || len(configured.Shots) != 0 || configured.QuoteMicro != 0 || !reflect.DeepEqual(configured.SourceAnalysis, source) || configured.Analysis.TaskID != task || configured.AnalysisFee.ID != feeID {
		t.Fatal("blocked reconfiguration lost source binding or retained obsolete execution")
	}
	replay, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil || replay.Version != configured.Version {
		t.Fatal("configure replay recreated analysis", err)
	}
	configured, err = f.s.Bind(ctx, f.team, p.ID, configured.Version, Analysis{Kind: "adaptation", TaskID: task, InputID: "new-adapt", WorkspacePath: "/fixture"})
	if err != nil || configured.AnalysisFee.ID != feeID {
		t.Fatal("reanalysis replaced settled source fee", err)
	}
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if team.UsedCredits != "1.000000" || f.submits.Load() != 0 {
		t.Fatal("reconfiguration charged twice or generated")
	}
}

func TestReplicaLegacyWarningCannotDisappearDuringPlanAcceptance(t *testing.T) {
	f, p, report, brief, plan := sourceFixture(t)
	p = completeSource(t, f, p, report)
	ctx := context.Background()
	var err error
	p, err = f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "task", InputID: "adapt", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	plan.Story = &StoryPlan{Review: &StoryPlanReview{Status: "warning", Issues: []string{"缺少目标内部结构"}}}
	p, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "adapt", plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p.Plan)
	if p.Plan.Story != nil || !strings.Contains(string(raw), `"analysis_requires_review"`) {
		t.Fatal("legacy warning was discarded")
	}
	if _, err = f.s.Action(ctx, f.team, p.ID, p.Version, "generate"); err == nil {
		t.Fatal("legacy blocked warning generated")
	}
}

func TestReplicaPurposeCompilerUsesActualMediaMentions(t *testing.T) {
	for _, purpose := range []string{"motion", "preserve"} {
		b, models, plan := wholeReferenceFixture()
		b = replicaPurposeBrief(t, b, purpose)
		b.ReferenceMode = "multimodal"
		b.AssetIDs = []string{"target-product-id", "target-package-id"}
		models[0].UpstreamModel = "doubao-seedance-2-0-mini-260615"
		plan = readyReplicaPlan(t, plan)
		for i := range plan.Shots {
			plan.Shots[i].AssetID = ""
			plan.Shots[i].ImagePrompt = ""
		}
		shots, _, err := BuildShots(b, plan, models)
		if err != nil {
			t.Fatal(err)
		}
		prompt := shots[0].Plan.Prompt
		for _, want := range []string{"@图片1", "@图片2", "@视频1"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("missing role %s: %s", want, prompt)
			}
		}
		if strings.Contains(prompt, "target-product-id") || strings.Contains(prompt, "target-package-id") {
			t.Fatal("private asset ID became model mention")
		}
		if purpose == "motion" && !strings.Contains(prompt, "不继承原商品") {
			t.Fatal("motion purpose lost its source-content exclusion")
		}
		f := setup(t)
		provider, _ := f.s.Provider(context.Background(), models[0])
		if _, err = provider.SubmitWithReferenceImages(context.Background(), models[0], b, shots[0].Plan, []string{"https://example.test/product.png", "https://example.test/package.png"}, "https://example.test/reference.mp4"); err != nil {
			t.Fatal(err)
		}
		f.requestMu.Lock()
		content := f.requestBodies[0]["content"].([]any)
		if content[0].(map[string]any)["text"] != prompt {
			t.Fatal("compiled purpose did not reach provider")
		}
		if content[1].(map[string]any)["role"] != "reference_image" || content[3].(map[string]any)["role"] != "reference_video" {
			t.Fatal("wire media roles changed")
		}
		if _, ok := f.requestBodies[0]["referenceVideoPurpose"]; ok {
			t.Fatal("unknown provider parameter sent")
		}
		f.requestMu.Unlock()
	}
}

func TestReplicaFrozenWireVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/replica-adaptation.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name            string
		Valid           bool
		GenerationReady bool
		Brief           json.RawMessage
		Plan            json.RawMessage
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	_, models, _ := wholeReferenceFixture()
	models[0].UpstreamModel = "doubao-seedance-2-0-mini-260615"
	models[0].Resolutions = []string{"480p"}
	assets := []Asset{{ID: "source", Kind: "video", DurationSeconds: 5}, {ID: "replacement", Kind: "image"}}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			var brief Brief
			var plan Plan
			err := json.Unmarshal(vector.Brief, &brief)
			if err == nil {
				err = json.Unmarshal(vector.Plan, &plan)
			}
			if err == nil {
				err = ValidateBrief(brief, models, assets)
			}
			if err == nil {
				_, _, err = BuildShots(brief, plan, models)
			}
			if (err == nil) != vector.Valid {
				t.Fatalf("wire acceptance differs from frozen vector: %v", err)
			}
			if err == nil && (validateReplicaGenerationReady(brief, plan) == nil) != vector.GenerationReady {
				t.Fatal("generation readiness differs from frozen vector")
			}
		})
	}
}

func TestReplicaReadyCannotConflictWithLegacyWarnings(t *testing.T) {
	b, _, _, base := replicaFixture()
	for _, kind := range []string{"warning", "upload"} {
		plan := readyReplicaPlan(t, base)
		plan.Story = &StoryPlan{}
		if kind == "warning" {
			plan.Story.Review = &StoryPlanReview{Status: "warning", Issues: []string{"需要补图"}}
		} else {
			plan.Story.Gaps = []StoryGap{{Description: "未知背面", Resolution: "upload", Suggestion: "补图"}}
		}
		if _, err := normalizeReplicaAdaptationPlan(b, plan); err == nil || err.Error() != "video_replica_adaptation_conflict" {
			t.Fatalf("ready overwrote legacy blocker: %v", err)
		}
	}
}

func TestReplicaBlockedReanalysisCannotChangeTaskOrReuseInput(t *testing.T) {
	f, p, brief, _ := acceptedBlockedReplica(t)
	brief.ReplacementInstructions = "只根据目标外观展示"
	p, err := f.s.ConfigureReference(context.Background(), f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	for _, analysis := range []Analysis{
		{Kind: "adaptation", TaskID: "other-task", InputID: "new", WorkspacePath: "/fixture"},
		{Kind: "adaptation", TaskID: "task", InputID: "adapt-input", WorkspacePath: "/fixture"},
		{Kind: "adaptation", TaskID: "task", InputID: "new", WorkspacePath: "/other"},
	} {
		if _, err := f.s.Bind(context.Background(), f.team, p.ID, p.Version, analysis); err == nil {
			t.Fatal("reanalysis changed task or reused old input")
		}
	}
}

func TestReplicaFramesMentionsUseExecutionLocalOrder(t *testing.T) {
	b, models, plan := wholeReferenceFixture()
	b = replicaPurposeBrief(t, b, "motion")
	b.ReferenceDurationSeconds, b.Duration = 20, 20
	b.AssetIDs = []string{"target-a", "target-b", "target-c"}
	models[0].VideoReference.MaxSegments = 2
	plan.Shots = plan.Shots[:2]
	for i, id := range []string{"target-a", "target-c"} {
		plan.Shots[i].SourceStart, plan.Shots[i].SourceEnd = float64(i*10), float64((i+1)*10)
		plan.Shots[i].AssetID = id
		plan.Shots[i].Prompt = []string{"按@图片1外观展示", "按@图片3外观展示"}[i]
	}
	plan = readyReplicaPlan(t, plan)
	shots, _, err := BuildShots(b, plan, models)
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != 2 || !strings.Contains(shots[1].Plan.Prompt, "按@图片1外观展示") || strings.Contains(shots[1].Plan.Prompt, "@图片3") {
		t.Fatal("global mention was not rebound to actual execution image")
	}
	plan.Shots[1].Prompt = "还引用未发送的@图片2"
	if _, _, err := BuildShots(b, plan, models); err == nil || err.Error() != "video_replica_reference_mapping_invalid" {
		t.Fatalf("unsent reference silently accepted: %v", err)
	}
}

func TestReplicaLegacyGapAliasesAndOverflowRemainBlocked(t *testing.T) {
	var gap StoryGap
	if err := json.Unmarshal([]byte(`{"gap":"需要补背面图","action":"upload","reason":"确认目标外观"}`), &gap); err != nil || gap.Resolution != "upload" {
		t.Fatalf("legacy upload gap lost: %v", err)
	}
	b, _, _, plan := replicaFixture()
	plan.Story = &StoryPlan{Review: &StoryPlanReview{Status: "warning"}}
	for i := 0; i < 20; i++ {
		plan.Story.Review.Issues = append(plan.Story.Review.Issues, "警告"+strconv.Itoa(i))
	}
	for i := 0; i < 5; i++ {
		plan.Story.Gaps = append(plan.Story.Gaps, StoryGap{Description: "补图" + strconv.Itoa(i), Resolution: "upload"})
	}
	normalized, err := normalizeReplicaAdaptationPlan(b, plan)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Adaptation.Status != "blocked" || len(normalized.Adaptation.Blockers) != 20 || normalized.Adaptation.Blockers[19].Message != "另有 6 项原分析待解决问题，请查看分析原文并补充素材或调整要求后重新提取方案。" {
		t.Fatal("overflow issues were silently dropped")
	}
}

func TestReplicaLegacyGapAliasesCannotOverwriteCanonicalBlocker(t *testing.T) {
	for _, raw := range []string{
		`{"gap":"缺结构","action":"omit","reason":"省略","resolution":"upload"}`,
		`{"gap":"缺结构","action":"upload","reason":"补图","suggestion":"保留原文"}`,
	} {
		var gap StoryGap
		if err := json.Unmarshal([]byte(raw), &gap); err == nil {
			t.Fatal("alias overwrote canonical blocker")
		}
	}
}

func TestReplicaFirstFramePromptCannotReferenceUnavailableVideo(t *testing.T) {
	b, models, plan := wholeReferenceFixture()
	b = replicaPurposeBrief(t, b, "motion")
	b.ImageModel = "im"
	models = append(models, Model{ID: "im", Kind: "image", PriceMicro: 10_000})
	plan = readyReplicaPlan(t, plan)
	plan.Shots[0].ImagePrompt = "按@视频1原片制作首帧"
	if _, _, err := BuildShots(b, plan, models); err == nil || err.Error() != "video_replica_reference_mapping_invalid" {
		t.Fatalf("image-only request accepted video reference: %v", err)
	}
}

func TestReplicaInvalidMentionNumbersCannotReachProvider(t *testing.T) {
	b, _, _, _ := replicaFixture()
	b = replicaPurposeBrief(t, b, "motion")
	b.AssetIDs = []string{"target"}
	for _, text := range []string{"@图片0", "@图片-1", "@图片01", "@图片2", "@视频0", "@视频2"} {
		if _, err := replicaExecutionReferenceText(b, []string{"target"}, true, text); err == nil {
			t.Fatalf("invalid media mention accepted: %s", text)
		}
	}
}

func TestReplicaLegacyStoredBriefKeepsPriorUnknownFieldCompatibility(t *testing.T) {
	b, _, _, _ := replicaFixture()
	raw, _ := json.Marshal(b)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["legacyMetadata"] = "旧快照的非执行元数据"
	raw, _ = json.Marshal(fields)
	var restored Brief
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Product != b.Product {
		t.Fatalf("new purpose broke legacy stored brief decoding: %v", err)
	}
}

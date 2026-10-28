package media

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func wholeReferenceFixture() (Brief, []Model, Plan) {
	boundaries := []float64{0, 4.083, 4.333, 4.708, 5.417, 5.5, 6.167, 7.542, 7.958, 8.962902}
	brief := Brief{Mode: "replicate", GenerationMode: "video_reference", Product: "合成角色", AssetIDs: []string{"replacement"}, ReferenceAssetID: "source", ReferenceDurationSeconds: boundaries[len(boundaries)-1], VideoModel: "v", Resolution: "480p", AspectRatio: "16:9", Duration: 9, MaxCreditsMicro: 50_000_000, ReviewPlan: true, ReplacementInstructions: "替换为同一合成角色"}
	model := fixtureVideoModel()
	model.ID, model.Durations, model.MinDuration, model.MaxDuration = "v", nil, 4, 15
	model.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 2, PriceMicro: 700_000}
	plan := Plan{References: []Reference{{URL: "", Title: "合成原片", CapturedAt: time.Now(), Evidence: "合成时间轴", SelectionReason: "回归夹具"}}}
	for i := 0; i < len(boundaries)-1; i++ {
		plan.Shots = append(plan.Shots, ShotPlan{SourceStart: boundaries[i], SourceEnd: boundaries[i+1], Duration: 4, Framing: "全景", Camera: "推进", Action: "舞蹈", Purpose: "延续动作", Prompt: "合成角色连续舞蹈", ImagePrompt: "历史首帧文字", AssetID: "replacement", Sound: "连续节拍", Subtitle: "镜头字幕"})
	}
	plan.Shots[0].Duration = 5
	return brief, []Model{model}, plan
}

func TestExplicitLegacyReviewSaveDropsOnlyUnstartedPerShotClipBindings(t *testing.T) {
	for _, kind := range []string{"pending", "queued", "submitted", "diagnostic", "image", "admission", "provider", "charged", "attempt", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			brief, models, plan := wholeReferenceFixture()
			p := &Project{ID: uuid.NewString(), Version: 1, Status: "review", TeamID: f.team, UserID: f.user, Brief: brief, Models: models, SourceChecksum: strings.Repeat("a", 64), SourceAnalysis: &SourceAnalysis{SourceChecksum: strings.Repeat("a", 64), Summary: "合成原片", References: plan.References}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			for i := range plan.Shots {
				plan.Shots[i].ReferenceVideoAssetID = fmt.Sprintf("legacy-clip-%d", i)
				shot := plan.Shots[i]
				p.SourceAnalysis.Shots = append(p.SourceAnalysis.Shots, SourceShot{SourceURL: shot.SourceURL, SourceStart: shot.SourceStart, SourceEnd: shot.SourceEnd})
				p.Shots = append(p.Shots, Shot{Plan: shot, ReferenceVideoAssetID: shot.ReferenceVideoAssetID, Status: "pending", Attempt: 1})
			}
			p.Plan = &plan
			switch kind {
			case "queued":
				p.Status = "queued"
			case "submitted":
				p.Shots[0].VideoTaskID = "already-submitted"
			case "diagnostic":
				p.Shots[0].ProviderError = &ProviderError{Code: "InvalidParameter", Category: "invalid_input"}
			case "image":
				p.Shots[0].ImageAssetID = "generated-image"
			case "admission":
				p.Shots[0].ReservationID = "admitted"
			case "provider":
				p.Shots[0].ProviderID = "selected-provider"
			case "charged":
				p.ChargedMicro = 1
			case "attempt":
				p.Shots[0].Attempt = 2
			case "cancelled":
				p.CancelRequested = true
			}
			ctx := context.Background()
			if err := f.s.Repo.Save(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
			saved, err := f.s.Revise(ctx, f.team, p.ID, p.Version, plan)
			if kind != "pending" {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("execution history migrated: %v", err)
				}
				latest, _ := f.s.Repo.Get(ctx, f.team, p.ID)
				if latest.Version != p.Version || len(latest.Shots) != 9 || latest.Shots[0].ReferenceVideoAssetID != "legacy-clip-0" {
					t.Fatal("frozen execution changed")
				}
				return
			}
			if err != nil || len(saved.Shots) != 1 || len(saved.Plan.Shots) != 9 || saved.Shots[0].Plan.SourceEnd != brief.ReferenceDurationSeconds || saved.Shots[0].ReferenceVideoAssetID != "" || saved.Shots[0].Plan.ReferenceVideoAssetID != "" {
				t.Fatalf("explicit review save did not prepare whole input: %+v %v", saved, err)
			}
			for i, shot := range saved.Plan.Shots {
				if shot.ReferenceVideoAssetID != "" || shot.SourceStart != plan.Shots[i].SourceStart || shot.SourceEnd != plan.Shots[i].SourceEnd || plan.Shots[i].ReferenceVideoAssetID == "" {
					t.Fatal("migration altered timeline or caller plan")
				}
			}
			if f.submits.Load() != 0 {
				t.Fatal("saving review submitted generation")
			}
		})
	}
}

func TestWholeReferencePreservesReviewAndBuildsOneExecution(t *testing.T) {
	brief, models, plan := wholeReferenceFixture()
	original := append([]ShotPlan(nil), plan.Shots...)
	shots, quote, err := BuildShots(brief, plan, models)
	if err != nil || len(shots) != 1 {
		t.Fatalf("whole reference requires one execution: shots=%d err=%v", len(shots), err)
	}
	shot := shots[0].Plan
	if !reflect.DeepEqual(plan.Shots, original) || shot.SourceStart != 0 || shot.SourceEnd != brief.ReferenceDurationSeconds || shot.Duration != 9 || quote != 6_300_000 || shot.Subtitle != "" {
		t.Fatalf("review timeline, full range or quote changed: shot=%+v quote=%d", shot, quote)
	}
	for _, text := range []string{brief.ReplacementInstructions, "4.083", "5.417", "8.962902", "连续节拍", "镜头字幕"} {
		if !strings.Contains(shot.Prompt, text) {
			t.Fatalf("combined script omitted %q", text)
		}
	}
	models[0].MinDuration, models[0].MaxDuration = 4, 15
	shots, quote, err = BuildShots(brief, plan, models)
	if err != nil || shots[0].Plan.Duration != 9 || quote != 6_300_000 {
		t.Fatalf("continuous duration range must take priority: %+v %d %v", shots, quote, err)
	}
}

func TestWholeReferenceBuildsMinimumSegmentsAtFactBoundaries(t *testing.T) {
	brief, models, plan := wholeReferenceFixture()
	boundaries := []float64{0, 4, 8, 12, 14, 16, 20.155}
	brief.ReferenceDurationSeconds = boundaries[len(boundaries)-1]
	brief.Duration = 20
	models[0].VideoReference.MaxSegments = 3
	plan.Shots = plan.Shots[:0]
	for i := 0; i < len(boundaries)-1; i++ {
		plan.Shots = append(plan.Shots, ShotPlan{SourceStart: boundaries[i], SourceEnd: boundaries[i+1], Duration: 4, Framing: "全景", Camera: "推进", Action: "连续动作", Purpose: "复刻", Prompt: fmt.Sprintf("第%d段事实动作", i+1), AssetID: "replacement"})
	}
	shots, quote, err := BuildShots(brief, plan, models)
	if err != nil || len(shots) != 2 {
		t.Fatalf("20.155s source must use two execution segments: %+v %v", shots, err)
	}
	if shots[0].Plan.SourceStart != 0 || shots[0].Plan.SourceEnd != 14 || shots[1].Plan.SourceStart != 14 || shots[1].Plan.SourceEnd != 20.155 {
		t.Fatalf("segments did not use stable fact boundaries: %+v", shots)
	}
	if shots[0].Plan.Duration != 15 || shots[1].Plan.Duration != 5 || quote != 14_000_000 {
		t.Fatalf("segment output duration or quote is wrong: %+v quote=%d", shots, quote)
	}
	if strings.Contains(shots[0].Plan.Prompt, "第5段事实动作") || !strings.Contains(shots[1].Plan.Prompt, "第5段事实动作") {
		t.Fatalf("segment prompt leaked facts across ranges: %+v", shots)
	}

	models[0].VideoReference.MaxSegments = 1
	if _, _, err = BuildShots(brief, plan, models); err == nil || err.Error() != "video_reference_duration_unsupported" {
		t.Fatalf("maxSegments=1 accepted two required segments: %v", err)
	}
	models[0].VideoReference.MaxSegments = 3
	plan.Shots = []ShotPlan{{SourceStart: 0, SourceEnd: 16, Duration: 16, Framing: "全景", Camera: "推进", Action: "不可拆动作", Purpose: "复刻", Prompt: "完整事实镜头", AssetID: "replacement"}, {SourceStart: 16, SourceEnd: 20.155, Duration: 5, Framing: "全景", Camera: "推进", Action: "收尾", Purpose: "复刻", Prompt: "收尾动作", AssetID: "replacement"}}
	if _, _, err = BuildShots(brief, plan, models); err == nil || err.Error() != "video_reference_duration_unsupported" {
		t.Fatalf("one over-limit fact shot was split or accepted: %v", err)
	}
}

func TestWholeReferenceRequiresAnApplicableOpeningImagePerSegment(t *testing.T) {
	brief, models, plan := wholeReferenceFixture()
	boundaries := []float64{0, 4, 8, 12, 14, 16, 20.155}
	brief.ReferenceDurationSeconds, brief.Duration = 20.155, 20
	brief.AssetIDs = []string{"opening-a", "later-only", "opening-b"}
	models[0].VideoReference.MaxSegments = 2
	plan.Shots = plan.Shots[:0]
	for i := 0; i < len(boundaries)-1; i++ {
		plan.Shots = append(plan.Shots, ShotPlan{SourceStart: boundaries[i], SourceEnd: boundaries[i+1], Duration: 4, Framing: "全景", Camera: "推进", Action: "连续动作", Purpose: "复刻", Prompt: "事实动作"})
	}
	plan.Shots[1].AssetID = "later-only"
	plan.Shots[4].AssetID = "opening-b"
	if _, _, err := BuildShots(brief, plan, models); err == nil || err.Error() != "first_frame_required" {
		t.Fatalf("a later fact image was borrowed as the first segment opening: %v", err)
	}

	plan.Shots[0].AssetID = "opening-a"
	shots, _, err := BuildShots(brief, plan, models)
	if err != nil || len(shots) != 2 || shots[0].Plan.AssetID != "opening-a" || shots[1].Plan.AssetID != "opening-b" {
		t.Fatalf("segment opening images did not follow their first facts: %+v %v", shots, err)
	}
}

func TestWholeReferenceAggregatesBindingsPerExecutionSegment(t *testing.T) {
	brief, models, plan := wholeReferenceFixture()
	boundaries := []float64{0, 5, 10, 15, 20}
	brief.ReferenceDurationSeconds, brief.Duration = 20, 20
	models[0].VideoReference.MaxSegments = 2
	plan.Shots = plan.Shots[:0]
	for i := 0; i < len(boundaries)-1; i++ {
		clip := "clip-a"
		if i >= 3 {
			clip = "clip-b"
		}
		plan.Shots = append(plan.Shots, ShotPlan{SourceStart: boundaries[i], SourceEnd: boundaries[i+1], ReferenceVideoAssetID: clip, Duration: 5, Framing: "全景", Camera: "推进", Action: "动作", Purpose: "复刻", Prompt: "事实动作", AssetID: "replacement"})
	}
	shots, _, err := BuildShots(brief, plan, models)
	if err != nil || len(shots) != 2 || shots[0].Plan.ReferenceVideoAssetID != "clip-a" || shots[1].Plan.ReferenceVideoAssetID != "clip-b" {
		t.Fatalf("segment bindings were not aggregated independently: %+v %v", shots, err)
	}
	plan.Shots[1].ReferenceVideoAssetID = ""
	if _, _, err = BuildShots(brief, plan, models); err == nil || err.Error() != "video_reference_plan_invalid" {
		t.Fatalf("partial segment binding was accepted: %v", err)
	}
}

func TestWholeReferenceExecutionIncludesEditedIdentitySceneAndTransitions(t *testing.T) {
	brief, models, plan := wholeReferenceFixture()
	plan.Shots[1].Subject = "第二镜修改后的同一角色"
	plan.Shots[1].Scene = "第二镜修改后的星空舞台"
	plan.Shots[1].Transition = "fade"
	plan.Shots[1].TransitionDescription = "第二镜由光雾渐入"
	shots, _, err := BuildShots(brief, plan, models)
	if err != nil || len(shots) != 1 {
		t.Fatalf("edited plan rejected: %v", err)
	}
	for _, value := range []string{plan.Shots[1].Subject, plan.Shots[1].Scene, plan.Shots[1].Transition, plan.Shots[1].TransitionDescription} {
		if !strings.Contains(shots[0].Plan.Prompt, value) {
			t.Fatalf("edited field was omitted: %q", value)
		}
	}
	plan.Shots[1].Subject = strings.Repeat("主体", 500)
	plan.Shots[1].Scene = strings.Repeat("场景", 500)
	plan.Shots[1].TransitionDescription = strings.Repeat("渐入", 500)
	plan.Shots[0].Prompt = strings.Repeat("动态", 700)
	if _, _, err := BuildShots(brief, plan, models); err == nil || err.Error() != "video_reference_prompt_limit" {
		t.Fatalf("edited fields must not be truncated: %v", err)
	}
}

func TestWholeReferenceRejectsUnsupportedWholeInputWithoutTruncation(t *testing.T) {
	for _, kind := range []string{"over-limit", "no-duration", "prompt-overflow", "utf16-overflow", "different-clips", "budget", "too-many-images"} {
		t.Run(kind, func(t *testing.T) {
			brief, models, plan := wholeReferenceFixture()
			want := "video_reference_duration_unsupported"
			switch kind {
			case "over-limit":
				models[0].VideoReference.MaxDurationSeconds = 8
			case "no-duration":
				models[0].Durations, models[0].MinDuration, models[0].MaxDuration = nil, 0, 0
			case "prompt-overflow":
				plan.Shots[0].Prompt = strings.Repeat("字", 4000)
				want = "video_reference_prompt_limit"
			case "utf16-overflow":
				plan.Shots[0].Prompt = strings.Repeat("😀", 2000)
				want = "video_reference_prompt_limit"
			case "different-clips":
				plan.Shots[0].ReferenceVideoAssetID = "clip-a"
				plan.Shots[1].ReferenceVideoAssetID = "clip-b"
				want = "video_reference_plan_invalid"
			case "budget":
				brief.MaxCreditsMicro = 6_299_999
				want = "duration_or_budget_exceeded"
			case "too-many-images":
				brief.AssetIDs = append(brief.AssetIDs, "second", "third")
				plan.Shots[1].AssetID, plan.Shots[2].AssetID = "second", "third"
				want = "video_reference_image_limit"
			}
			if _, _, err := BuildShots(brief, plan, models); err == nil || err.Error() != want {
				t.Fatalf("want %s, got %v", want, err)
			}
		})
	}
}

func TestWholeReferenceGeneratesOnlyOneSelectedImage(t *testing.T) {
	brief, models, plan := wholeReferenceFixture()
	brief.ImageModel = "im"
	models = append(models, Model{ID: "im", Kind: "image", PriceMicro: 20_000})
	brief.AssetIDs = append(brief.AssetIDs, "second", "third")
	plan.Shots[1].AssetID, plan.Shots[2].AssetID = "second", "third"
	plan.Shots[0].ImagePrompt = "首个替换图提示"
	shots, quote, err := BuildShots(brief, plan, models)
	if err != nil || len(shots) != 1 || shots[0].Plan.ImagePrompt != "首个替换图提示" || quote != 6_320_000 {
		t.Fatalf("image generation must happen once: %+v %d %v", shots, quote, err)
	}
	plan.Shots[0].ImagePrompt = ""
	if _, _, err := BuildShots(brief, plan, models); err == nil || err.Error() != "first_frame_required" {
		t.Fatalf("a later scene must not substitute for the opening frame: %v", err)
	}
}

func TestWholeReferenceValidatesActualImagesInsteadOfCandidatePool(t *testing.T) {
	brief, models, plan := wholeReferenceFixture()
	brief.Resolution = "720p"
	brief.AssetIDs = append(brief.AssetIDs, "unused-second", "unused-third")
	assets := []Asset{{ID: "source", Kind: "video", DurationSeconds: brief.ReferenceDurationSeconds}, {ID: "replacement", Kind: "image"}, {ID: "unused-second", Kind: "image"}, {ID: "unused-third", Kind: "image"}}
	if err := ValidateBrief(brief, models, assets); err != nil {
		t.Fatalf("candidate pool must remain available to planning: %v", err)
	}
	if _, _, err := BuildShots(brief, plan, models); err != nil {
		t.Fatalf("only one candidate is actually sent: %v", err)
	}
}

func TestFailedAdaptationRecoversSameCompletedInputWithoutChargingAgain(t *testing.T) {
	f, p, report, brief, plan := sourceFixture(t)
	ctx := context.Background()
	p = completeSource(t, f, p, report)
	feeID := p.AnalysisFee.ID
	p, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "adapt", InputID: "completed-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.FailAnalysis(ctx, f.team, p.ID, p.Version, "completed-input", "analysis_failed")
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.FailAnalysis(ctx, f.team, p.ID, p.Version, "completed-input", "video_reference_plan_invalid")
	if err != nil || p.Error != "video_reference_plan_invalid" {
		t.Fatalf("same input lost its new safe rejection reason: %+v %v", p, err)
	}
	if _, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "stale-input", plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale input restored a failed plan: %v", err)
	}
	p, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "completed-input", plan)
	if err != nil || p.Status != "review" || p.Error != "" || p.AnalysisFee.ID != feeID {
		t.Fatalf("same-input recovery failed: %+v %v", p, err)
	}
	identity, _ := f.s.Repo.Store.GetQuotaReservation(ctx, feeID)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if identity.Status != store.ReservationSettled || team.UsedCredits != "1.000000" || f.submits.Load() != 0 {
		t.Fatalf("recovery changed settled source fee or generated: %+v %+v", identity, team)
	}
}

func TestFailedAdaptationRecoveryRejectsChangedBindingSourceFeeAndExecution(t *testing.T) {
	for _, kind := range []string{"stale-version", "source-kind", "missing-fee", "changed-source", "generation-history", "cancelled", "unknown-fee"} {
		t.Run(kind, func(t *testing.T) {
			f, p, report, brief, plan := sourceFixture(t)
			ctx := context.Background()
			p = completeSource(t, f, p, report)
			p, err := f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "adapt", InputID: "completed-input", WorkspacePath: "/fixture"})
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.FailAnalysis(ctx, f.team, p.ID, p.Version, "completed-input", "analysis_failed")
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				switch kind {
				case "source-kind":
					v.Analysis.Kind = "source"
				case "missing-fee":
					v.AnalysisFee = nil
				case "changed-source":
					v.SourceChecksum = strings.Repeat("b", 64)
				case "generation-history":
					v.Shots = []Shot{{Status: "pending"}}
				case "cancelled":
					v.CancelRequested = true
				case "unknown-fee":
					v.AnalysisFee.ID = "unsettled-fee"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			version := p.Version
			if kind == "stale-version" {
				version--
			}
			if _, err = f.s.SetPlan(ctx, f.team, p.ID, version, "completed-input", plan); !errors.Is(err, ErrConflict) {
				t.Fatalf("unsafe recovery accepted: %v", err)
			}
			latest, _ := f.s.Repo.Get(ctx, f.team, p.ID)
			if latest.Version != p.Version || latest.Status != "analysis_failed" {
				t.Fatal("rejected recovery changed project")
			}
		})
	}
}

func TestWholeReferenceOutputDurationsCoverEverySegment(t *testing.T) {
	continuous := Model{MinDuration: 4, MaxDuration: 15}
	if got := wholeReferenceOutputDurations(continuous, []float64{14, 6}, 20); !reflect.DeepEqual(got, []int{15, 5}) {
		t.Fatalf("continuous catalog should allocate the historical 15+5 split: %v", got)
	}
	if got := wholeReferenceOutputDurations(continuous, []float64{14, 6.155}, 20); !reflect.DeepEqual(got, []int{15, 5}) {
		t.Fatalf("continuous catalog should round the whole-film total and preserve 15+5: %v", got)
	}
	if got := wholeReferenceOutputDurations(continuous, []float64{2.5, 2.5, 3}, 8); got != nil {
		t.Fatalf("continuous catalog must reject an impossible exact total: %v", got)
	}
}

package media

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func imageSelectedPlanFixture(count int, durations []int) (Brief, Plan, []Model) {
	b, p, models := wholeDurationFixture(durations, 15)
	b.ImageAnalysisFirst = true
	b.AssetIDs = nil
	p.Story.AssetRoles = nil
	p.Research.AssetObservations = nil
	for index := range count {
		id := fmt.Sprintf("image-%d", index+1)
		b.AssetIDs = append(b.AssetIDs, id)
		p.Story.AssetRoles = append(p.Story.AssetRoles, StoryAssetRole{AssetID: id, Role: "product", Facts: "真实可见素材"})
		p.Research.AssetObservations = append(p.Research.AssetObservations, AssetObservation{AssetID: id, Description: "真实可见素材"})
	}
	for index := range p.Shots {
		first, last := min(index*9, count), min((index+1)*9, count)
		p.Shots[index].Creative.AssetIDs = append([]string{}, b.AssetIDs[first:last]...)
	}
	return b, p, models
}

func TestImageFirstSelectedImagesRequireActualUsage(t *testing.T) {
	b, plan, models := imageSelectedPlanFixture(7, []int{15})
	if _, _, err := BuildShots(b, plan, models); err != nil {
		t.Fatal("seven photos should fit real nine-image capability", err)
	}
	plan.Story.AssetRoles[6].Role = "unused"
	if _, _, err := BuildShots(b, plan, models); err != nil {
		t.Fatal("actual input was blocked by advisory role", err)
	}
	plan.Story.AssetRoles[6].Role = "scene"
	plan.Shots[0].Creative.AssetIDs = plan.Shots[0].Creative.AssetIDs[:6]
	if _, _, err := BuildShots(b, plan, models); err == nil || err.Error() != "video_story_selected_asset_missing" {
		t.Fatal("description without actual input counted as used", err)
	}
	b.ImageAnalysisFirst = false
	if _, _, err := BuildShots(b, plan, models); err != nil {
		t.Fatal("legacy plan acquired a new coverage requirement", err)
	}
}

func TestImageFirstPostOnlyLogoCoverageAndCapacity(t *testing.T) {
	b, plan, models := imageSelectedPlanFixture(2, []int{15})
	b.ReferenceMode = "frames"
	plan.Shots[0].AssetID = b.AssetIDs[0]
	plan.Shots[0].Creative.AssetIDs = b.AssetIDs[:1]
	plan.Story.AssetRoles[1].Role = "logo"
	if err := validateSelectedImageUsage(b, plan); err == nil {
		t.Fatal("advisory logo role bypassed actual usage")
	}
	if got := planImageGenerationDurations(b, models[0]); !reflect.DeepEqual(got, []int{8, 7}) {
		t.Fatal("unbound logo lost input capacity", got)
	}
	b.Finishing.Brand = testVideoBrand(b.AssetIDs[1])
	if _, _, err := BuildShots(b, plan, models); err != nil {
		t.Fatal("post-only logo blocked the generated subject", err)
	}
	if got := planImageGenerationDurations(b, models[0]); !reflect.DeepEqual(got, []int{15}) {
		t.Fatal("post-only logo added a paid generation segment", got)
	}
	b.Finishing.Brand.LogoAssetID = "foreign"
	if err := validateSelectedImageUsage(b, plan); err == nil {
		t.Fatal("unrelated logo binding bypassed selected image coverage")
	}
}

func TestImageFirstAllocationUsesActualReferenceCapacity(t *testing.T) {
	b, plan, models := imageSelectedPlanFixture(10, []int{8, 7})
	if got := planImageGenerationDurations(b, models[0]); !reflect.DeepEqual(got, []int{8, 7}) {
		t.Fatal("ten images were not split into two legal segments", got)
	}
	if err := validateBriefReferenceImages(b, models[0]); err != nil {
		t.Fatal("creation rejected images that fit across segments", err)
	}
	if _, _, err := BuildShots(b, plan, models); err != nil {
		t.Fatal("legal image partition rejected", err)
	}
	plan.Shots[0].Creative.AssetIDs = append([]string{}, b.AssetIDs...)
	if _, _, err := BuildShots(b, plan, models); err == nil || err.Error() != "video_generation_reference_images_exceeded" {
		t.Fatal("single segment exceeded real model image capacity", err)
	}
	models[0].UpstreamModel = "doubao-seedance-2-5-260628"
	if got := planImageGenerationDurations(b, models[0]); !reflect.DeepEqual(got, []int{15}) {
		t.Fatal("2.5 incorrectly inherited the 2.0 or first-frame limit", got)
	}
}

func TestImageFirstFramesCountOnlyActualFirstFrames(t *testing.T) {
	b, plan, models := imageSelectedPlanFixture(3, []int{5, 5, 5})
	b.ReferenceMode = "frames"
	for index := range plan.Shots {
		plan.Shots[index].Creative.AssetIDs = []string{b.AssetIDs[index]}
		plan.Shots[index].AssetID = b.AssetIDs[index]
	}
	if got := planImageGenerationDurations(b, models[0]); !reflect.DeepEqual(got, []int{5, 5, 5}) {
		t.Fatal("frame mode did not allocate one image per segment", got)
	}
	if _, _, err := BuildShots(b, plan, models); err != nil {
		t.Fatal("first frame coverage rejected", err)
	}
	plan.Shots[2].AssetID = ""
	if err := validateSelectedImageUsage(b, plan); err == nil || err.Error() != "video_story_selected_asset_missing" {
		t.Fatal("creative list counted a photo not actually sent", err)
	}
}

func TestImageFirstWorkerSubmitsOnlyEachSegmentsFrozenImages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	b, plan, models := imageSelectedPlanFixture(10, []int{8, 7})
	shots, _, err := BuildShots(b, plan, models)
	if err != nil {
		t.Fatal(err)
	}
	// 这里只验证已受理项目的供应商输入，不依赖容器内的后期工具安装。
	b.Finishing = nil
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief, v.Plan, v.Models, v.Shots, v.Status = b, &plan, models, shots, "queued"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if f.submits.Load() != 1 || p.Shots[0].Status != "video_running" {
		t.Fatal("first segment never reached provider", p.Error, p.Shots[0].Error)
	}
	f.requestMu.Lock()
	content := f.requestBodies[0]["content"].([]any)
	f.requestMu.Unlock()
	if len(content) != 10 {
		t.Fatal("first segment did not send exactly nine reference images", len(content))
	}
	for index := range 9 {
		if got := submittedAssetID(t, content[index+1].(map[string]any), "image_url"); got != b.AssetIDs[index] {
			t.Fatal("first segment image order changed", got)
		}
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Shots[0].Status = "succeeded"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if f.submits.Load() != 2 || p.Shots[1].Status != "video_running" {
		t.Fatal("second segment never reached provider", p.Error, p.Shots[1].Error)
	}
	f.requestMu.Lock()
	content = f.requestBodies[1]["content"].([]any)
	f.requestMu.Unlock()
	if len(content) != 2 || submittedAssetID(t, content[1].(map[string]any), "image_url") != b.AssetIDs[9] {
		t.Fatal("last selected photo was omitted or all photos were repeated", content)
	}
}

func TestImageFirstLegacyReadRemainsAvailableButNewPaidSubmissionChecksCoverage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := f.project(t)
	b, plan, models := imageSelectedPlanFixture(7, []int{15})
	plan.Shots[0].Creative.AssetIDs = plan.Shots[0].Creative.AssetIDs[:6]
	b.Finishing = nil
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief, v.Plan, v.Models, v.Status = b, &plan, models, "queued"
		v.Shots = []Shot{{Plan: plan.Shots[0], Status: "pending", Attempt: 1}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	view := f.s.ProjectView(ctx, p)
	if view.Plan == nil || len(view.Brief.AssetIDs) != 7 {
		t.Fatal("historical projection was blocked by new plan validation")
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if f.submits.Load() != 0 || p.Error != "video_story_selected_asset_missing" {
		t.Fatal("missing selected photo passed new paid admission", p.Error)
	}
}

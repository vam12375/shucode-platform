package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestReferenceFidelityCompilerIncludesIndependentControls(t *testing.T) {
	b, models, plan := wholeReferenceFixture()
	plan.Shots[0].Framing = "FID_FACE_CLOSEUP"
	plan.Shots[0].Camera = "FID_PULL_BACK"
	plan.Shots[0].Action = "FID_SKATEBOARD_LANDING"
	shots, _, err := BuildShots(b, plan, models)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FID_FACE_CLOSEUP", "FID_PULL_BACK", "FID_SKATEBOARD_LANDING"} {
		if !strings.Contains(shots[0].Plan.Prompt, want) {
			t.Fatalf("independent control lost: %s", want)
		}
	}
}

func TestReferenceFidelityAnchorsValidatedAndCompiled(t *testing.T) {
	b, models, plan := wholeReferenceFixture()
	raw := `{"sourceUrl":"","sourceStart":0,"sourceEnd":4.083,"duration":5,"framing":"全景","camera":"推进","action":"抬手","purpose":"亮相","prompt":"改编","assetId":"replacement","subtitle":"","anchors":[{"sourceTime":2,"framing":"近景","camera":"FID_ORBIT","action":"FID_BOARD","requiredDetails":"脚踩滑板"}]}`
	if err := json.Unmarshal([]byte(raw), &plan.Shots[0]); err != nil {
		t.Fatal(err)
	}
	shots, _, err := BuildShots(b, plan, models)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FID_ORBIT", "FID_BOARD", "脚踩滑板", "局部", "请求输出"} {
		if !strings.Contains(shots[0].Plan.Prompt, want) {
			t.Fatalf("timing or anchor lost: %s", want)
		}
	}
	for _, invalid := range []string{strings.Replace(raw, `"sourceTime":2`, `"sourceTime":5`, 1), strings.Replace(raw, `"camera":"FID_ORBIT"`, `"camera":""`, 1)} {
		if err := json.Unmarshal([]byte(invalid), &plan.Shots[0]); err != nil {
			continue
		}
		if _, _, err := BuildShots(b, plan, models); err == nil {
			t.Fatal("invalid anchor reached execution")
		}
	}
}

func TestReferenceFidelitySourceStrictAndIsolated(t *testing.T) {
	raw := `{"sourceUrl":"","sourceStart":0,"sourceEnd":3,"framing":"全景","camera":"推进","action":"走路","purpose":"开场","anchors":[{"sourceTime":2,"framing":"近景","camera":"静止","action":"开门"}]}`
	var shot SourceShot
	if err := json.Unmarshal([]byte(raw), &shot); err != nil {
		t.Fatal(err)
	}
	report := &SourceAnalysis{Shots: []SourceShot{shot}}
	clone := cloneSourceAnalysis(report)
	if clone == nil {
		t.Fatal("clone failed")
	}
	encoded, _ := json.Marshal(clone)
	if !strings.Contains(string(encoded), `"sourceTime":2`) {
		t.Fatal("anchors lost in clone")
	}
	if err := json.Unmarshal([]byte(strings.Replace(raw, `"sourceTime":2`, `"unknown":true,"sourceTime":2`, 1)), &shot); err == nil {
		t.Fatal("unknown anchor field accepted")
	}
}

func TestReferenceFidelityStoryboardControlsAndBudget(t *testing.T) {
	b, models, plan := wholeReferenceFixture()
	b.GenerationMode = "storyboard"
	plan.Shots = plan.Shots[:1]
	plan.Shots[0].SourceEnd = 5
	b.ReferenceDurationSeconds = 5
	b.Duration = 5
	plan.Shots[0].Camera = "FID_CAMERA_ONLY"
	shots, _, err := BuildShots(b, plan, models)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shots[0].Plan.Prompt, "FID_CAMERA_ONLY") {
		t.Fatal("storyboard camera lost")
	}
	plan.Shots[0].Prompt = strings.Repeat("字", 3980)
	if _, _, err = BuildShots(b, plan, models); err == nil || err.Error() != "video_reference_prompt_limit" {
		t.Fatalf("must reject entire oversized script: %v", err)
	}
}

func TestReferenceFidelityFrozenAnchorContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/reference-anchors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string
		Valid bool
		Shot  json.RawMessage
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			var source SourceShot
			err := json.Unmarshal(v.Shot, &source)
			valid := err == nil && validReferenceAnchors(source.Anchors, source.SourceStart, source.SourceEnd)
			if valid != v.Valid {
				t.Fatalf("source acceptance mismatch: %v", err)
			}
			var plan ShotPlan
			err = json.Unmarshal(v.Shot, &plan)
			valid = err == nil && validReferenceAnchors(plan.Anchors, plan.SourceStart, plan.SourceEnd)
			if valid != v.Valid {
				t.Fatalf("plan acceptance mismatch: %v", err)
			}
		})
	}
}

func TestReferenceFidelityFreezesLegacyCompilerForRecovery(t *testing.T) {
	b, models, plan := wholeReferenceFixture()
	legacy, _, err := buildShotsWithReferenceScript(b, plan, models, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &Project{Brief: b, Models: models, Plan: &plan, Shots: legacy}
	if strings.Contains(legacy[0].Plan.Prompt, "景别：") || frozenReferenceScriptVersion(p) != 0 {
		t.Fatal("legacy compiler changed")
	}
	bound := withoutReferenceClipBindings(plan)
	for i := range bound.Shots {
		bound.Shots[i].ReferenceVideoAssetID = "clip"
	}
	if revisionReferenceScriptVersion(p, bound) != 0 {
		t.Fatal("clip binding migrated frozen prompt")
	}
	bound.Shots[0].Camera = "explicit user change"
	if revisionReferenceScriptVersion(p, bound) != 1 {
		t.Fatal("new review did not use current compiler")
	}
	if _, _, err := buildShotsWithReferenceScript(b, plan, models, 2); err == nil {
		t.Fatal("unknown compiler version silently downgraded")
	}
}

func TestReferenceFidelityLegacyPreparationRetryKeepsPaidOutputs(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			var f *fixture
			var p *Project
			if partial {
				prepared := failedPartialReferencePreparation(t)
				f, p = prepared.f, prepared.project
			} else {
				f, p = failedReferencePreparation(t, "video_reference_clip_invalid")
			}
			legacy, quote, err := buildShotsWithReferenceScript(p.Brief, *p.Plan, p.Models, 0)
			if err != nil || quote != p.QuoteMicro || len(legacy) != len(p.Shots) {
				t.Fatalf("legacy fixture: %v", err)
			}
			for i := range p.Shots {
				p.Shots[i].Plan = legacy[i].Plan
				p.Shots[i].ReferenceScriptVersion = 0
			}
			before := append([]Shot(nil), p.Shots...)
			if err := f.s.retryReferencePreparation(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			for i, shot := range p.Shots {
				if shot.Plan.Prompt != before[i].Plan.Prompt || shot.ReferenceScriptVersion != 0 || shot.ChargedMicro != before[i].ChargedMicro || shot.OutputAssetID != before[i].OutputAssetID {
					t.Fatal("recovery rewrote frozen paid output")
				}
			}
			if p.Status != "review" {
				t.Fatal("old project could not resume")
			}
		})
	}
}

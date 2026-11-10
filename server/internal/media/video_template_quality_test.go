package media

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVideoTemplateRewrittenCatalogContract(t *testing.T) {
	var templates []VideoTemplate
	if err := json.Unmarshal(videoTemplateCatalogJSON, &templates); err != nil {
		t.Fatal(err)
	}
	if len(templates) != 60 {
		t.Fatalf("expected sixty recipes, got %d", len(templates))
	}
	seen := map[string]bool{}
	actions := map[string]bool{}
	checks := map[string]bool{}
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			// 旧配方固定三拍；带标称秒数的 v2 配方是细分镜（约 15 镜，见 video_template_timing_test.go 的写作规则）。
			timed := recipe.NominalSeconds != 0
			legacyVersion := recipe.Version == "1.1.0" || len(recipe.SkillIDs) > 0 && recipe.Version == "1.0.0"
			structureOK := !timed && legacyVersion && len(recipe.Beats) == 3 || timed && recipe.Version == "2.0.0" && len(recipe.Beats) >= 12 && len(recipe.Beats) <= 16
			if seen[recipe.ID] || !structureOK || len(recipe.VerifiedModelIDs) != 0 {
				t.Fatalf("identity or verification changed: id=%s version=%s beats=%d verified=%v duplicate=%v", recipe.ID, recipe.Version, len(recipe.Beats), recipe.VerifiedModelIDs, seen[recipe.ID])
			}
			seen[recipe.ID] = true
			if len(recipe.RequiredFacts) < 3 || len(recipe.AssetRequirements) < 2 || len(recipe.AvoidWhen) < 3 || len(recipe.QualityChecks) < 8 || len(recipe.QualityChecks) > 12 {
				t.Fatal("recipe is missing evidence, applicability or quality criteria")
			}
			localChecks := map[string]bool{}
			for _, check := range recipe.QualityChecks {
				if strings.TrimSpace(check) == "" || localChecks[check] {
					t.Fatal("empty or duplicate quality check")
				}
				localChecks[check] = true
			}
			for _, beat := range recipe.Beats {
				for _, spec := range []struct {
					text    string
					markers []string
				}{
					{beat.Action, []string{"起态：", "动作：", "完成："}},
					{beat.Camera, []string{"起幅：", "路径：", "落幅："}},
				} {
					previous := -1
					for _, marker := range spec.markers {
						position := strings.Index(spec.text, marker)
						if position <= previous {
							t.Fatalf("missing or unordered %q in %q", marker, spec.text)
						}
						previous = position
					}
				}
				if strings.TrimSpace(beat.Purpose) == "" || strings.TrimSpace(beat.Framing) == "" || beat.Weight <= 0 {
					t.Fatal("missing narrative, composition or allocation weight")
				}
			}
			actionJSON, _ := json.Marshal(recipe.Beats)
			checksJSON, _ := json.Marshal(recipe.QualityChecks)
			if actions[string(actionJSON)] || checks[string(checksJSON)] {
				t.Fatal("different recipes reuse identical action or quality sequences")
			}
			actions[string(actionJSON)] = true
			checks[string(checksJSON)] = true
		})
	}
}

func TestRewrittenTemplateDoesNotInheritPreviousVersionReview(t *testing.T) {
	f, project, input := templateReviewFixture(t)
	ctx := context.Background()
	input.TemplateVersion = "1.0.0"
	signature := ""
	for _, model := range project.Models {
		if model.ID == project.Brief.VideoModel {
			signature = templateModelSignature(model)
		}
	}
	if signature == "" {
		t.Fatal("fixture model missing")
	}
	// 配方正文改写后必须使用新版本，不能因同一模型和模板id而借用旧成片审核。
	legacy := TemplateReview{TemplateReviewInput: input, ModelID: project.Brief.VideoModel, ModelSignature: signature, OutputAssetID: project.OutputAssetID, ReviewerID: "root-fixture", ReviewedAt: time.Now()}
	raw, err := json.Marshal([]TemplateReview{legacy})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.Store.SetConfigValue(ctx, TemplateReviewsKey, raw); err != nil {
		t.Fatal(err)
	}
	templates, err := f.s.VideoTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := findVideoTemplate(templates, input.TemplateID, "1.1.0")
	if err != nil || len(current.VerifiedModelIDs) != 0 {
		t.Fatalf("previous-version review leaked: %+v %v", current, err)
	}
	previousBrief := project.Brief
	previousBrief.TemplateVersion = "1.0.0"
	if err = f.s.validateTemplateBrief(ctx, previousBrief, project.Models); err == nil {
		t.Fatal("previous explicit version silently upgraded")
	}
	if previousBrief.TemplateVersion != "1.0.0" {
		t.Fatal("validation mutated the frozen version")
	}
}

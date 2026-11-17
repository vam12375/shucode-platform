package media

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 根因：一张图已满足准入，但旧正文没有区分可创作的缺失视角与须有据的商品事实，
// 规划器仍可能要求补图或反复慢推。回归覆盖实际创建、冻结与重放，而非仅检查素材计数。
func TestEveryRegularTemplateAcceptsAndFreezesOneSubjectImage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	expansions := []string{""}
	if FinishingCompositionAvailable() {
		expansions = append(expansions, "related", "strict")
	}
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			if recipe.MinAssets != 1 || !strings.Contains(recipe.Description.ZH, "单图创作：") || !strings.Contains(recipe.Description.EN, "Single-image creation:") {
				t.Fatal("single-image method is missing from the public/creative projection")
			}
			facts := strings.Join(recipe.RequiredFacts, "\n")
			if !strings.Contains(facts, "单图领域方法：") || !strings.Contains(facts, "shots[].prompt") || !strings.Contains(facts, "strict") || !strings.Contains(facts, "不当作") {
				t.Fatal("execution, category or explicit fidelity boundary was lost")
			}
			for _, expansion := range expansions {
				brief := Brief{Mode: "auto", ResearchPolicy: "disabled", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: "v", Resolution: "720p", AspectRatio: "16:9", Duration: 15, MaxCreditsMicro: 10_000_000, ReviewPlan: true, CreativeExpansion: expansion, TemplateID: recipe.ID, TemplateVersion: recipe.Version}
				if expansion != "" {
					brief.Finishing = &FinishingOptions{Subtitles: "none"}
				}
				p, err := f.s.Create(ctx, f.team, f.user, "", uuid.NewString(), brief)
				if err != nil {
					t.Fatalf("one-image %s creation: %v", expansion, err)
				}
				if p.TemplateSnapshot == nil || p.TemplateSnapshot.Template.Version != recipe.Version || len(p.Brief.AssetIDs) != 1 {
					t.Fatal("creation did not freeze the selected one-image recipe")
				}
				frozen, _ := json.Marshal(p.TemplateSnapshot)
				replay, err := f.s.Create(ctx, f.team, f.user, "", p.ID, brief)
				if err != nil {
					t.Fatal(err)
				}
				again, _ := json.Marshal(replay.TemplateSnapshot)
				if string(frozen) != string(again) || replay.Brief.CreativeExpansion != expansion {
					t.Fatal("replay changed the frozen method or user's fidelity choice")
				}
			}
		})
	}
}

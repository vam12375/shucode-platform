package media

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVideoTemplateRewrittenCatalogContract(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
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
			// 新模板给可重组叙事方法；KV 保留主角方法并修正收尾与声音。
			timed := recipe.NominalSeconds != 0
			versionOK := recipe.Version == "3.0.1" || recipe.ID == "brand-kv-unfold" && recipe.Version == "2.1.1"
			structureOK := timed && versionOK && len(recipe.Beats) >= 12 && len(recipe.Beats) <= 16
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

// 餐饮与品牌的 15 个配方写在景别规则之前，景别写在句中而不是句首；它们只受声音规则约束。
var freeformFramingRecipes = map[string]bool{
	"kitchen-prep": true, "food-serving": true, "drink-pour": true, "coffee-ritual": true, "food-gift": true,
	"fresh-ingredient": true, "restaurant-door-to-dish": true, "open-kitchen-finish": true, "hotpot-first-dip": true,
	"bakery-cut-reveal": true, "coffee-bar-ritual": true, "sharing-table-sequence": true, "cafe-rainy-vlog": true,
	"brand-kv-unfold": true, "brand-ip-mini-drama": true,
}

// 长的写在前面：按前缀匹配时“微距特写”“中近景”不会被“特写”“中景”抢走。
var framingScales = []string{"微距特写", "中近景", "远景", "全景", "中景", "近景", "特写"}

func framingScale(framing string) string {
	for _, scale := range framingScales {
		if strings.HasPrefix(framing, scale+"，") {
			return scale
		}
	}
	return ""
}

// 样片问题：旧写法“旁白（本单开启时）”只在后期独白开关打开时生效，而后期的统一独白与连续配乐
// 依赖尚未接入的后期服务、默认关闭，成片等于没有旁白与配乐；三拍配方也没有远、中、近、特写的景别变化。
// 配乐与旁白一律写成视频模型原声；每镜声音只能是画外音（原声）、角色台词或明确的无旁白。
func TestRecipesUseModelNativeAudioAndVariedShotScales(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			raw, _ := json.Marshal(recipe)
			if strings.Contains(string(raw), "旁白（本单开启时）") {
				t.Fatal("narration still depends on the post-production voiceover switch")
			}
			if recipe.Style == nil || !strings.Contains(recipe.Style.Sound, "原声") {
				t.Fatal("style.sound must describe model-native audio")
			}
			voicedCore := 0
			for i, beat := range recipe.Beats {
				switch {
				case strings.HasPrefix(beat.Sound, "画外音（原声）："), strings.HasPrefix(beat.Sound, "台词："):
					if beat.Tier == TemplateTierCore {
						voicedCore++
					}
				case strings.HasPrefix(beat.Sound, "无旁白"), strings.HasPrefix(beat.Sound, "声音（模型原声开启时）："):
				default:
					t.Fatalf("beat %d sound has no recognised prefix: %q", i+1, beat.Sound)
				}
			}
			// 15 秒成片只剩核心镜头：开头、主动作与结尾的三句必须留得住。
			if recipe.Version != "3.0.1" && recipe.Version != "2.1.1" && voicedCore < 3 {
				t.Fatalf("only %d voiced core beats", voicedCore)
			}
			if freeformFramingRecipes[recipe.ID] || recipe.Version == "3.0.1" {
				return
			}
			wide, detail := 0, 0
			scales := make([]string, len(recipe.Beats))
			for i, beat := range recipe.Beats {
				scales[i] = framingScale(beat.Framing)
				switch scales[i] {
				case "":
					t.Fatalf("beat %d framing must start with a shot scale: %q", i+1, beat.Framing)
				case "远景", "全景":
					wide++
				case "特写", "微距特写":
					detail++
				}
			}
			if wide < 1 || detail < 2 {
				t.Fatalf("shot scales lack range: wide=%d detail=%d", wide, detail)
			}
			// 任何时长下相邻两镜景别都不同，否则压缩后会出现同景别连切。
			for target := recipe.DurationRange.Min; target <= recipe.DurationRange.Max; target++ {
				kept, _, ok := planTemplateTiming(timingBeats(recipe.Beats), recipe.DurationRange, target)
				if !ok {
					t.Fatalf("%d seconds cannot be planned", target)
				}
				for k := 1; k < len(kept); k++ {
					if scales[kept[k-1]] == scales[kept[k]] {
						t.Fatalf("%d second cut repeats %s on beats %d and %d", target, scales[kept[k]], kept[k-1]+1, kept[k]+1)
					}
				}
			}
		})
	}
}

func TestRewrittenTemplateDoesNotInheritPreviousVersionReview(t *testing.T) {
	f, project, input := templateReviewFixture(t)
	ctx := context.Background()
	// 夹具取目录首条配方的现行版本；它随配方重写升版（商品质感现为 2.0.0），这里不写死，
	// 只要求现行版本确实不同于下面伪造的旧版审核。
	currentVersion := input.TemplateVersion
	if currentVersion == "1.0.0" {
		t.Fatal("fixture recipe must have been rewritten past 1.0.0")
	}
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
	current, err := findVideoTemplate(templates, input.TemplateID, currentVersion)
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

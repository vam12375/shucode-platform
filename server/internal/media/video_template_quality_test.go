package media

import (
	"encoding/json"
	"strings"
	"testing"
)

var onlineProductAdRecipeIDs = map[string]bool{
	"velocity-light-matchcut": true, "celestial-spark-reveal": true, "mixed-media-stride": true,
	"street-pop-product": true, "sport-to-detail-eyewear": true, "luminous-outline-turn": true,
	"editorial-product-pages": true, "botanical-sculpture-reveal": true, "counterbalance-object-film": true,
	"light-cone-material-response": true, "metallic-drape-orbit": true, "trunk-spatial-choreography": true,
	"sole-silhouette-formation": true, "screen-world-roundtrip": true, "mineral-frame-product": true,
}

func expectedRecipeVersion(id string) string {
	if onlineProductAdRecipeIDs[id] {
		return "3.0.0"
	}
	switch id {
	case "chromatic-micro-world":
		return "3.0.1"
	case "electronic-pulse-showcase", "product-detail-reassembly":
		return "3.0.3"
	case "material-assembly-timepiece", "precision-detail-lineup", "raking-light-layered-reveal", "rhythmic-feature-demo":
		return "3.0.0"
	case "brand-kv-unfold":
		return "2.1.6"
	case "black-gold-reveal", "floating-exploded-showcase":
		return "3.0.8"
	case "food-serving", "fresh-ingredient":
		return "3.0.7"
	default:
		return "3.0.6"
	}
}

func TestVideoTemplateRewrittenCatalogContract(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 84 {
		t.Fatalf("expected 84 recipes, got %d", len(templates))
	}
	seen := map[string]bool{}
	actions := map[string]bool{}
	checks := map[string]bool{}
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			// 新模板给可重组叙事方法；KV 保留主角方法并修正收尾与声音。
			timed := recipe.NominalSeconds != 0
			versionOK := recipe.Version == expectedRecipeVersion(recipe.ID)
			beatCountOK := len(recipe.Beats) >= 12 && len(recipe.Beats) <= 16
			if onlineProductAdRecipeIDs[recipe.ID] {
				beatCountOK = len(recipe.Beats) == 5
				if recipe.Family != "product" || recipe.Provenance == nil || !strings.HasPrefix(recipe.Provenance.URL, "https://") || recipe.Provenance.Observation == "" || recipe.Provenance.Adaptation == "" {
					t.Fatal("online product recipe lost its public source or adaptation boundary")
				}
			}
			switch recipe.ID {
			case "floating-exploded-showcase":
				beatCountOK = len(recipe.Beats) == 8
			case "black-gold-reveal":
				beatCountOK = len(recipe.Beats) == 7
			case "rhythmic-feature-demo":
				// 六个编辑窗口保留接触、反馈与明暗的节奏，拍内起音不是独立生成请求。
				beatCountOK = len(recipe.Beats) == 6
			case "chromatic-micro-world", "raking-light-layered-reveal":
				// 五个核心编辑窗口在拍内保留近观快切或连续视差，不拆成供应商短请求。
				beatCountOK = len(recipe.Beats) == 5
			case "precision-detail-lineup":
				// 十个参考镜头两两配成一个核心拍，拍内硬切，长短版均保留十镜。
				beatCountOK = len(recipe.Beats) == 5
			case "electronic-pulse-showcase":
				// 六个核心场次覆盖所有时长，白棚巡航与结构层次另有三个镜内延展段。
				beatCountOK = len(recipe.Beats) == 9
			}
			structureOK := timed && versionOK && beatCountOK
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
	for id := range onlineProductAdRecipeIDs {
		if !seen[id] {
			t.Fatalf("missing online product recipe: %s", id)
		}
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
				case strings.HasPrefix(beat.Sound, "无旁白"), strings.HasPrefix(beat.Sound, "声音（模型原声）："):
				default:
					t.Fatalf("beat %d sound has no recognised prefix: %q", i+1, beat.Sound)
				}
			}
			// 15 秒成片只剩核心镜头：开头、主动作与结尾的三句必须留得住。
			if recipe.Version != expectedRecipeVersion(recipe.ID) && voicedCore < 3 {
				t.Fatalf("only %d voiced core beats", voicedCore)
			}
			if freeformFramingRecipes[recipe.ID] || recipe.Version == expectedRecipeVersion(recipe.ID) {
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

// 已删除的后期声音开关不能让模板把模型原声写成条件功能，也不能让中间生成段提前终止音乐。
func TestRegularTemplatesUseNativeAudioHandoffWithoutRemovedSwitches(t *testing.T) {
	templates, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, recipe := range templates {
		t.Run(recipe.ID, func(t *testing.T) {
			raw, _ := json.Marshal(recipe)
			for _, removed := range []string{"原声开启", "原声关闭", "后期独白", "native audio enabled"} {
				if strings.Contains(string(raw), removed) {
					t.Errorf("removed sound switch remains: %s", removed)
				}
			}
			if recipe.Style == nil {
				t.Fatal("missing native audio style")
			}
			for _, required := range []string{"中间生成段", "全片最终段", "完整台词在自然句末交接", "接点前后各0.1秒是观察窗", "不是静音或裁切区", "跨独立段不承诺同一音乐轨"} {
				if !strings.Contains(recipe.Style.Sound, required) {
					t.Errorf("missing segment handoff requirement %q", required)
				}
			}
		})
	}
}

package media

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCreativeRecipesHaveDistinctStoriesAndFlexibleStaging(t *testing.T) {
	definitions, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{}
	for _, recipe := range definitions {
		if recipe.ID == "brand-kv-unfold" {
			continue
		}
		t.Run(recipe.ID, func(t *testing.T) {
			if recipe.Version != "3.0.0" || !strings.Contains(recipe.Description.ZH, "叙事方法") || !strings.Contains(recipe.Description.ZH, "可重组") {
				t.Fatal("recipe still acts as a fixed shooting script")
			}
			if methods[recipe.Description.ZH] {
				t.Fatal("templates share the same method")
			}
			methods[recipe.Description.ZH] = true
			raw, _ := json.Marshal(recipe)
			for _, old := range []string{"两座黏土", "一朵小云", "让光表演", "单一摄影棚", "没有配角素材", "产品保持真实材质"} {
				if strings.Contains(string(raw), old) {
					t.Errorf("old creative lock: %s", old)
				}
			}
			if !strings.Contains(recipe.Style.Motion, "信息") || !strings.Contains(recipe.Style.Sound, "本单") {
				t.Fatal("camera or audio ignores the actual brief")
			}
		})
	}
}

func TestCreativeDomainBoundaries(t *testing.T) {
	definitions, err := VideoTemplateDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for id, phrases := range map[string][]string{
		"clay-style-brand-world":  {"主体与环境", "黏土", "身份锚点"},
		"brand-ip-mini-drama":     {"普通虚构配角", "目标", "反馈", "Logo"},
		"product-texture":         {"视觉故事", "尺度", "运镜"},
		"restaurant-door-to-dish": {"门店动线", "招牌菜", "交付"},
		"hotpot-first-dip":        {"生熟", "同一筷"},
		"bakery-cut-reveal":       {"切口", "碎屑"},
		"coffee-ritual":           {"成杯", "冲煮参数"},
		"sharing-table-sequence":  {"份量", "递接"},
	} {
		t.Run(id, func(t *testing.T) {
			recipe, err := findVideoTemplate(definitions, id, "3.0.0")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(recipe)
			for _, phrase := range phrases {
				if !strings.Contains(string(raw), phrase) {
					t.Errorf("missing %s", phrase)
				}
			}
		})
	}
}

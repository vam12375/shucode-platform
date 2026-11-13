package media

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNarrativeAssetRolesContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/video-narrative-roles.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		AssetIDs []string        `json:"assetIds"`
		Assets   []ObservedAsset `json:"assets"`
		Story    *StoryPlan      `json:"story"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	b, plan, _ := wholeDurationFixture([]int{15}, 15)
	b.AssetIDs = fixture.AssetIDs
	b.AssetUnderstanding = &AssetUnderstanding{Product: "品牌角色", Category: "IP", Summary: "两个角色和标识", Keywords: []string{}, Assets: fixture.Assets, TemplateCandidates: []ObservedTemplate{}}
	if err = validateAssetUnderstanding(b); err != nil {
		t.Fatal(err)
	}
	plan.Story = fixture.Story
	plan.Shots[0].AssetID = "hero"
	plan.Shots[0].Creative.AssetIDs = []string{"hero", "helper"}
	if err = validateStoryPlan(b, plan); err != nil {
		t.Fatal(err)
	}
	template := VideoTemplate{ID: "t", Version: "1", Family: "brand", MinAssets: 2, AspectRatios: []string{b.AspectRatio}, DurationRange: TemplateDurationRange{Min: 10, Max: 60}, VerifiedModelIDs: []string{b.VideoModel}}
	b.TemplateID, b.TemplateVersion = template.ID, template.Version
	if _, err = validateTemplateSelection(b, []VideoTemplate{template}); err != nil {
		t.Fatal(err)
	}
	b.Finishing.Brand = testVideoBrand("helper")
	if _, err = validateTemplateSelection(b, []VideoTemplate{template}); err == nil {
		t.Fatal("post-only character inflated subject evidence")
	}
	b.Finishing.Brand = nil
	template.MinAssets = 3
	if _, err = validateTemplateSelection(b, []VideoTemplate{template}); err == nil {
		t.Fatal("Logo inflated subject evidence")
	}
	template.MinAssets, template.Family = 1, "texture"
	if _, err = validateTemplateSelection(b, []VideoTemplate{template}); err == nil {
		t.Fatal("character substituted product evidence")
	}
}

package media

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func adaptivePlanFixture() (Brief, Plan, []Model) {
	m := fixtureVideoModel()
	b := Brief{Mode: "auto", ResearchPolicy: "auto", ReferenceMode: "multimodal", Product: "product", AssetIDs: []string{"a"}, Platforms: []string{}, ReferenceURLs: []string{}, VideoModel: m.ID, Resolution: "720p", AspectRatio: "16:9", Duration: 10, MaxCreditsMicro: 10_000_000}
	p := Plan{References: []Reference{}, Research: &ResearchDecision{Mode: "original", Reason: "素材和需求足够", IntentSummary: "展示真实细节", AssetObservations: []AssetObservation{{AssetID: "a", Description: "白色完整商品"}}, CreativeGaps: []string{}}, Shots: []ShotPlan{{Duration: 10, Framing: "近景", Camera: "缓推", Action: "展示单一细节", Purpose: "证明", Prompt: "商品细节"}}}
	return b, p, []Model{m}
}

func TestAdaptiveOriginalOmitsSourceFieldsAndKeepsLegacyStrict(t *testing.T) {
	b, p, models := adaptivePlanFixture()
	if _, _, err := BuildShots(b, p, models); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sourceUrl", "sourceStart", "sourceEnd"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("invented source field: %s", raw)
		}
	}
	var decoded Plan
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, _, err = BuildShots(b, decoded, models); err != nil {
		t.Fatal(err)
	}
	b.ResearchPolicy = ""
	if _, _, err = BuildShots(b, p, models); err == nil {
		t.Fatal("legacy accepted original provenance")
	}
}

func TestAdaptiveResearchRejectsInvalidSourceAndMissingObservation(t *testing.T) {
	for _, mutation := range []func(*Brief, *Plan){
		func(b *Brief, p *Plan) { p.Research = nil },
		func(b *Brief, p *Plan) { p.Research.AssetObservations = nil },
		func(b *Brief, p *Plan) { p.Research.AssetObservations[0].AssetID = "foreign" },
		func(b *Brief, p *Plan) { p.Shots[0].SourceStart = 1 },
		func(b *Brief, p *Plan) { p.Research.Mode = "template" },
		func(b *Brief, p *Plan) { b.ResearchPolicy = "required"; b.Platforms = []string{"douyin"} },
		func(b *Brief, p *Plan) { b.ReferenceURLs = []string{"https://www.douyin.com/video/1"} },
	} {
		b, p, models := adaptivePlanFixture()
		mutation(&b, &p)
		if _, _, err := BuildShots(b, p, models); err == nil {
			t.Fatal("invalid adaptive plan accepted")
		}
	}
	b, p, models := adaptivePlanFixture()
	raw, _ := json.Marshal(p)
	raw = []byte(strings.Replace(string(raw), "\"duration\":10", "\"duration\":10,\"sourceStart\":0", 1))
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildShots(b, p, models); err == nil {
		t.Fatal("explicit fabricated zero source time accepted")
	}
}

func TestAdaptiveReferenceHonorsExplicitLinksAndDisabledPolicy(t *testing.T) {
	b, p, models := adaptivePlanFixture()
	b.ReferenceURLs = []string{"https://www.douyin.com/video/1"}
	p.Research.Mode = "reference"
	p.References = []Reference{{URL: b.ReferenceURLs[0], Title: "observed", CapturedAt: time.Now(), Evidence: "0-5s实际画面", SelectionReason: "指定来源"}}
	p.Shots[0].SourceURL = b.ReferenceURLs[0]
	p.Shots[0].SourceEnd = 5
	if _, _, err := BuildShots(b, p, models); err != nil {
		t.Fatal(err)
	}
	b.ResearchPolicy = "disabled"
	if _, _, err := BuildShots(b, p, models); err == nil {
		t.Fatal("disabled accepted online reference")
	}
	b.ResearchPolicy = "auto"
	p.References[0].URL = "https://www.douyin.com/video/2"
	p.Shots[0].SourceURL = p.References[0].URL
	if _, _, err := BuildShots(b, p, models); err == nil {
		t.Fatal("explicit source silently changed")
	}
}

func TestAdaptiveBriefAllowsNoResearchPlatformsButRejectsConflicts(t *testing.T) {
	b, _, models := adaptivePlanFixture()
	assets := []Asset{{ID: "a", Kind: "image"}}
	if err := ValidateBrief(b, models, assets); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*Brief){func(b *Brief) { b.ResearchPolicy = "required" }, func(b *Brief) {
		b.ResearchPolicy = "disabled"
		b.ReferenceURLs = []string{"https://www.douyin.com/video/1"}
	}, func(b *Brief) { b.Mode = "replicate" }, func(b *Brief) { b.TemplateID = "x" }, func(b *Brief) { b.DistributionPlatform = "invalid" }} {
		candidate := b
		mutation(&candidate)
		if err := ValidateBrief(candidate, models, assets); err == nil {
			t.Fatal("invalid adaptive brief accepted")
		}
	}
}

func TestAdaptiveSourceJSONPresenceAndDefaultPlatformBoundaries(t *testing.T) {
	b, p, models := adaptivePlanFixture()
	b.ResearchPolicy = "required"
	b.Platforms = []string{"douyin"}
	p.Research.Mode = "reference"
	p.References = []Reference{{URL: "https://www.douyin.com/video/1", Title: "observed", CapturedAt: time.Now(), Evidence: "0-5s", SelectionReason: "match"}}
	p.Shots[0].SourceURL = p.References[0].URL
	p.Shots[0].SourceEnd = 5
	raw, _ := json.Marshal(p)
	for _, candidate := range []string{strings.Replace(string(raw), `"sourceStart":0,`, "", 1), strings.Replace(string(raw), `"sourceEnd":5`, `"sourceEnd":null`, 1)} {
		var decoded Plan
		err := json.Unmarshal([]byte(candidate), &decoded)
		if err == nil {
			_, _, err = BuildShots(b, decoded, models)
		}
		if err == nil {
			t.Fatal("partial or null source accepted")
		}
	}
	for _, test := range []struct {
		url       string
		platforms []string
		want      bool
	}{{"https://www.douyin.com/video/1", nil, true}, {"https://xhslink.com/a", nil, true}, {"https://www.bilibili.com/video/1", nil, false}, {"https://b23.tv/a", []string{"bilibili"}, true}, {"https://douyin.com.evil.test/a", nil, false}, {"https://evildouyin.com/a", nil, false}} {
		if got := researchURLAllowed(test.url, test.platforms); got != test.want {
			t.Fatalf("scope %s: got %v", test.url, got)
		}
	}
}

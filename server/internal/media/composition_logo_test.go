package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"
)

func testVideoBrand(logo string) *VideoBrand {
	return &VideoBrand{Preset: "clean", PrimaryColor: "#123456", BackgroundColor: "#000000", TextColor: "#FFFFFF", FontFamily: "sans", LogoAssetID: logo}
}

func TestBrandLogoCannotReplaceProductEvidenceOrFirstFrame(t *testing.T) {
	m := fixtureVideoModel()
	b := Brief{Mode: "auto", Product: "fixture", AssetIDs: []string{"logo"}, Platforms: []string{"douyin"}, VideoModel: m.ID, Resolution: "720p", AspectRatio: "16:9", Duration: 5, MaxCreditsMicro: 10000000, Finishing: &FinishingOptions{Subtitles: "none", Brand: testVideoBrand("logo")}}
	assets := []Asset{{ID: "logo", Kind: "image"}, {ID: "product", Kind: "image"}}
	if err := ValidateBrief(b, []Model{m}, assets); err == nil {
		t.Fatal("logo-only project accepted as product evidence")
	}
	b.AssetIDs = []string{"product", "logo"}
	if err := ValidateBrief(b, []Model{m}, assets); err != nil {
		t.Fatal("product plus post-only logo rejected", err)
	}
	plan := Plan{References: []Reference{{URL: "https://example.test/ref", Title: "fixture", Evidence: "observed", SelectionReason: "related", CapturedAt: time.Now()}}, Shots: []ShotPlan{{SourceURL: "https://example.test/ref", SourceEnd: 5, Duration: 5, Framing: "close", Camera: "push", Action: "show", Purpose: "sell", Prompt: "product", AssetID: "logo"}}}
	if _, _, err := BuildShots(b, plan, []Model{m}); err == nil {
		t.Fatal("branding logo accepted as generated first frame")
	}
	plan.Shots[0].AssetID = "product"
	if _, _, err := BuildShots(b, plan, []Model{m}); err != nil {
		t.Fatal("product first frame rejected", err)
	}
}

func brandWorkerProject(t *testing.T) (*fixture, *Project, string) {
	t.Helper()
	f := setup(t)
	p := f.project(t)
	ctx := context.Background()
	logo := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			logo.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, logo); err != nil {
		t.Fatal(err)
	}
	id, _, err := f.s.Blobs.Put(f.team, &encoded, MaxUpload)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: id, Kind: "image", Mime: "image/png"}); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Brief.ReferenceMode = "multimodal"
		v.Brief.AssetIDs = []string{"a", id}
		v.Brief.Finishing = &FinishingOptions{Subtitles: "none", Brand: testVideoBrand(id)}
		v.Models[0].UpstreamModel = "doubao-seedance-2-0-mini-260615"
		for i := range v.Shots {
			v.Shots[i].Plan.AssetID = ""
			v.Shots[i].Plan.Subtitle = ""
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, p, id
}

func TestBrandLogoExcludedFromActualMultimodalSupplierPayload(t *testing.T) {
	requireBrandComposition(t)
	f, p, logo := brandWorkerProject(t)
	f.s.Tick(context.Background())
	p = current(t, f, p)
	if f.submits.Load() != 1 || p.Shots[0].Status != "video_running" {
		t.Fatalf("brand generation did not submit: %+v", p)
	}
	f.requestMu.Lock()
	defer f.requestMu.Unlock()
	content := f.requestBodies[0]["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("logo reached actual reference payload: %#v", content)
	}
	reference := content[1].(map[string]any)
	if reference["role"] != "reference_image" || submittedAssetID(t, reference, "image_url") != "a" || submittedAssetID(t, reference, "image_url") == logo {
		t.Fatalf("wrong product reference: %#v", reference)
	}
}

func TestBrandMissingLogoFailsBeforeNewPaidStage(t *testing.T) {
	requireBrandComposition(t)
	f, p, logo := brandWorkerProject(t)
	if err := os.Remove(f.s.Blobs.Path(f.team, logo)); err != nil {
		t.Fatal(err)
	}
	f.s.Tick(context.Background())
	p = current(t, f, p)
	if f.submits.Load() != 0 || p.Status != "failed" || p.Shots[0].Error != "composition_logo_unavailable" {
		t.Fatalf("missing logo incurred generation: %+v", p)
	}
}

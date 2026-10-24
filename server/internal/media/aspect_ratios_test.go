package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestModelAspectRatiosFollowVerifiedModelFamilies(t *testing.T) {
	fixed := []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"}
	for _, upstream := range []string{"doubao-seedance-2-0-260128", "doubao-seedance-2-0-mini-260615", "doubao-seedance-2-0-fast-260128"} {
		m := Model{Kind: "video", Adapter: "ark", UpstreamModel: upstream}
		if got := ModelAspectRatios(m); !slices.Equal(got, append([]string{"auto"}, fixed...)) {
			t.Fatalf("%s ratios = %v", upstream, got)
		}
	}
	for _, upstream := range []string{"doubao-seedance-1-0-pro-250528", "doubao-seedance-1-0-pro-fast-251015"} {
		if got := ModelAspectRatios(Model{Kind: "video", Adapter: "ark", UpstreamModel: upstream}); !slices.Equal(got, fixed) {
			t.Fatalf("1.0 fixed ratios = %v", got)
		}
	}
	for _, m := range []Model{
		{Kind: "video", Adapter: "ark", UpstreamModel: "unknown"},
		{Kind: "video", Adapter: "future", UpstreamModel: "doubao-seedance-2-0-260128"},
		{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-5-pro-260128"},
		{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-1-0-unknown-250528"},
		{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-1-0-lite-t2v-250428"},
		{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-1-0-pro-not-a-date"},
	} {
		if got := ModelAspectRatios(m); !slices.Equal(got, []string{"16:9", "9:16", "1:1"}) {
			t.Fatalf("unknown model capability was expanded: %v", got)
		}
	}
	got := ModelAspectRatios(Model{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-260128"})
	got[0] = "changed"
	if ModelAspectRatios(Model{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-260128"})[0] != "auto" {
		t.Fatal("capability caller mutated owner state")
	}
}

func TestCanvasVideoRatiosUseTheSelectedModel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	models, _ := f.s.Repo.Models(ctx)
	models[0].UpstreamModel = "doubao-seedance-2-0-mini-260615"
	raw, _ := json.Marshal(models)
	_ = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw)
	r := canvasRequest(t, f)
	r.Kind, r.ModelID, r.Resolution, r.Duration, r.Count, r.ExpectedQuoteMicro = "video", "v", "720p", 5, 1, 500_000
	for _, ratio := range []string{"auto", "16:9", "4:3", "1:1", "3:4", "9:16", "21:9"} {
		r.AspectRatio = ratio
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err != nil {
			t.Fatalf("verified ratio %s rejected: %v", ratio, err)
		}
	}
	for _, ratio := range []string{"adaptive", "3:2", "2:3", "2:1"} {
		r.AspectRatio = ratio
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
			t.Fatalf("unsupported wire ratio %s accepted", ratio)
		}
	}
}

func TestProviderMapsOnlyWireAutoToAdaptive(t *testing.T) {
	var bodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "fixture-task"})
	}))
	defer upstream.Close()
	p := Provider{BaseURL: upstream.URL, Client: upstream.Client()}
	m := Model{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-260128"}
	for _, ratio := range ModelAspectRatios(m) {
		if _, err := p.CanvasVideo(context.Background(), m, CanvasRequest{AspectRatio: ratio}, nil); err != nil {
			t.Fatal(err)
		}
		want := ratio
		if ratio == "auto" {
			want = "adaptive"
		}
		if bodies[len(bodies)-1]["ratio"] != want {
			t.Fatalf("wire %s sent as %v", ratio, bodies[len(bodies)-1]["ratio"])
		}
		body := videoSubmitBodyWithImages(m, Brief{AspectRatio: ratio}, ShotPlan{}, []string{}, "")
		if body["ratio"] != want {
			t.Fatalf("ordinary adapter maps %s incorrectly", ratio)
		}
	}
}

func TestBriefUsesFixedRatiosOfSelectedModel(t *testing.T) {
	f := setup(t)
	models, _ := f.s.Repo.Models(context.Background())
	models[0].UpstreamModel = "doubao-seedance-2-0-mini-260615"
	b := Brief{Mode: "auto", Product: "fixture", AssetIDs: []string{"a"}, Platforms: []string{"douyin"}, VideoModel: "v", Resolution: "720p", Duration: 5, MaxCreditsMicro: 1_000_000}
	for _, ratio := range []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9"} {
		b.AspectRatio = ratio
		if err := ValidateBrief(b, models, []Asset{{ID: "a", Kind: "image"}}); err != nil {
			t.Fatalf("fixed ratio %s rejected: %v", ratio, err)
		}
	}
	b.AspectRatio = "auto"
	if err := ValidateBrief(b, models, []Asset{{ID: "a", Kind: "image"}}); err == nil {
		t.Fatal("adaptive multi-shot canvas accepted")
	}
	models[0].UpstreamModel = "unknown"
	b.AspectRatio = "21:9"
	if err := ValidateBrief(b, models, []Asset{{ID: "a", Kind: "image"}}); err == nil {
		t.Fatal("unknown model expanded beyond legacy ratios")
	}
}

func TestOrdinaryFirstFrameUsesRequestedFixedRatio(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": "https://example.test/first-frame.png"}}})
	}))
	defer upstream.Close()
	p := Provider{BaseURL: upstream.URL, Client: upstream.Client()}
	if _, err := p.Image(context.Background(), Model{UpstreamModel: "doubao-seedream-4-5-251128"}, "first frame", "", "21:9"); err != nil {
		t.Fatal(err)
	}
	if body["prompt"] != "first frame（画面比例 21:9）" || body["watermark"] != false || body["size"] != "2K" {
		t.Fatalf("ordinary frame lost ratio: %+v", body)
	}
	if _, err := p.Image(context.Background(), Model{UpstreamModel: "doubao-seedream-3-0-t2i-250415"}, "first frame", "", "4:3"); err != nil {
		t.Fatal(err)
	}
	if body["size"] != "1152x864" {
		t.Fatalf("legacy first-frame size=%v", body["size"])
	}
}

func TestSourceRatioSuggestionPreservesFixedFraming(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want string
	}{{1600, 900, "16:9"}, {900, 1600, "9:16"}, {1200, 900, "4:3"}, {900, 1200, "3:4"}, {2100, 900, "21:9"}, {1000, 1000, "1:1"}, {0, 0, "9:16"}} {
		if got := nearestFixedVideoRatio(tc.w, tc.h); got != tc.want {
			t.Fatalf("%dx%d suggestion=%s want=%s", tc.w, tc.h, got, tc.want)
		}
	}
}

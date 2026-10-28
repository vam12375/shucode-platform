package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func toapisH3FixtureModel(upstream string) Model {
	m := toapisModel()
	m.UpstreamModel = upstream
	m.Resolutions = []string{"768p"}
	return m
}

func TestToAPIsH3CatalogCapabilities(t *testing.T) {
	for _, upstream := range []string{"MiniMax-H3", "MiniMax-H3-Max", "MiniMax-H3-Max-Turbo"} {
		m := toapisH3FixtureModel(upstream)
		m.Resolutions = []string{"480p", "768p"}
		if upstream == "MiniMax-H3" {
			m.Resolutions = []string{"2K", "768p"}
		}
		if upstream != "MiniMax-H3-Max-Turbo" {
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
		}
		if err := ValidateModels([]Model{m}); err != nil {
			t.Fatalf("%s rejected: %v", upstream, err)
		}
		if m.MinDuration != 4 || m.MaxDuration != 15 || len(m.Durations) != 0 {
			t.Fatalf("%s duration capability = %d–%d/%v, want continuous 4–15", upstream, m.MinDuration, m.MaxDuration, m.Durations)
		}
		if got := ModelAspectRatios(m); !slices.Equal(got, append([]string{"auto"}, fixedVideoRatios...)) {
			t.Fatalf("%s ratios=%v", upstream, got)
		}
	}
	for name, change := range map[string]func(*Model){
		"wrong-case": func(m *Model) { m.UpstreamModel = "minimax-h3" },
		"h3-720p":    func(m *Model) { m.Resolutions = []string{"720p"} },
		"h3-480p":    func(m *Model) { m.Resolutions = []string{"480p"} },
		"max-2K":     func(m *Model) { m.UpstreamModel = "MiniMax-H3-Max"; m.MinDuration = 4; m.Resolutions = []string{"2K"} },
		"wire-capital-in-catalog": func(m *Model) {
			m.UpstreamModel = "MiniMax-H3-Max"
			m.MinDuration = 4
			m.Resolutions = []string{"768P"}
		},
		"max-too-short": func(m *Model) { m.UpstreamModel = "MiniMax-H3-Max"; m.MinDuration = 3 },
		"turbo-invalid-range": func(m *Model) {
			m.UpstreamModel = "MiniMax-H3-Max-Turbo"
			m.MinDuration = 3
		},
		"audio-toggle": func(m *Model) { m.GenerateAudio = true },
		"turbo-reference": func(m *Model) {
			m.UpstreamModel = "MiniMax-H3-Max-Turbo"
			m.MinDuration = 4
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
		},
		"too-long-reference": func(m *Model) {
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 16, MaxImages: 8, PriceMicro: 100_000}
		},
		"reference-token-rate": func(m *Model) {
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000, TokenPriceMicroPerMillion: 5}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := toapisH3FixtureModel("MiniMax-H3")
			change(&m)
			if ValidateModels([]Model{m}) == nil {
				t.Fatal("unsupported catalog accepted")
			}
		})
	}
}

func TestToAPIsH3DocumentedParametersAndInputModes(t *testing.T) {
	for _, upstream := range []string{"MiniMax-H3", "MiniMax-H3-Max", "MiniMax-H3-Max-Turbo"} {
		for _, mode := range []string{"text", "first-frame", "first-last", "video-reference"} {
			if mode == "video-reference" && upstream == "MiniMax-H3-Max-Turbo" {
				continue
			}
			t.Run(upstream+"/"+mode, func(t *testing.T) {
				m := toapisH3FixtureModel(upstream)
				m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
				if upstream == "MiniMax-H3-Max-Turbo" {
					m.VideoReference = nil
				}
				var body map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/videos/generations" {
						t.Errorf("path=%s", r.URL.Path)
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					_, _ = w.Write([]byte(`{"id":"vid_h3_fixture","object":"generation.task","status":"queued"}`))
				}))
				defer server.Close()
				p := Provider{Adapter: "toapis", BaseURL: server.URL, Client: server.Client()}
				r := CanvasRequest{Prompt: "fixture", Resolution: "768p", AspectRatio: "9:16", Duration: 5}
				var images []string
				video := ""
				if mode != "text" {
					images = []string{"https://media.example.test/a.png"}
				}
				if mode == "first-last" || mode == "video-reference" {
					images = append(images, "https://media.example.test/b.png")
				}
				if mode == "video-reference" {
					video = "https://media.example.test/clip.mp4"
				}
				id, err := p.CanvasVideo(context.Background(), m, r, images, video)
				if err != nil || id != "vid_h3_fixture" {
					t.Fatalf("submit id=%s err=%v", id, err)
				}
				wantRes := "768p"
				if upstream != "MiniMax-H3" {
					wantRes = "768P"
				}
				wantRatio := "9:16"
				if mode == "first-frame" || mode == "first-last" {
					wantRatio = "adaptive"
				}
				if body["model"] != upstream || body["resolution"] != wantRes || body["aspect_ratio"] != wantRatio || body["watermark"] != false {
					t.Fatalf("body=%#v", body)
				}
				for _, key := range []string{"generate_audio", "sound", "content", "ratio"} {
					if _, ok := body[key]; ok {
						t.Fatalf("unsupported H3 field %s", key)
					}
				}
				if len(images) > 0 {
					for i, image := range body["image_with_roles"].([]any) {
						role := "first_frame"
						if i == 1 {
							role = "last_frame"
						}
						if video != "" {
							role = "reference_image"
						}
						if image.(map[string]any)["role"] != role {
							t.Fatalf("roles=%#v", body)
						}
					}
				}
			})
		}
	}
}

func TestToAPIsH3InvalidInputsFailWithoutHTTP(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	p := Provider{Adapter: "toapis", BaseURL: server.URL, Client: server.Client()}
	for _, tc := range []struct {
		upstream, resolution, ratio string
		duration                    int
		audio                       bool
		video                       string
	}{
		{"MiniMax-H3", "768p", "auto", 5, false, ""},
		{"MiniMax-H3", "720p", "16:9", 5, false, ""},
		{"MiniMax-H3", "768p", "16:9", 5, true, ""},
		{"MiniMax-H3-Max", "768p", "16:9", 3, false, ""},
		{"MiniMax-H3-Max-Turbo", "768p", "16:9", 5, false, "https://media.example.test/clip.mp4"},
	} {
		m := toapisH3FixtureModel(tc.upstream)
		m.GenerateAudio = tc.audio
		m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
		var images []string
		if tc.video != "" {
			images = []string{"https://media.example.test/a.png"}
		}
		if _, err := p.CanvasVideo(context.Background(), m, CanvasRequest{Prompt: "fixture", Resolution: tc.resolution, AspectRatio: tc.ratio, Duration: tc.duration}, images, tc.video); err == nil {
			t.Fatalf("invalid input accepted: %+v", tc)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid input made %d requests", requests)
	}
}

func TestToAPIsH3CanvasRejectsTextAutoAndTurboReferenceBeforeProjectCreation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, upstream := range []string{"MiniMax-H3", "MiniMax-H3-Max", "MiniMax-H3-Max-Turbo"} {
		m := toapisH3FixtureModel(upstream)
		m.ID = "v"
		raw, _ := json.Marshal([]Model{m})
		_ = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw)
		r := CanvasRequest{Kind: "video", ModelID: "v", Prompt: "fixture", AssetIDs: []string{}, Resolution: "768p", AspectRatio: "auto", Duration: 5, Count: 1, ExpectedQuoteMicro: m.PriceMicro * 5, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture"}
		id := uuid.NewString()
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", id, r); err == nil {
			t.Fatalf("%s text auto accepted", upstream)
		}
		if project, _ := f.s.Repo.Get(ctx, f.team, id); project != nil {
			t.Fatal("invalid text created a project")
		}
		r.AspectRatio = "16:9"
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err != nil {
			t.Fatalf("valid text rejected: %v", err)
		}
		if upstream == "MiniMax-H3-Max-Turbo" {
			r.AssetIDs = []string{"a"}
			r.VideoReferences = []CanvasVideoReference{{AssetID: uuid.NewString(), ReferenceClipAssetID: uuid.NewString()}}
			if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
				t.Fatal("turbo reference accepted")
			}
		}
	}
	if f.submits.Load() != 0 {
		t.Fatal("canvas validation submitted upstream")
	}
}

func TestToAPIsH3WorkerSharesTaskRecoveryAndSettlement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
			_, _ = w.Write([]byte(`{"id":"vid_h3_fixture","object":"generation.task","status":"queued"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "vid_h3_fixture", "status": "completed", "result": map[string]any{"type": "video", "data": []map[string]string{{"url": f.server.URL + "/result.mp4"}}}})
	}))
	defer server.Close()
	f.s.Provider = func(_ context.Context, m Model) (Provider, error) {
		return Provider{Adapter: m.Adapter, BaseURL: server.URL, Client: server.Client()}, nil
	}
	m := toapisH3FixtureModel("MiniMax-H3-Max")
	m.ID = "v"
	raw, _ := json.Marshal([]Model{m})
	_ = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw)
	r := CanvasRequest{Kind: "video", ModelID: "v", Prompt: "fixture", AssetIDs: []string{"a"}, Resolution: "768p", AspectRatio: "16:9", Duration: 5, Count: 1, ExpectedQuoteMicro: m.PriceMicro * 5, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture"}
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "completed" || p.ChargedMicro != m.PriceMicro*5 || posts != 1 {
		t.Fatalf("h3 completion p=%+v posts=%d", p, posts)
	}
}

func TestToAPIsH3RestoredInvalidInputsFailBeforeAdmission(t *testing.T) {
	for _, invalid := range []string{"text-auto", "turbo-reference", "audio"} {
		t.Run(invalid, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			m := toapisH3FixtureModel("MiniMax-H3-Max-Turbo")
			m.ID = "v"
			raw, _ := json.Marshal([]Model{m})
			_ = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw)
			r := CanvasRequest{Kind: "video", ModelID: "v", Prompt: "fixture", Resolution: "768p", AspectRatio: "16:9", Duration: 5, Count: 1, ExpectedQuoteMicro: m.PriceMicro * 5, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture"}
			p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				switch invalid {
				case "text-auto":
					v.Canvas.AspectRatio = "auto"
				case "turbo-reference":
					v.Canvas.AssetIDs = []string{"a"}
					v.Canvas.VideoReferences = []CanvasVideoReference{{AssetID: uuid.NewString(), ReferenceClipAssetID: uuid.NewString()}}
					v.Models[0].VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
				case "audio":
					v.Models[0].GenerateAudio = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			f.s.Tick(ctx)
			p = current(t, f, p)
			if p.Status != "failed" || p.Error != "provider_input_unsupported" || p.Shots[0].ReservationID != "" || p.ChargedMicro != 0 || f.submits.Load() != 0 {
				t.Fatalf("invalid restore admitted: %+v", p)
			}
		})
	}
}

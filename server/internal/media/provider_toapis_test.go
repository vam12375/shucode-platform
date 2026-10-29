package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func toapisModel() Model {
	m := fixtureVideoModel()
	m.Adapter, m.UpstreamModel = "toapis", "seedance-2-mini"
	m.Durations, m.MinDuration, m.MaxDuration = nil, 4, 15
	return m
}

func TestToAPIsModelCapabilitiesAndLimits(t *testing.T) {
	for _, upstream := range []string{"seedance-2", "seedance-2-fast", "seedance-2-mini", "seedance-2.5-ext", "seedance-2-5"} {
		m := toapisModel()
		m.UpstreamModel = upstream
		m.Resolutions = []string{"480p", "720p"}
		if upstream == "seedance-2" {
			m.Resolutions = append(m.Resolutions, "1080p", "4k")
		}
		if upstream == "seedance-2.5-ext" {
			m.Resolutions = []string{"720p"}
			m.BillingUnit = BillingUnitRequest
		}
		if upstream == "seedance-2-5" {
			m.Resolutions = []string{"480p", "720p", "1080p"}
			m.BillingUnit, m.PriceMicro, m.TokenPriceMicroPerMillion = BillingUnitToken, 0, 70_000_000
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 0, TokenPriceMicroPerMillion: 42_000_000}
		}
		if upstream != "seedance-2-5" {
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
		}
		if err := ValidateModels([]Model{m}); err != nil {
			t.Fatalf("%s rejected: %v", upstream, err)
		}
		if got := ModelAspectRatios(m); !slices.Equal(got, append([]string{"auto"}, fixedVideoRatios...)) {
			t.Fatalf("%s ratios: %v", upstream, got)
		}
	}
	for name, change := range map[string]func(*Model){
		"unknown-model": func(m *Model) { m.UpstreamModel = "seedance-2-global" },
		"image":         func(m *Model) { m.Kind = "image"; m.Resolutions = nil },
		"fast-4k":       func(m *Model) { m.UpstreamModel = "seedance-2-fast"; m.Resolutions = []string{"4k"} },
		"mini-1080p":    func(m *Model) { m.Resolutions = []string{"1080p"} },
		"ext-480p": func(m *Model) {
			m.UpstreamModel = "seedance-2.5-ext"
			m.BillingUnit = BillingUnitRequest
			m.Resolutions = []string{"480p"}
		},
		"short-range":        func(m *Model) { m.MinDuration = 3 },
		"long-range":         func(m *Model) { m.MaxDuration = 16 },
		"automatic-duration": func(m *Model) { m.MinDuration, m.MaxDuration, m.Durations = 0, 0, []int{0} },
		"short-enum":         func(m *Model) { m.MinDuration, m.MaxDuration, m.Durations = 0, 0, []int{2, 5} },
		"long-reference": func(m *Model) {
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 16, MaxImages: 8, PriceMicro: 100_000}
		},
		"reference-token-price": func(m *Model) {
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000, TokenPriceMicroPerMillion: 10}
		},
		"reference-token-cost": func(m *Model) {
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000, TokenCostMicroPerMillion: 10}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := toapisModel()
			change(&m)
			if ValidateModels([]Model{m}) == nil {
				t.Fatal("unsupported configuration accepted")
			}
		})
	}
}

func TestToAPIsTokenQueryPreservesTotalUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"tsk_vid_fixture","status":"completed","result":{"type":"video","data":[{"url":"https://cdn.example.test/result.mp4"}]},"usage":{"completion_tokens":108900,"total_tokens":120000}}`))
	}))
	defer upstream.Close()
	result, err := (Provider{BaseURL: upstream.URL, Adapter: "toapis", Client: upstream.Client()}).Query(context.Background(), "tsk_vid_fixture")
	if err != nil || result.Status != "succeeded" || result.Usage.TotalTokens != 120000 || result.Usage.CompletionTokens != 108900 {
		t.Fatalf("query=%+v err=%v", result, err)
	}
}

func TestToAPIsSubmitUsesDocumentedBodyAcrossInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		images []string
		video  string
	}{
		{"text", nil, ""},
		{"first-frame", []string{"https://media.example.test/first.png"}, ""},
		{"first-last", []string{"https://media.example.test/first.png", "https://media.example.test/last.png"}, ""},
		{"video-reference", []string{"https://media.example.test/a.png", "https://media.example.test/b.png"}, "https://media.example.test/clip.mp4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/videos/generations" || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_, _ = w.Write([]byte(`{"id":"tsk_vid_fixture","object":"generation.task","model":"seedance-2-mini","status":"queued"}`))
			}))
			defer upstream.Close()
			p := Provider{BaseURL: upstream.URL + "/v1/", Key: "fixture-key", Adapter: "toapis", Client: upstream.Client()}
			m := toapisModel()
			m.GenerateAudio = true
			m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
			id, err := p.CanvasVideo(context.Background(), m, CanvasRequest{Prompt: "fixture prompt", Resolution: "720p", AspectRatio: "auto", Duration: 5}, tc.images, tc.video)
			if err != nil || id != "tsk_vid_fixture" {
				t.Fatalf("submit id=%s err=%v", id, err)
			}
			if body["model"] != m.UpstreamModel || body["prompt"] != "fixture prompt" || body["aspect_ratio"] != "adaptive" || body["duration"] != float64(5) || body["resolution"] != "720p" || body["generate_audio"] != true {
				t.Fatalf("body = %#v", body)
			}
			for _, key := range []string{"content", "ratio", "watermark", "image_urls"} {
				if _, ok := body[key]; ok {
					t.Fatalf("unexpected %s", key)
				}
			}
			if len(tc.images) > 0 {
				images := body["image_with_roles"].([]any)
				if len(images) != len(tc.images) {
					t.Fatalf("images = %#v", images)
				}
				for i, image := range images {
					role := "first_frame"
					if tc.video != "" {
						role = "reference_image"
					} else if i == 1 {
						role = "last_frame"
					}
					got := image.(map[string]any)
					if got["url"] != tc.images[i] || got["role"] != role {
						t.Fatalf("image role = %#v", got)
					}
				}
			} else if _, ok := body["image_with_roles"]; ok {
				t.Fatal("empty images must be omitted")
			}
			if tc.video != "" {
				videos := body["video_with_roles"].([]any)
				if len(videos) != 1 || videos[0].(map[string]any)["url"] != tc.video || videos[0].(map[string]any)["role"] != "reference_video" {
					t.Fatalf("videos = %#v", videos)
				}
			}
		})
	}
}

func TestToAPIsMultimodalImageWithoutVideoUsesReferenceRole(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/videos/generations" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("{\"id\":\"tsk_vid_fixture\"}"))
	}))
	defer upstream.Close()
	p := Provider{BaseURL: upstream.URL + "/v1/", Key: "fixture-key", Adapter: "toapis", Client: upstream.Client()}
	m := toapisModel()
	b := Brief{ReferenceMode: "multimodal", Resolution: "720p", AspectRatio: "16:9", AssetIDs: []string{"asset-a"}}
	if _, err := p.SubmitWithReferenceImages(context.Background(), m, b, ShotPlan{Prompt: "保持主体", Duration: 5}, []string{"https://media.example.test/a.png"}, ""); err != nil {
		t.Fatal(err)
	}
	images, ok := body["image_with_roles"].([]any)
	if !ok || len(images) != 1 || images[0].(map[string]any)["role"] != "reference_image" {
		t.Fatalf("multimodal image was submitted as a frame: %#v", body)
	}
}

func TestToAPIsStoryboardUsesSameAdapterAndExplicitAudioFalse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/videos/generations" || body["generate_audio"] != false {
			t.Errorf("request path/body = %s %#v", r.URL.Path, body)
		}
		_, _ = w.Write([]byte(`{"id":"tsk_vid_storyboard","object":"generation.task","status":"in_progress"}`))
	}))
	defer upstream.Close()
	p := Provider{BaseURL: upstream.URL, Adapter: "toapis", Client: upstream.Client()}
	id, err := p.Submit(context.Background(), toapisModel(), Brief{Resolution: "720p", AspectRatio: "9:16"}, ShotPlan{Prompt: "product", Duration: 5}, "https://media.example.test/image.png")
	if err != nil || id != "tsk_vid_storyboard" {
		t.Fatalf("id=%s err=%v", id, err)
	}
}

func TestToAPIsQueryNormalizesTaskStatesAndResult(t *testing.T) {
	for _, tc := range []struct{ status, want string }{{"queued", "queued"}, {"in_progress", "running"}, {"completed", "succeeded"}, {"failed", "failed"}, {"future_status", "unknown"}, {"succeeded", "unknown"}} {
		t.Run(tc.status, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/videos/generations/tsk_vid_fixture" {
					t.Errorf("query = %s %s", r.Method, r.URL.Path)
				}
				_, _ = fmt.Fprintf(w, `{"id":"tsk_vid_fixture","object":"generation.task","status":%q,"result":{"type":"video","data":[{"url":"https://cdn.example.test/result.mp4","format":"mp4"}]},"error":{"code":"generation_failed","message":"private"},"usage":{"credits":"99","billed_tokens":1000}}`, tc.status)
			}))
			defer upstream.Close()
			p := Provider{BaseURL: upstream.URL + "/v1", Adapter: "toapis", Client: upstream.Client()}
			result, err := p.Query(context.Background(), "tsk_vid_fixture")
			if err != nil || result.Status != tc.want || result.Content.VideoURL != "https://cdn.example.test/result.mp4" || result.Error.Code != "generation_failed" || result.Usage.TotalTokens != 0 {
				t.Fatalf("query=%+v err=%v", result, err)
			}
		})
	}
	for _, detail := range []string{`null`, `{"code":404,"message":null}`, `{"code":null}`, `"private"`} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `{"id":"tsk_vid_fixture","object":"generation.task","status":"failed","error":%s}`, detail)
		}))
		p := Provider{BaseURL: upstream.URL, Adapter: "toapis", Client: upstream.Client()}
		result, err := p.Query(context.Background(), "tsk_vid_fixture")
		upstream.Close()
		if err != nil || result.Status != "failed" {
			t.Fatalf("malformed diagnostic swallowed terminal result: %+v %v", result, err)
		}
	}
}

func TestToAPIsSubmissionErrorsStayBoundedAndUnknownResponsesDoNotInventTask(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{400, `{"error":{"code":"invalid_request","message":"private"}}`, "provider_http_400"},
		{402, `{"error":{"code":"insufficient_quota","message":"private"}}`, "provider_http_402"},
		{429, `{"error":{"code":429,"message":"private"}}`, "provider_http_429"},
		{502, `private`, "provider_http_502"},
		{200, `{"object":"generation.task","status":"queued"}`, "provider_task_missing"},
	} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		p := Provider{BaseURL: upstream.URL, Adapter: "toapis", Client: upstream.Client()}
		id, err := p.Submit(context.Background(), toapisModel(), Brief{Resolution: "720p", AspectRatio: "9:16"}, ShotPlan{Prompt: "product", Duration: 5}, "https://media.example.test/image.png")
		upstream.Close()
		if err == nil || err.Error() != tc.want || id != "" || strings.Contains(err.Error(), "private") {
			t.Fatalf("id=%q err=%v", id, err)
		}
	}
	p := Provider{Adapter: "toapis"}
	for _, id := range []string{"", "../private", "tsk%2fprivate", "tsk?secret", "tsk#x", strings.Repeat("x", 257)} {
		if _, err := p.Query(context.Background(), id); err == nil {
			t.Fatalf("unsafe task ID accepted: %q", id)
		}
	}
}

func TestToAPIsRejectsUnusableCompletedResultsAndMismatchedTask(t *testing.T) {
	for _, body := range []string{
		`{"id":"tsk_vid_fixture","status":"completed"}`,
		`{"id":"tsk_vid_fixture","status":"completed","result":{"type":"video","data":[]}}`,
		`{"id":"tsk_vid_fixture","status":"completed","result":{"type":"image","data":[{"url":"https://cdn.example.test/output.png"}]}}`,
		`{"id":"tsk_vid_fixture","status":"completed","result":{"type":"video","data":[{"url":"asset://private"}]}}`,
		`{"id":"tsk_vid_other","status":"completed","result":{"type":"video","data":[{"url":"https://cdn.example.test/output.mp4"}]}}`,
	} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		p := Provider{BaseURL: upstream.URL, Adapter: "toapis", Client: upstream.Client()}
		result, err := p.Query(context.Background(), "tsk_vid_fixture")
		upstream.Close()
		if err == nil || result.Status == "succeeded" {
			t.Fatalf("invalid completion accepted: %+v %v", result, err)
		}
	}
}

func TestToAPIsRejectsUnsupportedRuntimeInputBeforeHTTP(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer upstream.Close()
	p := Provider{BaseURL: upstream.URL, Adapter: "toapis", Client: upstream.Client()}
	m := toapisModel()
	r := CanvasRequest{Prompt: "fixture", Resolution: "720p", AspectRatio: "16:9", Duration: 5}
	for _, tc := range []struct {
		request CanvasRequest
		images  []string
		video   string
	}{
		{CanvasRequest{Prompt: "fixture", Resolution: "1080p", AspectRatio: "16:9", Duration: 5}, nil, ""},
		{CanvasRequest{Prompt: "fixture", Resolution: "720p", AspectRatio: "16:9", Duration: 0}, nil, ""},
		{r, []string{"https://media.example.test/a", "https://media.example.test/b", "https://media.example.test/c"}, ""},
		{r, []string{"asset://private"}, ""},
		{r, []string{"https://media.example.test/a"}, "https://media.example.test/video"},
	} {
		if _, err := p.CanvasVideo(context.Background(), m, tc.request, tc.images, tc.video); err == nil {
			t.Fatal("unsupported input accepted")
		}
	}
	if requests != 0 {
		t.Fatalf("invalid input made %d HTTP requests", requests)
	}
}

func TestToAPIsWorkerReusesTaskAndSettlesOrRejectsWithoutResubmitting(t *testing.T) {
	for _, outcome := range []string{"completed", "failed", "unknown", "quota"} {
		t.Run(outcome, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			f.video, _ = os.ReadFile("testdata/blue.mp4")
			posts, queries := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
					if outcome == "unknown" {
						w.WriteHeader(502)
						return
					}
					if outcome == "quota" {
						w.WriteHeader(402)
						return
					}
					_, _ = w.Write([]byte(`{"id":"tsk_vid_worker","object":"generation.task","status":"queued"}`))
					return
				}
				queries++
				_, _ = fmt.Fprintf(w, `{"id":"tsk_vid_worker","object":"generation.task","status":%q,"result":{"type":"video","data":[{"url":%q}]},"error":{"code":null}}`, outcome, f.server.URL+"/result.mp4")
			}))
			defer upstream.Close()
			f.s.Provider = func(_ context.Context, m Model) (Provider, error) {
				return Provider{BaseURL: upstream.URL, Adapter: m.Adapter, Client: upstream.Client()}, nil
			}
			p := f.project(t)
			p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Models[0].Adapter = "toapis"
				v.Models[0].UpstreamModel = "seedance-2-mini"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for range 8 {
				f.s.Tick(ctx)
			}
			p = current(t, f, p)
			if outcome == "completed" {
				if posts != 2 || queries != 2 || p.ChargedMicro != 1_000_000 || p.Shots[0].OutputAssetID == "" || p.Shots[1].Status != "succeeded" {
					t.Fatalf("success p=%+v posts=%d queries=%d", p, posts, queries)
				}
			} else {
				if posts != 1 || p.ChargedMicro != 0 {
					t.Fatalf("failure posts=%d charged=%d", posts, p.ChargedMicro)
				}
				want := "failed"
				if outcome == "unknown" {
					want = "reconciliation_required"
				}
				if p.Status != want {
					t.Fatalf("status=%s want=%s", p.Status, want)
				}
			}
		})
	}
}

func TestToAPIsRejectsOversizedValidatedReferenceBeforeAdmission(t *testing.T) {
	for _, upstream := range []string{"seedance-2-mini", "MiniMax-H3", "MiniMax-H3-Max"} {
		t.Run(upstream, func(t *testing.T) { testToAPIsOversizedReference(t, upstream) })
	}
}

func testToAPIsOversizedReference(t *testing.T, upstream string) {
	t.Helper()
	m := toapisModel()
	if toapisMiniMaxModel(upstream) {
		m = toapisH3FixtureModel(upstream)
	}
	capability, _ := toapisModelCapabilities(upstream)
	clipBytes := capability.referenceMaxBytes + 1
	f := setup(t)
	ctx := context.Background()
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 5, Width: 160, Height: 90}, nil
	}
	sourceID := uuid.NewString()
	sourcePath := f.s.Blobs.Path(f.team, sourceID)
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
		t.Fatal(err)
	}
	video, err := os.ReadFile("testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(sourcePath, video, 0600); err != nil {
		t.Fatal(err)
	}
	sourceChecksum, err := checksumFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: sourceID, Name: "source.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len(video)), DurationSeconds: 5, Checksum: sourceChecksum, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	clipFile, err := os.CreateTemp(filepath.Dir(sourcePath), ".oversized-fixture-*")
	if err != nil {
		t.Fatal(err)
	}
	if err = clipFile.Truncate(clipBytes); err != nil {
		t.Fatal(err)
	}
	if err = clipFile.Close(); err != nil {
		t.Fatal(err)
	}
	clipChecksum, err := checksumFile(clipFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	clipID := clipIdentity(f.team, sourceID, sourceChecksum, 0, 5, clipChecksum)
	if err = os.Rename(clipFile.Name(), f.s.Blobs.Path(f.team, clipID)); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddInternalAssetIfSame(ctx, f.team, Asset{ID: clipID, Name: "clip.mp4", Kind: "video", Mime: "video/mp4", Size: clipBytes, DurationSeconds: 5, Checksum: clipChecksum, Width: 160, Height: 90, Internal: true}); err != nil {
		t.Fatal(err)
	}
	m.ID = "v"
	m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 100_000}
	raw, _ := json.Marshal([]Model{m})
	if err = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), CanvasRequest{Kind: "video", ModelID: "v", Prompt: "fixture", AssetIDs: []string{"a"}, VideoReferences: []CanvasVideoReference{{AssetID: sourceID, ReferenceClipAssetID: clipID}}, Resolution: m.Resolutions[0], AspectRatio: "16:9", Duration: 5, Count: 1, ExpectedQuoteMicro: 500_000, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Status != "failed" || p.Error != "video_reference_clip_size_limit" || p.Shots[0].ReservationID != "" || p.ChargedMicro != 0 || f.submits.Load() != 0 {
		t.Fatalf("oversized clip admitted: %+v", p)
	}
}

package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func toapisImageModel(upstream, resolution string) Model {
	return Model{ID: "i", Name: upstream + " " + resolution, Kind: "image", Adapter: "toapis", ProviderID: "fixture", UpstreamModel: upstream, Resolutions: []string{resolution}, PriceMicro: 100_000, PriceVersion: "v1"}
}

func TestToAPIsImageCapabilitiesUseExactModelsAndSingleResolutionSKUs(t *testing.T) {
	for _, upstream := range []string{"gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		for _, resolution := range []string{"1K", "2K", "4K"} {
			m := toapisImageModel(upstream, resolution)
			if err := ValidateModels([]Model{m}); err != nil || !slices.Equal(ImageResolutions(m), []string{resolution}) {
				t.Fatalf("%s/%s err=%v resolutions=%v", upstream, resolution, err, ImageResolutions(m))
			}
			want := []string{"1:1", "3:2", "2:3", "4:3", "3:4", "5:4", "4:5", "16:9", "9:16", "21:9"}
			if upstream == "gpt-image-2" {
				want = append(want, "2:1", "1:2", "9:21")
			}
			got := ModelAspectRatios(m)
			if len(got) != len(want) {
				t.Fatalf("%s ratios=%v", upstream, got)
			}
			for _, ratio := range want {
				if !slices.Contains(got, ratio) {
					t.Fatalf("%s missing %s", upstream, ratio)
				}
			}
		}
	}
	for name, change := range map[string]func(*Model){
		"series":                 func(m *Model) { m.UpstreamModel = "gpt-image-2.5" },
		"vip":                    func(m *Model) { m.UpstreamModel = "gpt-image-2-vip" },
		"empty-resolution":       func(m *Model) { m.Resolutions = nil },
		"multi-resolution":       func(m *Model) { m.Resolutions = []string{"1K", "2K"} },
		"unsupported-resolution": func(m *Model) { m.Resolutions = []string{"3K"} },
		"lowercase-catalog":      func(m *Model) { m.Resolutions = []string{"2k"} },
		"audio":                  func(m *Model) { m.GenerateAudio = true },
		"video": func(m *Model) {
			m.VideoReference = &VideoReferenceCapability{MaxImages: 1, MaxDurationSeconds: 5, PriceMicro: 1}
		},
		"duration": func(m *Model) { m.MinDuration, m.MaxDuration = 4, 15 },
	} {
		t.Run(name, func(t *testing.T) {
			m := toapisImageModel("gpt-image-2", "2K")
			change(&m)
			if ValidateModels([]Model{m}) == nil {
				t.Fatal("unsupported image SKU accepted")
			}
		})
	}
}

func TestToAPIsImageSubmissionMatchesDocumentedBody(t *testing.T) {
	for _, upstream := range []string{"gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		for _, resolution := range []string{"1K", "2K", "4K"} {
			for _, prefix := range []string{"", "/v1", "/proxy/v1"} {
				t.Run(upstream+resolution+prefix, func(t *testing.T) {
					for _, refs := range [][]string{nil, {"https://media.example.test/a.png", "https://media.example.test/b.png"}} {
						var body map[string]any
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							wantPath := "/v1/images/generations"
							if prefix == "/proxy/v1" {
								wantPath = "/proxy/v1/images/generations"
							}
							if r.Method != "POST" || r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer fixture" {
								t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
							}
							_ = json.NewDecoder(r.Body).Decode(&body)
							_, _ = w.Write([]byte(`{"id":"tsk_img_fixture","object":"generation.task","status":"pending"}`))
						}))
						p := Provider{Adapter: "toapis", BaseURL: server.URL + prefix, Key: "fixture", Client: server.Client()}
						result, err := p.SubmitCanvasImage(context.Background(), toapisImageModel(upstream, resolution), CanvasRequest{Kind: "image", Prompt: "fixture prompt", Resolution: resolution, AspectRatio: "5:4"}, refs)
						server.Close()
						if err != nil || result.TaskID != "tsk_img_fixture" || result.URL != "" {
							t.Fatalf("submission=%+v err=%v", result, err)
						}
						wireResolution := resolution
						if upstream == "gpt-image-2" {
							wireResolution = strings.ToLower(resolution)
						}
						if body["model"] != upstream || body["prompt"] != "fixture prompt" || body["size"] != "5:4" || body["resolution"] != wireResolution || body["n"] != float64(1) {
							t.Fatalf("body=%#v", body)
						}
						if upstream == "gpt-image-2" {
							if body["response_format"] != "url" || body["quality"] != nil {
								t.Fatalf("GPT2 options=%#v", body)
							}
						} else if body["quality"] != "high" || body["response_format"] != nil {
							t.Fatalf("GPT2.5 options=%#v", body)
						}
						for _, key := range []string{"image", "watermark", "seed", "background", "aspect_ratio", "generate_audio"} {
							if _, ok := body[key]; ok {
								t.Fatalf("undocumented %s", key)
							}
						}
						if len(refs) == 0 {
							if _, ok := body["reference_images"]; ok {
								t.Fatal("empty refs sent")
							}
						} else {
							images := body["reference_images"].([]any)
							if len(images) != 2 || images[0] != refs[0] || images[1] != refs[1] {
								t.Fatalf("refs=%v", images)
							}
						}
					}
				})
			}
		}
	}
}

func TestToAPIsImageRuntimeValidationRejectsBeforeHTTP(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	p := Provider{Adapter: "toapis", BaseURL: server.URL, Client: server.Client()}
	for _, tc := range []struct {
		model             Model
		ratio, resolution string
		refs              []string
	}{
		{toapisImageModel("gpt-image-2", "2K"), "auto", "2K", nil},
		{toapisImageModel("gpt-image-2.5-flare", "2K"), "2:1", "2K", nil},
		{toapisImageModel("gpt-image-2", "2K"), "1:1", "4K", nil},
		{toapisImageModel("gpt-image-2", "3K"), "1:1", "3K", nil},
		{toapisImageModel("gpt-image-2", "2K"), "1:1", "2K", []string{"asset://private"}},
		{toapisImageModel("gpt-image-2", "2K"), "1:1", "2K", make([]string, 9)},
	} {
		if _, err := p.SubmitCanvasImage(context.Background(), tc.model, CanvasRequest{Resolution: tc.resolution, AspectRatio: tc.ratio}, tc.refs); err == nil {
			t.Fatal("unsupported input accepted")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
}

func TestToAPIsImageQueryNormalizesAndRejectsMalformedCompletion(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{"pending", "queued"}, {"queued", "queued"}, {"in_progress", "running"}, {"completed", "succeeded"}, {"failed", "failed"}, {"succeeded", "unknown"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/v1/images/generations/tsk_img_fixture" {
				t.Errorf("query %s %s", r.Method, r.URL.Path)
			}
			_, _ = fmt.Fprintf(w, `{"id":"tsk_img_fixture","status":%q,"result":{"type":"image","data":[{"url":"https://cdn.example.test/image.png"}]},"error":{"code":12,"message":"private"},"usage":{"credits":999}}`, tc.raw)
		}))
		p := Provider{Adapter: "toapis", BaseURL: server.URL, Client: server.Client()}
		result, err := p.QueryImage(context.Background(), "tsk_img_fixture")
		server.Close()
		if err != nil || result.Status != tc.want || (tc.want == "succeeded" && result.URL != "https://cdn.example.test/image.png") || result.Error.Code != "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	for _, body := range []string{
		`{"id":"other","status":"pending"}`,
		`{"id":"tsk_img_fixture","status":"completed"}`,
		`{"id":"tsk_img_fixture","status":"completed","result":{"type":"video","data":[{"url":"https://cdn.example.test/image.png"}]}}`,
		`{"id":"tsk_img_fixture","status":"completed","result":{"type":"image","data":[{"url":"asset://private"}]}}`,
		`{"id":"tsk_img_fixture","status":"completed","result":{"type":"image","data":[{"url":"https://cdn.example.test/a"},{"url":"https://cdn.example.test/b"}]}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		p := Provider{Adapter: "toapis", BaseURL: server.URL, Client: server.Client()}
		if _, err := p.QueryImage(context.Background(), "tsk_img_fixture"); err == nil {
			t.Fatal("invalid completion accepted")
		}
		server.Close()
	}
	p := Provider{Adapter: "toapis"}
	for _, id := range []string{"", "../x", "tsk%2fx", "tsk?x", strings.Repeat("x", 257)} {
		if _, err := p.QueryImage(context.Background(), id); err == nil {
			t.Fatal("unsafe ID accepted")
		}
	}
}

type toapisImageFixture struct {
	*fixture
	posts, queries atomic.Int64
	queryCode      atomic.Int64
	postCode       int
	status         atomic.Value
	upstream       *httptest.Server
}

func newToAPIsImageFixture(t *testing.T, postCode int) (*toapisImageFixture, *Project) {
	t.Helper()
	f := &toapisImageFixture{fixture: setup(t), postCode: postCode}
	f.status.Store("completed")
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			f.posts.Add(1)
			if f.postCode != 0 {
				w.WriteHeader(f.postCode)
				return
			}
			_, _ = w.Write([]byte(`{"id":"tsk_img_worker","status":"pending"}`))
			return
		}
		f.queries.Add(1)
		if code := int(f.queryCode.Load()); code != 0 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(code)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"tsk_img_worker","status":%q,"result":{"type":"image","data":[{"url":%q}]},"error":{"code":"generation_failed","message":"private"}}`, f.status.Load().(string), f.server.URL+"/result.png")
	}))
	t.Cleanup(f.upstream.Close)
	f.s.Provider = func(_ context.Context, m Model) (Provider, error) {
		return Provider{Adapter: m.Adapter, BaseURL: f.upstream.URL, Client: f.upstream.Client()}, nil
	}
	r := canvasRequest(t, f.fixture)
	raw, _ := json.Marshal([]Model{toapisImageModel("gpt-image-2.5-flare", "2K")})
	if err := f.s.Repo.Store.SetConfigValue(context.Background(), ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreateCanvas(context.Background(), f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

func forceImageQueryDue(t *testing.T, f *toapisImageFixture, p *Project) *Project {
	t.Helper()
	p = current(t, f.fixture, p)
	next, err := f.s.Mutate(context.Background(), f.team, p.ID, p.Version, func(v *Project) error {
		for i := range v.Shots {
			v.Shots[i].ImageNextQueryAt = time.Time{}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestToAPIsImageWorkerPersistsPollingAndResumesDownloadAndSettlement(t *testing.T) {
	f, p := newToAPIsImageFixture(t, 0)
	ctx := context.Background()
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	identity := p.Shots[0].ReservationID
	delay := time.Until(p.Shots[0].ImageNextQueryAt)
	if p.Shots[0].ImageTaskID != "tsk_img_worker" || p.Shots[0].VideoTaskID != "" || p.Shots[0].Status != "image_running" || delay < 4*time.Second || delay > 10*time.Second {
		t.Fatalf("async state=%+v delay=%s", p.Shots[0], delay)
	}
	view, _ := json.Marshal(p.View())
	if strings.Contains(string(view), "imageTaskId") || strings.Contains(string(view), "imageNextQueryAt") {
		t.Fatal("internal scheduling leaked")
	}
	restarted := *f.s
	restarted.WorkerID = uuid.NewString()
	f.s = &restarted
	f.s.Tick(ctx)
	if f.queries.Load() != 0 {
		t.Fatal("restart ignored next query time")
	}
	f.status.Store("pending")
	p = forceImageQueryDue(t, f, p)
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	if p.Shots[0].Status != "image_running" || !p.Shots[0].ImageNextQueryAt.After(time.Now()) || p.Shots[0].ReservationID != identity {
		t.Fatalf("pending=%+v", p.Shots[0])
	}
	f.status.Store("completed")
	p = forceImageQueryDue(t, f, p)
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	if p.Shots[0].Status != "image_generated" || p.Shots[0].ResultURL == "" {
		t.Fatal("completion URL not persisted")
	}
	queries := f.queries.Load()
	f.s.Tick(ctx) // fixture deliberately fails the first download.
	p = current(t, f.fixture, p)
	if p.Shots[0].Status != "image_generated" || p.Shots[0].Error != "result_download_failed" {
		t.Fatalf("download recovery=%+v", p.Shots[0])
	}
	for range 12 {
		p = forceImageQueryDue(t, f, p)
		f.s.Tick(ctx)
	}
	p = current(t, f.fixture, p)
	team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
	if p.Status != "completed" || p.ChargedMicro != 200_000 || f.posts.Load() != 2 || f.queries.Load() != queries+1 || team.UsedCredits != "0.200000" {
		t.Fatalf("p=%+v posts=%d queries=%d ledger=%+v", p, f.posts.Load(), f.queries.Load(), team)
	}
}

func TestToAPIsImageWorkerFailureUnknownAndQueryRecovery(t *testing.T) {
	for _, outcome := range []string{"failed", "unknown", "401", "403", "404", "429", "503"} {
		t.Run(outcome, func(t *testing.T) {
			postCode := 0
			if outcome == "unknown" {
				postCode = 502
			}
			f, p := newToAPIsImageFixture(t, postCode)
			ctx := context.Background()
			f.s.Tick(ctx)
			p = current(t, f.fixture, p)
			identity := p.Shots[0].ReservationID
			switch outcome {
			case "failed":
				f.status.Store("failed")
			case "401":
				f.queryCode.Store(401)
			case "403":
				f.queryCode.Store(403)
			case "404":
				f.queryCode.Store(404)
			case "429":
				f.queryCode.Store(429)
			case "503":
				f.queryCode.Store(503)
			}
			p = forceImageQueryDue(t, f, p)
			f.s.Tick(ctx)
			p = current(t, f.fixture, p)
			if p.ChargedMicro != 0 || f.posts.Load() != 1 {
				t.Fatal("failure charged or resubmitted")
			}
			if outcome == "failed" {
				f.s.Tick(ctx)
				p = current(t, f.fixture, p)
				if p.Status != "failed" || p.Shots[0].ReservationID != "" || p.Shots[0].ProviderError.Code != "generation_failed" {
					t.Fatalf("failed=%+v", p)
				}
				p, err := f.s.Action(ctx, f.team, p.ID, p.Version, "retry")
				if err != nil || p.Shots[0].ImageTaskID != "" || !p.Shots[0].ImageNextQueryAt.IsZero() {
					t.Fatalf("retry=%+v %v", p, err)
				}
			} else if outcome == "429" || outcome == "503" {
				if p.Shots[0].Status != "image_running" || p.Shots[0].ReservationID != identity || !p.Shots[0].ImageNextQueryAt.After(time.Now()) {
					t.Fatalf("retry state=%+v", p)
				}
				if outcome == "429" && time.Until(p.Shots[0].ImageNextQueryAt) < 29*time.Second {
					t.Fatal("Retry-After ignored")
				}
				f.s.Tick(ctx)
				if f.queries.Load() != 1 {
					t.Fatal("query backoff ignored")
				}
				f.queryCode.Store(0)
				p = forceImageQueryDue(t, f, p)
				f.s.Tick(ctx)
				if current(t, f.fixture, p).Shots[0].Status != "image_generated" {
					t.Fatal("query failed to recover")
				}
			} else if p.Status != "reconciliation_required" || p.Shots[0].ReservationID != identity {
				t.Fatalf("unknown=%+v", p)
			}
			for range 3 {
				f.s.Tick(ctx)
			}
			if outcome != "failed" && f.posts.Load() != 1 {
				t.Fatal("unknown task resubmitted")
			}
		})
	}
}

func TestToAPIsImageCancellationWaitsForAcceptedTaskAndChargesOneImage(t *testing.T) {
	f, p := newToAPIsImageFixture(t, 0)
	ctx := context.Background()
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	p, err := f.s.Action(ctx, f.team, p.ID, p.Version, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		p = forceImageQueryDue(t, f, p)
		f.s.Tick(ctx)
	}
	p = current(t, f.fixture, p)
	if p.Status != "cancelled" || p.ChargedMicro != 100_000 || f.posts.Load() != 1 || f.queries.Load() != 1 || p.Shots[0].OutputAssetID == "" || p.Shots[1].Status != "pending" {
		t.Fatalf("cancel p=%+v posts=%d queries=%d", p, f.posts.Load(), f.queries.Load())
	}
}

func TestToAPIsImageTaskReconciliationUsesFrozenImageCapability(t *testing.T) {
	f, p := newToAPIsImageFixture(t, 502)
	ctx := context.Background()
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	identity := p.Shots[0].ReservationID
	for _, value := range []string{"", "../x", "tsk%2fx", strings.Repeat("x", 257)} {
		if _, err := f.s.Reconcile(ctx, f.team, p.ID, Reconciliation{Version: p.Version, Shot: 0, Outcome: "image_task", Value: value, Reason: "已核实上游任务编号"}); err == nil {
			t.Fatal("invalid task ID accepted")
		}
	}
	for _, change := range []func(*Project){
		func(v *Project) { v.Models[0].Adapter = "ark" },
		func(v *Project) { v.Models[0].UpstreamModel = "gpt-image-2.5" },
		func(v *Project) { v.Models[0].Kind = "video" },
		func(v *Project) { v.Shots[0].Stage = "video" },
	} {
		original := *p
		original.Models = slices.Clone(p.Models)
		original.Shots = slices.Clone(p.Shots)
		p, _ = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { change(v); return nil })
		if _, err := f.s.Reconcile(ctx, f.team, p.ID, Reconciliation{Version: p.Version, Shot: 0, Outcome: "image_task", Value: "tsk_img_worker", Reason: "已核实上游任务编号"}); err == nil {
			t.Fatal("invalid frozen capability accepted")
		}
		p, _ = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Models = original.Models; v.Shots = original.Shots; return nil })
	}
	var err error
	p, err = f.s.Reconcile(ctx, f.team, p.ID, Reconciliation{Version: p.Version, Shot: 0, Outcome: "image_task", Value: "tsk_img_worker", Reason: "已核实上游任务编号"})
	if err != nil || p.Shots[0].Status != "image_running" || p.Shots[0].ImageTaskID != "tsk_img_worker" || p.Shots[0].ReservationID != identity || p.Shots[0].PendingChargeMicro != 100_000 {
		t.Fatalf("reconcile=%+v err=%v", p, err)
	}
	for range 10 {
		p = forceImageQueryDue(t, f, p)
		f.s.Tick(ctx)
	}
	p = current(t, f.fixture, p)
	if p.Shots[0].OutputAssetID == "" || p.ChargedMicro != 100_000 || f.posts.Load() != 2 {
		t.Fatalf("reconciled result=%+v posts=%d", p, f.posts.Load())
	}
}

func TestToAPIsImageFirstFrameUsesFrozenSKUAndExecutionEvidence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var imagePosts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			imagePosts.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["resolution"] != "4k" || body["size"] != "16:9" || body["prompt"] != "first frame" {
				t.Errorf("frozen first frame=%#v", body)
			}
			_, _ = w.Write([]byte(`{"id":"tsk_img_first","status":"pending"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"tsk_img_first","status":"completed","result":{"type":"image","data":[{"url":%q}]}}`, f.server.URL+"/result.png")
	}))
	defer upstream.Close()
	f.s.Provider = func(_ context.Context, m Model) (Provider, error) {
		if m.Adapter == "toapis" {
			return Provider{Adapter: m.Adapter, BaseURL: upstream.URL, Client: upstream.Client()}, nil
		}
		return Provider{BaseURL: f.server.URL, Client: f.server.Client()}, nil
	}
	p := f.project(t)
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Models = append(v.Models, toapisImageModel("gpt-image-2", "4K"))
		v.Brief.ImageModel = "i"
		v.Shots[0].Plan.ImagePrompt = "first frame"
		v.Shots[0].Plan.AssetID = ""
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Shots[0].Stage != "image" || p.Shots[0].ImageTaskID == "" {
		t.Fatalf("first frame=%+v", p.Shots[0])
	}
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Shots[0].ImageNextQueryAt = time.Time{}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if imagePosts.Load() != 1 || f.submits.Load() != 1 || p.Shots[0].Status != "video_running" || p.Shots[0].ChargedMicro != 100_000 {
		t.Fatalf("pipeline=%+v posts=%d", p, imagePosts.Load())
	}
	if !referenceExecutionHasEvidence(Shot{ImageTaskID: "tsk_img_evidence"}) {
		t.Fatal("image execution evidence ignored")
	}
	_, err = referenceReviewPlan(&Project{Status: "review", Brief: Brief{ReferenceAssetID: "source", GenerationMode: "video_reference"}, SourceAnalysis: &SourceAnalysis{}, Shots: []Shot{{Status: "pending", ImageTaskID: "tsk_img_evidence"}, {Status: "pending"}}}, Plan{})
	if err != ErrConflict {
		t.Fatal("executed image plan reopened for review")
	}
}

func TestToAPIsImageCanvasAdmissionAndLegacyBriefCompatibility(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	r.Count, r.ExpectedQuoteMicro = 1, 100_000
	for _, upstream := range []string{"gpt-image-2", "gpt-image-2.5-flare"} {
		m := toapisImageModel(upstream, "2K")
		raw, _ := json.Marshal([]Model{m})
		if err := f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
			t.Fatal(err)
		}
		for _, ratio := range toapisImageRatios(upstream) {
			r.AspectRatio = ratio
			if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err != nil {
				t.Fatalf("%s %s: %v", upstream, ratio, err)
			}
		}
		r.AspectRatio = "auto"
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
			t.Fatal("auto image accepted")
		}
		if upstream == "gpt-image-2.5-flare" {
			r.AspectRatio = "2:1"
			if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
				t.Fatal("unconfirmed GPT2.5 ratio accepted")
			}
		}
	}
	models := []Model{fixtureVideoModel(), toapisImageModel("gpt-image-2", "4K")}
	b := Brief{Mode: "auto", Product: "fixture", Platforms: []string{"douyin"}, AssetIDs: []string{"a"}, VideoModel: models[0].ID, ImageModel: "i", Duration: 5, Resolution: models[0].Resolutions[0], AspectRatio: "16:9", MaxCreditsMicro: 1_000_000}
	if err := ValidateBrief(b, models, []Asset{{ID: "a", Kind: "image"}}); err != nil {
		t.Fatal(err)
	}
	models[1].Resolutions = []string{"2K", "4K"}
	if ValidateBrief(b, models, []Asset{{ID: "a", Kind: "image"}}) == nil {
		t.Fatal("ambiguous image SKU in brief accepted")
	}
	models[1].Adapter, models[1].UpstreamModel, models[1].Resolutions = "ark", "legacy-image", nil
	if err := ValidateBrief(b, models, []Asset{{ID: "a", Kind: "image"}}); err != nil || firstFrameResolution(models[1]) != "2K" {
		t.Fatalf("legacy Ark changed: %v", err)
	}
}

func TestToAPIsImageCorruptFrozenSKURejectedBeforeAdmission(t *testing.T) {
	f, p := newToAPIsImageFixture(t, 0)
	ctx := context.Background()
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Models[0].Resolutions = []string{"4K"}; return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	if p.Status != "failed" || p.Error != "provider_input_unsupported" || p.Shots[0].ReservationID != "" || f.posts.Load() != 0 {
		t.Fatalf("bad SKU admitted: %+v", p)
	}
}

func TestToAPIsImageUnknownCreationNeverInventsAcceptedTask(t *testing.T) {
	for _, raw := range []string{`{"status":"pending"}`, `{"id":"../private"}`, `{"data":[{"url":"https://cdn.example.test/image.png"}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) }))
		p := Provider{Adapter: "toapis", BaseURL: server.URL, Client: server.Client()}
		result, err := p.SubmitCanvasImage(context.Background(), toapisImageModel("gpt-image-2", "2K"), CanvasRequest{Prompt: "fixture", Resolution: "2K", AspectRatio: "1:1"}, nil)
		server.Close()
		if err == nil || result.TaskID != "" || result.URL != "" {
			t.Fatalf("unknown creation=%+v err=%v", result, err)
		}
	}
}

func TestToAPIsImageRetryAfterAcceptsDeltaAndHTTPDate(t *testing.T) {
	now := time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		want time.Time
	}{
		{"30", now.Add(30 * time.Second)},
		{now.Add(time.Minute).Format(http.TimeFormat), now.Add(time.Minute)},
		{"0", now},
		{"-1", time.Time{}},
		{"invalid", time.Time{}},
		{now.Add(-time.Minute).Format(http.TimeFormat), time.Time{}},
	} {
		if got := providerRetryAfter(tc.raw, now); !got.Equal(tc.want) {
			t.Fatalf("Retry-After %q=%s want=%s", tc.raw, got, tc.want)
		}
	}
}

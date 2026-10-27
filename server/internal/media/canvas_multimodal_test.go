package media

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCanvasMultimodalProviderRolesAndOptions(t *testing.T) {
	for _, adapter := range []string{"ark", "toapis"} {
		t.Run(adapter, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "task_fixture"})
			}))
			defer server.Close()
			upstream := "doubao-seedance-2-0-mini-260615"
			if adapter == "toapis" {
				upstream = "seedance-2-mini"
			}
			m := Model{Kind: "video", Adapter: adapter, UpstreamModel: upstream, GenerateAudio: true, VideoReference: &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 1}}
			silent := false
			p := Provider{BaseURL: server.URL, Client: server.Client(), Adapter: adapter}
			_, err := p.CanvasMultimodalVideo(context.Background(), m, CanvasRequest{ReferenceMode: "multimodal", Prompt: "图片1与图片2、视频1、音频1", Resolution: "720p", AspectRatio: "9:16", Duration: 15, GenerateAudio: &silent}, []string{"https://example.test/i2.png", "https://example.test/i1.png"}, []string{"https://example.test/v.mp4"}, []string{"https://example.test/a.wav"})
			if err != nil {
				t.Fatal(err)
			}
			if body["generate_audio"] != false {
				t.Fatalf("audio not forwarded: %#v", body)
			}
			if adapter == "ark" {
				content := body["content"].([]any)
				for i, want := range []string{"reference_image", "reference_image", "reference_video", "reference_audio"} {
					item := content[i+1].(map[string]any)
					if item["role"] != want {
						t.Fatalf("role: %#v", item)
					}
				}
				image := content[1].(map[string]any)["image_url"].(map[string]any)["url"]
				if image != "https://example.test/i2.png" {
					t.Fatal(image)
				}
			} else {
				images := body["image_with_roles"].([]any)
				if images[0].(map[string]any)["url"] != "https://example.test/i2.png" || images[0].(map[string]any)["role"] != "reference_image" {
					t.Fatal(images)
				}
				if body["audio_with_roles"] == nil {
					t.Fatal("missing reference audio")
				}
			}
		})
	}
}

func TestCanvasSeedanceCapabilitiesAndUnsupportedOptions(t *testing.T) {
	m := Model{Kind: "video", Adapter: "ark", UpstreamModel: "doubao-seedance-2-0-fast-260128", GenerateAudio: true}
	c := CanvasCapabilitiesFor(m)
	if c == nil || c.MaxImages != 9 || c.MaxVideos != 0 || !c.ReturnLastFrame {
		t.Fatalf("caps: %+v", c)
	}
	m.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 1}
	if CanvasCapabilitiesFor(m).MaxVideos != 3 {
		t.Fatal("configured videos not enabled")
	}
	m.Adapter = "toapis"
	m.UpstreamModel = "seedance-2-fast"
	yes := true
	if validateCanvasOptions(m, CanvasRequest{ReferenceMode: "multimodal", ReturnLastFrame: &yes}) == nil {
		t.Fatal("unsupported tail frame accepted")
	}
	m.UpstreamModel = "MiniMax-H3"
	if CanvasCapabilitiesFor(m) != nil {
		t.Fatal("seedance capabilities leaked to H3")
	}
}

func seedanceCanvasRequest(t *testing.T, f *fixture) CanvasRequest {
	t.Helper()
	models, err := f.s.Repo.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	models[0].UpstreamModel = "doubao-seedance-2-0-260128"
	models[0].GenerateAudio = true
	models[0].VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 15, MaxImages: 8, PriceMicro: 200000}
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(context.Background(), ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	return CanvasRequest{Kind: "video", ModelID: "v", ReferenceMode: "multimodal", Prompt: "图片1 视频1 音频1", AssetIDs: []string{}, Resolution: "720p", AspectRatio: "16:9", Duration: 5, Count: 1, ExpectedQuoteMicro: 500000, BoardID: "b", NodeID: "n", WorkspacePath: "/fixture"}
}

func canvasReferenceAsset(t *testing.T, f *fixture, kind string, duration float64) Asset {
	t.Helper()
	id, size, err := f.s.Blobs.Put(f.team, bytes.NewBufferString("fixture-content"), 100)
	if err != nil {
		t.Fatal(err)
	}
	mime := map[string]string{"image": "image/png", "video": "video/mp4", "audio": "audio/wav"}[kind]
	a := Asset{ID: id, Name: kind, Kind: kind, Mime: mime, Size: size, DurationSeconds: duration}
	if err := f.s.Repo.AddAsset(context.Background(), f.team, a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCanvasMultimodalAdmissionCountIdentityAndTiming(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := seedanceCanvasRequest(t, f)
	images := []string{}
	for range 9 {
		images = append(images, canvasReferenceAsset(t, f, "image", 0).ID)
	}
	videos := []CanvasVideoReference{}
	for range 3 {
		videos = append(videos, CanvasVideoReference{AssetID: canvasReferenceAsset(t, f, "video", 5).ID})
	}
	audio := canvasReferenceAsset(t, f, "audio", 3).ID
	r.AssetIDs = images
	if p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err != nil || len(p.Canvas.AssetIDs) != 9 {
		t.Fatalf("9 images: %+v %v", p, err)
	}
	r.VideoReferences = videos
	r.ExpectedQuoteMicro = 1000000
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil || len(p.Shots[0].CanvasVideoAssetIDs) != 3 {
		t.Fatalf("12 refs: %+v %v", p, err)
	}
	for i, ref := range videos {
		if p.Shots[0].CanvasVideoAssetIDs[i] != ref.AssetID {
			t.Fatal("connection order lost")
		}
	}
	r.AudioAssetIDs = []string{audio}
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("13 references accepted")
	}
	r.AssetIDs = nil
	r.VideoReferences = videos[:1]
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err != nil {
		t.Fatalf("video plus audio rejected: %v", err)
	}
	r.AudioAssetIDs = nil
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err != nil {
		t.Fatalf("video only rejected: %v", err)
	}
	r.VideoReferences = nil
	r.AudioAssetIDs = []string{audio}
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("audio only accepted")
	}
	r.AudioAssetIDs = nil
	r.AssetIDs = []string{images[0], images[0]}
	r.ExpectedQuoteMicro = 500000
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, id := range []string{"foreign", "a"} {
		r.AssetIDs = []string{id}
		if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
			t.Fatal("foreign/missing blob accepted")
		}
	}
	internal := canvasReferenceAsset(t, f, "image", 0)
	internal.ID = uuid.NewString()
	internal.Internal = true
	if err := f.s.Repo.AddAsset(ctx, f.team, internal); err != nil {
		t.Fatal(err)
	}
	r.AssetIDs = []string{internal.ID}
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("internal image accepted")
	}
	r.AssetIDs = images[:1]
	r.AudioAssetIDs = []string{audio, canvasReferenceAsset(t, f, "audio", 13).ID}
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("overlong accumulated audio accepted")
	}
	r.AudioAssetIDs = nil
	r.VideoReferences = []CanvasVideoReference{{AssetID: canvasReferenceAsset(t, f, "video", 8).ID}, {AssetID: canvasReferenceAsset(t, f, "video", 8).ID}}
	r.ExpectedQuoteMicro = 1000000
	if _, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("overlong accumulated video accepted")
	}
	if f.submits.Load() != 0 {
		t.Fatal("admission tests submitted a paid task")
	}
}

func TestCanvasMultimodalTailFrameRetriesWithoutResubmitOrVideoDownload(t *testing.T) {
	for _, tailAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "retry", false: "missing_url"}[tailAvailable], func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			r := seedanceCanvasRequest(t, f)
			yes := true
			r.ReturnLastFrame = &yes
			f.returnLastFrame = tailAvailable
			var err error
			f.video, err = os.ReadFile("testdata/blue.mp4")
			if err != nil {
				t.Fatal(err)
			}
			p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
			if err != nil {
				t.Fatal(err)
			}
			f.s.Tick(ctx)
			f.s.Tick(ctx)
			p = current(t, f, p)
			if p.Shots[0].OutputAssetID == "" || f.submits.Load() != 1 || f.videoDownloads.Load() != 1 {
				t.Fatalf("video not retained: %+v", p.Shots[0])
			}
			if tailAvailable && (p.Shots[0].Error != "last_frame_download_failed" || p.Shots[0].Status != "video_ready") {
				t.Fatalf("tail retry lost: %+v", p.Shots[0])
			}
			for range 6 {
				f.s.Tick(ctx)
			}
			p = current(t, f, p)
			if f.submits.Load() != 1 || f.videoDownloads.Load() != 1 || f.queries.Load() != 1 {
				t.Fatalf("repeated task or video: %d/%d/%d", f.submits.Load(), f.videoDownloads.Load(), f.queries.Load())
			}
			team, _ := f.s.Repo.Store.GetTeam(ctx, f.team)
			if tailAvailable {
				if p.Status != "completed" || p.Shots[0].LastFrameAssetID == "" || p.View().Shots[0].LastFrameAssetID == "" || p.ChargedMicro != 500000 || team.UsedCredits != "0.500000" || f.imageDownloads.Load() != 2 {
					t.Fatalf("tail lifecycle: %+v ledger=%+v", p.View(), team)
				}
				f.s.ArchiveCanvasOutput = func(_ context.Context, input CanvasArchiveInput) CanvasTeamAssetArchive {
					return CanvasTeamAssetArchive{SourceAssetID: input.Source.ID, Status: "saved", TeamAssetID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(input.Source.ID)).String()}
				}
				f.s.Tick(ctx)
				p = current(t, f, p)
				if len(p.View().TeamAssetArchives) != 2 || p.View().TeamAssetArchives[1].SourceAssetID != p.Shots[0].LastFrameAssetID || f.submits.Load() != 1 || p.ChargedMicro != 500000 {
					t.Fatalf("tail frame not archived independently: %+v", p.View())
				}
			} else if p.Status != "reconciliation_required" || p.Error != "last_frame_result_missing" || p.ChargedMicro != 0 {
				t.Fatalf("missing tail reported success: %+v", p.View())
			}
			f.requestMu.Lock()
			defer f.requestMu.Unlock()
			if f.requestBodies[0]["return_last_frame"] != true || !strings.Contains(f.requestBodies[0]["model"].(string), "seedance-2") {
				t.Fatal(f.requestBodies)
			}
		})
	}
}

func TestCanvasMultimodalCatalogueAudioDefaultIsExplicit(t *testing.T) {
	for _, adapter := range []string{"ark", "toapis"} {
		t.Run(adapter, func(t *testing.T) {
			var body map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "task_fixture"})
			}))
			defer upstream.Close()
			modelName := "doubao-seedance-2-0-mini-260615"
			if adapter == "toapis" {
				modelName = "seedance-2-mini"
			}
			p := Provider{BaseURL: upstream.URL, Client: upstream.Client(), Adapter: adapter}
			_, err := p.CanvasMultimodalVideo(context.Background(), Model{Kind: "video", Adapter: adapter, UpstreamModel: modelName}, CanvasRequest{Kind: "video", ReferenceMode: "multimodal", Duration: 5}, nil, nil, nil)
			if err != nil || body["generate_audio"] != false {
				t.Fatalf("silent catalogue default lost: %#v %v", body, err)
			}
		})
	}
}

package media

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestValidateModelsVideoReferenceCapability(t *testing.T) {
	model := fixtureVideoModel()
	model.VideoReference = &VideoReferenceCapability{
		MaxDurationSeconds: 30,
		MaxImages:          3,
		PriceMicro:         900_000,
		CostMicro:          400_000,
	}
	if err := ValidateModels([]Model{model}); err != nil {
		t.Fatalf("valid capability rejected: %v", err)
	}
	zeroCost := model
	zeroCapability := *model.VideoReference
	zeroCapability.CostMicro = 0
	zeroCost.VideoReference = &zeroCapability
	if err := ValidateModels([]Model{zeroCost}); err != nil {
		t.Fatalf("zero capability cost rejected: %v", err)
	}
	for name, mutate := range map[string]func(*VideoReferenceCapability){
		"duration-zero": func(c *VideoReferenceCapability) { c.MaxDurationSeconds = 0 },
		"duration-over": func(c *VideoReferenceCapability) { c.MaxDurationSeconds = 91 },
		"images-zero":   func(c *VideoReferenceCapability) { c.MaxImages = 0 },
		"images-over":   func(c *VideoReferenceCapability) { c.MaxImages = 9 },
		"price-zero":    func(c *VideoReferenceCapability) { c.PriceMicro = 0 },
		"cost-negative": func(c *VideoReferenceCapability) { c.CostMicro = -1 },
		"cost-overflow": func(c *VideoReferenceCapability) { c.CostMicro = 100_000_001 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := model
			capability := *model.VideoReference
			mutate(&capability)
			invalid.VideoReference = &capability
			if err := ValidateModels([]Model{invalid}); err == nil {
				t.Fatal("invalid capability accepted")
			}
		})
	}
}

func TestBuildShotsUsesIndependentVideoReferencePriceAndLimit(t *testing.T) {
	brief := Brief{
		Mode:                     "replicate",
		GenerationMode:           "video_reference",
		Product:                  "舞蹈角色",
		AssetIDs:                 []string{"replacement"},
		ReferenceAssetID:         "source",
		ReferenceDurationSeconds: 5,
		VideoModel:               "v",
		Resolution:               "720p",
		AspectRatio:              "9:16",
		Duration:                 5,
		MaxCreditsMicro:          10_000_000,
		ReviewPlan:               true,
	}
	model := fixtureVideoModel()
	model.ID = "v"
	model.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 5, MaxImages: 2, PriceMicro: 700_000, CostMicro: 350_000}
	plan := Plan{
		References: []Reference{{URL: "", Title: "原片", CapturedAt: time.Now(), Evidence: "看过原片", SelectionReason: "用户指定"}},
		Shots:      []ShotPlan{{SourceURL: "", SourceStart: 0, SourceEnd: 5, Duration: 5, Framing: "全景", Camera: "跟拍", Action: "舞蹈", Purpose: "展示", Prompt: "替换角色", AssetID: "replacement"}},
	}
	shots, quote, err := BuildShots(brief, plan, []Model{model})
	if err != nil || len(shots) != 1 || quote != 3_500_000 {
		t.Fatalf("quote=%d shots=%d err=%v", quote, len(shots), err)
	}

	plan.Shots[0].SourceEnd = 5.001
	if _, _, err = BuildShots(brief, plan, []Model{model}); err == nil {
		t.Fatal("source clip beyond reference-model limit accepted")
	}

	model.VideoReference = nil
	if _, _, err = BuildShots(brief, plan, []Model{model}); err == nil {
		t.Fatal("unsupported model accepted for video-reference mode")
	}
}

func TestVideoSubmitBodyUsesReferenceRoles(t *testing.T) {
	model := fixtureVideoModel()
	model.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 30, MaxImages: 2, PriceMicro: 700_000, CostMicro: 350_000}
	body := videoSubmitBody(model, Brief{Resolution: "720p", AspectRatio: "9:16"}, ShotPlan{Prompt: "保留动作并替换角色", Duration: 5}, "https://media.example.test/replacement.png", "https://media.example.test/clip.mp4")
	content, ok := body["content"].([]any)
	if !ok || len(content) != 3 {
		t.Fatalf("content = %#v", body["content"])
	}
	image := content[1].(map[string]any)
	video := content[2].(map[string]any)
	if image["type"] != "image_url" || image["role"] != "reference_image" || image["image_url"].(map[string]string)["url"] != "https://media.example.test/replacement.png" {
		t.Fatalf("replacement role = %#v", image)
	}
	if video["type"] != "video_url" || video["role"] != "reference_video" || video["video_url"].(map[string]string)["url"] != "https://media.example.test/clip.mp4" {
		t.Fatalf("source role = %#v", video)
	}
	if _, exists := body["first_frame"]; exists {
		t.Fatal("video-reference input was also sent as a first frame")
	}
}

func TestCanvasVideoReferenceUsesUploadedClipBeforeSubmission(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.s.ReferenceProbe = func(_ context.Context, path string) (ReferenceMetadata, error) {
		if strings.Contains(path, ".reference-clip-upload-") {
			return ReferenceMetadata{DurationSeconds: 2, Width: 160, Height: 90}, nil
		}
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
	f.video = video
	if err = os.WriteFile(sourcePath, video, 0600); err != nil {
		t.Fatal(err)
	}
	sourceChecksum, err := checksumFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: sourceID, Name: "source.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len(video)), DurationSeconds: 5, Checksum: sourceChecksum, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	clipBytes := append([]byte(nil), video...)
	clip, err := f.s.UploadReferenceClip(ctx, f.team, sourceID, sourceChecksum, 1, 3, bytes.NewReader(clipBytes))
	if err != nil {
		t.Fatal(err)
	}
	uploadReplay, err := f.s.UploadReferenceClip(ctx, f.team, sourceID, sourceChecksum, 1, 3, bytes.NewReader(clipBytes))
	if err != nil || uploadReplay.ID != clip.ID {
		t.Fatalf("same clip upload was not idempotent: %+v %v", uploadReplay, err)
	}
	models, err := f.s.Repo.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := models[0]
	model.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 3, MaxImages: 2, PriceMicro: 600_000, CostMicro: 250_000}
	models[0] = model
	raw, _ := json.Marshal(models)
	if err = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	r := CanvasRequest{
		Kind: "video", ModelID: model.ID, Prompt: "复刻动作并替换人物", AssetIDs: []string{"a"},
		VideoReferences: []CanvasVideoReference{{AssetID: sourceID, StartSeconds: float64Pointer(1), EndSeconds: float64Pointer(3), ReferenceClipAssetID: clip.ID}},
		Resolution:      "720p", AspectRatio: "16:9", Duration: 5, Count: 1,
		ExpectedQuoteMicro: 3_000_000, BoardID: "board", NodeID: "node", WorkspacePath: "/fixture",
	}
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	if r.VideoReferences[0].ReferenceClipAssetID != clip.ID || p.Canvas.VideoReferences[0].ReferenceClipAssetID != "" {
		t.Fatalf("clip identity must be generation-only: request=%+v persisted=%+v", r.VideoReferences[0], p.Canvas.VideoReferences[0])
	}
	for range 5 {
		f.s.Tick(ctx)
		p = current(t, f, p)
		if p.Status == "completed" || p.Status == "failed" {
			break
		}
	}
	if p.Status != "completed" || p.ChargedMicro != 3_000_000 || f.submits.Load() != 1 {
		t.Fatalf("status=%s charged=%d submits=%d error=%s", p.Status, p.ChargedMicro, f.submits.Load(), p.Error)
	}
	f.requestMu.Lock()
	body := f.requestBodies[0]
	f.requestMu.Unlock()
	content := body["content"].([]any)
	refVideo := content[2].(map[string]any)
	videoURL := refVideo["video_url"].(map[string]any)
	clipURL := videoURL["url"].(string)
	if refVideo["role"] != "reference_video" || clipURL != clip.URL || clipURL == f.s.Blobs.URL(f.team, sourceID) {
		t.Fatalf("provider did not receive the uploaded reference clip: %#v", refVideo)
	}
	assets, err := f.s.Repo.Assets(ctx, f.team)
	if err != nil {
		t.Fatal(err)
	}
	clipCount := 0
	for _, asset := range assets {
		if asset.Internal && asset.Kind == "video" {
			clipCount++
		}
	}
	if clipCount != 1 || p.Shots[0].ReferenceVideoAssetID != clip.ID {
		t.Fatalf("clip identity not bound to generation: clip_count=%d shot=%+v", clipCount, p.Shots[0])
	}
	if math.Abs(p.Shots[0].Plan.SourceStart-1) > 0.001 || math.Abs(p.Shots[0].Plan.SourceEnd-3) > 0.001 {
		t.Fatal("canvas source interval was changed")
	}

	// A replayed project reuses the frozen provider input and never creates another paid job.
	replayed, err := f.s.CreateCanvas(ctx, f.team, f.user, "", p.ID, r)
	if err != nil || replayed.ID != p.ID {
		t.Fatalf("replay=%v project=%v", err, replayed)
	}
}

func TestCanvasRejectsUnreadyMultipleAndUnsupportedVideoReferences(t *testing.T) {
	f := setup(t)
	r := canvasRequest(t, f)
	r.Kind, r.ModelID, r.Duration, r.AspectRatio = "video", "v", 5, "16:9"
	r.ExpectedQuoteMicro = 500_000
	r.VideoReferences = []CanvasVideoReference{{AssetID: "not-in-team"}}
	if _, err := f.s.CreateCanvas(context.Background(), f.team, f.user, "", uuid.NewString(), r); err == nil {
		t.Fatal("foreign video reference accepted")
	}
}

func TestReferenceClipIdentityIncludesBytesAndConcurrentReplay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.s.ReferenceProbe = func(_ context.Context, path string) (ReferenceMetadata, error) {
		if strings.Contains(path, ".reference-clip-upload-") {
			return ReferenceMetadata{DurationSeconds: 2, Width: 160, Height: 90}, nil
		}
		return ReferenceMetadata{DurationSeconds: 5, Width: 160, Height: 90}, nil
	}
	video, err := os.ReadFile("testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	sourceID := uuid.NewString()
	sourcePath := f.s.Blobs.Path(f.team, sourceID)
	if err = os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(sourcePath, video, 0600); err != nil {
		t.Fatal(err)
	}
	sourceChecksum, err := checksumFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: sourceID, Name: "source.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len(video)), DurationSeconds: 5, Checksum: sourceChecksum}); err != nil {
		t.Fatal(err)
	}
	const workers = 4
	results := make(chan Asset, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			asset, uploadErr := f.s.UploadReferenceClip(ctx, f.team, sourceID, sourceChecksum, 1, 3, bytes.NewReader(video))
			results <- asset
			errors <- uploadErr
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	var stableID string
	for asset := range results {
		if uploadErr := <-errors; uploadErr != nil {
			t.Fatalf("concurrent identical upload failed: %v", uploadErr)
		}
		if stableID == "" {
			stableID = asset.ID
		} else if asset.ID != stableID {
			t.Fatalf("concurrent replay changed asset identity: %q != %q", asset.ID, stableID)
		}
	}
	differentBytes := append([]byte(nil), video...)
	differentBytes[len(differentBytes)-1] ^= 1
	different, err := f.s.UploadReferenceClip(ctx, f.team, sourceID, sourceChecksum, 1, 3, bytes.NewReader(differentBytes))
	if err != nil {
		t.Fatal(err)
	}
	if different.ID == stableID {
		t.Fatal("different clip bytes reused an existing clip identity")
	}
	assets, err := f.s.Repo.Assets(ctx, f.team)
	if err != nil {
		t.Fatal(err)
	}
	internalCount := 0
	for _, asset := range assets {
		if asset.Internal {
			internalCount++
		}
	}
	if internalCount != 2 {
		t.Fatalf("expected one record per unique clip checksum; got %d", internalCount)
	}
}

func TestReplicaReviseRequiresUploadedClipBoundToFrozenSourceRange(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 1.3, Width: 160, Height: 90}, nil
	}
	video, err := os.ReadFile("testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	sourceID := uuid.NewString()
	sourcePath := f.s.Blobs.Path(f.team, sourceID)
	if err = os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(sourcePath, video, 0600); err != nil {
		t.Fatal(err)
	}
	sourceChecksum, err := checksumFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Repo.AddAsset(ctx, f.team, Asset{ID: sourceID, Name: "source.mp4", Kind: "video", Mime: "video/mp4", Size: int64(len(video)), DurationSeconds: 1.3, Checksum: sourceChecksum}); err != nil {
		t.Fatal(err)
	}
	videoModel := fixtureVideoModel()
	videoModel.VideoReference = &VideoReferenceCapability{MaxDurationSeconds: 5, MaxImages: 2, PriceMicro: 700_000, CostMicro: 350_000}
	imageModel := Model{ID: "im", Kind: "image", PriceMicro: 10_000}
	models := []Model{videoModel, imageModel}
	raw, _ := json.Marshal(models)
	if err = f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreateReferenceAnalysis(ctx, f.team, f.user, "device", uuid.NewString(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	ref := Reference{URL: "", Title: "原片", CapturedAt: time.Now(), Evidence: "确认动作范围", SelectionReason: "用户导入"}
	report := SourceAnalysis{SourceChecksum: sourceChecksum, Summary: "舞蹈原片", References: []Reference{ref}, Shots: []SourceShot{{SourceURL: "", SourceStart: 0, SourceEnd: 1.3, Framing: "全景", Camera: "跟拍", Action: "舞蹈", Purpose: "展示"}}}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "source", TaskID: "source-task", InputID: "source-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.SetSourceAnalysis(ctx, f.team, p.ID, p.Version, "source-input", report)
	if err != nil {
		t.Fatal(err)
	}
	brief := p.Brief
	brief.SourceOnly = false
	brief.GenerationMode = "video_reference"
	brief.AssetIDs = []string{"a"}
	brief.VideoModel = videoModel.ID
	brief.ImageModel = imageModel.ID
	brief.Resolution = "720p"
	brief.Duration = 4
	brief.MaxCreditsMicro = 20_000_000
	p, err = f.s.ConfigureReference(ctx, f.team, p.ID, p.Version, brief)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Bind(ctx, f.team, p.ID, p.Version, Analysis{Kind: "adaptation", TaskID: "adapt-task", InputID: "adapt-input", WorkspacePath: "/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{References: []Reference{ref}, Shots: []ShotPlan{{SourceURL: "", SourceStart: 0, SourceEnd: 1.3, Duration: 4, Framing: "全景", Camera: "跟拍", Action: "替换主体舞蹈", Purpose: "展示", Prompt: "保留动作并替换人物", ImagePrompt: "生成人物首帧", AssetID: "a"}}}
	p, err = f.s.SetPlan(ctx, f.team, p.ID, p.Version, "adapt-input", plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Revise(ctx, f.team, p.ID, p.Version, plan); err == nil || err.Error() != "video_reference_clip_required" {
		t.Fatalf("revision without uploaded clip should fail before generation: %v", err)
	}
	clip, err := f.s.UploadReferenceClip(ctx, f.team, sourceID, sourceChecksum, 0, 1.3, bytes.NewReader(video))
	if err != nil {
		t.Fatal(err)
	}
	plan.Shots[0].ReferenceVideoAssetID = clip.ID
	p, err = f.s.Revise(ctx, f.team, p.ID, p.Version, plan)
	if err != nil || p.Shots[0].ReferenceVideoAssetID != clip.ID {
		t.Fatalf("valid clip was not bound to replica shot: %+v %v", p, err)
	}
}

func float64Pointer(value float64) *float64 { return &value }

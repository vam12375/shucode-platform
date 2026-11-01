package apiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func compositionAPIFixture(t *testing.T) (*apiFixture, string, string, *media.Project, map[string]any) {
	t.Helper()
	f, token, team := newUserConfigPolicyFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	ctx := context.Background()
	user, err := f.store.FindUserByEmail(ctx, "config-policy@test.local")
	if err != nil || user == nil {
		t.Fatalf("fixture account: %v", err)
	}
	raw, err := os.ReadFile("../media/testdata/blue.mp4")
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := f.deps.Media.Blobs.Put(team, bytes.NewReader(raw), media.MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.deps.Media.Repo.AddAsset(ctx, team, media.Asset{ID: id, Kind: "video", Mime: "video/mp4", Size: int64(len(raw))}); err != nil {
		t.Fatal(err)
	}
	p := &media.Project{ID: uuid.NewString(), TeamID: team, UserID: user.ID, Version: 1, Status: "completed", OutputAssetID: id, ChargedMicro: 12345, Brief: media.Brief{Mode: "auto", Product: "fixture", Resolution: "720p", AspectRatio: "16:9", Finishing: &media.FinishingOptions{Subtitles: "none"}}, Shots: []media.Shot{{Status: "succeeded", OutputAssetID: id, Plan: media.ShotPlan{Duration: 5}}}}
	if err = f.deps.Media.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.deps.Media.Repo.Index(ctx, media.ProjectKey(team, p.ID)); err != nil {
		t.Fatal(err)
	}
	edit := map[string]any{"fps": 30, "voiceover": false, "music": false, "subtitles": "none", "clips": []any{map[string]any{"sourceShotIndex": 0, "inFrame": 0, "outFrame": 150, "speed": 1}}}
	return f, token, team, p, edit
}

func TestMediaRecomposeStrictJSONAndAuthenticatedReplay(t *testing.T) {
	f, token, team, p, edit := compositionAPIFixture(t)
	path := "/api/v1/media/projects/" + p.ID + "/recompose"
	requestID := uuid.NewString()
	body := map[string]any{"version": p.Version, "requestId": requestID, "edit": edit}
	if response := sourceRequest(f, "", "POST", path, body); response.Code != 401 {
		t.Fatal("missing auth", response.Code)
	}
	for _, change := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown-top", func(v map[string]any) { v["unexpected"] = true }},
		{"alias-version", func(v map[string]any) { v["Version"] = 999 }},
		{"alias-edit", func(v map[string]any) { v["Edit"] = v["edit"] }},
		{"alias-voiceover", func(v map[string]any) { v["edit"].(map[string]any)["Voiceover"] = true }},
		{"alias-index", func(v map[string]any) {
			v["edit"].(map[string]any)["clips"].([]any)[0].(map[string]any)["SourceShotIndex"] = 1
		}},
		{"missing-edit", func(v map[string]any) { delete(v, "edit") }},
		{"missing-voiceover", func(v map[string]any) { delete(v["edit"].(map[string]any), "voiceover") }},
		{"null-music", func(v map[string]any) { v["edit"].(map[string]any)["music"] = nil }},
		{"unknown-edit", func(v map[string]any) { v["edit"].(map[string]any)["assetId"] = "arbitrary" }},
		{"null-index", func(v map[string]any) {
			v["edit"].(map[string]any)["clips"].([]any)[0].(map[string]any)["sourceShotIndex"] = nil
		}},
		{"missing-in-frame", func(v map[string]any) {
			delete(v["edit"].(map[string]any)["clips"].([]any)[0].(map[string]any), "inFrame")
		}},
		{"null-subtitle", func(v map[string]any) {
			v["edit"].(map[string]any)["clips"].([]any)[0].(map[string]any)["subtitle"] = nil
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			raw, _ := json.Marshal(body)
			var invalid map[string]any
			_ = json.Unmarshal(raw, &invalid)
			change.mutate(invalid)
			response := sourceRequest(f, token, "POST", path, invalid)
			if response.Code != 400 {
				t.Fatalf("invalid payload status %d: %s", response.Code, response.Body.String())
			}
			stored, _ := f.deps.Media.Repo.Get(context.Background(), team, p.ID)
			if stored.Version != p.Version || stored.Composition != nil {
				t.Fatal("invalid payload mutated state")
			}
		})
	}
	// 严格 JSON 拒绝用例不依赖 ffmpeg；真实重合成受理在 CI 安装后期依赖后执行。
	if !media.FinishingCompositionAvailable() {
		t.Skip("ffmpeg finishing composition unavailable")
	}
	response := sourceRequest(f, token, "POST", path, body)
	if response.Code != 200 {
		t.Fatalf("recompose: %d %s", response.Code, response.Body.String())
	}
	accepted := dataOf(t, response)
	if accepted["status"] != "composing" || accepted["outputAssetId"] != p.OutputAssetID || accepted["chargedMicro"] != float64(p.ChargedMicro) {
		t.Fatal(response.Body.String())
	}
	replay := sourceRequest(f, token, "POST", path, body)
	if replay.Code != 200 || dataOf(t, replay)["version"] != accepted["version"] {
		t.Fatal("same request did not replay stale version", replay.Body.String())
	}
	for _, forbidden := range []string{"compositionReceipts", "signature", "compositionAssetId", f.deps.Media.Blobs.Root} {
		if strings.Contains(replay.Body.String(), forbidden) {
			t.Fatal("private state leaked", forbidden)
		}
	}
	edit["musicVolume"] = 0.2
	if conflict := sourceRequest(f, token, "POST", path, body); conflict.Code != 409 {
		t.Fatal("same request accepted changed content", conflict.Code)
	}
}

func TestMediaRecomposeChecksProjectAccountAndTeamBeforeReplay(t *testing.T) {
	f, token, team, p, edit := compositionAPIFixture(t)
	ctx := context.Background()
	p.UserID = uuid.NewString()
	previous, _ := f.deps.Media.Repo.Get(ctx, team, p.ID)
	if err := f.deps.Media.Repo.Save(ctx, p, previous); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"version": p.Version, "requestId": uuid.NewString(), "edit": edit}
	path := "/api/v1/media/projects/" + p.ID + "/recompose"
	if response := sourceRequest(f, token, "POST", path, body); response.Code == 200 {
		t.Fatal("another account admitted recompose")
	}
	other := *p
	other.ID = uuid.NewString()
	other.TeamID = uuid.NewString()
	if err := f.deps.Media.Repo.Save(ctx, &other, nil); err != nil {
		t.Fatal(err)
	}
	if response := sourceRequest(f, token, "POST", "/api/v1/media/projects/"+other.ID+"/recompose", body); response.Code == 200 {
		t.Fatal("another team admitted recompose")
	}
}

func TestMediaOverviewOnlySignsReferencedSameTeamQualityFrames(t *testing.T) {
	f, token, team, p, _ := compositionAPIFixture(t)
	ctx := context.Background()
	add := func(id, kind string) {
		t.Helper()
		mime := "image/jpeg"
		if kind == "audio" {
			mime = "audio/wav"
		}
		if err := f.deps.Media.Repo.AddAsset(ctx, team, media.Asset{ID: id, Kind: kind, Mime: mime, Internal: true, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	shown, hidden, narration, cross, canvas := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{shown, hidden, cross, canvas} {
		add(id, "image")
	}
	add(narration, "audio")
	previous, _ := f.deps.Media.Repo.Get(ctx, team, p.ID)
	p.QualityReport = &media.VideoQualityReport{Version: 1, Status: "passed", Keyframes: []media.VideoQualityKeyframe{{Frame: 0, AssetID: shown}, {Frame: 1, AssetID: narration}}}
	if err := f.deps.Media.Repo.Save(ctx, p, previous); err != nil {
		t.Fatal(err)
	}
	foreign := *p
	foreign.ID = uuid.NewString()
	foreign.TeamID = uuid.NewString()
	foreign.QualityReport = &media.VideoQualityReport{Keyframes: []media.VideoQualityKeyframe{{AssetID: cross}}}
	if err := f.deps.Media.Repo.Save(ctx, &foreign, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Media.Repo.Index(ctx, media.ProjectKey(foreign.TeamID, foreign.ID)); err != nil {
		t.Fatal(err)
	}
	canvasProject := *p
	canvasProject.ID = uuid.NewString()
	canvasProject.Canvas = &media.CanvasRequest{}
	canvasProject.QualityReport = &media.VideoQualityReport{Keyframes: []media.VideoQualityKeyframe{{AssetID: canvas}}}
	if err := f.deps.Media.Repo.Save(ctx, &canvasProject, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Media.Repo.Index(ctx, media.ProjectKey(team, canvasProject.ID)); err != nil {
		t.Fatal(err)
	}
	response := sourceRequest(f, token, "GET", "/api/v1/media/overview", nil)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Assets []media.Asset `json:"assets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	visible := map[string]string{}
	for _, asset := range envelope.Data.Assets {
		visible[asset.ID] = asset.URL
	}
	if visible[shown] == "" {
		t.Fatal("referenced frame not signed")
	}
	for _, id := range []string{hidden, narration, cross, canvas} {
		if visible[id] != "" {
			t.Fatal("unreferenced/cross-domain internal asset exposed", id)
		}
	}
	signed := strings.TrimPrefix(visible[shown], f.deps.Env.PublicAPIOrigin)
	request := httptest.NewRequest("GET", strings.Replace(signed, team, uuid.NewString(), 1), nil)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, request)
	if rec.Code != 403 {
		t.Fatal("quality frame signature crossed team", rec.Code)
	}
}

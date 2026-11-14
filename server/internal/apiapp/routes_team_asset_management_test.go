package apiapp

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTeamAssetManagedRoutes(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	owner := newTeamAssetTestActor(t, f, "Owner")
	member := newTeamAssetTestActor(t, f, "Member")
	newTeamAssetTestTeam(t, f, owner, member)
	asset := teamAssetTestDecodeAsset(t, teamAssetTestUpload(t, f, owner, uuid.NewString(), "poster.md", []byte("hello")))
	if len(asset.ContentHash) != 64 {
		t.Fatal("missing content hash")
	}
	request := func(actor teamAssetTestActor, path, body string) int {
		return teamAssetTestRequest(f, actor, http.MethodPut, "/api/v1/team-assets/"+asset.ID+path, "application/json", strings.NewReader(body)).Code
	}
	if request(member, "/favorite", `{"favorite":true}`) != 200 {
		t.Fatal("favorite failed")
	}
	if request(member, "/tags", `{"tags":["campaign"]}`) != 403 {
		t.Fatal("tag permission missing")
	}
	if request(owner, "/tags", `{"tags":["campaign"]}`) != 200 {
		t.Fatal("tag edit failed")
	}
	if request(owner, "/tags", `{"tags":[],"teamId":"other"}`) != 400 {
		t.Fatal("extra field accepted")
	}
	w := teamAssetTestRequest(f, member, "GET", "/api/v1/team-assets?v=2&favoritesOnly=true&tag=campaign", "", nil)
	var result struct {
		Data struct {
			Revision    string
			Assets      []json.RawMessage
			NotModified bool
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data.Assets) != 1 {
		t.Fatalf("managed page: %s", w.Body)
	}
	w = teamAssetTestRequest(f, member, "GET", "/api/v1/team-assets?v=2&favoritesOnly=true&tag=campaign&knownRevision="+result.Data.Revision, "", nil)
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Data.NotModified || len(result.Data.Assets) != 0 {
		t.Fatalf("revision check: %s", w.Body)
	}
	w = teamAssetTestRequest(f, owner, "GET", "/api/v1/team-assets?v=2&favoritesOnly=true", "", nil)
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data.Assets) != 0 {
		t.Fatal("personal preference leaked")
	}
}

func TestTeamAssetThumbnailAndHashValidation(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	owner := newTeamAssetTestActor(t, f, "Owner")
	newTeamAssetTestTeam(t, f, owner)
	var encoded bytes.Buffer
	source := image.NewRGBA(image.Rect(0, 0, 800, 400))
	source.Set(5, 5, color.White)
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	asset := teamAssetTestDecodeAsset(t, teamAssetTestUpload(t, f, owner, uuid.NewString(), "poster.png", encoded.Bytes()))
	w := teamAssetTestRequest(f, owner, "GET", "/api/v1/team-assets/"+asset.ID+"/thumbnail", "", nil)
	config, _, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if w.Code != 200 || err != nil || config.Width != 256 || config.Height != 128 || w.Body.Len() > 65536 {
		t.Fatalf("thumbnail: %d %+v %v", w.Code, config, err)
	}
	if w := teamAssetTestRequest(f, teamAssetTestActor{}, "GET", "/api/v1/team-assets/"+asset.ID+"/thumbnail", "", nil); w.Code != 401 {
		t.Fatal("thumbnail was public")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("id", uuid.NewString())
	_ = form.WriteField("contentHash", strings.Repeat("0", 64))
	file, _ := form.CreateFormFile("file", "bad.txt")
	_, _ = file.Write([]byte("mismatch"))
	_ = form.Close()
	w = teamAssetTestRequest(f, owner, "POST", "/api/v1/team-assets", form.FormDataContentType(), &body)
	if w.Code != 422 {
		t.Fatalf("mismatched digest: %d %s", w.Code, w.Body)
	}
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, owner, "GET", "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 1 {
		t.Fatal("mismatched digest committed")
	}
}

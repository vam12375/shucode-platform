package apiapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestReferenceRoutesAuthenticateLimitUploadsAndHideConfiguration(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	f.deps.Media.ReferenceProbe = func(context.Context, string) (media.ReferenceMetadata, error) {
		return media.ReferenceMetadata{DurationSeconds: 1.3, Width: 100, Height: 200}, nil
	}
	request := func(method, path string, body *bytes.Buffer, auth bool, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/api/v1/media/references/import", bytes.NewBufferString(`{"id":"bad","url":"https://example.test/v"}`), false, "application/json"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/console/api/media/source-parser", bytes.NewBuffer(nil), true, ""); w.Code == 200 {
		t.Fatal("device can read admin config")
	}
	if w := request("POST", "/api/v1/media/references/import", bytes.NewBufferString(`{"id":"bad","url":"https://example.test/v","apiKey":"secret"}`), true, "application/json"); w.Code != 400 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "original.mp4")
	part.Write(append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...))
	writer.Close()
	w := request("POST", "/api/v1/media/references/upload", body, true, writer.FormDataContentType())
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"durationSeconds":1.3`) || !strings.Contains(w.Body.String(), `"checksum"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestReferenceClipUploadIsTeamScopedAndIdempotent(t *testing.T) {
	f, token, teamID := newUserConfigPolicyFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	f.deps.Media.ReferenceProbe = func(_ context.Context, path string) (media.ReferenceMetadata, error) {
		if strings.Contains(path, ".reference-clip-upload-") {
			return media.ReferenceMetadata{DurationSeconds: 2, Width: 160, Height: 90}, nil
		}
		return media.ReferenceMetadata{DurationSeconds: 5, Width: 160, Height: 90}, nil
	}
	video, err := os.ReadFile(filepath.Join("..", "media", "testdata", "blue.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	sourceID, size, err := f.deps.Media.Blobs.Put(teamID, bytes.NewReader(video), media.MaxVideo)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256(video)
	sourceChecksum := hex.EncodeToString(sourceHash[:])
	if err = f.deps.Media.Repo.AddAsset(context.Background(), teamID, media.Asset{ID: sourceID, Name: "source.mp4", Kind: "video", Mime: "video/mp4", Size: size, DurationSeconds: 5, Checksum: sourceChecksum}); err != nil {
		t.Fatal(err)
	}
	request := func(body *bytes.Buffer, contentType string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/media/reference-clips", body)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	makeBody := func(clip []byte, checksum string) (*bytes.Buffer, string) {
		body := new(bytes.Buffer)
		writer := multipart.NewWriter(body)
		_ = writer.WriteField("sourceAssetId", sourceID)
		_ = writer.WriteField("sourceChecksum", checksum)
		_ = writer.WriteField("startSeconds", "1")
		_ = writer.WriteField("endSeconds", "3")
		part, _ := writer.CreateFormFile("file", "clip.mp4")
		_, _ = part.Write(clip)
		_ = writer.Close()
		return body, writer.FormDataContentType()
	}
	body, contentType := makeBody(video, sourceChecksum)
	if w := request(body, contentType, false); w.Code != 401 {
		t.Fatalf("unauthenticated upload status=%d body=%s", w.Code, w.Body.String())
	}
	body, contentType = makeBody(video, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if w := request(body, contentType, true); w.Code != 422 {
		t.Fatalf("wrong source checksum status=%d body=%s", w.Code, w.Body.String())
	}
	body, contentType = makeBody(video, sourceChecksum)
	w := request(body, contentType, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"internal":true`) || !strings.Contains(w.Body.String(), `"url":"http://localhost`) {
		t.Fatalf("valid upload status=%d body=%s", w.Code, w.Body.String())
	}
	var first struct {
		Data media.Asset `json:"data"`
	}
	if err = json.NewDecoder(w.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	body, contentType = makeBody(video, sourceChecksum)
	w = request(body, contentType, true)
	var replay struct {
		Data media.Asset `json:"data"`
	}
	decodeErr := json.NewDecoder(w.Body).Decode(&replay)
	if w.Code != 200 || decodeErr != nil || replay.Data.ID != first.Data.ID {
		t.Fatalf("upload replay status=%d body=%s asset=%+v", w.Code, w.Body.String(), replay.Data)
	}
	assets, err := f.deps.Media.Repo.Assets(context.Background(), teamID)
	if err != nil {
		t.Fatal(err)
	}
	internal := 0
	for _, asset := range assets {
		if asset.Internal {
			internal++
		}
	}
	if internal != 1 {
		t.Fatalf("replay created duplicate internal assets: %d", internal)
	}
}

package apiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestMediaPreviewOverviewSignedAccessIsolationAndPersistentCache(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	b := &f.deps.Media.Blobs
	b.Root = t.TempDir()
	assets := []media.Asset{}
	for _, kind := range []string{"image", "video", "audio", "internal"} {
		id, size, err := b.Put(team, bytes.NewBufferString("original must not be encoded on overview"), media.MaxUpload)
		if err != nil {
			t.Fatal(err)
		}
		asset := media.Asset{ID: id, Name: "fixture", Kind: kind, Mime: "image/png", Size: size, CreatedAt: time.Now()}
		if kind == "internal" {
			asset.Kind, asset.Internal = "video", true
		}
		if err = f.deps.Media.Repo.AddAsset(context.Background(), team, asset); err != nil {
			t.Fatal(err)
		}
		assets = append(assets, asset)
	}
	request := func(method, path string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	overview := request("GET", "/api/v1/media/overview", true)
	if overview.Code != 200 {
		t.Fatal(overview.Code, overview.Body.String())
	}
	var envelope struct {
		Data struct {
			Assets []media.Asset `json:"assets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(overview.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Assets) != 3 {
		t.Fatal("internal asset leaked", overview.Body.String())
	}
	for i, asset := range envelope.Data.Assets {
		if (asset.ThumbnailURL != "") != (i == 0) || (asset.PosterURL != "") != (i == 1) {
			t.Fatal("wrong preview kind", asset)
		}
	}
	if _, err := os.Stat(filepath.Join(b.Root, ".previews")); !os.IsNotExist(err) {
		t.Fatal("overview encoded previews", err)
	}
	previewURL := envelope.Data.Assets[0].ThumbnailURL
	u, err := url.Parse(previewURL)
	if err != nil {
		t.Fatal(err)
	}
	// 缓存命中应在没有FFmpeg的普通Go镜像也可验证，编码器另有真实FFmpeg回归。
	path := filepath.Join(b.Root, ".previews", "jpeg-640-v1", team, assets[0].ID+".jpg")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 320, 180)), nil); err != nil {
		t.Fatal(err)
	}
	file.Close()
	for _, method := range []string{"GET", "HEAD"} {
		response := request(method, u.RequestURI(), false)
		if response.Code != 200 || response.Header().Get("Content-Type") != "image/jpeg" || response.Header().Get("Cache-Control") != "private, max-age=300" || response.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" {
			t.Fatal(method, response.Code, response.Header())
		}
		if method == "HEAD" && response.Body.Len() != 0 {
			t.Fatal("HEAD returned bytes")
		}
	}
	if response := request("GET", strings.Replace(u.RequestURI(), team, uuid.NewString(), 1), false); response.Code != 403 {
		t.Fatal("cross-team signature accepted", response.Code)
	}
	query := u.Query()
	query.Set("expires", "1")
	u.RawQuery = query.Encode()
	if response := request("GET", u.RequestURI(), false); response.Code != 403 {
		t.Fatal("expired signature accepted", response.Code)
	}
	for _, asset := range []media.Asset{assets[2], assets[3], {ID: uuid.NewString()}} {
		u, _ := url.Parse(b.PreviewURL(team, asset.ID))
		if response := request("GET", u.RequestURI(), false); response.Code != 404 {
			t.Fatal("nonvisible preview accepted", asset, response.Code)
		}
	}
	if err = os.Remove(b.Path(team, assets[0].ID)); err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(previewURL)
	if response := request("GET", u.RequestURI(), false); response.Code == 200 {
		t.Fatal("deleted original served")
	}
	raw, _ := os.ReadFile(b.Path(team, assets[1].ID))
	if string(raw) != "original must not be encoded on overview" {
		t.Fatal("original changed")
	}
}

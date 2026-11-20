package apiapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func assertSignedImageCache(t *testing.T, response *httptest.ResponseRecorder, lifetime int64) {
	t.Helper()
	policy := response.Header().Get("Cache-Control")
	if !strings.HasPrefix(policy, "public, max-age=") || !strings.Contains(policy, "s-maxage=") || !strings.Contains(policy, "must-revalidate") {
		t.Fatalf("signed cache policy: %q", policy)
	}
	maxAge, err := strconv.ParseInt(strings.Split(strings.TrimPrefix(policy, "public, max-age="), ",")[0], 10, 64)
	if err != nil || maxAge < 0 || maxAge > 300 || maxAge > lifetime {
		t.Fatalf("cache exceeds authorization: %q lifetime=%d", policy, lifetime)
	}
	if response.Header().Get("ETag") == "" {
		t.Fatal("missing immutable resource ETag")
	}
}

func TestSignedMediaCacheBoundedExpiryAndErrors(t *testing.T) {
	f, _, team := newUserConfigPolicyFixture(t)
	blobs := &f.deps.Media.Blobs
	blobs.Root = t.TempDir()
	raw := []byte("E2E_SIGNED_IMAGE_BYTES")
	id, size, err := blobs.Put(team, bytes.NewReader(raw), media.MaxUpload)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Media.Repo.AddAsset(context.Background(), team, media.Asset{ID: id, Kind: "image", Mime: "image/png", Size: size, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	expires := strconv.FormatInt(time.Now().Unix()+12, 10)
	mac := hmac.New(sha256.New, []byte(blobs.SigningKey))
	mac.Write([]byte(team + "/" + id + "/" + expires))
	path := "/api/v1/media/blobs/" + team + "/" + id + "?expires=" + expires + "&signature=" + hex.EncodeToString(mac.Sum(nil))
	request := func(method, path, etag, byteRange string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		if byteRange != "" {
			req.Header.Set("Range", byteRange)
		}
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		return rec
	}
	get := request("GET", path, "", "")
	if get.Code != 200 || !bytes.Equal(get.Body.Bytes(), raw) {
		t.Fatal(get.Code, get.Body.String())
	}
	assertSignedImageCache(t, get, 12)
	head := request("HEAD", path, "", "")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatal(head.Code, head.Body.String())
	}
	assertSignedImageCache(t, head, 12)
	conditional := request("GET", path, get.Header().Get("ETag"), "")
	if conditional.Code != 304 || conditional.Body.Len() != 0 {
		t.Fatal(conditional.Code)
	}
	assertSignedImageCache(t, conditional, 12)
	partial := request("GET", path, "", "bytes=0-3")
	if partial.Code != 206 || !bytes.Equal(partial.Body.Bytes(), raw[:4]) {
		t.Fatal(partial.Code)
	}
	assertSignedImageCache(t, partial, 12)
	for _, response := range []*httptest.ResponseRecorder{
		request("GET", path+"&signature=invalid", "", ""),
		request("GET", strings.Replace(path, "expires="+expires, "expires=1", 1), "", ""),
		request("GET", path, "", "bytes=999999-"),
	} {
		if response.Code < 400 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("errors must not cache: %d %q", response.Code, response.Header().Get("Cache-Control"))
		}
	}
}

func TestTeamAssetSignedCDNPreviewAndScope(t *testing.T) {
	f, root := newTeamAssetTestFixture(t)
	actor := newTeamAssetTestActor(t, f, "E2E_CDN_USER")
	team := newTeamAssetTestTeam(t, f, actor)
	f.deps.Media.Blobs.Origin = "https://cdn.example.test"
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 400, 200))); err != nil {
		t.Fatal(err)
	}
	id := "6772cde5-72de-4cff-a29a-4a60f15b361c"
	teamAssetTestDecodeAsset(t, teamAssetTestUpload(t, f, actor, id, "E2E_CDN_IMAGE.png", original.Bytes()))
	response := teamAssetTestRequest(f, actor, "GET", "/api/v1/team-assets?v=2", "", nil)
	var envelope struct {
		Data struct {
			MediaOrigin string `json:"mediaOrigin"`
			Assets      []struct {
				ID           string `json:"id"`
				ContentHash  string `json:"contentHash"`
				PreviewURL   string `json:"previewUrl"`
				ThumbnailURL string `json:"thumbnailUrl"`
			} `json:"assets"`
		} `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || len(envelope.Data.Assets) != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || envelope.Data.MediaOrigin != "https://cdn.example.test" {
		t.Fatal(response.Header(), response.Body.String())
	}
	asset := envelope.Data.Assets[0]
	for variant, rawURL := range map[string]string{"original": asset.PreviewURL, "thumbnail": asset.ThumbnailURL} {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme+"://"+parsed.Host != envelope.Data.MediaOrigin || parsed.Path != "/api/v1/team-asset-media/"+team+"/"+id+"/"+variant || parsed.Query().Get("v") != asset.ContentHash {
			t.Fatalf("bad %s URL: %q", variant, rawURL)
		}
		expires, err := strconv.ParseInt(parsed.Query().Get("expires"), 10, 64)
		if err != nil || expires <= time.Now().Unix() || expires > time.Now().Unix()+300 {
			t.Fatal("authorization exceeds five minutes", rawURL)
		}
		get := teamAssetTestRequest(f, teamAssetTestActor{}, "GET", parsed.RequestURI(), "", nil)
		if get.Code != 200 {
			t.Fatal(get.Code, get.Body.String())
		}
		assertSignedImageCache(t, get, 300)
		var cachedTime time.Time
		if variant == "original" && !bytes.Equal(get.Body.Bytes(), original.Bytes()) {
			t.Fatal("original changed")
		}
		if variant == "thumbnail" {
			config, _, err := image.DecodeConfig(bytes.NewReader(get.Body.Bytes()))
			if err != nil || config.Width > 256 || config.Height > 256 || get.Body.Len() > 65536 || get.Header().Get("Content-Type") != "image/jpeg" {
				t.Fatal("bad thumbnail", config, err)
			}
			info, err := os.Stat(filepath.Join(root, ".previews", "jpeg-256-v1", team, id+"-"+asset.ContentHash+".jpg"))
			if err != nil {
				t.Fatal("缩略图未持久复用", err)
			}
			cachedTime = info.ModTime()
		}
		head := teamAssetTestRequest(f, teamAssetTestActor{}, "HEAD", parsed.RequestURI(), "", nil)
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatal(head.Code)
		}
		assertSignedImageCache(t, head, 300)
		if variant == "thumbnail" {
			info, err := os.Stat(filepath.Join(root, ".previews", "jpeg-256-v1", team, id+"-"+asset.ContentHash+".jpg"))
			if err != nil || !info.ModTime().Equal(cachedTime) {
				t.Fatal("重复读取重新派生", err)
			}
		}
		req := httptest.NewRequest("GET", parsed.RequestURI(), nil)
		req.Header.Set("If-None-Match", get.Header().Get("ETag"))
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Fatal("missing conditional reuse", rec.Code)
		}
		assertSignedImageCache(t, rec, 300)
		for _, invalid := range []string{
			strings.Replace(parsed.RequestURI(), "/"+variant+"?", "/other?", 1),
			strings.Replace(parsed.RequestURI(), "v="+asset.ContentHash, "v="+strings.Repeat("0", 64), 1),
			strings.Replace(parsed.RequestURI(), "/"+team+"/", "/40f02623-b2c1-4c62-b1a0-d741223e448d/", 1),
			parsed.RequestURI() + "&signature=invalid",
		} {
			bad := teamAssetTestRequest(f, teamAssetTestActor{}, "GET", invalid, "", nil)
			if bad.Code < 400 || bad.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unsafe signed preview", bad.Code, bad.Header())
			}
		}
	}
	record, err := f.store.GetTeamAsset(context.Background(), team, id)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, team, record.BlobID)); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(asset.ThumbnailURL)
	missing := teamAssetTestRequest(f, teamAssetTestActor{}, "GET", parsed.RequestURI(), "", nil)
	if missing.Code < 400 || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("派生缓存复活已删除原件", missing.Code, missing.Header())
	}
}

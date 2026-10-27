package apiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestMediaOriginalSignedHeadAndRange(t *testing.T) {
	f, _, team := newUserConfigPolicyFixture(t)
	blobs := &f.deps.Media.Blobs
	blobs.Root = t.TempDir()
	raw := []byte("fixture-range-body")
	id, size, err := blobs.Put(team, bytes.NewReader(raw), media.MaxUpload)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.deps.Media.Repo.AddAsset(context.Background(), team, media.Asset{ID: id, Kind: "video", Mime: "video/mp4", Size: size, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(blobs.URL(team, id))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, byteRange string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if byteRange != "" {
			req.Header.Set("Range", byteRange)
		}
		rec := httptest.NewRecorder()
		f.engine.ServeHTTP(rec, req)
		return rec
	}
	head := request("HEAD", u.RequestURI(), "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(raw)) {
		t.Fatalf("合法 HEAD 应只返回原件元数据: status=%d body=%q headers=%v", head.Code, head.Body.String(), head.Header())
	}
	if head.Header().Get("Cache-Control") != "private, max-age=300" || head.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Fatalf("HEAD 必须保留媒体缓存和跨源展示边界: %v", head.Header())
	}
	partial := request("GET", u.RequestURI(), "bytes=4-9")
	if partial.Code != 206 || !bytes.Equal(partial.Body.Bytes(), raw[4:10]) || partial.Header().Get("Content-Range") != "bytes 4-9/"+strconv.Itoa(len(raw)) {
		t.Fatalf("GET Range 应返回原件指定字节: status=%d body=%q headers=%v", partial.Code, partial.Body.String(), partial.Header())
	}
	for _, invalid := range []string{"expired", "signature"} {
		copyURL := *u
		query := copyURL.Query()
		if invalid == "expired" {
			query.Set("expires", "1")
		} else {
			query.Set("signature", "invalid")
		}
		copyURL.RawQuery = query.Encode()
		if rec := request("HEAD", copyURL.RequestURI(), ""); rec.Code != 403 || rec.Body.Len() != 0 {
			t.Fatalf("%s HEAD 必须拒绝: status=%d body=%q", invalid, rec.Code, rec.Body.String())
		}
	}
	unknown, _ := url.Parse(blobs.URL(team, uuid.NewString()))
	if rec := request("HEAD", unknown.RequestURI(), ""); rec.Code != 404 {
		t.Fatalf("HEAD 必须校验资产存在性: %d", rec.Code)
	}
}

func TestMediaServiceOriginConfigurationAndFallback(t *testing.T) {
	f := newAPIEmptyFixture(t)
	if got := NewMediaService(f.deps).Blobs.Origin; got != f.deps.Env.PublicAPIOrigin {
		t.Fatalf("未配置媒体 CDN 时应使用 API origin: %q", got)
	}
	f.deps.Env.PublicMediaCDNOrigin = "https://cdn.example.test/"
	if got := NewMediaService(f.deps).Blobs.Origin; got != "https://cdn.example.test" {
		t.Fatalf("媒体服务未使用 CDN origin: %q", got)
	}
	if f.deps.Env.PublicAPIOrigin != "http://localhost:8787" {
		t.Fatal("媒体 origin 配置不得修改 API origin")
	}
}

func TestMediaOverviewUsesCDNForOriginalsAndPreviews(t *testing.T) {
	f, token, team := newUserConfigPolicyFixture(t)
	f.deps.Env.PublicMediaCDNOrigin = "https://cdn.example.test"
	f.deps.Media.Blobs = NewMediaService(f.deps).Blobs
	f.deps.Media.Blobs.Root = t.TempDir()
	for _, kind := range []string{"image", "video"} {
		asset := media.Asset{ID: uuid.NewString(), Kind: kind, Name: "fixture", CreatedAt: time.Now()}
		if err := f.deps.Media.Repo.AddAsset(context.Background(), team, asset); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/media/overview", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			MediaOrigin string        `json:"mediaOrigin"`
			Assets      []media.Asset `json:"assets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.MediaOrigin != f.deps.Env.PublicMediaCDNOrigin || len(envelope.Data.Assets) != 2 {
		t.Fatal("媒体概览 origin 或资产数量不符", rec.Body.String())
	}
	for _, asset := range envelope.Data.Assets {
		preview := asset.ThumbnailURL
		if asset.Kind == "video" {
			preview = asset.PosterURL
		}
		if preview == "" {
			t.Fatalf("缺少 %s 预览 URL", asset.Kind)
		}
		for _, raw := range []string{asset.URL, preview} {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme+"://"+u.Host != envelope.Data.MediaOrigin || !strings.HasPrefix(u.Path, "/api/v1/media/") {
				t.Fatalf("媒体地址未使用 CDN origin: %q (%v)", raw, err)
			}
			if !f.deps.Media.Blobs.Verify(team, asset.ID, u.Query().Get("expires"), u.Query().Get("signature")) {
				t.Fatalf("CDN 地址未保留有效签名: %q", raw)
			}
		}
	}
}

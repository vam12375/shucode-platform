package apiapp

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestHealthzAndStaticHosting(t *testing.T) {
	f := newAPIFixture(t)
	// 无静态目录时不注册(其余测试即覆盖);此处单独构造 dist。
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>console</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.deps.Env.ConsoleDistDir = dist
	engine := New(f.deps)

	health := httptest.NewRecorder()
	engine.ServeHTTP(health, httptest.NewRequest("GET", "/healthz", nil))
	if health.Code != 200 || !strings.Contains(health.Body.String(), `"memoryDb":true`) {
		t.Fatalf("healthz 不符: %d %s", health.Code, health.Body.String())
	}

	// TS 路由对尾斜杠直接 404,不能继承 Gin 默认的 301 去斜杠重定向。
	for _, path := range []string{"/healthz/", "/n/configs/"} {
		trailing := httptest.NewRecorder()
		engine.ServeHTTP(trailing, httptest.NewRequest("GET", path, nil))
		if trailing.Code != 404 {
			t.Fatalf("%s 应 404(不跟随尾斜杠重定向): %d", path, trailing.Code)
		}
	}

	redirect := httptest.NewRecorder()
	engine.ServeHTTP(redirect, httptest.NewRequest("GET", "/console", nil))
	if redirect.Code != 302 || redirect.Header().Get("location") != "/console/" {
		t.Fatalf("/console 应 302 到 /console/: %d %s", redirect.Code, redirect.Header().Get("location"))
	}

	index := httptest.NewRecorder()
	engine.ServeHTTP(index, httptest.NewRequest("GET", "/console/", nil))
	if index.Code != 200 || !strings.Contains(index.Body.String(), "console") ||
		index.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index 托管不符: %d %q", index.Code, index.Header().Get("Cache-Control"))
	}

	asset := httptest.NewRecorder()
	engine.ServeHTTP(asset, httptest.NewRequest("GET", "/console/assets/app.js", nil))
	if asset.Code != 200 || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") ||
		!strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("静态资源响应头不符: %d %v", asset.Code, asset.Header())
	}

	// SPA fallback:未知路径回落 index.html。
	fallback := httptest.NewRecorder()
	engine.ServeHTTP(fallback, httptest.NewRequest("GET", "/console/teams/42", nil))
	if fallback.Code != 200 || !strings.Contains(fallback.Body.String(), "console") {
		t.Fatalf("SPA fallback 失败: %d", fallback.Code)
	}

	// 路径穿越防护。
	traversal := httptest.NewRecorder()
	engine.ServeHTTP(traversal, httptest.NewRequest("GET", "/console/../go.mod", nil))
	if traversal.Code != 404 {
		t.Fatalf("穿越路径应 404: %d", traversal.Code)
	}
}

func TestConsoleCORS(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.PublicConsoleOrigin = "https://console.example.com"
	engine := New(f.deps)

	preflight := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/console/api/auth/me", nil)
	req.Header.Set("origin", "https://console.example.com")
	req.Header.Set("access-control-request-headers", "content-type, cookie")
	engine.ServeHTTP(preflight, req)
	if preflight.Code != 204 ||
		preflight.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" ||
		preflight.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(preflight.Header().Get("Access-Control-Allow-Headers"), "content-type") {
		t.Fatalf("CORS 预检不符: %d %v", preflight.Code, preflight.Header())
	}
	// 非 /console/api 路径不附加 CORS 头。
	other := httptest.NewRecorder()
	engine.ServeHTTP(other, httptest.NewRequest("GET", "/api/v1/client/configs", nil))
	if other.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("公开客户端路径不应带 CORS 头")
	}
}

func TestConfigKVAndPublicEnvelope(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	put := f.do("PUT", "/console/api/config", adminCookie,
		`{"help":{"feedback_url":"https://help.example.com","feedback_use_external_form":true},"forceUpdate":{"minimalVersion":" 9.9.9 "}}`)
	if put.Code != 200 {
		t.Fatalf("写配置: %d %s", put.Code, put.Body.String())
	}
	publicRec := f.do("GET", "/api/v1/client/configs", "", "")
	if publicRec.Code != 200 {
		t.Fatalf("公开配置: %d", publicRec.Code)
	}
	configs := dataOf(t, publicRec)["configs"].(map[string]any)
	help := configs["feedbackUrl"].(map[string]any)
	if help["feedback_url"] != "https://help.example.com" {
		t.Fatalf("help 透传不符: %v", help)
	}
	force := configs["forceUpdate"].(map[string]any)
	if force["minimalVersion"] != "9.9.9" {
		t.Fatalf("强制更新版本应 trim: %v", force)
	}
	consoleCfg := dataOf(t, f.do("GET", "/console/api/config", adminCookie, ""))
	if consoleCfg["help"] == nil || consoleCfg["forceUpdate"] == nil {
		t.Fatalf("控制台配置读取不符: %v", consoleCfg)
	}
}

func TestUsageSeriesBuckets(t *testing.T) {
	f := newAPIFixture(t)
	adminCookie := f.loginAdmin()
	ctx := context.Background()
	teams, _ := f.store.ListTeams(ctx)
	teamID := teams[0].Team.ID
	member, _ := f.store.FindUserByEmail(ctx, "member@mendao.chat")
	// 上海墙钟日界:15:59:59Z 属 17 日,16:00:00Z 属 18 日。
	for i, at := range []time.Time{
		time.Date(2026, 10, 17, 15, 59, 59, 0, time.UTC),
		time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC),
	} {
		if err := f.store.InsertUsage(ctx, store.UsageRecord{
			ID: uuid.NewString(), RequestID: "series-" + time.Duration(i).String(),
			UserID: member.ID, TeamID: teamID, Model: "m-s", UpstreamModel: "m-s",
			CreditsMicro: 100, Status: store.UsageStatusOk, CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.do("GET", "/console/api/usage/series?granularity=day&teamId="+teamID, adminCookie, "")
	if rec.Code != 200 {
		t.Fatalf("series: %d %s", rec.Code, rec.Body.String())
	}
	series := dataOf(t, rec)["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("应有两个日桶: %v", series)
	}
	first := series[0].(map[string]any)
	second := series[1].(map[string]any)
	if first["bucketStart"] != "2026-10-17T00:00:00" || second["bucketStart"] != "2026-10-18T00:00:00" {
		t.Fatalf("上海墙钟日桶不符: %v", series)
	}
	// 小时粒度 + 31 天上限。
	if rec := f.do("GET",
		"/console/api/usage/series?granularity=hour&from=2026-09-01T00:00:00&to=2026-10-20T00:00:00",
		adminCookie, ""); rec.Code != 400 {
		t.Fatalf("小时粒度超 31 天应 400: %d", rec.Code)
	}
}

package apiapp

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// 设备登录网页（Signal 视觉）的回归测试，验收见 specs/device-login-pages.md。

var (
	inlineScriptPattern  = regexp.MustCompile(`<script(?:\s[^>]*)?>`)
	inlineHandlerPattern = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

func allDevicePages() map[string]string {
	return map[string]string{
		"login": renderLoginPage(deviceAuthPage{}, loginPageOptions{FlowID: "flow-1", DeviceName: "MacBook Pro", TurnstileSiteKey: "1x00000000000000000000AA"}),
		"login-error": renderLoginPage(deviceAuthPage{}, loginPageOptions{
			FlowID: "flow-1", DeviceName: "MacBook Pro", Error: "邮箱或密码不正确。", Email: "a@mendao.chat",
		}),
		"authorize":  renderAuthorizePage(deviceAuthPage{}, authorizePageOptions{FlowID: "flow-1", DeviceName: "MacBook Pro", DevicePlatform: "darwin", Email: "a@mendao.chat"}),
		"authorized": renderAuthorizedPage(deviceAuthPage{}, "mendao://platform/callback?flow_id=flow-1"),
		"message":    renderMessagePage(deviceAuthPage{}, messagePageOptions{Title: "已取消", Message: "已取消本次授权，可关闭本页面。"}),
		"message-error": renderMessagePage(deviceAuthPage{}, messagePageOptions{
			Title: "需要双因素验证", Message: "请先在控制台完成登录。", Tone: "error",
			Action: &pageAction{Href: "https://console.example/console/#/login", Label: "打开控制台登录", External: true},
		}),
	}
}

func TestDevicePagesHaveNoInlineScriptOrHandlers(t *testing.T) {
	for name, html := range allDevicePages() {
		for _, tag := range inlineScriptPattern.FindAllString(html, -1) {
			if !strings.Contains(tag, ` src="`) {
				t.Fatalf("%s 页面出现内联脚本：%s", name, tag)
			}
		}
		if match := inlineHandlerPattern.FindString(html); match != "" {
			t.Fatalf("%s 页面出现 on* 事件处理器：%q", name, match)
		}
		for _, want := range []string{
			`/app/device-auth/auth.css?v=` + deviceAuthAssetVersion,
			`/app/device-auth/auth.js?v=` + deviceAuthAssetVersion,
			`class="sig-bg"`,
			`class="toasts"`,
			`id="term-title"`,
		} {
			if !strings.Contains(html, want) {
				t.Fatalf("%s 页面缺少 %q", name, want)
			}
		}
	}
}

// 授权成功页不得自动触发 deep link（会弹出浏览器原生确认框），改为 toast + 用户主动点击。
func TestAuthorizedPageUsesToastWithoutAutoRedirect(t *testing.T) {
	html := renderAuthorizedPage(deviceAuthPage{}, "mendao://platform/callback?flow_id=flow-1")
	for _, banned := range []string{"data-redirect", "redirect.js", "location.href", "http-equiv"} {
		if strings.Contains(html, banned) {
			t.Fatalf("授权成功页不应自动跳转，出现 %q", banned)
		}
	}
	for _, want := range []string{
		`href="mendao://platform/callback?flow_id=flow-1"`,
		`data-toast-title="授权成功"`,
		`data-toast-tone="ok"`,
		"返回门道",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("授权成功页缺少 %q", want)
		}
	}
}

func TestLoginPageErrorRendersInlineAndToast(t *testing.T) {
	html := renderLoginPage(deviceAuthPage{}, loginPageOptions{
		FlowID: "flow-1", DeviceName: `<img src=x onerror=1>`, Error: "邮箱或密码不正确。", Email: `a"@mendao.chat`,
	})
	for _, want := range []string{
		`<p class="err-line" role="alert">邮箱或密码不正确。</p>`,
		`data-toast-title="登录未完成"`,
		`data-toast-desc="邮箱或密码不正确。"`,
		`data-toast-tone="error"`,
		`value="a&quot;@mendao.chat"`,
		`&lt;img src=x onerror=1&gt;`,
		`required autofocus />`, // 回填邮箱后焦点在密码框
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("登录错误页缺少 %q", want)
		}
	}
	if strings.Contains(html, "<img src=x") {
		t.Fatal("设备名必须转义")
	}
	turnstile := renderLoginPage(deviceAuthPage{}, loginPageOptions{FlowID: "f", DeviceName: "d", TurnstileSiteKey: "k"})
	if !strings.Contains(turnstile, `data-theme="dark"`) {
		t.Fatal("Turnstile 应与 Signal 暗色视觉一致")
	}
}

func TestMessagePageEscapesTitleOnce(t *testing.T) {
	html := renderMessagePage(deviceAuthPage{}, messagePageOptions{Title: "A&B", Message: "m"})
	if !strings.Contains(html, "<title>A&amp;B - Mendao</title>") {
		t.Fatalf("标题应只转义一次：%s", html[:strings.Index(html, "</title>")+8])
	}
	if strings.Contains(html, "&amp;amp;") {
		t.Fatal("标题出现双重转义")
	}
}

func TestDeviceAuthAssetsServed(t *testing.T) {
	f := newAPIFixture(t)
	for name, wantType := range map[string]string{"auth.css": "text/css", "auth.js": "javascript"} {
		rec := f.do("GET", "/app/device-auth/"+name+"?v="+deviceAuthAssetVersion, "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d", name, rec.Code)
		}
		if ct := headerOf(rec, "content-type"); !strings.Contains(ct, wantType) {
			t.Fatalf("%s content-type 异常：%q", name, ct)
		}
		if cc := headerOf(rec, "cache-control"); !strings.Contains(cc, "immutable") {
			t.Fatalf("%s 应 immutable 长缓存：%q", name, cc)
		}
	}
	js := f.do("GET", "/app/device-auth/auth.js", "", "").Body.String()
	for _, banned := range []string{"alert(", "confirm(", "prompt(", "location.href"} {
		if strings.Contains(js, banned) {
			t.Fatalf("auth.js 不应使用 %q", banned)
		}
	}
	if !strings.Contains(js, `getAttribute("data-video")`) {
		t.Fatal("auth.js 应只在页面提供视频地址时加载视频，否则用程序噪声")
	}
	if !strings.Contains(js, "data-toast-title") {
		t.Fatal("auth.js 应读取 body[data-toast-*] 展示 toast")
	}

	// 有内置视频时：页面带 data-video（带内容哈希），mp4 支持 Range。
	videoURL := deviceAuthPageFor(f.deps).VideoURL
	if videoURL == "" || !strings.HasPrefix(videoURL, "/app/device-auth/signal.mp4?v=") {
		t.Fatalf("未配置 CDN 时视频应同源，实际 %q", videoURL)
	}
	if page := renderLoginPage(deviceAuthPageFor(f.deps), loginPageOptions{FlowID: "f", DeviceName: "d"}); !strings.Contains(page, `data-video="`+videoURL+`"`) {
		t.Fatal("登录页应把视频地址交给 auth.js")
	}
	rec := f.doForge("GET", videoURL, "", "", map[string]string{"range": "bytes=0-99"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("mp4 Range 请求应 206，实际 %d", rec.Code)
	}
	if cr := headerOf(rec, "content-range"); !strings.HasPrefix(cr, "bytes 0-99/") {
		t.Fatalf("content-range 异常：%q", cr)
	}
	if ct := headerOf(rec, "content-type"); ct != "video/mp4" {
		t.Fatalf("mp4 content-type 异常：%q", ct)
	}
	if rec := f.do("GET", "/app/device-auth/signal.jpg", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("不再提供 poster 图片，实际 %d", rec.Code)
	}
	if rec := f.do("GET", "/app/device-auth/../html.go", "", ""); rec.Code == http.StatusOK {
		t.Fatal("不应通过路径穿越读到其它文件")
	}
	if rec := f.do("GET", "/app/device-auth/unknown.js", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("未知资源应 404，实际 %d", rec.Code)
	}
	if rec := f.do("GET", "/app/redirect.js", "", ""); rec.Code == http.StatusOK {
		t.Fatal("旧的自动跳转脚本 /app/redirect.js 已移除")
	}
}

// 视频经媒体 CDN 缓存一年：地址走 PUBLIC_MEDIA_CDN_ORIGIN、内容哈希版本、immutable + ETag、
// CORS 供 WebGL 读帧；错误与旧版本不可缓存；CSP 只对设备登录页放行该 origin。
func TestDeviceAuthSignalVideoServedForCDN(t *testing.T) {
	f := newAPIFixture(t)
	f.deps.Env.PublicMediaCDNOrigin = "https://media.example.com/"
	asset := deviceAuthAssets[deviceAuthSignalVideo]
	want := "https://media.example.com/app/device-auth/signal.mp4?v=" + asset.version
	if got := deviceAuthPageFor(f.deps).VideoURL; got != want {
		t.Fatalf("视频地址应走媒体 CDN：%q，期望 %q", got, want)
	}

	rec := f.do("GET", "/app/device-auth/signal.mp4?v="+asset.version, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("视频应 200，实际 %d", rec.Code)
	}
	for header, wantValue := range map[string]string{
		"cache-control":                "public, max-age=31536000, s-maxage=31536000, immutable",
		"etag":                         `"` + asset.version + `"`,
		"access-control-allow-origin":  "*",
		"cross-origin-resource-policy": "cross-origin",
		"content-type":                 "video/mp4",
	} {
		if got := headerOf(rec, header); got != wantValue {
			t.Fatalf("%s = %q，期望 %q", header, got, wantValue)
		}
	}
	if rec.Body.Len() != len(asset.data) {
		t.Fatalf("视频字节数 %d，期望 %d", rec.Body.Len(), len(asset.data))
	}

	cond := f.doForge("GET", "/app/device-auth/signal.mp4?v="+asset.version, "", "", map[string]string{"if-none-match": `"` + asset.version + `"`})
	if cond.Code != http.StatusNotModified {
		t.Fatalf("条件请求应 304，实际 %d", cond.Code)
	}
	head := f.doForge("HEAD", "/app/device-auth/signal.mp4?v="+asset.version, "", "", nil)
	if head.Code != http.StatusOK || headerOf(head, "cache-control") != deviceAuthImmutableCache {
		t.Fatalf("HEAD 应 200 且同缓存头：%d %q", head.Code, headerOf(head, "cache-control"))
	}

	for _, path := range []string{"/app/device-auth/signal.mp4", "/app/device-auth/signal.mp4?v=stale"} {
		bad := f.do("GET", path, "", "")
		if bad.Code != http.StatusNotFound || headerOf(bad, "cache-control") != "no-store" {
			t.Fatalf("%s 应 404 no-store，实际 %d %q", path, bad.Code, headerOf(bad, "cache-control"))
		}
	}
	badRange := f.doForge("GET", "/app/device-auth/signal.mp4?v="+asset.version, "", "", map[string]string{"range": "bytes=999999999-"})
	if badRange.Code != http.StatusRequestedRangeNotSatisfiable || headerOf(badRange, "cache-control") != "no-store" || headerOf(badRange, "etag") != "" {
		t.Fatalf("越界 Range 应 416 no-store 且无 ETag：%d %q %q", badRange.Code, headerOf(badRange, "cache-control"), headerOf(badRange, "etag"))
	}

	page := f.do("GET", "/app/platform/login?flow_id=missing", "", "")
	if csp := headerOf(page, "content-security-policy"); !strings.Contains(csp, "media-src 'self' https://media.example.com") {
		t.Fatalf("设备登录页 CSP 应放行媒体 CDN：%q", csp)
	}
	if !strings.Contains(page.Body.String(), `data-video="`+want+`"`) {
		t.Fatal("设备登录页应引用 CDN 视频地址")
	}
	cfg := f.do("GET", "/console/api/public-config", "", "")
	if cfg.Code != http.StatusOK || !strings.Contains(cfg.Body.String(), `"signalVideoUrl":"`+want+`"`) {
		t.Fatalf("public-config 应下发控制台背景视频地址：%d %s", cfg.Code, cfg.Body.String())
	}
	if csp := headerOf(f.do("GET", "/console/", "", ""), "content-security-policy"); !strings.Contains(csp, "media-src 'self' https://media.example.com") {
		t.Fatalf("控制台页面 CSP 应放行媒体 CDN：%q", csp)
	}
	if csp := headerOf(cfg, "content-security-policy"); strings.Contains(csp, "media.example.com") {
		t.Fatalf("/console/api/ 不应放宽 media-src：%q", csp)
	}
	if csp := headerOf(f.do("GET", "/healthz", "", ""), "content-security-policy"); strings.Contains(csp, "media.example.com") {
		t.Fatalf("其它路径不应放宽 media-src：%q", csp)
	}

	for _, name := range []string{"auth.css", "auth.js"} {
		stale := f.do("GET", "/app/device-auth/"+name+"?v=stale", "", "")
		if stale.Code != http.StatusOK || strings.Contains(headerOf(stale, "cache-control"), "immutable") {
			t.Fatalf("%s 版本不符时不应长缓存：%d %q", name, stale.Code, headerOf(stale, "cache-control"))
		}
	}
}

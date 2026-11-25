package apiapp

import (
	"strings"
)

// 设备登录 / 授权网页（Signal 视觉），规范见 specs/device-login-pages.md。
//
// 纯字符串模板：所有动态值都经过 escapeHTML。页面不出现任何内联 <script> 或
// on* 事件处理器，样式与脚本由 /app/device-auth/* 同源提供，CSP 保持
// script-src 'self'——把 XSS 面从「字符串拼接是否转义正确」降低到「有没有
// 外部脚本注入」。脚本只负责视觉与 toast，关闭 JS 时表单流程照常可用。

func escapeHTML(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(value)
}

// logLine 是终端面板里的一行命令日志；Tone 取 p（信号橙）、w（亮）、g（成功）、e（错误）或空。
type logLine struct {
	Tone string
	Text string
}

// toastOptions 由服务端写入 body[data-toast-*]，auth.js 读取后展示，
// 替代浏览器原生提示；Tone 取 ok / error / info。
type toastOptions struct {
	Title string
	Desc  string
	Tone  string
}

type deviceLayoutOptions struct {
	Page  deviceAuthPage
	Title string
	// Tone 控制面板状态灯：空（进行中）、ok、error。
	Tone  string
	Log   []logLine
	Panel string
	Toast *toastOptions
}

func renderDeviceLayout(options deviceLayoutOptions) string {
	var bodyAttr strings.Builder
	if options.Toast != nil && options.Toast.Title != "" {
		bodyAttr.WriteString(` data-toast-title="` + escapeHTML(options.Toast.Title) + `"`)
		if options.Toast.Desc != "" {
			bodyAttr.WriteString(` data-toast-desc="` + escapeHTML(options.Toast.Desc) + `"`)
		}
		bodyAttr.WriteString(` data-toast-tone="` + escapeHTML(options.Toast.Tone) + `"`)
	}
	var log strings.Builder
	for i, line := range options.Log {
		if i > 0 {
			log.WriteString("\n")
		}
		class := ""
		if line.Tone != "" {
			class = ` class="` + escapeHTML(line.Tone) + `"`
		}
		log.WriteString(`<span` + class + `>` + escapeHTML(line.Text) + `</span>`)
	}
	tone := ""
	if options.Tone != "" {
		tone = ` data-tone="` + escapeHTML(options.Tone) + `"`
	}
	version := deviceAuthAssetVersion
	// 有内置视频时把（CDN）地址交给 auth.js；没有则只用程序噪声。
	signalVideoAttr := ""
	if options.Page.VideoURL != "" {
		signalVideoAttr = ` data-video="` + escapeHTML(options.Page.VideoURL) + `"`
	}
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
<meta name="robots" content="noindex" />
<meta name="color-scheme" content="dark" />
<title>` + escapeHTML(options.Title) + ` - Mendao</title>
<link rel="stylesheet" href="/app/device-auth/auth.css?v=` + version + `" />
</head>
<body` + bodyAttr.String() + `>
<div class="sig-bg" aria-hidden="true"` + signalVideoAttr + `></div>
<div class="hud" aria-hidden="true"><i class="tl"></i><i class="tr"></i><i class="bl"></i><i class="br"></i></div>
<div class="lens-tag m" aria-hidden="true">RAW · 原片</div>
<div class="toasts"></div>
<div class="ui">
  <header class="top m">
    <div class="brand"><span class="mark" aria-hidden="true"></span><b>MENDAO / 门道</b></div>
    <div class="stats" aria-hidden="true"><span>SIGNAL</span><b>DEVICE AUTH</b><span>FRAME</span><b data-frame>0000</b><span>DITHER</span><b>BAYER 8×8</b></div>
  </header>
  <section class="hero" aria-hidden="true">
    <div class="k m">AI Film Workspace</div>
    <p class="display"><span data-scr>From noise</span><span data-scr>to motion.</span></p>
    <p class="zh">从一束信号，到一部影片</p>
  </section>
  <p class="legend m" aria-hidden="true"><span><b>●</b> 移动鼠标查看原片</span><span>点击投下涟漪</span></p>
  <main class="term"` + tone + ` aria-labelledby="term-title">
    <div class="term-bar m"><span><i class="led" aria-hidden="true"></i>auth.mendao</span><span data-clock aria-hidden="true">--:--:--</span></div>
    <div class="term-body">
      <div class="log" aria-hidden="true">` + log.String() + `</div>
` + options.Panel + `
    </div>
  </main>
</div>
<script src="/app/device-auth/auth.js?v=` + version + `" defer></script>
</body>
</html>`
}

func deviceCard(name, meta string) string {
	return `<div class="device"><span class="dot" aria-hidden="true"></span><div>
        <div class="device-name">` + escapeHTML(name) + `</div>
        <div class="device-meta">` + escapeHTML(meta) + `</div>
      </div></div>`
}

type loginPageOptions struct {
	FlowID           string
	DeviceName       string
	Error            string
	Email            string
	TurnstileSiteKey string
}

func renderLoginPage(page deviceAuthPage, options loginPageOptions) string {
	errorBlock := ""
	status := logLine{Tone: "w", Text: "  status     AWAITING_LOGIN"}
	var toast *toastOptions
	if options.Error != "" {
		errorBlock = `<p class="err-line" role="alert">` + escapeHTML(options.Error) + `</p>`
		status = logLine{Tone: "e", Text: "  status     FAILED"}
		toast = &toastOptions{Title: "登录未完成", Desc: options.Error, Tone: "error"}
	}
	// 未配置 Turnstile 时页面保持原样；配置后由 Cloudflare 自动渲染并注入
	// cf-turnstile-response 隐藏字段，POST 处理按该字段校验。
	turnstileBlock := ""
	if options.TurnstileSiteKey != "" {
		turnstileBlock = `
        <div class="turnstile cf-turnstile" data-sitekey="` + escapeHTML(options.TurnstileSiteKey) + `" data-action="login" data-theme="dark" data-language="zh-cn"></div>
        <script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>`
	}
	// 回填邮箱时把焦点交给密码框，减少一次点击。
	emailFocus, passwordFocus := " autofocus", ""
	if options.Email != "" {
		emailFocus, passwordFocus = "", " autofocus"
	}
	panel := `
      <h1 id="term-title">登录以连接设备</h1>
      <p class="lead">使用门道账号登录，授权后设备将使用你的团队额度。</p>
      ` + deviceCard(options.DeviceName, "正在请求登录") + `
      ` + errorBlock + `
      <form method="post" action="/app/platform/login" data-busy="验证中…">
        <input type="hidden" name="flow_id" value="` + escapeHTML(options.FlowID) + `" />
        <div class="field">
          <label for="email">邮箱</label>
          <div class="input"><input id="email" name="email" type="email" autocomplete="username" placeholder="name@company.com" value="` + escapeHTML(options.Email) + `" required` + emailFocus + ` /></div>
        </div>
        <div class="field">
          <label for="password">密码</label>
          <div class="input"><input id="password" name="password" type="password" autocomplete="current-password" placeholder="输入密码" required` + passwordFocus + ` /><button type="button" class="reveal" data-reveal="password" aria-controls="password" aria-pressed="false" aria-label="显示或隐藏密码" hidden>显示</button></div>
        </div>` + turnstileBlock + `
        <button type="submit" class="go"><span data-label="登录并继续">登录并继续</span><span><i class="spin" aria-hidden="true"></i><kbd aria-hidden="true">⏎ ENTER</kbd></span></button>
      </form>
      <p class="hint">没有账号？请联系你的团队管理员开通。</p>`
	panelTone := ""
	if options.Error != "" {
		panelTone = "error"
	}
	return renderDeviceLayout(deviceLayoutOptions{
		Page:  page,
		Title: "登录",
		Tone:  panelTone,
		Log: []logLine{
			{Tone: "p", Text: "$ mendao auth --device"},
			{Text: "  device     " + options.DeviceName},
			status,
		},
		Panel: panel,
		Toast: toast,
	})
}

type authorizePageOptions struct {
	FlowID         string
	DeviceName     string
	DevicePlatform string
	Email          string
}

func renderAuthorizePage(page deviceAuthPage, options authorizePageOptions) string {
	meta := "请求使用你的账号登录"
	if options.DevicePlatform != "" {
		meta = options.DevicePlatform
	}
	panel := `
      <h1 id="term-title">授权设备登录</h1>
      <p class="lead">以下设备正在请求使用你的账号（<strong>` + escapeHTML(options.Email) + `</strong>）登录。</p>
      ` + deviceCard(options.DeviceName, meta) + `
      <form method="post" action="/app/platform/authorize" data-busy="授权中…">
        <input type="hidden" name="flow_id" value="` + escapeHTML(options.FlowID) + `" />
        <input type="hidden" name="decision" value="approve" />
        <button type="submit" class="go" autofocus><span data-label="授权">授权</span><span><i class="spin" aria-hidden="true"></i><kbd aria-hidden="true">⏎ ENTER</kbd></span></button>
      </form>
      <form method="post" action="/app/platform/authorize" data-busy="正在取消…">
        <input type="hidden" name="flow_id" value="` + escapeHTML(options.FlowID) + `" />
        <input type="hidden" name="decision" value="deny" />
        <button type="submit" class="ghost"><span data-label="取消">取消</span><span aria-hidden="true">→</span></button>
      </form>
      <p class="hint">授权后应用会自动完成登录。</p>`
	return renderDeviceLayout(deviceLayoutOptions{
		Page:  page,
		Title: "授权设备",
		Log: []logLine{
			{Tone: "p", Text: "$ mendao auth --device"},
			{Text: "  account    " + options.Email},
			{Tone: "w", Text: "  status     AWAITING_APPROVAL"},
		},
		Panel: panel,
	})
}

// renderAuthorizedPage 是授权成功页。
//
// 旧实现由 /app/redirect.js 在 300ms 后自动 location.href = mendao://…，浏览器会
// 弹出原生「要打开门道吗？」确认框。桌面凭据本就由 Host 轮询领取，deep link 只负责
// 唤醒/聚焦窗口，所以这里改为：页面内成功 toast + 用户主动点击「返回门道」，不再
// 自动触发协议跳转（浏览器的协议确认框无法由网页替换，只能不主动触发）。
func renderAuthorizedPage(page deviceAuthPage, deepLink string) string {
	panel := `
      <div class="status"><span class="badge" aria-hidden="true">✓</span><div><b>AUTHORIZED</b><small>设备已获得授权</small></div></div>
      <h1 id="term-title">授权成功</h1>
      <p class="lead">门道已完成登录，回到应用即可继续。本页面可以关闭。</p>
      <a class="go" href="` + escapeHTML(deepLink) + `"><span>返回门道</span><span aria-hidden="true">↗</span></a>
      <p class="hint">点击后浏览器可能会询问是否打开门道，这是浏览器的安全确认。</p>`
	return renderDeviceLayout(deviceLayoutOptions{
		Page:  page,
		Title: "授权成功",
		Tone:  "ok",
		Log: []logLine{
			{Tone: "p", Text: "$ mendao auth --device"},
			{Text: "  credentials  issued to device"},
			{Tone: "g", Text: "  status     AUTHORIZED ✓"},
		},
		Panel: panel,
		Toast: &toastOptions{Title: "授权成功", Desc: "门道已完成登录，可以回到应用继续。", Tone: "ok"},
	})
}

// pageAction 是消息页可选的操作链接。
type pageAction struct {
	Href     string
	Label    string
	External bool
}

type messagePageOptions struct {
	Title   string
	Message string
	Tone    string
	Action  *pageAction
}

func renderMessagePage(page deviceAuthPage, options messagePageOptions) string {
	isError := options.Tone == "error"
	badge, word, logTone, toastTone, panelTone := "i", "DONE", "w", "info", ""
	if isError {
		badge, word, logTone, toastTone, panelTone = "!", "FAILED", "e", "error", "error"
	}
	action := ""
	if options.Action != nil && options.Action.Href != "" {
		target := ""
		if options.Action.External {
			target = ` target="_blank" rel="noopener"`
		}
		action = `
      <a class="go" href="` + escapeHTML(options.Action.Href) + `"` + target + `><span>` + escapeHTML(options.Action.Label) + `</span><span aria-hidden="true">↗</span></a>`
	}
	panel := `
      <div class="status"><span class="badge" aria-hidden="true">` + badge + `</span><div><b>` + word + `</b><small>` + escapeHTML(options.Title) + `</small></div></div>
      <h1 id="term-title">` + escapeHTML(options.Title) + `</h1>
      <p class="lead">` + escapeHTML(options.Message) + `</p>` + action
	// 修复：旧实现先 escapeHTML(Title) 再交给会再次转义的布局，标题会出现 &amp;amp; 双重转义。
	return renderDeviceLayout(deviceLayoutOptions{
		Page:  page,
		Title: options.Title,
		Tone:  panelTone,
		Log: []logLine{
			{Tone: "p", Text: "$ mendao auth --device"},
			{Tone: logTone, Text: "  status     " + word},
		},
		Panel: panel,
		Toast: &toastOptions{Title: options.Title, Desc: options.Message, Tone: toastTone},
	})
}

package apiapp

import (
	"encoding/json"
	"strings"
)

// html.ts 的镜像:设备登录/授权页(纯字符串模板,转义规则一致)。

const pageStyle = `
  :root { color-scheme: light; }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100dvh; display: flex; align-items: center; justify-content: center;
    background: #f4f1ea; color: #1f2320;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
  }
  .card {
    width: min(92vw, 400px); background: #fffdf7; border: 1px solid #d8d2c4; border-radius: 16px;
    padding: 32px 28px; box-shadow: 0 10px 30px rgba(31, 35, 32, 0.08);
  }
  .brand { font-size: 13px; letter-spacing: 0.14em; color: #8a8578; text-transform: uppercase; margin: 0 0 12px; }
  h1 { font-size: 20px; margin: 0 0 8px; font-weight: 600; }
  p { font-size: 14px; line-height: 1.6; color: #4c4f49; margin: 0 0 16px; }
  label { display: block; font-size: 13px; color: #4c4f49; margin: 14px 0 6px; }
  input {
    width: 100%; height: 40px; padding: 0 12px; border: 1px solid #cfc9bb; border-radius: 10px;
    background: #fff; font-size: 14px;
  }
  input:focus { outline: 2px solid #4c5b4f; outline-offset: 1px; border-color: transparent; }
  button {
    width: 100%; height: 42px; margin-top: 18px; border: 1px solid transparent; border-radius: 10px;
    background: #2f3a31; color: #fff; font-size: 14px; font-weight: 500; cursor: pointer;
  }
  button:hover { background: #26302a; }
  button.secondary { background: transparent; color: #2f3a31; border-color: #cfc9bb; }
  button.secondary:hover { background: #efece2; }
  .device {
    display: flex; align-items: center; gap: 10px; border: 1px solid #e3ded1; border-radius: 12px;
    padding: 12px 14px; margin: 16px 0; background: #faf7ef;
  }
  .device .dot { width: 8px; height: 8px; border-radius: 50%; background: #7c9c6f; flex: none; }
  .device-name { font-size: 14px; font-weight: 500; }
  .device-meta { font-size: 12px; color: #8a8578; }
  .error {
    border: 1px solid #d8b7ae; background: #f8ece8; color: #8d3d28;
    border-radius: 10px; padding: 10px 12px; font-size: 13px; margin: 0 0 12px;
  }
  .hint { font-size: 12px; color: #8a8578; margin-top: 14px; }
  .turnstile { display: flex; justify-content: center; min-height: 65px; margin: 14px 0 0; }
  a { color: #3d5a43; }
`

func escapeHTML(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(value)
}

func htmlLayout(title, body, script string) string {
	scriptTag := ""
	if script != "" {
		scriptTag = "<script>" + script + "</script>"
	}
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<meta name="robots" content="noindex" />
<title>` + escapeHTML(title) + `</title>
<style>` + pageStyle + `</style>
</head>
<body>
<main class="card">
` + body + `
</main>
` + scriptTag + `
</body>
</html>`
}

type loginPageOptions struct {
	FlowID           string
	DeviceName       string
	Error            string
	Email            string
	TurnstileSiteKey string
}

func renderLoginPage(options loginPageOptions) string {
	errorBlock := ""
	if options.Error != "" {
		errorBlock = `<div class="error">` + escapeHTML(options.Error) + `</div>`
	}
	// 未配置 Turnstile 时页面保持原样；配置后由 Cloudflare 自动渲染并注入
	// cf-turnstile-response 隐藏字段，POST 处理按该字段校验。
	turnstileBlock := ""
	if options.TurnstileSiteKey != "" {
		turnstileBlock = `
<div class="turnstile cf-turnstile" data-sitekey="` + escapeHTML(options.TurnstileSiteKey) + `" data-action="login" data-theme="light" data-language="zh-cn"></div>
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>`
	}
	body := `
<p class="brand">Mendao</p>
<h1>登录以连接设备</h1>
<p>请输入 Mendao 账号，授权后设备将使用你的团队额度。</p>
<div class="device"><span class="dot"></span><div>
  <div class="device-name">` + escapeHTML(options.DeviceName) + `</div>
  <div class="device-meta">正在请求登录</div>
</div></div>
` + errorBlock + `
<form method="post" action="/app/platform/login">
  <input type="hidden" name="flow_id" value="` + escapeHTML(options.FlowID) + `" />
  <label for="email">邮箱</label>
  <input id="email" name="email" type="email" autocomplete="username" value="` + escapeHTML(options.Email) + `" required />
  <label for="password">密码</label>
  <input id="password" name="password" type="password" autocomplete="current-password" required />
  ` + turnstileBlock + `
  <button type="submit">登录并继续</button>
</form>
<p class="hint">没有账号？请联系你的团队管理员开通。</p>`
	return htmlLayout("登录 - Mendao", body, "")
}

type authorizePageOptions struct {
	FlowID         string
	DeviceName     string
	DevicePlatform string
	Email          string
}

func renderAuthorizePage(options authorizePageOptions) string {
	platformLine := ""
	if options.DevicePlatform != "" {
		platformLine = `<div class="device-meta">` + escapeHTML(options.DevicePlatform) + `</div>`
	}
	body := `
<p class="brand">Mendao</p>
<h1>授权设备登录</h1>
<p>以下设备正在请求使用你的账号（<strong>` + escapeHTML(options.Email) + `</strong>）登录。</p>
<div class="device"><span class="dot"></span><div>
  <div class="device-name">` + escapeHTML(options.DeviceName) + `</div>
  ` + platformLine + `
</div></div>
<form method="post" action="/app/platform/authorize">
  <input type="hidden" name="flow_id" value="` + escapeHTML(options.FlowID) + `" />
  <input type="hidden" name="decision" value="approve" />
  <button type="submit">授权</button>
</form>
<form method="post" action="/app/platform/authorize">
  <input type="hidden" name="flow_id" value="` + escapeHTML(options.FlowID) + `" />
  <input type="hidden" name="decision" value="deny" />
  <button type="submit" class="secondary">取消</button>
</form>
<p class="hint">授权后应用会自动完成登录，可关闭本页面。</p>`
	return htmlLayout("授权设备 - Mendao", body, "")
}

func renderAuthorizedPage(flowID, deepLink string) string {
	quoted := jsStringLiteral(deepLink)
	body := `
<p class="brand">Mendao</p>
<h1>授权成功</h1>
<p>正在返回门道。如果没有自动跳转，请点击下方按钮。</p>
<button type="button" onclick="window.location.href=` + quoted + `">返回门道</button>
<p class="hint">本页面可以关闭。</p>`
	script := `setTimeout(function () { window.location.href = ` + quoted + `; }, 300);`
	return htmlLayout("授权成功 - Mendao", body, script)
}

// jsStringLiteral:等价 JSON.stringify 的 JS 字符串字面量(含引号)。
func jsStringLiteral(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

type messagePageOptions struct {
	Title   string
	Message string
	Tone    string
}

func renderMessagePage(options messagePageOptions) string {
	body := `
<p class="brand">Mendao</p>
<h1>` + escapeHTML(options.Title) + `</h1>
<p>` + escapeHTML(options.Message) + `</p>`
	return htmlLayout(escapeHTML(options.Title)+" - Mendao", body, "")
}

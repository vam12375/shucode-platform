const PAGE_STYLE = `
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
  a { color: #3d5a43; }
`;

export function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function layout(title: string, body: string, script?: string): string {
  return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<meta name="robots" content="noindex" />
<title>${escapeHtml(title)}</title>
<style>${PAGE_STYLE}</style>
</head>
<body>
<main class="card">
${body}
</main>
${script ? `<script>${script}</script>` : ""}
</body>
</html>`;
}

export function renderLoginPage(options: {
  flowId: string;
  deviceName: string;
  error?: string | null;
  email?: string;
}): string {
  const body = `
<p class="brand">Mendao</p>
<h1>登录以连接设备</h1>
<p>请输入 Mendao 账号，授权后设备将使用你的团队额度。</p>
<div class="device"><span class="dot"></span><div>
  <div class="device-name">${escapeHtml(options.deviceName)}</div>
  <div class="device-meta">正在请求登录</div>
</div></div>
${options.error ? `<div class="error">${escapeHtml(options.error)}</div>` : ""}
<form method="post" action="/app/platform/login">
  <input type="hidden" name="flow_id" value="${escapeHtml(options.flowId)}" />
  <label for="email">邮箱</label>
  <input id="email" name="email" type="email" autocomplete="username" value="${escapeHtml(options.email ?? "")}" required />
  <label for="password">密码</label>
  <input id="password" name="password" type="password" autocomplete="current-password" required />
  <button type="submit">登录并继续</button>
</form>
<p class="hint">没有账号？请联系你的团队管理员开通。</p>`;
  return layout("登录 - Mendao", body);
}

export function renderAuthorizePage(options: {
  flowId: string;
  deviceName: string;
  devicePlatform: string | null;
  email: string;
}): string {
  const body = `
<p class="brand">Mendao</p>
<h1>授权设备登录</h1>
<p>以下设备正在请求使用你的账号（<strong>${escapeHtml(options.email)}</strong>）登录。</p>
<div class="device"><span class="dot"></span><div>
  <div class="device-name">${escapeHtml(options.deviceName)}</div>
  ${options.devicePlatform ? `<div class="device-meta">${escapeHtml(options.devicePlatform)}</div>` : ""}
</div></div>
<form method="post" action="/app/platform/authorize">
  <input type="hidden" name="flow_id" value="${escapeHtml(options.flowId)}" />
  <input type="hidden" name="decision" value="approve" />
  <button type="submit">授权</button>
</form>
<form method="post" action="/app/platform/authorize">
  <input type="hidden" name="flow_id" value="${escapeHtml(options.flowId)}" />
  <input type="hidden" name="decision" value="deny" />
  <button type="submit" class="secondary">取消</button>
</form>
<p class="hint">授权后应用会自动完成登录，可关闭本页面。</p>`;
  return layout("授权设备 - Mendao", body);
}

export function renderAuthorizedPage(options: { flowId: string; deepLink: string }): string {
  const body = `
<p class="brand">Mendao</p>
<h1>授权成功</h1>
<p>正在返回应用。如果没有自动跳转，请点击下方按钮。</p>
<button type="button" onclick="window.location.href=${JSON.stringify(options.deepLink)}">打开 Shucode</button>
<p class="hint">本页面可以关闭。</p>`;
  const script = `setTimeout(function () { window.location.href = ${JSON.stringify(options.deepLink)}; }, 300);`;
  return layout("授权成功 - Mendao", body, script);
}

export function renderMessagePage(options: {
  title: string;
  message: string;
  tone?: "info" | "error";
}): string {
  const body = `
<p class="brand">Mendao</p>
<h1>${escapeHtml(options.title)}</h1>
<p>${escapeHtml(options.message)}</p>`;
  return layout(`${options.title} - Mendao`, body);
}

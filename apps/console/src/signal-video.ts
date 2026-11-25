// 控制台认证页背景视频地址校验（specs/device-login-pages.md）。
// 只接受同源绝对路径或 http(s) 绝对地址；其它值（协议相对、javascript:、含空白或引号）按无视频处理。
export function normalizeSignalVideoUrl(value: unknown): string {
  if (typeof value !== "string") return "";
  const url = value.trim();
  if (url.startsWith("/") && !url.startsWith("//") && !/[\s"'<>]/.test(url)) return url;
  return /^https?:\/\/[^\s"'<>]+$/i.test(url) ? url : "";
}

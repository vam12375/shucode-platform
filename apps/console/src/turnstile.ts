/** Cloudflare Turnstile 的登录动作；服务端会校验 siteverify 返回的 action。 */
export const TURNSTILE_LOGIN_ACTION = "login";

/** 公开配置里的 site key 可能是任意 JSON 值，统一收敛成可渲染的字符串。 */
export function normalizeTurnstileSiteKey(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

export function turnstileRequired(siteKey: string): boolean {
  return normalizeTurnstileSiteKey(siteKey) !== "";
}

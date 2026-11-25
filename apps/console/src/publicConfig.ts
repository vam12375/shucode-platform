import { api } from "./api.js";

// 匿名公开配置：/console/api/public-config 有匿名限速，登录页与认证外壳共用这一次请求。
export interface PublicConfig {
  turnstileSiteKey?: unknown;
  turnstileEnabled?: unknown;
  signalVideoUrl?: unknown;
}

let pending: Promise<PublicConfig | null> | null = null;

// 单次记忆：同一页面生命周期只请求一次；失败时也记住结果，避免认证页反复触发限速。
// 调用方必须容忍 null（按未启用 Turnstile、无视频渲染；服务端仍是最终校验方）。
export function loadPublicConfig(): Promise<PublicConfig | null> {
  pending ??= api<PublicConfig>("/public-config").catch(() => null);
  return pending;
}

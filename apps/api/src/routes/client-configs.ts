import {
  buildClientConfigsEnvelope,
  PLATFORM_CONFIG_KEYS,
  type HelpConfig,
} from "@shucode-platform/contracts/client-configs";
import { buildBuiltinReleaseDownloadUrl } from "@shucode-platform/contracts/release";
import type { Context, Hono } from "hono";
import type { AppContext } from "../context.js";

function readForceUpdateMinimalVersion(value: unknown): string | null {
  if (!value || typeof value !== "object") return null;
  const minimalVersion = (value as { minimalVersion?: unknown }).minimalVersion;
  return typeof minimalVersion === "string" && minimalVersion.trim() ? minimalVersion.trim() : null;
}

/**
 * /api/v1/client/configs 与 /n/configs：匿名公开配置。
 * 客户端把该响应同时用于帮助配置、插件商店排序、强制更新与内置配置下载入口，
 * 因此必须与客户端各自的解析器字段保持一致（见 contracts/client-configs.ts）。
 */
export function registerClientConfigRoutes(app: Hono, context: AppContext): void {
  const handler = async (c: Context) => {
    const [help, forceUpdate, pluginStoreOrder, activeRelease] = await Promise.all([
      context.store.getConfigValue(PLATFORM_CONFIG_KEYS.help),
      context.store.getConfigValue(PLATFORM_CONFIG_KEYS.forceUpdate),
      context.store.getConfigValue(PLATFORM_CONFIG_KEYS.pluginStoreOrder),
      context.store.getActiveRelease(),
    ]);
    // 客户端 download 边界只接受 https 绝对地址；非 https（本地开发）时不下发，
    // 客户端会回落到打包内置配置，行为安全。
    const builtinReleaseUrl =
      activeRelease && context.env.releaseBaseUrl.startsWith("https://")
        ? buildBuiltinReleaseDownloadUrl(context.env.releaseBaseUrl, activeRelease.revision)
        : null;
    const envelope = buildClientConfigsEnvelope({
      help: (help as HelpConfig | null) ?? undefined,
      builtinReleaseUrl,
      forceUpdateMinimalVersion: readForceUpdateMinimalVersion(forceUpdate),
      ...(pluginStoreOrder !== null ? { pluginStoreOrder } : {}),
    });
    c.header("Cache-Control", "no-cache");
    return c.json(envelope);
  };
  app.get("/api/v1/client/configs", handler);
  app.get("/n/configs", handler);
}

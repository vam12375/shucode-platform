import type { PlatformStore } from "@shucode-platform/db";
import type { SecretBox } from "@shucode-platform/db";
import type { PlatformEnv } from "./env.js";
import type { ConfigEventBus } from "./services/config-events.js";

export type Logger = {
  info: (message: string, detail?: unknown) => void;
  warn: (message: string, detail?: unknown) => void;
  error: (message: string, detail?: unknown) => void;
};

export function createConsoleLogger(scope: string): Logger {
  const prefix = `[${scope}]`;
  return {
    info: (message, detail) => console.log(prefix, message, detail ?? ""),
    warn: (message, detail) => console.warn(prefix, message, detail ?? ""),
    error: (message, detail) => console.error(prefix, message, detail ?? ""),
  };
}

export interface AppContext {
  store: PlatformStore;
  env: PlatformEnv;
  now: () => number;
  /** null 时无法加密/恢复设备 token：内存体验模式且未配置密钥。 */
  secretBox: SecretBox | null;
  /** 进程内配置变更事件总线：SSE 推送的来源（详见 services/config-events.ts）。 */
  configEvents: ConfigEventBus;
  logger: Logger;
}

export function isoNow(context: AppContext): string {
  return new Date(context.now()).toISOString();
}

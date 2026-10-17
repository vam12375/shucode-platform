/**
 * 配置变更事件总线（进程内）：管理端的模型/供应商/团队/设备/平台配置/Release
 * 变更经 emit(domain) 进入总线；订阅者是客户端 SSE 长连接（routes/client-events.ts）。
 *
 * 合并语义：300ms 去抖窗口内的多次 emit 合并为一次回调，domains 取并集——
 * 批量导入 500 个模型也只向每个客户端推一条 config.changed。
 * 单实例进程内实现；多实例部署时换共享存储/发布订阅即可，端口语义不变。
 */

export type ConfigEventDomain =
  | "models"
  | "providers"
  | "teams"
  | "devices"
  | "platform-config"
  | "releases";

export interface ConfigEventBus {
  emit(domain: ConfigEventDomain): void;
  subscribe(listener: (domains: Set<ConfigEventDomain>) => void): () => void;
}

/** 去抖合并窗口：管理端连续保存（或批量导入）合并为一次推送。 */
const COALESCE_WINDOW_MS = 300;

export function createConfigEventBus(): ConfigEventBus {
  const listeners = new Set<(domains: Set<ConfigEventDomain>) => void>();
  let pending: Set<ConfigEventDomain> | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  return {
    emit(domain) {
      (pending ??= new Set()).add(domain);
      if (timer) return;
      timer = setTimeout(() => {
        timer = null;
        const domains = pending ?? new Set<ConfigEventDomain>();
        pending = null;
        for (const listener of listeners) listener(domains);
      }, COALESCE_WINDOW_MS);
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

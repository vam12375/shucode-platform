import type { Hono } from "hono";
import { streamSSE } from "hono/streaming";
import type { AppContext } from "../context.js";
import { authenticateDevice } from "../services/sessions.js";
import type { ConfigEventDomain } from "../services/config-events.js";

/**
 * GET /api/v1/client/events（Bearer 平台设备凭据，SSE 长连接）。
 *
 * 配置变更实时通知：管理端改动（模型/供应商/团队/设备/平台配置/Release）经
 * 事件总线推送 `config.changed` 事件；客户端收到后触发既有的 user-config
 * 同步（ETag 匹配则 304，成本极低）。
 *
 * 语义：
 * - 初始帧 `{ type: "hello" }` + `retry: 5000`（断线重连建议间隔）；
 * - 事件帧 `event: config.changed`，data 形如
 *   `{"type":"config.changed","domains":["models","providers"]}`（去抖合并）；
 * - 静默期每 ~25s 发 `event: ping` 空数据帧保活（穿透反代空闲断连）；
 * - 设备凭据吊销/禁用：新建连接立即 401；存活连接由事件触发重拉 user-config
 *   时 401，客户端按既有「401 清凭据」语义处理。
 */
export const CLIENT_EVENTS_API_PATH = "/api/v1/client/events";
const HEARTBEAT_MS = 25_000;
/** 唤醒轮询间隔：事件到达的兜底延迟上限（亚秒级，代价可忽略）。 */
const TICK_MS = 1000;

export function registerClientEventRoutes(app: Hono, context: AppContext): void {
  app.get(CLIENT_EVENTS_API_PATH, async (c) => {
    const auth = await authenticateDevice(context, c);
    if (!auth) {
      return c.json({ code: 401, msg: "凭据无效或已吊销" }, 401);
    }

    return streamSSE(c, async (stream) => {
      let pendingDomains: ConfigEventDomain[] = [];
      let aborted = false;
      let wake: (() => void) | null = null;
      const notify = (): void => {
        wake?.();
        wake = null;
      };
      const unsubscribe = context.configEvents.subscribe((domains) => {
        pendingDomains = [...new Set([...pendingDomains, ...domains])];
        notify();
      });
      stream.onAbort(() => {
        aborted = true;
        unsubscribe();
        notify();
      });

      await stream.writeSSE({
        retry: 5000,
        data: JSON.stringify({ type: "hello" }),
      });

      let lastPing = Date.now();
      while (!aborted) {
        await Promise.race([
          new Promise<void>((resolve) => {
            wake = resolve;
          }),
          new Promise<void>((resolve) => setTimeout(resolve, TICK_MS)),
        ]);
        if (aborted) break;
        if (pendingDomains.length > 0) {
          const domains = [...new Set(pendingDomains)];
          pendingDomains = [];
          try {
            await stream.writeSSE({
              event: "config.changed",
              data: JSON.stringify({ type: "config.changed", domains }),
            });
          } catch {
            break;
          }
        } else if (Date.now() - lastPing >= HEARTBEAT_MS) {
          try {
            await stream.writeSSE({ event: "ping", data: "" });
          } catch {
            break;
          }
          lastPing = Date.now();
        }
      }
      unsubscribe();
    });
  });
}

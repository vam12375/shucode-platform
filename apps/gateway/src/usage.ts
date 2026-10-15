import type { UsageTokenCounts } from "@shucode-platform/contracts/credits";

/**
 * Anthropic messages 协议的 usage 采集。
 *
 * 非流式：响应体 `usage.input_tokens / output_tokens / cache_*`。
 * 流式：`message_start.message.usage` 提供输入侧（含缓存），
 *       `message_delta.usage.output_tokens` 为累计输出（取最后一次）。
 */
export function parseUsageFromAnthropicJson(body: unknown): UsageTokenCounts | null {
  if (!body || typeof body !== "object") return null;
  const usage = (body as { usage?: unknown }).usage;
  if (!usage || typeof usage !== "object") return null;
  return countsFromUsageRecord(usage as Record<string, unknown>);
}

function readCount(record: Record<string, unknown>, key: string): number {
  const value = record[key];
  const numeric = typeof value === "number" ? value : Number(value);
  return Number.isFinite(numeric) && numeric > 0 ? Math.floor(numeric) : 0;
}

function countsFromUsageRecord(
  record: Record<string, unknown>,
): UsageTokenCounts | null {
  const anyKnownKey =
    "input_tokens" in record ||
    "output_tokens" in record ||
    "cache_read_input_tokens" in record ||
    "cache_creation_input_tokens" in record;
  if (!anyKnownKey) return null;
  return {
    inputTokens: readCount(record, "input_tokens"),
    outputTokens: readCount(record, "output_tokens"),
    cacheReadTokens: readCount(record, "cache_read_input_tokens"),
    cacheCreationTokens: readCount(record, "cache_creation_input_tokens"),
  };
}

export interface AnthropicSseUsageScanner {
  push(chunk: string): void;
  usage(): UsageTokenCounts;
}

interface MutableUsageCounts {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
}

function createAccumulator() {
  const usage: MutableUsageCounts = {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
  };
  return {
    observe(message: unknown): void {
      if (!message || typeof message !== "object") return;
      const record = message as Record<string, unknown>;
      if (record.type === "message_start") {
        const startUsage = (record.message as { usage?: unknown } | undefined)?.usage;
        const counts = startUsage
          ? countsFromUsageRecord(startUsage as Record<string, unknown>)
          : null;
        if (counts) {
          usage.inputTokens = counts.inputTokens;
          usage.cacheReadTokens = counts.cacheReadTokens;
          usage.cacheCreationTokens = counts.cacheCreationTokens;
          usage.outputTokens = counts.outputTokens;
        }
        return;
      }
      if (record.type === "message_delta") {
        const deltaUsage = record.usage;
        const counts = deltaUsage
          ? countsFromUsageRecord(deltaUsage as Record<string, unknown>)
          : null;
        if (counts) {
          // message_delta 的 output_tokens 是累计值，直接覆盖。
          usage.outputTokens = counts.outputTokens;
          if (counts.inputTokens > 0) usage.inputTokens = counts.inputTokens;
          if (counts.cacheReadTokens > 0) usage.cacheReadTokens = counts.cacheReadTokens;
          if (counts.cacheCreationTokens > 0) {
            usage.cacheCreationTokens = counts.cacheCreationTokens;
          }
        }
      }
    },
    usage: (): UsageTokenCounts => ({ ...usage }),
  };
}

export function createAnthropicSseUsageScanner(): AnthropicSseUsageScanner {
  const accumulator = createAccumulator();
  let buffer = "";
  return {
    push(chunk: string): void {
      buffer += chunk;
      for (;;) {
        const match = /\r?\n\r?\n/u.exec(buffer);
        if (!match) break;
        const rawEvent = buffer.slice(0, match.index);
        buffer = buffer.slice(match.index + match[0].length);
        handleRawEvent(rawEvent, accumulator.observe);
      }
    },
    usage: accumulator.usage,
  };
}

function handleRawEvent(rawEvent: string, observe: (message: unknown) => void): void {
  let eventName = "";
  const dataLines: string[] = [];
  for (const line of rawEvent.split(/\r?\n/u)) {
    if (line.startsWith("event:")) {
      eventName = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trimStart());
    }
  }
  const dataText = dataLines.join("\n");
  if (!dataText || dataText === "[DONE]") return;
  let parsed: unknown;
  try {
    parsed = JSON.parse(dataText);
  } catch {
    return;
  }
  if (eventName && parsed && typeof parsed === "object" && !("type" in parsed)) {
    (parsed as Record<string, unknown>).type = eventName;
  }
  observe(parsed);
}

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
  /** 异常丢弃计数（buffer 超限 / 无法解析的帧），仅用于观测日志。 */
  dropped(): number;
}

/** 单个未完结 SSE 事件的缓冲上限：SSE usage 帧至多几 KB，超限必是异常上游。 */
const MAX_SSE_BUFFER_CHARS = 1_000_000;

/**
 * OpenAI chat usage 解析。
 *
 * 口径对齐计费公式：prompt_tokens 是「含缓存」的总量，fresh input = prompt - cached，
 * cacheRead 单独按缓存价计。兼容两类字段：
 * - OpenAI：usage.prompt_tokens_details.cached_tokens
 * - DeepSeek：usage.prompt_cache_hit_tokens
 */
export function parseUsageFromOpenAiJson(body: unknown): UsageTokenCounts | null {
  if (!body || typeof body !== "object") return null;
  const usage = (body as { usage?: unknown }).usage;
  if (!usage || typeof usage !== "object") return null;
  return countsFromOpenAiUsage(usage as Record<string, unknown>);
}

function countsFromOpenAiUsage(usage: Record<string, unknown>): UsageTokenCounts {
  const prompt = readCount(usage, "prompt_tokens");
  const completion = readCount(usage, "completion_tokens");
  const details = usage.prompt_tokens_details;
  const cached =
    (details && typeof details === "object" ? readCount(details as Record<string, unknown>, "cached_tokens") : 0) ||
    readCount(usage, "prompt_cache_hit_tokens");
  return {
    inputTokens: Math.max(0, prompt - cached),
    outputTokens: completion,
    cacheReadTokens: cached,
    cacheCreationTokens: 0,
  };
}

export interface OpenAiSseUsageScanner {
  push(chunk: string): void;
  usage(): UsageTokenCounts;
  /** 异常丢弃计数（buffer 超限 / 无法解析的帧），仅用于观测日志。 */
  dropped(): number;
}

/**
 * OpenAI 流式 usage 采集：usage 只出现在某个 chunk（通常是最后一个，
 * 需要上游支持 stream_options.include_usage——网关会在转发前注入）。
 * 取最后看到的 usage 值。
 */
/**
 * 公共 SSE 事件切分：按空行切事件帧，统一 buffer 上限与溢出丢弃计数；
 * 各协议扫描器只负责自己如何解析收到的事件帧。
 */
function createSseEventSplitter(
  onRawEvent: (rawEvent: string) => void,
): { push(chunk: string): void; overflowed(): number } {
  let buffer = "";
  let overflowed = 0;
  return {
    push(chunk: string): void {
      buffer += chunk;
      // 异常上游可能永不产出空行分隔的事件：超限即丢弃存量并计数，防止内存失控。
      if (buffer.length > MAX_SSE_BUFFER_CHARS) {
        overflowed += 1;
        buffer = "";
      }
      for (;;) {
        const match = /\r?\n\r?\n/u.exec(buffer);
        if (!match) break;
        const rawEvent = buffer.slice(0, match.index);
        buffer = buffer.slice(match.index + match[0].length);
        onRawEvent(rawEvent);
      }
    },
    overflowed: (): number => overflowed,
  };
}

export function createOpenAiSseUsageScanner(): OpenAiSseUsageScanner {
  const usage: MutableUsageCounts = {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
  };
  let malformed = 0;
  const splitter = createSseEventSplitter((rawEvent) => {
    for (const line of rawEvent.split(/\r?\n/u)) {
      if (!line.startsWith("data:")) continue;
      const dataText = line.slice(5).trim();
      if (!dataText || dataText === "[DONE]") continue;
      try {
        const parsed = JSON.parse(dataText) as unknown;
        if (parsed && typeof parsed === "object" && "usage" in parsed) {
          const parsedUsage = (parsed as { usage: unknown }).usage;
          if (parsedUsage && typeof parsedUsage === "object") {
            const counts = countsFromOpenAiUsage(parsedUsage as Record<string, unknown>);
            // usage 是绝对值：直接覆盖（最后一条为准）。
            usage.inputTokens = Math.max(0, counts.inputTokens);
            usage.outputTokens = counts.outputTokens;
            usage.cacheReadTokens = counts.cacheReadTokens;
            usage.cacheCreationTokens = 0;
          }
        }
      } catch {
        // 非 JSON 帧计数后丢弃：dropped() 汇总告警，不静默。
        malformed += 1;
      }
    }
  });
  return {
    push: (chunk) => splitter.push(chunk),
    usage: (): UsageTokenCounts => ({ ...usage }),
    dropped: (): number => malformed + splitter.overflowed(),
  };
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
  let malformed = 0;
  const splitter = createSseEventSplitter((rawEvent) => {
    handleRawEvent(rawEvent, accumulator.observe, () => {
      malformed += 1;
    });
  });
  return {
    push: (chunk) => splitter.push(chunk),
    usage: accumulator.usage,
    dropped: (): number => malformed + splitter.overflowed(),
  };
}

function handleRawEvent(
  rawEvent: string,
  observe: (message: unknown) => void,
  onMalformed: () => void,
): void {
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
    onMalformed();
    return;
  }
  if (eventName && parsed && typeof parsed === "object" && !("type" in parsed)) {
    (parsed as Record<string, unknown>).type = eventName;
  }
  observe(parsed);
}

/**
 * OpenAI Responses API 的 usage 解析（口径对齐计费公式：input_tokens 为含缓存总量，
 * fresh input = input − cached，缓存读按缓存价计）。
 *
 * 载荷两种形态：非流式的响应体即 response 对象（`usage.input_tokens / output_tokens`，
 * `input_tokens_details.cached_tokens` 为缓存读）；流式 terminal 事件
 * `response.completed` / `response.incomplete` 的 `response.usage` 携带累计值。
 * 两种都归一为「取 usage 对象」再解析。
 */
export function parseUsageFromResponsesJson(body: unknown): UsageTokenCounts | null {
  if (!body || typeof body !== "object") return null;
  const record = body as Record<string, unknown>;
  const response = record.response;
  const usage =
    record.usage ??
    (response && typeof response === "object"
      ? (response as Record<string, unknown>).usage
      : undefined);
  if (!usage || typeof usage !== "object") return null;
  // 早期事件的 usage 可能没有完整字段。
  return countsFromResponsesUsage(usage as Record<string, unknown>);
}

function countsFromResponsesUsage(usage: Record<string, unknown>): UsageTokenCounts | null {
  if (!("input_tokens" in usage) && !("output_tokens" in usage)) return null;
  const input = readCount(usage, "input_tokens");
  const output = readCount(usage, "output_tokens");
  const details = usage.input_tokens_details;
  const cached =
    details && typeof details === "object"
      ? readCount(details as Record<string, unknown>, "cached_tokens")
      : 0;
  return {
    inputTokens: Math.max(0, input - cached),
    outputTokens: output,
    cacheReadTokens: cached,
    cacheCreationTokens: 0,
  };
}

export interface ResponsesSseUsageScanner {
  push(chunk: string): void;
  usage(): UsageTokenCounts;
  /** 异常丢弃计数（buffer 超限 / 无法解析的帧），仅用于观测日志。 */
  dropped(): number;
}

/**
 * Responses 流式 usage 采集：usage 随 terminal 事件自动下发，
 * 不需要（也不支持）像 chat 那样注入 stream_options.include_usage。
 */
export function createResponsesSseUsageScanner(): ResponsesSseUsageScanner {
  const usage: MutableUsageCounts = {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
  };
  let malformed = 0;
  const splitter = createSseEventSplitter((rawEvent) => {
    for (const line of rawEvent.split(/\r?\n/u)) {
      if (!line.startsWith("data:")) continue;
      const dataText = line.slice(5).trim();
      if (!dataText || dataText === "[DONE]") continue;
      try {
        const parsed = JSON.parse(dataText) as unknown;
        const counts = parseUsageFromResponsesJson(parsed);
        if (counts) {
          // response.usage 是绝对值：直接覆盖（最后一条为准）。
          usage.inputTokens = counts.inputTokens;
          usage.outputTokens = counts.outputTokens;
          usage.cacheReadTokens = counts.cacheReadTokens;
          usage.cacheCreationTokens = counts.cacheCreationTokens;
        }
      } catch {
        malformed += 1;
      }
    }
  });
  return {
    push: (chunk) => splitter.push(chunk),
    usage: (): UsageTokenCounts => ({ ...usage }),
    dropped: (): number => malformed + splitter.overflowed(),
  };
}

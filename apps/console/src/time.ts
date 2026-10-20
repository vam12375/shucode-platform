/**
 * 控制台时间统一按上海时区（UTC+8）处理。
 *
 * 根因：存储层（Postgres 经 toISOString、内存库同口径）下发的都是带 Z 的 UTC
 * ISO 串，页面此前直接 `replace("T").slice()` 展示，把 UTC 当成了墙钟时间。
 * 上海无夏令时，固定 +8，用「即时点 + 偏移后读 UTC 分量」换算，不依赖
 * Intl 与浏览器本地时区；日界（"今日"、近 14 天桶）也按上海墙钟取。
 *
 * 注意：后端对 usage 的 from/to 无时区串按 UTC 解析，而 series 分桶按上海
 * 墙钟切分。因此查询边界必须用 shanghaiDayStartUtc/shanghaiDayEndUtc 转成
 * UTC 即时点，不能把上海日期串直接下传。
 */

const SHANGHAI_OFFSET_MS = 8 * 60 * 60 * 1000;

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

/** UTC ISO 串 → 上海墙钟的时间戳；解析失败原样返回。 */
export function formatShanghaiTime(iso: string, withMs = false): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  const shifted = new Date(date.getTime() + SHANGHAI_OFFSET_MS);
  const base = `${shifted.getUTCFullYear()}-${pad2(shifted.getUTCMonth() + 1)}-${pad2(shifted.getUTCDate())} ` +
    `${pad2(shifted.getUTCHours())}:${pad2(shifted.getUTCMinutes())}:${pad2(shifted.getUTCSeconds())}`;
  return withMs ? `${base}.${String(date.getUTCMilliseconds()).padStart(3, "0")}` : base;
}

/** UTC ISO 串 → 上海墙钟的日期串 "YYYY-MM-DD"。 */
export function formatShanghaiDate(iso: string): string {
  return formatShanghaiTime(iso).slice(0, 10);
}

/** 给定即时点在上海墙钟下的日期串（用于"今日"与近 N 天桶的边界键）。 */
export function shanghaiDateOf(date: Date): string {
  return formatShanghaiDate(date.toISOString());
}

/**
 * 上海自然日 "YYYY-MM-DD" → 当日 00:00:00 对应的 UTC naive 串。
 * 服务端把无时区串按 UTC 解析，所以上海 10-19 00:00 要传 10-18T16:00:00。
 */
export function shanghaiDayStartUtc(date: string): string {
  const parsed = parseDateParts(date);
  if (!parsed) return "";
  const utc =
    Date.UTC(parsed.year, parsed.month - 1, parsed.day) - SHANGHAI_OFFSET_MS;
  return new Date(utc).toISOString().slice(0, 19);
}

/** 上海自然日 "YYYY-MM-DD" → 当日 23:59:59.999 对应的 UTC naive 串。 */
export function shanghaiDayEndUtc(date: string): string {
  const parsed = parseDateParts(date);
  if (!parsed) return "";
  const utc =
    Date.UTC(parsed.year, parsed.month - 1, parsed.day, 23, 59, 59, 999) -
    SHANGHAI_OFFSET_MS;
  return new Date(utc).toISOString().slice(0, 23);
}

function parseDateParts(
  date: string,
): { year: number; month: number; day: number } | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!match) return null;
  return {
    year: Number(match[1]),
    month: Number(match[2]),
    day: Number(match[3]),
  };
}

/** 以上海墙钟为准的年月偏移：monthOffset=0 本月、-1 上月。返回该月 1 号到月末（YYYY-MM-DD）。 */
export function shanghaiMonthRange(date: Date, monthOffset = 0): { from: string; to: string } {
  const shifted = new Date(date.getTime() + SHANGHAI_OFFSET_MS);
  const year = shifted.getUTCFullYear();
  const month = shifted.getUTCMonth() + monthOffset;
  const anchorYear = year + Math.floor(month / 12);
  const anchorMonth = ((month % 12) + 12) % 12;
  const pad = (n: number) => String(n).padStart(2, "0");
  const lastDay = new Date(Date.UTC(anchorYear, anchorMonth + 1, 0)).getUTCDate();
  return {
    from: `${anchorYear}-${pad(anchorMonth + 1)}-01`,
    to: `${anchorYear}-${pad(anchorMonth + 1)}-${pad(lastDay)}`,
  };
}

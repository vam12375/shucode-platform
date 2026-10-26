export type ChartPreset =
  | "today"
  | "yesterday"
  | "last24h"
  | "7d"
  | "14d"
  | "30d"
  | "month"
  | "lastMonth"
  | "custom";

export type UsageChartPreset = ChartPreset;
export type ChartGranularity = "hour" | "day";

export interface UsageChartRange {
  from: string;
  to: string;
  queryFrom: string;
  queryTo: string;
  granularity: ChartGranularity;
  bucketKeys: string[];
  label: string;
}

export const USAGE_CHART_PRESETS: readonly {
  value: Exclude<ChartPreset, "custom">;
  label: string;
}[] = [
  { value: "today", label: "今天" },
  { value: "yesterday", label: "昨天" },
  { value: "last24h", label: "近 24 小时" },
  { value: "7d", label: "近 7 天" },
  { value: "14d", label: "近 14 天" },
  { value: "30d", label: "近 30 天" },
  { value: "month", label: "本月" },
  { value: "lastMonth", label: "上月" },
];

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
const SHANGHAI_OFFSET = 8 * HOUR;

/** 日期输入必须回验，不能让 Date 把 2 月 30 日等无效日期悄悄归一化。 */
function calendarDay(value: string): number {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    throw new RangeError("请选择有效的开始和结束日期");
  }
  const parsed = Date.parse(`${value}T00:00:00.000Z`);
  if (!Number.isFinite(parsed) || new Date(parsed).toISOString().slice(0, 10) !== value) {
    throw new RangeError("请选择有效的开始和结束日期");
  }
  return parsed;
}

function dateKey(wallClock: number): string {
  return new Date(wallClock).toISOString().slice(0, 10);
}

function hourKey(instant: number): string {
  return `${new Date(instant + SHANGHAI_OFFSET).toISOString().slice(0, 13)}:00:00`;
}

/**
 * 查询边界是无时区 UTC，series 的桶键却是上海墙钟；二者不能直接复用。
 * 滚动 24 小时保留即时点，首尾不足一小时的桶也必须保留，不能扩成两整天。
 */
export function resolveUsageChartRange(
  preset: ChartPreset,
  customFrom = "",
  customTo = "",
  now = new Date(),
): UsageChartRange {
  const nowMs = now.getTime();
  if (!Number.isFinite(nowMs)) throw new RangeError("当前时间无效");
  const today = dateKey(nowMs + SHANGHAI_OFFSET);
  const todayWall = calendarDay(today);

  if (preset === "last24h") {
    const start = nowMs - DAY;
    const bucketKeys: string[] = [];
    for (let instant = Math.floor(start / HOUR) * HOUR; instant <= nowMs; instant += HOUR) {
      bucketKeys.push(hourKey(instant));
    }
    return {
      from: dateKey(start + SHANGHAI_OFFSET),
      to: today,
      queryFrom: new Date(start).toISOString().slice(0, -1),
      queryTo: now.toISOString().slice(0, -1),
      granularity: "hour",
      bucketKeys,
      label: "近 24 小时",
    };
  }

  let from = today;
  let to = today;
  let label: string;
  switch (preset) {
    case "today":
      label = "今天";
      break;
    case "yesterday":
      from = to = dateKey(todayWall - DAY);
      label = "昨天";
      break;
    case "7d":
    case "14d":
    case "30d": {
      const span = Number.parseInt(preset, 10);
      from = dateKey(todayWall - (span - 1) * DAY);
      label = `近 ${span} 天`;
      break;
    }
    case "month":
      from = `${today.slice(0, 7)}-01`;
      label = "本月";
      break;
    case "lastMonth": {
      const thisMonth = calendarDay(`${today.slice(0, 7)}-01`);
      to = dateKey(thisMonth - DAY);
      from = `${to.slice(0, 7)}-01`;
      label = "上月";
      break;
    }
    case "custom":
      from = customFrom;
      to = customTo;
      label = from === to ? from : `${from} 至 ${to}`;
      break;
    default:
      throw new RangeError("未知的日期范围");
  }

  const startWall = calendarDay(from);
  const endWall = calendarDay(to);
  if (startWall > endWall) throw new RangeError("开始日期不能晚于结束日期");
  // 先限制查询跨度再补桶，避免极端年份输入生成数百万个桶阻塞页面；这不是数据保留期。
  if ((endWall - startWall) / DAY + 1 > 3660) {
    throw new RangeError("日期范围最多 3660 天，请缩小范围");
  }
  const granularity: ChartGranularity = from === to ? "hour" : "day";
  const bucketKeys: string[] = [];
  for (let day = startWall; day <= endWall; day += DAY) {
    const key = dateKey(day);
    if (granularity === "day") {
      bucketKeys.push(`${key}T00:00:00`);
    } else {
      for (let hour = 0; hour < 24; hour += 1) {
        bucketKeys.push(`${key}T${String(hour).padStart(2, "0")}:00:00`);
      }
    }
  }

  return {
    from,
    to,
    queryFrom: new Date(startWall - SHANGHAI_OFFSET).toISOString().slice(0, 19),
    // PostgreSQL 保留微秒，毫秒 .999 会漏掉日尾记录；闭区间使用该秒的纳秒上界。
    queryTo: `${new Date(endWall + DAY - SHANGHAI_OFFSET - 1_000).toISOString().slice(0, 19)}.999999999`,
    granularity,
    bucketKeys,
    label,
  };
}

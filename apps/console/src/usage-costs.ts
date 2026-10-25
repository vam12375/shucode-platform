export interface PriceSnapshot {
  schemaVersion: number;
  capturedAt: string;
  sale: SnapshotRates;
  cost: {
    status: "estimated" | "unknown";
    pricingVersion?: string;
    modelId?: string;
    rates?: SnapshotRates;
    estimatedMicro?: number;
    reason?: string;
    usageEstimated?: boolean;
  };
}
interface SnapshotRates {
  inputPerMtok: string;
  outputPerMtok: string;
  cacheReadPerMtok: string;
  cacheWritePerMtok: string;
  multiplier: string;
  currency: string;
}

interface HistoricalCostDisplay {
  kind: "estimated" | "unknown" | "missing";
  text: string;
  reason: string;
}

const costReasons: Record<string, string> = {
  not_configured: "未配置供应商成本价库。",
  model_not_configured: "未配置此次请求对应模型的供应商成本价。",
  pricing_unavailable: "成本价库读取失败，未取得请求时的采购价。",
  unsupported_currency: "成本价使用了当前不支持的币种。",
  invalid_price: "请求时的采购价或倍率无效。",
  usage_unavailable: "缺少有效的上游用量，无法确定成本。",
  invalid_usage: "上游用量无效，无法计算成本。",
};

/** 用户实扣与采购成本是不同事实；历史成本只读冻结快照，缺失不能显示为零。 */
export function historicalCostDisplay(
  snapshot?: PriceSnapshot | null,
): HistoricalCostDisplay {
  if (!snapshot) {
    return {
      kind: "missing",
      text: "无历史快照",
      reason: "该请求没有保存采购成本快照，无法确定历史成本。",
    };
  }
  const cost = snapshot.cost;
  if (cost?.status !== "estimated") {
    return {
      kind: "unknown",
      text: "成本未知",
      reason: costReasons[cost?.reason ?? ""] ?? "成本未知，原因未记录。",
    };
  }
  const micro = cost.estimatedMicro;
  const currency = cost.rates?.currency?.trim().toUpperCase() ?? "";
  if (
    typeof micro !== "number" ||
    !Number.isSafeInteger(micro) ||
    micro < 0 ||
    !/^[A-Z]{3}$/.test(currency)
  ) {
    return {
      kind: "unknown",
      text: "成本未知",
      reason: "冻结成本金额或币种无效，无法展示成本。",
    };
  }
  // 按整数 micro 拆分，避免低额采购成本和安全整数边界被浮点格式化为零或舍入。
  const whole = Math.floor(micro / 1_000_000).toLocaleString("en-US");
  const fraction = String(micro % 1_000_000).padStart(6, "0").replace(/0+$/, "");
  return {
    kind: "estimated",
    text: `${currency} ${whole}${fraction ? `.${fraction}` : ""}`,
    reason: cost.usageEstimated
      ? "按估算 Token 与请求时的冻结采购价计算，供参考，非供应商最终账单。"
      : "按实际上游 Token 与请求时的冻结采购价计算，供参考，非供应商最终账单。",
  };
}

export function frozenCostRateText(
  rates: SnapshotRates | undefined,
  key: "inputPerMtok" | "outputPerMtok" | "cacheReadPerMtok" | "cacheWritePerMtok",
): string {
  if (!rates || !rates[key]?.trim()) return "未记录";
  return `${rates.currency?.trim().toUpperCase() || "币种未记录"} ${rates[key]} / 百万 Token`;
}

export function usageModelName(model: string, displayName?: string): string {
  if (displayName?.trim()) return displayName.trim();
  if (model === "media:reference-analysis") return "原片分镜分析（固定费）";
  // 老平台没有对外投影字段时也不把内部媒体型号回显给团队成员。
  return model.startsWith("media:") ? "媒体模型" : model;
}

interface CostRecord {
  model: string;
  creditsMicro: number;
  priceSnapshot?: PriceSnapshot | null;
}
interface CostGroup {
  model: string;
  currency: string;
  count: number;
  costMicro: number;
  chargedMicro: number;
}

/** 历史成本只读取请求时冻结的快照；无快照与未知成本不能混为零成本。 */
export function summarizeHistoricalCosts(records: CostRecord[]) {
  const groups = new Map<string, CostGroup>();
  const totals = new Map<
    string,
    { count: number; costMicro: number; chargedMicro: number }
  >();
  let missingCount = 0;
  let estimatedUsageCount = 0;
  for (const record of records) {
    const cost = record.priceSnapshot?.cost;
    const currency = cost?.rates?.currency;
    if (
      cost?.status !== "estimated" ||
      !currency ||
      typeof cost.estimatedMicro !== "number" ||
      !Number.isFinite(cost.estimatedMicro) ||
      cost.estimatedMicro < 0
    ) {
      missingCount += 1;
      continue;
    }
    if (cost.usageEstimated) estimatedUsageCount += 1;
    const key = `${record.model}\0${currency}`;
    const group = groups.get(key) ?? {
      model: record.model,
      currency,
      count: 0,
      costMicro: 0,
      chargedMicro: 0,
    };
    group.count += 1;
    group.costMicro += cost.estimatedMicro;
    group.chargedMicro += record.creditsMicro;
    groups.set(key, group);
    const total = totals.get(currency) ?? {
      count: 0,
      costMicro: 0,
      chargedMicro: 0,
    };
    total.count += 1;
    total.costMicro += cost.estimatedMicro;
    total.chargedMicro += record.creditsMicro;
    totals.set(currency, total);
  }
  return {
    groups: [...groups.values()],
    totals: [...totals.entries()],
    missingCount,
    estimatedUsageCount,
    estimatedCount: records.length - missingCount,
  };
}

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

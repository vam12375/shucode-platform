export interface OpsSeriesPoint {
  bucketStart: string;
  requests: number;
  errors: number;
  creditsMicro: number;
  p50LatencyMs?: number | null;
  p95LatencyMs: number | null;
}

export function fillOpsSeries(points: OpsSeriesPoint[], hours: number, now: number): OpsSeriesPoint[] {
  const returned = new Map(points.map((point) => [point.bucketStart, point]));
  const first = Math.floor((now - hours * 3_600_000) / 300_000) * 300_000;
  const last = Math.floor(now / 300_000) * 300_000;
  const buckets: OpsSeriesPoint[] = [];
  for (let time = first; time <= last; time += 300_000) {
    // API 的桶键是带 T 的上海墙钟；使用空格键会丢失真实非零桶，把所有流量误补为 0。
    const bucketStart = new Date(time + 8 * 3_600_000).toISOString().slice(0, 19);
    buckets.push(returned.get(bucketStart) ?? { bucketStart, requests: 0, errors: 0, creditsMicro: 0, p50LatencyMs: null, p95LatencyMs: null });
  }
  return buckets;
}

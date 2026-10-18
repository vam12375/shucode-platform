import { useCallback, useEffect, useState, type CSSProperties } from "react";
import { api } from "../api.js";
import { shanghaiDateOf } from "../time.js";
import { useCountUp } from "./Gauge.js";

interface UsageAggregate {
  requests: number;
  creditsMicro: number;
}

interface DeviceRow {
  lastUsedAt: string | null;
  revokedAt: string | null;
}

interface ReleaseRow {
  revision: number;
  isActive: boolean;
}

interface RailData {
  requests: number;
  creditsMicro: number;
  avgRequests: number;
  avgCredits: number;
  activeDevices: number;
  totalDevices: number;
  revision: number | null;
}

const EMPTY: RailData = {
  requests: 0,
  creditsMicro: 0,
  avgRequests: 0,
  avgCredits: 0,
  activeDevices: 0,
  totalDevices: 0,
  revision: null,
};

function sumAggregates(rows: UsageAggregate[]): { requests: number; creditsMicro: number } {
  return rows.reduce(
    (sum, row) => ({ requests: sum.requests + row.requests, creditsMicro: sum.creditsMicro + row.creditsMicro }),
    { requests: 0, creditsMicro: 0 },
  );
}

async function loadRail(): Promise<RailData> {
  const now = new Date();
  // 「今日」按上海墙钟取日界，与后端对 from/to 的 +8 解读一致。
  const today = shanghaiDateOf(now);
  const start = shanghaiDateOf(new Date(now.getTime() - 13 * 86_400_000));
  const [todayUsage, windowUsage, devicesRes, releasesRes] = await Promise.all([
    api<{ aggregates: UsageAggregate[] }>(`/usage?from=${today}T00:00:00&to=${today}T23:59:59`),
    api<{ aggregates: UsageAggregate[] }>(`/usage?from=${start}T00:00:00&to=${today}T23:59:59`),
    api<{ devices: DeviceRow[] }>("/devices"),
    api<{ releases: ReleaseRow[] }>("/releases"),
  ]);
  const todaySum = sumAggregates(todayUsage.aggregates);
  const windowSum = sumAggregates(windowUsage.aggregates);
  const activeDevices = devicesRes.devices.filter(
    (device) => !device.revokedAt && device.lastUsedAt && Date.now() - Date.parse(device.lastUsedAt) < 86_400_000,
  ).length;
  return {
    requests: todaySum.requests,
    creditsMicro: todaySum.creditsMicro,
    avgRequests: windowSum.requests / 14,
    avgCredits: windowSum.creditsMicro / 14,
    activeDevices,
    totalDevices: devicesRes.devices.length,
    revision: releasesRes.releases.find((r) => r.isActive)?.revision ?? null,
  };
}

/** 顶部读数条（签名元素）：今日请求 / 今日费用 / 活跃设备 / 生效版本，数值带进位动效。 */
export function ReadoutRail() {
  const [data, setData] = useState<RailData>(EMPTY);

  const refresh = useCallback(() => {
    loadRail()
      .then(setData)
      .catch(() => setData(EMPTY));
  }, []);

  useEffect(() => {
    refresh();
    const timer = window.setInterval(refresh, 60_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  const requestsText = useCountUp(data.requests, 0);
  const creditsText = useCountUp(data.creditsMicro / 1_000_000, 2);
  const devicesText = useCountUp(data.activeDevices, 0);

  const ratio = (value: number, baseline: number) => (baseline > 0 ? Math.min(value / baseline, 1) : 0);
  const requestWidth = `${(ratio(data.requests, data.avgRequests) * 100).toFixed(1)}%`;
  const creditWidth = `${(ratio(data.creditsMicro, data.avgCredits) * 100).toFixed(1)}%`;
  const deviceWidth = `${(ratio(data.activeDevices, data.totalDevices) * 100).toFixed(1)}%`;

  return (
    <header className="rail" aria-label="平台实时读数">
      <div className="read">
        <div className="label">今日请求</div>
        <div className="value">
          {requestsText}
          <span className="unit">次</span>
        </div>
        <div className="mini" aria-hidden="true" style={{ "--w": requestWidth } as CSSProperties}>
          <i />
        </div>
      </div>
      <div className="read">
        <div className="label">今日费用</div>
        <div className="value">
          {creditsText}
          <span className="unit">CR</span>
        </div>
        <div className="mini" aria-hidden="true" style={{ "--w": creditWidth } as CSSProperties}>
          <i />
        </div>
      </div>
      <div className="read" title="近 24 小时有调用且未吊销的设备数">
        <div className="label">活跃设备</div>
        <div className="value">
          {devicesText}
          <span className="unit">台</span>
        </div>
        <div className="mini" aria-hidden="true" style={{ "--w": deviceWidth } as CSSProperties}>
          <i />
        </div>
      </div>
      <div className="read">
        <div className="label">生效版本</div>
        <div className="value">{data.revision === null ? "—" : `R${data.revision}`}</div>
        <div className="mini" aria-hidden="true" style={{ "--w": data.revision === null ? "0%" : "100%" } as CSSProperties}>
          <i />
        </div>
      </div>
    </header>
  );
}

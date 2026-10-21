import { formatCreditsDisplay } from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState, type CSSProperties } from "react";
import { useAuth } from "../auth.js";
import { api } from "../api.js";
import {
  shanghaiDateOf,
  shanghaiDayEndUtc,
  shanghaiDayStartUtc,
} from "../time.js";
import { useCountUp } from "./Gauge.js";
import { Select } from "./Select.js";
import { appVersion, versionText } from "../version.js";

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

function sumAggregates(rows: UsageAggregate[]): {
  requests: number;
  creditsMicro: number;
} {
  return rows.reduce(
    (sum, row) => ({
      requests: sum.requests + row.requests,
      creditsMicro: sum.creditsMicro + row.creditsMicro,
    }),
    { requests: 0, creditsMicro: 0 },
  );
}

async function loadRail(isRoot: boolean, teamId: string): Promise<RailData> {
  const scope = teamId ? `&teamId=${encodeURIComponent(teamId)}` : "";
  const now = new Date();
  // 展示按上海日期取今日；服务端按 UTC 解析 from/to，必须换算上海日界。
  const today = shanghaiDateOf(now);
  const start = shanghaiDateOf(new Date(now.getTime() - 13 * 86_400_000));
  const [todayUsage, windowUsage, devicesRes, releasesRes] = await Promise.all([
    api<{ aggregates: UsageAggregate[] }>(
      `/usage?from=${shanghaiDayStartUtc(today)}&to=${shanghaiDayEndUtc(today)}${scope}`,
    ),
    api<{ aggregates: UsageAggregate[] }>(
      `/usage?from=${shanghaiDayStartUtc(start)}&to=${shanghaiDayEndUtc(today)}${scope}`,
    ),
    api<{ devices: DeviceRow[] }>(
      `/devices${teamId ? `?teamId=${encodeURIComponent(teamId)}` : ""}`,
    ),
    isRoot
      ? api<{ releases: ReleaseRow[] }>("/releases")
      : Promise.resolve({ releases: [] }),
  ]);
  const todaySum = sumAggregates(todayUsage.aggregates);
  const windowSum = sumAggregates(windowUsage.aggregates);
  const activeDevices = devicesRes.devices.filter(
    (device) =>
      !device.revokedAt &&
      device.lastUsedAt &&
      Date.now() - Date.parse(device.lastUsedAt) < 86_400_000,
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

/** 顶部读数条（签名元素）：今日请求 / 今日积分 / 活跃设备 / 生效版本，数值带进位动效。 */
export function ReadoutRail() {
  const { isRoot, canManageTeam, teamId, team, teams, selectTeam, user, refreshSession } =
    useAuth();
  const [data, setData] = useState<RailData | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    loadRail(isRoot, teamId)
      .then((next) => {
        setData(next);
        setLoadError(null);
      })
      .catch((error: unknown) => {
        // 失败时保留 null，读数显示“—”而不是 0，避免把故障伪装成真实零用量。
        setData(null);
        setLoadError(error instanceof Error ? error.message : "读数加载失败");
      });
  }, [isRoot, teamId]);

  useEffect(() => {
    refresh();
    const timer = window.setInterval(() => {
      refresh();
      // 余额来自会话内团队快照；轮询同时更新快照，避免读数长期停在登录时余额。
      void refreshSession().catch(() => {});
    }, 60_000);
    return () => window.clearInterval(timer);
  }, [refresh, refreshSession]);

  const displayed = data ?? EMPTY;
  const requestsText = useCountUp(displayed.requests, 0);
  const creditsText = useCountUp(displayed.creditsMicro / 1_000_000, 2);
  const devicesText = useCountUp(displayed.activeDevices, 0);

  const ratio = (value: number, baseline: number) =>
    baseline > 0 ? Math.min(value / baseline, 1) : 0;
  const requestWidth = `${(ratio(displayed.requests, displayed.avgRequests) * 100).toFixed(1)}%`;
  const creditWidth = `${(ratio(displayed.creditsMicro, displayed.avgCredits) * 100).toFixed(1)}%`;
  const deviceWidth = `${(ratio(displayed.activeDevices, displayed.totalDevices) * 100).toFixed(1)}%`;
  // 团队余额按 credit 展示，进度条表示剩余额度占总额度的比例。
  // 原实现固定 100%/0%，与真实余额无关；近额时填充转为信号色。
  const quotaCredits = Number(team?.quotaCredits ?? 0);
  const remainingCredits = team
    ? quotaCredits - Number(team.usedCredits) - Number(team.reservedCredits ?? 0)
    : 0;
  const remainingPercent =
    team?.unlimited === true
      ? 100
      : quotaCredits > 0
        ? Math.max(0, Math.min((remainingCredits / quotaCredits) * 100, 100))
        : 0;
  const balanceWidth = `${remainingPercent.toFixed(1)}%`;
  const balanceLow = Boolean(
    !isRoot && team && !team.unlimited && remainingPercent <= 10,
  );
  // 只有 root 或多团队用户需要切换;单团队展示静态团队名,避免无意义的单选项下拉。
  const showScopeSelect = isRoot || teams.length > 1;

  return (
    <header
      className="rail"
      aria-label={
        isRoot
          ? "平台实时读数"
          : canManageTeam
            ? "团队实时读数"
            : "本人实时读数"
      }
    >
      <div className="read scope">
        <div className="label">{isRoot ? "查看范围" : "当前团队"}</div>
        {showScopeSelect ? (
          <Select
            ariaLabel={isRoot ? "切换查看范围" : "切换当前团队"}
            style={{ width: "100%" }}
            value={teamId}
            onChange={selectTeam}
            options={[
              ...(isRoot ? [{ value: "", label: "全平台" }] : []),
              ...teams.map((item) => ({ value: item.id, label: item.name })),
            ]}
          />
        ) : (
          <div className="scope-value" title={team?.name}>
            {team?.name ?? "尚未加入团队"}
          </div>
        )}
        {loadError ? (
          <button
            className="btn btn-sm"
            type="button"
            title={loadError}
            style={{ marginTop: 6, width: "100%" }}
            onClick={refresh}
          >
            读数加载失败 · 重试
          </button>
        ) : null}
      </div>
      <div className="read" title="今日请求相对近 14 日均值">
        <div className="label">今日请求</div>
        <div className="value">
          {data ? (
            <>
              {requestsText}
              <span className="unit">次</span>
            </>
          ) : (
            "—"
          )}
        </div>
        <div
          className="mini"
          aria-hidden="true"
          style={{ "--w": requestWidth } as CSSProperties}
        >
          <i />
        </div>
      </div>
      <div className="read" title="今日积分相对近 14 日均值">
        <div className="label">今日积分</div>
        <div className="value">
          {data ? (
            <>
              {creditsText}
              <span className="unit">credit</span>
            </>
          ) : (
            "—"
          )}
        </div>
        <div
          className="mini"
          aria-hidden="true"
          style={{ "--w": creditWidth } as CSSProperties}
        >
          <i />
        </div>
      </div>
      <div className="read" title="近 24 小时有调用且未吊销的设备数">
        <div className="label">{isRoot ? "活跃设备" : "本人活跃设备"}</div>
        <div className="value">
          {data ? (
            <>
              {devicesText}
              <span className="unit">台</span>
            </>
          ) : (
            "—"
          )}
        </div>
        <div
          className="mini"
          aria-hidden="true"
          style={{ "--w": deviceWidth } as CSSProperties}
        >
          <i />
        </div>
      </div>
      <div
        className="read"
        title={
          isRoot
            ? versionText(appVersion) || undefined
            : team
              ? team.unlimited
                ? "团队额度不限"
                : `剩余 ${formatCreditsDisplay(remainingCredits)} / ${formatCreditsDisplay(quotaCredits)} credit`
              : undefined
        }
      >
        <div className="label">
          {isRoot ? "生效版本" : team ? "团队余额" : "当前账户"}
        </div>
        <div className="value">
          {isRoot
            ? appVersion.tag || (data?.revision == null ? "—" : `R${data.revision}`)
            : team
              ? team.unlimited
                ? "不限"
                : (
                    <>
                      {formatCreditsDisplay(remainingCredits)}
                      <span className="unit">credit</span>
                    </>
                  )
              : user.email.split("@")[0]}
        </div>
        <div
          className="mini"
          aria-hidden="true"
          style={
            {
              "--w": isRoot
                ? appVersion.tag || data?.revision != null
                  ? "100%"
                  : "0%"
                : team
                  ? balanceWidth
                  : "0%",
            } as CSSProperties
          }
        >
          <i className={balanceLow ? "hot" : undefined} />
        </div>
        {isRoot && appVersion.tag && appVersion.subject ? (
          <div className="note" title={versionText(appVersion)}>
            {appVersion.subject}
          </div>
        ) : null}
      </div>
    </header>
  );
}

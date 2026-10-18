import { formatCreditsDisplay } from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";
import { toastErr, toastOk } from "../components/Feedback.js";
import { Gauge } from "../components/Gauge.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";
import { formatShanghaiDate } from "../time.js";

interface TeamRow {
  id: string;
  name: string;
  quotaCredits: string;
  usedCredits: string;
  reservedCredits: string;
  unlimited: boolean;
  memberCount: number;
  createdAt: string;
}

const HOT_RATIO = 0.9;

/** 按「成功率状态」取印章语义：不限 → mute，近额 → hot，其余 → ok 生效。 */
function statusChip(team: TeamRow): { tone?: ChipTone; label: string } {
  if (team.unlimited) return { tone: "mute", label: "不限" };
  const quota = Number.parseFloat(team.quotaCredits) || 0;
  const used = Number.parseFloat(team.usedCredits) || 0;
  const ratio = quota > 0 ? Math.min(used / quota, 1) : 0;
  return ratio >= HOT_RATIO ? { tone: "hot", label: "近额" } : { tone: "ok", label: "生效" };
}

export function TeamsPage() {
  const [teams, setTeams] = useState<TeamRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [quota, setQuota] = useState("100");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api<{ teams: TeamRow[] }>("/teams");
      setTeams(data.teams);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const createTeam = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const teamName = name.trim();
    setBusy(true);
    try {
      await api("/teams", {
        method: "POST",
        body: JSON.stringify({ name, quotaCredits: quota }),
      });
      setName("");
      await load();
      toastOk(`已创建团队「${teamName}」`, "已创建");
    } catch (createError) {
      toastErr(createError instanceof Error ? createError.message : "创建失败", "创建失败");
    } finally {
      setBusy(false);
    }
  };

  const rows = teams ?? [];
  const totalMembers = rows.reduce((sum, team) => sum + team.memberCount, 0);
  const totalUsed = rows.reduce((sum, team) => sum + (Number.parseFloat(team.usedCredits) || 0), 0);

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">组织 / 台账</p>
          <h1>团队</h1>
        </div>
      </header>

      {error ? (
        <Notice tone="stop" k="操作未完成">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>新建团队</h2>
        </div>
        <div className="pane-b">
          <form className="form-row" onSubmit={(event) => void createTeam(event)}>
            <input
              className="input grow"
              placeholder="团队名称"
              aria-label="团队名称"
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
            />
            <input
              className="input num"
              style={{ width: 150 }}
              placeholder="额度 credit"
              aria-label="额度 credit"
              value={quota}
              onChange={(event) => setQuota(event.target.value)}
            />
            <button className="btn" type="submit" disabled={busy || !name.trim()}>
              新建团队
            </button>
          </form>
          <p className="readout" style={{ margin: 0, marginTop: 10 }}>
            额度以 credit 计（按模型单价折算）；团队成员共享同一额度池。
          </p>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>团队额度</h2>
          <span className="readout">实时 · 网关计量</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">团队</th>
                <th scope="col" className="num">
                  成员
                </th>
                <th scope="col">额度尺</th>
                <th scope="col">状态</th>
                <th scope="col" className="num">
                  立账
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((team) => {
                const used = Number.parseFloat(team.usedCredits) || 0;
                const quotaValue = Number.parseFloat(team.quotaCredits) || 0;
                const status = statusChip(team);
                return (
                  <tr key={team.id}>
                    <td>
                      <a className="name" href={`#/teams/${team.id}`}>
                        {team.name}
                      </a>
                    </td>
                    <td className="num">{team.memberCount}</td>
                    <td>
                      <Gauge used={used} quota={team.unlimited ? null : quotaValue} hotAt={HOT_RATIO} />
                    </td>
                    <td>
                      <Chip tone={status.tone}>{status.label}</Chip>
                    </td>
                    <td className="num">{formatShanghaiDate(team.createdAt)}</td>
                  </tr>
                );
              })}
              {rows.length === 0 ? (
                <tr>
                  <td colSpan={5} className="readout">
                    还没有团队，先创建一个。
                  </td>
                </tr>
              ) : null}
            </tbody>
            <tfoot>
              <tr>
                <td>合计 · {rows.length} 个团队</td>
                <td className="num">{totalMembers}</td>
                <td colSpan={2}>已用 {formatCreditsDisplay(totalUsed)} CR</td>
                <td />
              </tr>
            </tfoot>
          </table>
        </div>
      </section>
    </div>
  );
}

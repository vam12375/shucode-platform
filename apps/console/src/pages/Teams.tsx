import { formatCreditsDisplay } from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api.js";

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
    setBusy(true);
    try {
      await api("/teams", {
        method: "POST",
        body: JSON.stringify({ name, quotaCredits: quota }),
      });
      setName("");
      await load();
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : "创建失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h1>团队</h1>
      {error ? <p className="error">{error}</p> : null}

      <div className="card">
        <form className="row" onSubmit={(event) => void createTeam(event)}>
          <input
            className="grow"
            placeholder="新团队名称"
            value={name}
            onChange={(event) => setName(event.target.value)}
            required
          />
          <input
            style={{ width: 140 }}
            placeholder="额度 credit"
            value={quota}
            onChange={(event) => setQuota(event.target.value)}
          />
          <button type="submit" disabled={busy || !name.trim()}>
            新建团队
          </button>
        </form>
        <p className="muted" style={{ marginBottom: 0 }}>
          额度以 credit 计（按模型单价折算）；团队成员共享同一额度池。
        </p>
      </div>

      <div className="card">
        <table>
          <thead>
            <tr>
              <th>团队</th>
              <th>成员</th>
              <th>已用 / 额度</th>
              <th>进度</th>
            </tr>
          </thead>
          <tbody>
            {(teams ?? []).map((team) => {
              const used = Number.parseFloat(team.usedCredits) || 0;
              const quotaValue = Number.parseFloat(team.quotaCredits) || 0;
              const ratio = team.unlimited || quotaValue <= 0 ? 0 : Math.min(used / quotaValue, 1);
              return (
                <tr key={team.id}>
                  <td>
                    <a href={`#/teams/${team.id}`}>{team.name}</a>
                  </td>
                  <td>{team.memberCount}</td>
                  <td className="mono">
                    {formatCreditsDisplay(team.usedCredits)} /{" "}
                    {team.unlimited ? "不限" : formatCreditsDisplay(team.quotaCredits)}
                  </td>
                  <td>
                    <div className="progress">
                      <span style={{ width: `${Math.round(ratio * 100)}%` }} />
                    </div>
                  </td>
                </tr>
              );
            })}
            {teams && teams.length === 0 ? (
              <tr>
                <td colSpan={4} className="muted">
                  还没有团队，先创建一个。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}

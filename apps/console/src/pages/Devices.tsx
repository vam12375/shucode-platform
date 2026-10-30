import { useCallback, useEffect, useState } from "react";
import { Smartphone } from "lucide-react";
import { api } from "../api.js";
import { confirmDialog, toastErr, toastOk } from "../components/Feedback.js";
import { PageTitle } from "../components/Icon.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { formatShanghaiTime } from "../time.js";

interface DeviceRow {
  id: string;
  name: string;
  platform: string | null;
  hint: string;
  userEmail: string;
  createdAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
}

export function DevicesPage() {
  const [devices, setDevices] = useState<DeviceRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api<{ devices: DeviceRow[] }>("/devices");
      setDevices(data.devices);
      setError(null);
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : "加载失败");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const revoke = async (device: DeviceRow): Promise<void> => {
    const answer = await confirmDialog({
      k: "吊销设备",
      title: `吊销「${device.name}」的凭据？`,
      message: `吊销后该设备的网关请求立即 401，需重新走设备登录流方可恢复（${device.userEmail}）。`,
      confirmText: "吊销",
      danger: true,
    });
    if (!answer.confirmed) return;
    setBusy(true);
    try {
      await api(`/devices/${device.id}/revoke`, { method: "POST" });
      await load();
      toastOk(`已吊销「${device.name}」的设备凭据`, "已吊销");
    } catch (revokeError) {
      toastErr(revokeError instanceof Error ? revokeError.message : "吊销失败", "吊销失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">接入 / 凭据</p>
          <PageTitle icon={Smartphone}>设备凭据</PageTitle>
        </div>
        <span className="readout">
          {devices.filter((d) => !d.revokedAt).length} 有效 · {devices.length} 台
        </span>
      </header>
      <p className="readout" style={{ margin: 0, marginBottom: 18 }}>
        每台登录设备持有独立凭据（共享团队额度、可单独吊销）。吊销后该设备的网关请求立即 401。
      </p>

      {error ? (
        <Notice tone="stop" k="操作未完成">
          {error}
        </Notice>
      ) : null}

      <section className="pane">
        <div className="pane-h">
          <h2>设备清单</h2>
          <span className="readout">共享团队额度 · 独立凭据</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">设备</th>
                <th scope="col">账号</th>
                <th scope="col">后缀</th>
                <th scope="col" className="num">
                  最近使用
                </th>
                <th scope="col">状态</th>
                <th scope="col" aria-label="操作" />
              </tr>
            </thead>
            <tbody>
              {devices.map((device) => (
                <tr key={device.id}>
                  <td className="name">
                    {device.name}
                    {device.platform ? <span className="readout"> · {device.platform}</span> : null}
                  </td>
                  <td>{device.userEmail}</td>
                  <td className="num">…{device.hint}</td>
                  <td className="num">{device.lastUsedAt ? formatShanghaiTime(device.lastUsedAt) : "从未"}</td>
                  <td>{device.revokedAt ? <Chip tone="stop">已吊销</Chip> : <Chip tone="ok">有效</Chip>}</td>
                  <td>
                    <div className="form-row" style={{ justifyContent: "flex-end" }}>
                      {device.revokedAt ? null : (
                        <button
                          className="btn btn-danger btn-sm"
                          disabled={busy}
                          onClick={() => void revoke(device)}
                        >
                          吊销
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
              {devices.length === 0 ? (
                <tr>
                  <td colSpan={6} className="readout">
                    还没有设备登录记录。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

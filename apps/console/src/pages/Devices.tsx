import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";

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

  return (
    <div>
      <h1>设备凭据</h1>
      <p className="muted">
        每台登录设备持有独立凭据（共享团队额度、可单独吊销）。吊销后该设备的网关请求立即 401。
      </p>
      {error ? <p className="error">{error}</p> : null}
      <div className="card">
        <table>
          <thead>
            <tr>
              <th>设备</th>
              <th>账号</th>
              <th>后缀</th>
              <th>最近使用</th>
              <th>状态</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {devices.map((device) => (
              <tr key={device.id}>
                <td>
                  {device.name}
                  {device.platform ? <span className="muted"> · {device.platform}</span> : null}
                </td>
                <td className="mono">{device.userEmail}</td>
                <td className="mono">…{device.hint}</td>
                <td className="mono">
                  {device.lastUsedAt ? device.lastUsedAt.replace("T", " ").slice(0, 19) : "从未"}
                </td>
                <td>{device.revokedAt ? "已吊销" : "有效"}</td>
                <td style={{ textAlign: "right" }}>
                  {device.revokedAt ? null : (
                    <button
                      className="danger"
                      disabled={busy}
                      onClick={() =>
                        void (async () => {
                          setBusy(true);
                          try {
                            await api(`/devices/${device.id}/revoke`, { method: "POST" });
                            await load();
                          } catch (revokeError) {
                            setError(
                              revokeError instanceof Error ? revokeError.message : "吊销失败",
                            );
                          } finally {
                            setBusy(false);
                          }
                        })()
                      }
                    >
                      吊销
                    </button>
                  )}
                </td>
              </tr>
            ))}
            {devices.length === 0 ? (
              <tr>
                <td colSpan={6} className="muted">
                  还没有设备登录记录。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );
}

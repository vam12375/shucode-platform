import { MediaReconciliation } from "./MediaReconciliation.js";
import { useEffect, useState } from "react";
import { api } from "../api.js";
import { Notice } from "../components/StatusChip.js";

const example = [
  {
    id: "product-video",
    name: "商品视频",
    kind: "video",
    adapter: "ark",
    providerId: "替换为供应商ID",
    upstreamModel: "替换为已开通模型ID",
    resolutions: ["720p", "1080p"],
    durations: [5, 10],
    priceMicro: 1_000_000,
    priceVersion: "v1",
  },
];
export function MediaModelsPage() {
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    void api<unknown[]>("/media/models")
      .then((value) => setText(JSON.stringify(value, null, 2)))
      .catch((e: Error) => setError(e.message));
  }, []);
  return (
    <>
      <section className="panel">
        <div className="panel-head">
          <h1>媒体模型与价格</h1>
        </div>
        <p>
          复用「上游供应商」里的加密密钥。首版支持 Ark 生图 / 生视频接口，供应商 Base URL 填完整 API
          前缀。模型 ID 必须填写已开通的真实模型。
        </p>
        <p>
          视频 priceMicro 为每请求镜头秒的 micro-credit，图片为每张。1 credit = ¥1 = 1,000,000
          micro-credit。修改 priceVersion 后仅影响新制作单，已报价任务继续使用原快照。
        </p>
        {error && (
          <Notice tone="stop" k="操作失败">
            {error}
          </Notice>
        )}
        {saved && <Notice k="已保存">新制作单将使用本次配置。</Notice>}
        <label className="field">
          <span>媒体模型 JSON</span>
          <textarea
            aria-label="媒体模型 JSON"
            rows={24}
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setSaved(false);
            }}
            style={{ width: "100%", fontFamily: "monospace" }}
          />
        </label>
        <div className="actions">
          <button
            className="btn"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              setError("");
              void (async () => {
                const models: unknown = JSON.parse(text);
                await api("/media/models", { method: "PUT", body: JSON.stringify(models) });
                setSaved(true);
              })()
                .catch((e: Error) => setError(e.message))
                .finally(() => setBusy(false));
            }}
          >
            保存配置
          </button>
          <button className="btn ghost" onClick={() => setText(JSON.stringify(example, null, 2))}>
            填入格式示例
          </button>
        </div>
      </section>
      <MediaReconciliation />
    </>
  );
}

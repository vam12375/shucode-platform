import {
  MICRO_CREDITS_PER_CREDIT,
  formatMicroCredits,
  parseCreditsToMicro,
} from "@shucode-platform/contracts/credits";
import { useEffect, useRef, useState } from "react";
import { api } from "../api.js";
import { toastOk } from "../components/Feedback.js";
import { Notice } from "../components/StatusChip.js";

interface ReferencePricingState {
  snapAnyImportMicro: number;
  analysisMicro: number;
}

const MICRO_PER_CREDIT = BigInt(MICRO_CREDITS_PER_CREDIT);

function exactMicro(value: string): bigint {
  const [whole, fraction = ""] = value.split(".");
  return BigInt(whole) * MICRO_PER_CREDIT + BigInt(fraction.padEnd(6, "0"));
}

function priceInputValue(micro: number): string {
  const formatted = formatMicroCredits(micro).replace(/\.?0+$/, "");
  const exact = BigInt(micro);
  if (exactMicro(formatted) === exact) return formatted;
  // 浮点除法在安全整数上界附近会丢失 1 micro，边界输入必须按定点值回显。
  const fraction = (exact % MICRO_PER_CREDIT).toString().padStart(6, "0").replace(/0+$/, "");
  return `${exact / MICRO_PER_CREDIT}${fraction ? `.${fraction}` : ""}`;
}

function parsePrice(value: string, label: string): number {
  const trimmed = value.trim();
  if (!/^\d+(\.\d{1,6})?$/.test(trimmed)) {
    throw new Error(`${label}须为非负数，最多保留 6 位小数；填 0 表示免费`);
  }
  const exact = exactMicro(trimmed);
  if (exact > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new Error(`${label}不能超过 9007199254.740991 credit`);
  }
  const parsed = parseCreditsToMicro(trimmed);
  // 复用统一 credit 转换；仅浮点舍入与精确定点值不一致时使用已校验的整数。
  return Number.isSafeInteger(parsed) && BigInt(parsed) === exact ? parsed : Number(exact);
}

export function ReferencePricing() {
  const [state, setState] = useState<ReferencePricingState | null>(null);
  const [importCredits, setImportCredits] = useState("");
  const [analysisCredits, setAnalysisCredits] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const actionPending = useRef(false);

  useEffect(() => {
    let active = true;
    api<ReferencePricingState>("/media/reference-pricing").then(
      (value) => {
        if (!active) return;
        setState(value);
        setImportCredits(priceInputValue(value.snapAnyImportMicro));
        setAnalysisCredits(priceInputValue(value.analysisMicro));
      },
      (reason: unknown) => {
        if (active) {
          setError(reason instanceof Error ? reason.message : "价格读取失败");
        }
      },
    );
    return () => {
      active = false;
    };
  }, []);

  const dirty = state !== null && (
    importCredits !== priceInputValue(state.snapAnyImportMicro) ||
    analysisCredits !== priceInputValue(state.analysisMicro)
  );

  const save = async (): Promise<void> => {
    if (!state || !dirty || actionPending.current) return;
    setError("");
    let next: ReferencePricingState;
    try {
      next = {
        snapAnyImportMicro: parsePrice(importCredits, "SnapAny 导入价格"),
        analysisMicro: parsePrice(analysisCredits, "原片分镜分析总价"),
      };
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "价格格式不正确");
      return;
    }
    actionPending.current = true;
    setBusy(true);
    try {
      const saved = await api<ReferencePricingState>("/media/reference-pricing", {
        method: "PUT",
        body: JSON.stringify(next),
      });
      setState(saved);
      setImportCredits(priceInputValue(saved.snapAnyImportMicro));
      setAnalysisCredits(priceInputValue(saved.analysisMicro));
      toastOk("原片导入与分镜分析价格已保存，仅影响新受理操作", "已保存");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "价格保存失败");
    } finally {
      actionPending.current = false;
      setBusy(false);
    }
  };

  if (!state) {
    return error ? (
      <Notice tone="stop" k="原片定价">{error}</Notice>
    ) : (
      <Notice k="原片定价">正在读取原片导入与分析价格…</Notice>
    );
  }

  return (
    <section className="pane" aria-label="原片导入与分析定价">
      <div className="pane-h">
        <h2>原片导入与分析定价</h2>
      </div>
      <div className="pane-b">
        <p className="readout">
          当前价格：SnapAny 导入 {priceInputValue(state.snapAnyImportMicro)} credit / 次
          {` · 原片分镜分析 ${priceInputValue(state.analysisMicro)} credit / 次`}
        </p>
        {error ? <Notice tone="stop" k="价格未保存">{error}</Notice> : null}
        <form
          className="form-stack"
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <div className="form-row" style={{ alignItems: "flex-start" }}>
            <label className="field" style={{ flex: "1 1 240px" }}>
              <span className="field-label">SnapAny 导入价格（credit / 次）</span>
              <input
                className="input num"
                inputMode="decimal"
                value={importCredits}
                disabled={busy}
                onChange={(event) => setImportCredits(event.target.value)}
              />
              <span className="readout">分享链接成功导入后收费；本地上传、选择团队原片免费。</span>
            </label>
            <label className="field" style={{ flex: "1 1 240px" }}>
              <span className="field-label">原片分镜分析总价（credit / 次）</span>
              <input
                className="input num"
                inputMode="decimal"
                value={analysisCredits}
                disabled={busy}
                onChange={(event) => setAnalysisCredits(event.target.value)}
              />
              <span className="readout">所有来源原片分析成功后收费；总价已含文字模型，不再另计 token。</span>
            </label>
          </div>
          <p className="readout" style={{ margin: 0 }}>
            最多保留 6 位小数，0 表示免费。改价仅对新受理操作生效，已受理操作保留锁定价格。
          </p>
          <div className="form-row">
            <button className="btn" type="submit" disabled={busy || !dirty}>
              {busy ? "保存中…" : "保存原片价格"}
            </button>
            <span className="readout">{dirty ? "有未保存修改" : "价格已同步"}</span>
          </div>
        </form>
      </div>
    </section>
  );
}

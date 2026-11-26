import { ChevronDown } from "lucide-react";
import { useId, useMemo, useState, type CSSProperties } from "react";
import { promptTokens, usageCredits, type UsageAggregate } from "../usage-aggregate.js";
import { usageModelName } from "../usage-costs.js";
import { formatPct } from "./Gauge.js";
import { Glyph } from "./Icon.js";
import { entityColor } from "./polar-colors.js";
import "./usage-model-composition.css";

type Metric = "credits" | "tokens";
type Segment = { key: string; label: string; value: number; color: string };
type Rect = { x: number; y: number; width: number; height: number };
const visibleModels = 6;
const otherKey = "other";

// 按权重沿长边二分，每个叶子的面积与其数值成正比。
function partition(segments: Segment[], rect: Rect): (Rect & Segment)[] {
  if (!segments.length) return [];
  if (segments.length === 1) return [{ ...segments[0], ...rect }];
  const total = segments.reduce((sum, segment) => sum + segment.value, 0);
  let boundary = 1;
  let subtotal = segments[0].value;
  while (boundary < segments.length - 1) {
    const next = subtotal + segments[boundary].value;
    if (Math.abs(total / 2 - next) >= Math.abs(total / 2 - subtotal)) break;
    subtotal = next;
    boundary++;
  }
  const first = { ...rect };
  const second = { ...rect };
  if (rect.width >= rect.height) {
    first.width *= subtotal / total;
    second.x += first.width;
    second.width -= first.width;
  } else {
    first.height *= subtotal / total;
    second.y += first.height;
    second.height -= first.height;
  }
  return [...partition(segments.slice(0, boundary), first), ...partition(segments.slice(boundary), second)];
}

function shareText(value: number, total: number) {
  return value / total < 0.001 ? "<0.1%" : formatPct(value / total);
}

export function UsageModelComposition({ rows }: { rows: UsageAggregate[] }) {
  const [metric, setMetric] = useState<Metric>("credits");
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const membersId = useId();
  const { segments, remaining, total } = useMemo(() => {
    const ranked = rows.map((row) => ({
      key: `model:${row.model}`,
      label: usageModelName(row.model, row.modelDisplayName),
      value: metric === "credits" ? row.creditsMicro : promptTokens(row),
      color: entityColor(row.model),
    })).filter((segment) => segment.value > 0).sort((a, b) => b.value - a.value);
    const primary = ranked.slice(0, visibleModels);
    const remaining = ranked.slice(visibleModels);
    if (remaining.length) {
      primary.push({
        key: otherKey, label: `其他 ${remaining.length} 个模型`,
        value: remaining.reduce((sum, segment) => sum + segment.value, 0), color: "var(--muted)",
      });
    }
    return { segments: primary, remaining, total: primary.reduce((sum, segment) => sum + segment.value, 0) };
  }, [metric, rows]);
  const selected = segments.find((segment) => segment.key === selectedKey);
  const showMembers = selected?.key === otherKey;
  const unit = metric === "credits" ? "credit" : "Token";
  const amount = (value: number) => metric === "credits"
    ? usageCredits(value)
    : value.toLocaleString("en-US");
  const select = (key: string) => setSelectedKey(selectedKey === key ? null : key);
  const tiles = partition(segments, { x: 0, y: 0, width: 200, height: 100 });

  return (
    <>
      <div className="pane-h composition-head">
        <div>
          <h2>模型消费构成</h2>
          <p>{metric === "credits" ? "面积按实扣积分占比" : "面积按 Prompt Token 占比（输入、缓存读与缓存写）"}</p>
        </div>
        <div className="polar-segmented" role="group" aria-label="模型构成统计口径">
          {(["credits", "tokens"] as const).map((option) => (
            <button key={option} type="button" aria-pressed={metric === option}
              onClick={() => { setMetric(option); setSelectedKey(null); }}>
              {option === "credits" ? "按积分" : "按 Token"}
            </button>
          ))}
        </div>
      </div>
      {!total ? (
        <p className="composition-empty">所选范围内暂无{metric === "credits" ? "积分消费" : "Prompt Token"}。</p>
      ) : (
        <div className="composition-body">
          <div className="composition-map" role="group" aria-label="模型占比矩形树图">
            {tiles.map((tile) => (
              <div key={tile.key} className="composition-cell" style={{
                left: `${tile.x / 2}%`, top: `${tile.y}%`, width: `${tile.width / 2}%`, height: `${tile.height}%`,
                "--tile-color": tile.color,
              } as CSSProperties}>
                <button type="button" className={tile.key === otherKey ? "composition-tile is-other" : "composition-tile"}
                  aria-label={`${tile.label}，${shareText(tile.value, total)}，${amount(tile.value)} ${unit}`}
                  aria-pressed={tile.key === selected?.key}
                  onClick={() => select(tile.key)}>
                  <span className="composition-tile-label">{tile.label}</span>
                  <b className="composition-tile-share">{shareText(tile.value, total)}</b>
                </button>
              </div>
            ))}
          </div>
          <div className="composition-readout" aria-live="polite" aria-atomic="true">
            <span>{selected?.label ?? "范围合计"}</span>
            <b>{amount(selected?.value ?? total)} <small>{unit}</small></b>
          </div>
          <div className="composition-legend" role="group" aria-label="模型占比图例">
            {segments.map((segment) => (
              <button key={segment.key} type="button" aria-pressed={selected?.key === segment.key}
                aria-expanded={segment.key === otherKey ? showMembers : undefined}
                aria-controls={segment.key === otherKey && showMembers ? membersId : undefined}
                onClick={() => select(segment.key)}>
                <i aria-hidden="true" style={{ backgroundColor: segment.color }} />
                <span>{segment.label}</span>
                <b>{shareText(segment.value, total)}</b>
                {segment.key === otherKey && <Glyph icon={ChevronDown} size={12} />}
              </button>
            ))}
          </div>
          {showMembers && (
            <ul id={membersId} className="composition-members" tabIndex={0} aria-label="其他模型明细">
              {remaining.map((segment) => (
                <li key={segment.key}>
                  <i aria-hidden="true" style={{ backgroundColor: segment.color }} />
                  <span>{segment.label}</span>
                  <b>{amount(segment.value)} <small>{unit}</small></b>
                  <small>{shareText(segment.value, total)}</small>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </>
  );
}

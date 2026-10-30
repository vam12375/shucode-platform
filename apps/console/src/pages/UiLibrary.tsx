import { useState } from "react";
import {
  Activity,
  Boxes,
  Building2,
  ChartColumn,
  Clapperboard,
  HandCoins,
  LayoutGrid,
  LogOut,
  PackageCheck,
  Palette,
  Plug,
  Plus,
  RefreshCw,
  ScrollText,
  Settings2,
  ShieldCheck,
  SlidersHorizontal,
  Smartphone,
  Tags,
  Trash2,
  UserCog,
  Users,
} from "lucide-react";
import { confirmDialog, showToast, toastErr, toastOk } from "../components/Feedback.js";
import { Gauge } from "../components/Gauge.js";
import { Glyph, PageTitle, type GlyphIcon } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice, type ChipTone } from "../components/StatusChip.js";

/**
 * UI 组件库（参考页，#/ui）：把「计量台」设计体系可视化的自查清单。
 * 只陈列全站真实用到的令牌与组件；新增状态/组件先改 design.md §9 再来此登记。
 */

interface SwatchRow {
  tone: ChipTone;
  /** styles.css 令牌名（swatch 与 Chip 色点同源）。 */
  dotVar: string;
  name: string;
  doc: string;
  dot: string;
  text: string;
  usage: string;
  chips: Array<{ label: string; where: string }>;
}

/** 五级语义色板：dot/text 色值与 styles.css 令牌一一对应。 */
const TONES: SwatchRow[] = [
  {
    tone: "ok",
    dotVar: "ok",
    name: "成功",
    doc: "--ok / --ok-text",
    dot: "#2E8B47",
    text: "#226B35",
    usage: "操作按预期完成，或资源当前可用——确定性、结论性的状态。",
    chips: [
      { label: "成功", where: "用量流水 / 用量明细" },
      { label: "生效", where: "团队 / 上游供应商" },
      { label: "有效", where: "设备凭据" },
      { label: "正常", where: "用户" },
      { label: "生效中", where: "内置配置发布" },
      { label: "通过", where: "供应商测试" },
    ],
  },
  {
    tone: "info",
    dotVar: "info",
    name: "进行中",
    doc: "--info / --info-text",
    dot: "#2E7DBF",
    text: "#1F5F92",
    usage: "异步动作已受理但未出结果——时间性状态，结束后必须转为 ok 或 stop。",
    chips: [
      { label: "进行中", where: "预留 · 长任务" },
      { label: "处理中", where: "预留 · 批量导入" },
    ],
  },
  {
    tone: "hot",
    dotVar: "amber",
    name: "告警",
    doc: "--amber / --amber-text",
    dot: "#E8A008",
    text: "#8A5C03",
    usage: "未失败但需要留意的临界态——额度逼近或客户端中断。",
    chips: [
      { label: "近额", where: "团队（用量 ≥ 90%）" },
      { label: "中断", where: "用量流水（客户端中止）" },
      { label: "是 → 否", where: "团队详情 · 不限额度切换标记" },
    ],
  },
  {
    tone: "stop",
    dotVar: "signal",
    name: "失败",
    doc: "--signal",
    dot: "#BF3A1F",
    text: "#BF3A1F",
    usage: "已发生错误或资源被强制终止——需要人工介入的状态。",
    chips: [
      { label: "错误", where: "用量流水" },
      { label: "已吊销", where: "设备凭据" },
      { label: "自动停用", where: "供应商（连续失败）" },
      { label: "测试失败", where: "供应商测试" },
    ],
  },
  {
    tone: "mute",
    dotVar: "line",
    name: "中性",
    doc: "--line / --ink-soft",
    dot: "#C7CBC1",
    text: "#666D70",
    usage: "降噪：非当前、非活跃或无语义结果——不参与告警但保留信息。",
    chips: [
      { label: "不限", where: "团队额度" },
      { label: "历史", where: "内置配置发布" },
      { label: "已禁用", where: "用户" },
      { label: "手动停用", where: "上游供应商" },
      { label: "未测", where: "供应商测试" },
    ],
  },
];

/** 图标清单：全站真实使用的客户端同款 lucide 图标（新增页面图标先改 design.md §10）。 */
const ICON_SAMPLES: Array<{ icon: GlyphIcon; label: string; where: string }> = [
  { icon: Users, label: "团队与账号", where: "导航" },
  { icon: ScrollText, label: "审计日志", where: "导航" },
  { icon: HandCoins, label: "额度申请", where: "导航" },
  { icon: ChartColumn, label: "用量", where: "导航" },
  { icon: Activity, label: "运维大盘", where: "导航" },
  { icon: PackageCheck, label: "内置配置发布", where: "导航" },
  { icon: Boxes, label: "模型目录", where: "导航" },
  { icon: Tags, label: "模型定价库", where: "导航" },
  { icon: SlidersHorizontal, label: "平台配置", where: "导航" },
  { icon: Plug, label: "上游供应商", where: "导航" },
  { icon: Clapperboard, label: "媒体模型", where: "导航" },
  { icon: LayoutGrid, label: "模型展示", where: "导航" },
  { icon: Smartphone, label: "设备凭据", where: "导航" },
  { icon: Building2, label: "团队概览", where: "导航" },
  { icon: ShieldCheck, label: "预算与限制", where: "导航" },
  { icon: Settings2, label: "团队设置", where: "导航" },
  { icon: UserCog, label: "个人设置", where: "导航" },
  { icon: Palette, label: "UI 组件库", where: "导航" },
  { icon: RefreshCw, label: "刷新 / 重试", where: "按钮" },
  { icon: Plus, label: "新增", where: "按钮" },
  { icon: Trash2, label: "删除", where: "按钮" },
  { icon: LogOut, label: "退出登录", where: "侧栏" },
];

function demoConfirmDialog(): void {
  void confirmDialog({
    k: "删除模型",
    title: "确认删除模型？",
    message: "demo-gpt-4.7-turbo 将从模型目录移除；已下发的内置配置不受影响。",
    confirmText: "删除",
    danger: true,
  }).then((result) => {
    if (result.confirmed) toastOk("演示：模型已删除（仅示例，无实际操作）", "已删除");
  });
}

function demoInputDialog(): void {
  void confirmDialog({
    k: "吊销设备",
    title: "输入设备名以确认吊销",
    message: "吊销后该设备立即失去调用资格，仅演示。",
    input: { label: "设备名", placeholder: "输入 dev-demo 确认", minLength: 4 },
    confirmText: "吊销",
    danger: true,
  }).then((result) => {
    if (result.confirmed) toastOk(`演示：已吊销（输入：${result.value}）`, "已吊销");
  });
}

export function UiLibraryPage() {
  const [selectValue, setSelectValue] = useState("claude-sonnet-4-6");
  const [switchOn, setSwitchOn] = useState(true);

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">参考 / 组件库</p>
          <PageTitle icon={Palette}>UI 组件库</PageTitle>
        </div>
        <span className="readout m0">「计量台」视觉语言 · lucide 图标（客户端同款）· 纯 CSS 令牌</span>
      </header>

      <section className="pane">
        <div className="pane-h">
          <h2>状态色板 · 五级语义</h2>
          <span className="readout">文字色对比度 ≥ 4.5:1（design.md §9）</span>
        </div>
        <div className="pane-b">
          <div className="swatch-grid">
            {TONES.map((row) => (
              <div className="swatch" key={row.tone}>
                <div className="swatch-name">
                  <span className="swatch-dot" style={{ background: `var(--${row.dotVar})` }} />
                  {row.name} · {row.tone}
                </div>
                <p className="swatch-hex">
                  {row.dot} / 文字 {row.text}
                </p>
                <p className="swatch-use">{row.usage}</p>
                <div className="swatch-chips">
                  {row.chips.map((chip) => (
                    <Chip key={chip.label} tone={row.tone} title={chip.where}>
                      {chip.label}
                    </Chip>
                  ))}
                </div>
              </div>
            ))}
            <div className="swatch">
              <div className="swatch-name">
                <span className="swatch-dot" style={{ background: "var(--ink)" }} />
                通用标签 · 缺省
              </div>
              <p className="swatch-hex">--ink（墨点，非状态）</p>
              <p className="swatch-use">与状态无关的中性标签：不要用于任何业务状态。</p>
              <div className="swatch-chips">
                <Chip title="上游返回的模型 ID 列表">grok-4.7-fast</Chip>
                <Chip title="上游返回的模型 ID 列表">qwen-max</Chip>
              </div>
            </div>
          </div>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>业务状态对照</h2>
          <span className="readout">同一状态必须全站同一 tone</span>
        </div>
        <div className="pane-b flush table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">状态</th>
                <th scope="col">tone</th>
                <th scope="col">出现位置</th>
              </tr>
            </thead>
            <tbody>
              {TONES.flatMap((row) =>
                row.chips.map((chip) => (
                  <tr key={`${chip.label}-${chip.where}`}>
                    <td>
                      <Chip tone={row.tone}>{chip.label}</Chip>
                    </td>
                    <td className="num">{row.tone}</td>
                    <td>{chip.where}</td>
                  </tr>
                )),
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section className="pane">
        <div className="pane-h">
          <h2>反馈与提示</h2>
          <span className="readout">Toast / 确认弹窗 / 提示条</span>
        </div>
        <div className="pane-b lib-pane">
          <div className="lib-row">
            <span className="lib-label">提示条</span>
            <Notice k="操作提示">上游模型列表已刷新，对照「上游模型」列填写映射。</Notice>
          </div>
          <div className="lib-row" style={{ marginTop: 10 }}>
            <span className="lib-label">错误条</span>
            <Notice tone="stop" k="加载失败">
              上游连接超时，请稍后重试。
            </Notice>
          </div>
        </div>
        <div className="pane-b lib-pane" style={{ paddingTop: 0 }}>
          <div className="lib-row">
            <span className="lib-label">Toast</span>
            <button className="btn btn-sm" type="button" onClick={() => toastOk("发布会话已归档", "操作成功")}>
              成功
            </button>
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              onClick={() => showToast("强制更新版本号会要求全体客户端升级", { tone: "warn", k: "注意" })}
            >
              警告
            </button>
            <button className="btn btn-danger btn-sm" type="button" onClick={() => toastErr("上游返回 401，请检查密钥", "测试失败")}>
              失败
            </button>
          </div>
          <div className="lib-row" style={{ marginTop: 10 }}>
            <span className="lib-label">确认弹窗</span>
            <button className="btn btn-sm" type="button" onClick={() => demoConfirmDialog()}>
              危险确认
            </button>
            <button className="btn btn-ghost btn-sm" type="button" onClick={() => demoInputDialog()}>
              带输入确认
            </button>
          </div>
        </div>
      </section>

      <section className="pane d1">
        <div className="pane-h">
          <h2>控件</h2>
          <span className="readout">按钮 / 输入 / 下拉 / 开关</span>
        </div>
        <div className="pane-b lib-pane">
          <div className="lib-row">
            <span className="lib-label">按钮</span>
            <button className="btn" type="button">主要</button>
            <button className="btn btn-ghost" type="button">次级</button>
            <button className="btn btn-danger" type="button">危险</button>
            <button className="btn btn-sm" type="button">小号</button>
            <button className="btn" type="button" disabled>禁用</button>
          </div>
          <div className="lib-row" style={{ marginTop: 10 }}>
            <span className="lib-label">输入</span>
            <input className="input" style={{ width: 200 }} placeholder="团队名称" aria-label="示例输入" />
            <input className="input num" style={{ width: 130 }} defaultValue="1000" aria-label="示例数字输入" />
            <Select
              ariaLabel="示例下拉"
              value={selectValue}
              onChange={setSelectValue}
              options={[
                { value: "claude-sonnet-4-6", label: "claude-sonnet-4-6" },
                { value: "grok-4.7", label: "grok-4.7" },
                { value: "qwen-max", label: "qwen-max" },
              ]}
            />
            <label className="switch" title="示例开关">
              <input
                type="checkbox"
                aria-label="示例开关"
                checked={switchOn}
                onChange={(event) => setSwitchOn(event.target.checked)}
              />
              <i />
            </label>
          </div>
        </div>
      </section>

      <section className="pane">
        <div className="pane-h">
          <h2>图标 · 客户端同款</h2>
          <span className="readout">lucide-react 1.17 · 16px / 2 描边 · 颜色继承 currentColor</span>
        </div>
        <div className="pane-b">
          <div className="icon-grid">
            {ICON_SAMPLES.map((sample) => (
              <div className="icon-cell" key={sample.label}>
                <Glyph icon={sample.icon} size={18} />
                <span className="icon-cell-label">{sample.label}</span>
                <span className="icon-cell-where">{sample.where}</span>
              </div>
            ))}
          </div>
          <p className="lib-foot" style={{ marginTop: 12 }}>
            图标只做语义强化，不替代文字：状态仍按「形状 + 颜色 + 文字」三重编码；不要用 unicode 字形或手写 SVG 代替。
          </p>
        </div>
      </section>

      <section className="pane">
        <div className="pane-h">
          <h2>额度尺 · 签名元素</h2>
          <span className="readout">填充 + 指针 + 刻度 + 读数</span>
        </div>
        <div className="pane-b lib-pane">
          <div className="lib-row">
            <span className="lib-label">常规</span>
            <div style={{ flex: 1, minWidth: 260 }}>
              <Gauge used={428.63} quota={1000} />
            </div>
          </div>
          <div className="lib-row" style={{ marginTop: 14 }}>
            <span className="lib-label">近额</span>
            <div style={{ flex: 1, minWidth: 260 }}>
              <Gauge used={934.5} quota={1000} />
            </div>
          </div>
          <div className="lib-row" style={{ marginTop: 14 }}>
            <span className="lib-label">不限额</span>
            <Gauge used={0} quota={null} />
          </div>
          <p className="lib-foot" style={{ marginTop: 10 }}>
            数字口径：credit 以 formatCreditsDisplay 展示（4 位小数以内）、token 千分位；读数进位动效遵循 prefers-reduced-motion。
          </p>
        </div>
      </section>
    </div>
  );
}

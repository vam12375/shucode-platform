import { useEffect, useRef, useState, type ReactNode } from "react";
import { loadPublicConfig } from "../publicConfig.js";
import { normalizeSignalVideoUrl } from "../signal-video.js";
import { SignalBackdrop } from "./SignalBackdrop.js";
import "./polar-auth.css";

// 控制台认证页外壳（登录 / 部署凭据 / 创建管理员 / 邀请 / MFA 门禁共用），
// 与设备登录网页同一 Signal 视觉，规范见 specs/device-login-pages.md。
// 只负责呈现；表单字段、提交与会话逻辑仍归各页面所有。

const GLYPHS = "▚▞▖▗▘▙▛▜▟#/<>_=+01";

// 标题解码：用定时器而非 rAF（后台标签页会暂停 rAF，可能停在乱码），并在时长后兜底恢复原文。
function ScrambleLine({ text, delay }: { text: string; delay: number }) {
  const [shown, setShown] = useState(text);
  useEffect(() => {
    if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
    const total = text.length * 3 + 18;
    let step = 0;
    let interval = 0;
    const start = window.setTimeout(() => {
      interval = window.setInterval(() => {
        step += 1;
        if (step >= total) {
          window.clearInterval(interval);
          setShown(text);
          return;
        }
        setShown(Array.from(text, (char, index) => (char === " " || step > index * 3 + 10 ? char : GLYPHS[(Math.random() * GLYPHS.length) | 0])).join(""));
      }, 16);
    }, delay);
    const settle = window.setTimeout(() => { window.clearInterval(interval); setShown(text); }, delay + total * 16 + 400);
    return () => { window.clearTimeout(start); window.clearTimeout(settle); window.clearInterval(interval); };
  }, [text, delay]);
  return <span aria-label={text}>{shown}</span>;
}

function useClock() {
  const [now, setNow] = useState(() => new Date().toTimeString().slice(0, 8));
  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date().toTimeString().slice(0, 8)), 1000);
    return () => window.clearInterval(timer);
  }, []);
  return now;
}

// 面板顶部：终端标题栏 + 命令日志（替代旧的圆形「门」品牌块，各页面调用方式不变）。
export function PolarAuthBrand({ command = "mendao console --auth", status = "AWAITING_AUTH" }: { command?: string; status?: string }) {
  const clock = useClock();
  return <div className="signal-term-head" aria-label="门道控制台">
    <div className="signal-term-bar"><span><i className="signal-led" aria-hidden="true" />console.mendao</span><span aria-hidden="true">{clock}</span></div>
    <div className="signal-log" aria-hidden="true">
      <span className="p">$ {command}</span>{"\n"}
      <span className="w">  status     {status}</span><span className="signal-caret" />
    </div>
  </div>;
}

export function PolarAuthFrame({ children }: { children: ReactNode }) {
  const [videoUrl, setVideoUrl] = useState("");
  const frameRef = useRef<HTMLElement>(null);
  useEffect(() => {
    let cancelled = false;
    void loadPublicConfig().then((config) => {
      if (!cancelled) setVideoUrl(normalizeSignalVideoUrl(config?.signalVideoUrl));
    });
    return () => { cancelled = true; };
  }, []);
  return <div className="login-page polar-auth-page signal-auth">
    <SignalBackdrop videoUrl={videoUrl} frameRef={frameRef} />
    <div className="signal-hud" aria-hidden="true"><i className="tl" /><i className="tr" /><i className="bl" /><i className="br" /></div>
    <div className="signal-ui">
      <header className="signal-top signal-m">
        <div className="signal-brand"><span className="signal-mark" aria-hidden="true" /><b>MENDAO / 门道</b></div>
        <div className="signal-stats" aria-hidden="true"><span>SIGNAL</span><b>CONSOLE</b><span>FRAME</span><b ref={frameRef}>0000</b><span>DITHER</span><b>BAYER 8×8</b></div>
      </header>
      <section className="signal-hero" aria-hidden="true">
        <div className="signal-kicker signal-m">Mendao Console</div>
        <p className="signal-display"><ScrambleLine text="From noise" delay={200} /><ScrambleLine text="to motion." delay={460} /></p>
        <p className="signal-zh">从一束信号，到一部影片</p>
      </section>
      <p className="signal-legend signal-m" aria-hidden="true"><span><b>●</b> 移动鼠标查看原片</span><span>点击投下涟漪</span></p>
      <div className="signal-panel">{children}</div>
    </div>
  </div>;
}

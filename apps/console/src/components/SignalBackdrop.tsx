import { useEffect, useRef, type RefObject } from "react";
import { SIGNAL_FRAGMENT_SHADER, SIGNAL_UNIFORMS, SIGNAL_VERTEX_SHADER } from "./signal/signalShader.js";

// Signal 背景（specs/device-login-pages.md）：信号源 → Bayer 8×8 有序抖动双色调，鼠标处透出“原片”。
// 有 videoUrl 时加载视频并在就绪后从噪声平滑过渡；无视频、失败、无 WebGL 或减弱动态时，
// 只保留 CSS 静态点阵（.signal-bg 自身背景），认证表单不依赖本组件。
export function SignalBackdrop({ videoUrl, frameRef }: { videoUrl: string; frameRef?: RefObject<HTMLElement | null> }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const tagRef = useRef<HTMLDivElement>(null);
  const videoUrlRef = useRef(videoUrl);
  const attachVideoRef = useRef<((url: string) => void) | null>(null);

  useEffect(() => {
    videoUrlRef.current = videoUrl;
    attachVideoRef.current?.(videoUrl);
  }, [videoUrl]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
    const canvas = document.createElement("canvas");
    canvas.setAttribute("aria-hidden", "true");
    const gl = canvas.getContext("webgl", { antialias: false, premultipliedAlpha: false });
    if (!gl) return;
    const shader = (type: number, source: string) => {
      const created = gl.createShader(type);
      if (!created) return null;
      gl.shaderSource(created, source);
      gl.compileShader(created);
      return gl.getShaderParameter(created, gl.COMPILE_STATUS) ? created : null;
    };
    const vs = shader(gl.VERTEX_SHADER, SIGNAL_VERTEX_SHADER);
    const fs = shader(gl.FRAGMENT_SHADER, SIGNAL_FRAGMENT_SHADER);
    const program = gl.createProgram();
    if (!vs || !fs || !program) return;
    gl.attachShader(program, vs);
    gl.attachShader(program, fs);
    gl.linkProgram(program);
    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) return;
    gl.useProgram(program);
    gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
    const position = gl.getAttribLocation(program, "p");
    gl.enableVertexAttribArray(position);
    gl.vertexAttribPointer(position, 2, gl.FLOAT, false, 0, 0);
    const uniform = Object.fromEntries(SIGNAL_UNIFORMS.map((name) => [name, gl.getUniformLocation(program, name)])) as Record<(typeof SIGNAL_UNIFORMS)[number], WebGLUniformLocation | null>;
    const texture = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, texture);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);

    let video: HTMLVideoElement | null = null;
    let mix = 0;
    let mixTarget = 0;
    const releaseVideo = () => {
      if (!video) return;
      video.pause();
      video.removeAttribute("src");
      video.load();
      video = null;
      mixTarget = 0;
    };
    const attachVideo = (url: string) => {
      if (video?.dataset.src === url) return;
      releaseVideo();
      if (!url) return;
      const next = document.createElement("video");
      next.muted = true;
      next.loop = true;
      next.playsInline = true;
      next.preload = "auto";
      // 视频经媒体 CDN 跨源分发：CORS 匿名模式加载，WebGL 读帧才不会污染 canvas。
      next.crossOrigin = "anonymous";
      next.dataset.src = url;
      next.addEventListener("error", () => { if (video === next) releaseVideo(); });
      next.src = url;
      void next.play().catch(() => {});
      video = next;
    };
    attachVideoRef.current = attachVideo;
    attachVideo(videoUrlRef.current);

    host.appendChild(canvas);
    host.classList.add("live");
    let dpr = 1;
    let width = 0;
    let height = 0;
    const resize = () => {
      dpr = Math.min(window.devicePixelRatio || 1, 2);
      width = canvas.width = Math.round(window.innerWidth * dpr);
      height = canvas.height = Math.round(window.innerHeight * dpr);
      gl.viewport(0, 0, width, height);
    };
    resize();
    let tx = window.innerWidth * 0.4;
    let ty = window.innerHeight * 0.45;
    let mx = tx;
    let my = ty;
    let radius = 0;
    let radiusTarget = 150;
    let touched = false;
    let click: [number, number] = [-9999, -9999];
    let clickAt = -99;
    const started = performance.now();
    const onMove = (event: PointerEvent) => { tx = event.clientX; ty = event.clientY; touched = true; };
    const onDown = (event: PointerEvent) => {
      const target = event.target as Element | null;
      if (target?.closest?.(".login-card,a,button,input,summary,label")) return;
      click = [event.clientX * dpr, (window.innerHeight - event.clientY) * dpr];
      clickAt = performance.now() / 1000;
    };
    // 镜头移到面板上时收小，避免干扰阅读与输入。
    const onOver = (event: PointerEvent) => {
      const target = event.target as Element | null;
      radiusTarget = target?.closest?.(".login-card,.signal-top") ? 34 : window.innerWidth < 860 ? 110 : 150;
    };
    window.addEventListener("resize", resize);
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerdown", onDown);
    document.addEventListener("pointerover", onOver);

    let frame = 0;
    const render = () => {
      const now = performance.now() / 1000;
      if (!touched) {
        tx = window.innerWidth * (0.38 + 0.16 * Math.sin(now * 0.35));
        ty = window.innerHeight * (0.45 + 0.14 * Math.sin(now * 0.53));
      }
      mx += (tx - mx) * 0.12;
      my += (ty - my) * 0.12;
      radius += (radiusTarget - radius) * 0.08;
      if (video && video.readyState >= 2) {
        try {
          gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGB, gl.RGB, gl.UNSIGNED_BYTE, video);
          mixTarget = 1;
        } catch {
          // 视频帧暂不可读（跨源未授权或解码中）时保持程序噪声。
          mixTarget = 0;
        }
      }
      mix += (mixTarget - mix) * 0.04;
      gl.uniform1f(uniform.mixV, mix);
      gl.uniform2f(uniform.vid, video?.videoWidth || 16, video?.videoHeight || 9);
      gl.uniform2f(uniform.res, width, height);
      gl.uniform2f(uniform.mouse, mx * dpr, (window.innerHeight - my) * dpr);
      gl.uniform2f(uniform.click, click[0], click[1]);
      gl.uniform1f(uniform.time, now);
      gl.uniform1f(uniform.clickT, clickAt);
      gl.uniform1f(uniform.px, Math.round(3 * dpr));
      gl.uniform1f(uniform.r, radius * dpr);
      gl.uniform1f(uniform.dpr, dpr);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
      const tag = tagRef.current;
      if (tag) {
        tag.style.transform = `translate(${mx}px, ${my + radius + 12}px) translateX(-50%)`;
        tag.style.opacity = radius > 60 ? "1" : "0";
      }
      const seconds = video && mixTarget ? video.currentTime : (performance.now() - started) / 1000;
      if (frameRef?.current) frameRef.current.textContent = String(Math.floor(seconds * 24) % 10000).padStart(4, "0");
      frame = requestAnimationFrame(render);
    };
    frame = requestAnimationFrame(render);

    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("resize", resize);
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerdown", onDown);
      document.removeEventListener("pointerover", onOver);
      attachVideoRef.current = null;
      releaseVideo();
      gl.getExtension("WEBGL_lose_context")?.loseContext();
      canvas.remove();
      host.classList.remove("live");
    };
  }, [frameRef]);

  return <>
    <div className="signal-bg" ref={hostRef} aria-hidden="true" />
    <div className="signal-lens-tag" ref={tagRef} aria-hidden="true">RAW · 原片</div>
  </>;
}

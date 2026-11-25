/* 设备登录网页 · Signal 视觉脚本（规范：specs/device-login-pages.md）
 *
 * 只负责呈现：WebGL 抖动背景、标题解码、终端日志打字、toast、提交忙碌态、密码显隐。
 * 不发请求、不持有登录状态；关闭 JS 时表单与授权流程照常可用。
 * 页面不含任何内联脚本 / on* 处理器，CSP 保持 script-src 'self'。 */
(function () {
  "use strict";
  var doc = document;
  var body = doc.body;
  var reduceMotion = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  function $(sel, root) { return (root || doc).querySelector(sel); }
  function $$(sel, root) { return Array.prototype.slice.call((root || doc).querySelectorAll(sel)); }

  /* ================= Toast ================= */
  var toastRoot = $(".toasts");
  function toast(opts) {
    if (!toastRoot || !opts || !opts.title) return;
    var tone = opts.tone === "error" || opts.tone === "info" ? opts.tone : "ok";
    var el = doc.createElement("div");
    el.className = "toast";
    el.setAttribute("data-tone", tone);
    el.setAttribute("role", tone === "error" ? "alert" : "status");
    var ic = doc.createElement("span");
    ic.className = "ic";
    ic.setAttribute("aria-hidden", "true");
    ic.textContent = tone === "error" ? "!" : tone === "info" ? "i" : "✓";
    var text = doc.createElement("div");
    var b = doc.createElement("b");
    b.textContent = opts.title;
    text.appendChild(b);
    if (opts.desc) {
      var p = doc.createElement("p");
      p.textContent = opts.desc;
      text.appendChild(p);
    }
    var x = doc.createElement("button");
    x.type = "button";
    x.className = "x";
    x.setAttribute("aria-label", "关闭提示");
    x.textContent = "×";
    var life = doc.createElement("i");
    life.className = "life";
    el.appendChild(ic);
    el.appendChild(text);
    el.appendChild(x);
    el.appendChild(life);
    function dismiss() {
      if (el.classList.contains("out")) return;
      el.classList.add("out");
      setTimeout(function () { if (el.parentNode) el.parentNode.removeChild(el); }, 420);
    }
    x.addEventListener("click", dismiss);
    // 进度条走完即消失；悬停时进度条暂停（CSS），保证用户读完。
    life.addEventListener("animationend", dismiss);
    if (reduceMotion) setTimeout(dismiss, 6000);
    toastRoot.appendChild(el);
  }
  // 服务端把提示文案写在 body[data-toast-*]（已转义），这里只负责展示。
  if (body.getAttribute("data-toast-title")) {
    setTimeout(function () {
      toast({
        title: body.getAttribute("data-toast-title"),
        desc: body.getAttribute("data-toast-desc"),
        tone: body.getAttribute("data-toast-tone"),
      });
    }, reduceMotion ? 0 : 420);
  }

  /* ================= 表单：忙碌态 / 防重复提交 ================= */
  $$("form[data-busy]").forEach(function (form) {
    form.addEventListener("submit", function (event) {
      if (form.getAttribute("data-submitting") === "1") {
        event.preventDefault();
        return;
      }
      form.setAttribute("data-submitting", "1");
      // 同页其它表单（如「授权」与「取消」）一并锁定，避免并发提交两个决定。
      $$("form[data-busy]").forEach(function (other) { if (other !== form) other.setAttribute("data-submitting", "1"); });
      var btn = event.submitter || $("button[type=submit]", form);
      if (btn) {
        btn.setAttribute("aria-busy", "true");
        var label = $("[data-label]", btn);
        if (label && form.getAttribute("data-busy")) label.textContent = form.getAttribute("data-busy");
      }
    });
  });
  // 从 bfcache 返回时复原按钮，避免停在「验证中」。
  window.addEventListener("pageshow", function (event) {
    if (!event.persisted) return;
    $$("form[data-busy]").forEach(function (form) {
      form.removeAttribute("data-submitting");
      $$("[aria-busy]", form).forEach(function (btn) {
        btn.removeAttribute("aria-busy");
        var label = $("[data-label]", btn);
        if (label) label.textContent = label.getAttribute("data-label");
      });
    });
  });

  /* ================= 密码显隐 ================= */
  $$("[data-reveal]").forEach(function (btn) {
    var input = doc.getElementById(btn.getAttribute("data-reveal"));
    if (!input) return;
    btn.hidden = false;
    btn.addEventListener("click", function () {
      var show = input.type === "password";
      input.type = show ? "text" : "password";
      btn.setAttribute("aria-pressed", show ? "true" : "false");
      btn.textContent = show ? "隐藏" : "显示";
      input.focus();
    });
  });

  /* ================= 时钟 ================= */
  var clock = $("[data-clock]");
  if (clock) {
    var tick = function () { clock.textContent = new Date().toTimeString().slice(0, 8); };
    tick();
    setInterval(tick, 1000);
  }

  if (reduceMotion) return;

  /* ================= 标题解码 ================= */
  var glyph = "▚▞▖▗▘▙▛▜▟#/<>_=+01";
  $$("[data-scr]").forEach(function (el, idx) {
    var final = el.textContent;
    var f = 0;
    var total = final.length * 3 + 18;
    el.setAttribute("aria-label", final);
    // 用定时器而不是 rAF：后台标签页会暂停 rAF，标题可能停在乱码；
    // 另设兜底，无论如何在动画时长后恢复原文。
    setTimeout(function () {
      var timer = setInterval(function () {
        var out = "";
        for (var i = 0; i < final.length; i++) {
          var ch = final.charAt(i);
          out += ch === " " ? " " : f > i * 3 + 10 ? ch : glyph.charAt((Math.random() * glyph.length) | 0);
        }
        el.textContent = out;
        if (f++ >= total) {
          clearInterval(timer);
          el.textContent = final;
        }
      }, 16);
      setTimeout(function () { clearInterval(timer); el.textContent = final; }, total * 16 + 400);
    }, 200 + idx * 260);
  });

  /* ================= 终端日志打字 ================= */
  var log = $(".log");
  if (log) {
    var lines = $$(".log > span");
    var texts = lines.map(function (l) { return l.textContent; });
    lines.forEach(function (l) { l.textContent = ""; });
    var caret = doc.createElement("span");
    caret.className = "caret";
    caret.setAttribute("aria-hidden", "true");
    var li = 0;
    var ci = 0;
    var typer = setInterval(function () {
      if (li >= lines.length) {
        clearInterval(typer);
        log.appendChild(caret);
        return;
      }
      ci++;
      lines[li].textContent = texts[li].slice(0, ci);
      if (ci >= texts[li].length) { li++; ci = 0; }
    }, 14);
  }

  /* ================= WebGL：信号源 → 有序抖动双色调，鼠标处透出“原片” =================
   * 信号源：有内置视频（.sig-bg[data-video]）时使用视频，就绪后从噪声平滑过渡；
   * 没有视频或视频加载失败时，使用两层流动的值噪声（与原型 B 的降级效果一致）。 */
  var bg = $(".sig-bg");
  if (!bg) return;
  var cv = doc.createElement("canvas");
  cv.setAttribute("aria-hidden", "true");
  var gl = cv.getContext("webgl", { antialias: false, premultipliedAlpha: false });
  if (!gl) return; // 保持静态 poster

  var VS = "attribute vec2 p;void main(){gl_Position=vec4(p,0,1);}";
  var FS = [
    "precision highp float;",
    "uniform sampler2D t;uniform vec2 res,vid,mouse,click;uniform float time,px,r,clickT,dpr,mixV;",
    "float b2(vec2 a){a=floor(a);return fract(a.x/2.+a.y*a.y*.75);}",
    "float b4(vec2 a){return b2(.5*a)*.25+b2(a);}",
    "float b8(vec2 a){return b4(.5*a)*.25+b2(a);}",
    "float hash(vec2 p){return fract(sin(dot(p,vec2(41.3,289.1)))*43758.5);}",
    "float noise(vec2 p){vec2 i=floor(p),f=fract(p);f=f*f*(3.-2.*f);return mix(mix(hash(i),hash(i+vec2(1,0)),f.x),mix(hash(i+vec2(0,1)),hash(i+1.),f.x),f.y);}",
    "vec2 cover(vec2 u){return u;}",
    "vec3 src(vec2 u){vec2 a=vec2((u.x-.5)*res.x/res.y+.5,u.y);float n=noise(a*4.+time*.15)*.6+noise(a*11.-time*.2)*.4;vec3 nc=vec3(n);if(mixV<.001)return nc;float sr=res.x/res.y,vr=vid.x/vid.y;vec2 s=sr>vr?vec2(1.,vr/sr):vec2(sr/vr,1.);vec2 v=(u-.5)*s+.5;return mix(nc,texture2D(t,vec2(v.x,1.-v.y)).rgb,mixV);}",
    "void main(){",
    "  vec2 fc=gl_FragCoord.xy;",
    "  float cd=distance(fc,click);float age=time-clickT;",
    "  float wave=sin(cd*.045-age*9.)*exp(-age*1.6)*(1.-smoothstep(age*620.-60.,age*620.+60.,cd))*step(0.,age);",
    "  vec2 dir=normalize(fc-click+.001);",
    "  vec2 cell=floor((fc+dir*wave*18.)/px)*px+px*.5;",
    "  vec2 u=cover(cell/res);",
    "  u+=vec2(noise(u*3.+time*.1),noise(u*3.-time*.1)-.5)*.004;",
    "  float l=dot(src(u),vec3(.299,.587,.114));",
    "  l=pow(smoothstep(.06,.95,l),1.6)*.92;",
    "  float on=step(b8(fc/px),l);",
    "  vec3 ink=vec3(.047,.047,.051),paper=vec3(.925,.902,.847),sig=vec3(1.,.415,.17);",
    "  vec3 dith=mix(ink,paper,on);",
    "  float d=distance(fc,mouse);",
    "  float lens=1.-smoothstep(r-1.5*dpr,r+1.5*dpr,d);",
    "  vec2 off=(fc-mouse)/res;float k=1.-.35*(1.-d*d/max(r*r,1.));",
    "  vec2 lc=cover((mouse+(fc-mouse)*k)/res);",
    "  vec3 raw=vec3(src(lc+off*.012).r,src(lc).g,src(lc-off*.012).b);",
    "  raw=pow(raw,vec3(.92))*1.08;",
    "  float ring=smoothstep(2.*dpr,0.,abs(d-r))*.9;",
    "  float halo=smoothstep(r*1.9,r,d)*(1.-lens);",
    "  dith=mix(dith,mix(ink,sig,on),halo*.85);",
    "  vec3 col=mix(dith,raw,lens);",
    "  col=mix(col,sig,ring);",
    "  col*=.94+.06*sin(fc.y*1.6/dpr);",
    "  gl_FragColor=vec4(col,1.);",
    "}",
  ].join("\n");
  function shader(type, src) {
    var s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    return gl.getShaderParameter(s, gl.COMPILE_STATUS) ? s : null;
  }
  var vs = shader(gl.VERTEX_SHADER, VS);
  var fs = shader(gl.FRAGMENT_SHADER, FS);
  if (!vs || !fs) return;
  var pr = gl.createProgram();
  gl.attachShader(pr, vs);
  gl.attachShader(pr, fs);
  gl.linkProgram(pr);
  if (!gl.getProgramParameter(pr, gl.LINK_STATUS)) return;
  gl.useProgram(pr);
  gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  var pl = gl.getAttribLocation(pr, "p");
  gl.enableVertexAttribArray(pl);
  gl.vertexAttribPointer(pl, 2, gl.FLOAT, false, 0, 0);
  var U = {};
  ["t", "res", "vid", "mouse", "click", "time", "px", "r", "clickT", "dpr", "mixV"].forEach(function (n) { U[n] = gl.getUniformLocation(pr, n); });
  var tex = gl.createTexture();
  gl.bindTexture(gl.TEXTURE_2D, tex);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);

  // 视频只在服务端提供地址且 WebGL 可用时才加载；失败则一直保持程序噪声。
  var video = null;
  var mixV = 0, mixTarget = 0;
  var videoUrl = bg.getAttribute("data-video");
  if (videoUrl) {
    video = doc.createElement("video");
    video.muted = true;
    video.loop = true;
    video.playsInline = true;
    video.setAttribute("playsinline", "");
    video.preload = "auto";
    // 视频经媒体 CDN 跨源分发：必须以 CORS 匿名模式加载，WebGL 才能读取帧（否则 canvas 被污染）。
    video.crossOrigin = "anonymous";
    video.addEventListener("error", function () { mixTarget = 0; video = null; });
    video.src = videoUrl;
    var playing = video.play();
    if (playing && playing.catch) playing.catch(function () {});
  }
  bg.appendChild(cv);
  bg.classList.add("live");

  var dpr = 1, W = 0, H = 0;
  function resize() {
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    W = cv.width = Math.round(window.innerWidth * dpr);
    H = cv.height = Math.round(window.innerHeight * dpr);
    gl.viewport(0, 0, W, H);
  }
  window.addEventListener("resize", resize);
  resize();

  var tag = $(".lens-tag");
  var frameEl = $("[data-frame]");
  var tx = window.innerWidth * 0.4, ty = window.innerHeight * 0.45, mx = tx, my = ty;
  var rad = 0, radT = 150, touched = false, t0 = performance.now();
  var clk = [-9999, -9999], clkT = -99;
  window.addEventListener("pointermove", function (e) { tx = e.clientX; ty = e.clientY; touched = true; });
  window.addEventListener("pointerdown", function (e) {
    if (e.target.closest && e.target.closest(".term,.toast,a,button,input")) return;
    clk = [e.clientX * dpr, (window.innerHeight - e.clientY) * dpr];
    clkT = performance.now() / 1000;
  });
  // 镜头移到面板上时收小，避免干扰阅读与输入。
  doc.addEventListener("pointerover", function (e) {
    var onUi = e.target.closest && e.target.closest(".term,.top,.toasts");
    radT = onUi ? 34 : window.innerWidth < 860 ? 110 : 150;
  });

  function render() {
    var now = performance.now() / 1000;
    if (!touched) {
      tx = window.innerWidth * (0.38 + 0.16 * Math.sin(now * 0.35));
      ty = window.innerHeight * (0.45 + 0.14 * Math.sin(now * 0.53));
    }
    mx += (tx - mx) * 0.12;
    my += (ty - my) * 0.12;
    rad += (radT - rad) * 0.08;
    if (video && video.readyState >= 2) {
      try {
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGB, gl.RGB, gl.UNSIGNED_BYTE, video);
        mixTarget = 1;
      } catch {
        // 视频帧暂不可读（如跨源或解码中）时保持程序噪声。
        mixTarget = 0;
      }
    }
    mixV += (mixTarget - mixV) * 0.04;
    gl.uniform1f(U.mixV, mixV);
    gl.uniform2f(U.vid, (video && video.videoWidth) || 16, (video && video.videoHeight) || 9);
    gl.uniform2f(U.res, W, H);
    gl.uniform2f(U.mouse, mx * dpr, (window.innerHeight - my) * dpr);
    gl.uniform2f(U.click, clk[0], clk[1]);
    gl.uniform1f(U.time, now);
    gl.uniform1f(U.clickT, clkT);
    gl.uniform1f(U.px, Math.round(3 * dpr));
    gl.uniform1f(U.r, rad * dpr);
    gl.uniform1f(U.dpr, dpr);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    if (tag) {
      tag.style.left = mx + "px";
      tag.style.top = my + rad + 12 + "px";
      tag.style.opacity = rad > 60 ? "1" : "0";
    }
    var secs = video && mixTarget ? video.currentTime : (now * 1000 - t0) / 1000;
    if (frameEl) frameEl.textContent = String(Math.floor(secs * 24) % 10000).padStart(4, "0");
    requestAnimationFrame(render);
  }
  requestAnimationFrame(render);
})();

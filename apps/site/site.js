(() => {
  "use strict";
  const root = document.documentElement;
  const gsap = window.gsap;
  const scrollTrigger = window.ScrollTrigger;
  const systemMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
  const finePointer = window.matchMedia("(hover: hover) and (pointer: fine)");
  const doorSpace = document.getElementById("door-space");
  const doorButton = document.getElementById("door-toggle");
  const doorLeaf = document.querySelector(".door-leaf");
  const doorCat = document.querySelector(".door-cat");
  const doorHint = document.getElementById("door-hint-text");
  const motionButton = document.getElementById("motion-toggle");
  const tabs = [...document.querySelectorAll('[role="tab"]')];
  const panels = [...document.querySelectorAll('[role="tabpanel"]')];
  const scene = document.querySelector(".scene");
  let doorOpen = true;
  let userPaused = false;
  let motionContext;
  let pointerFrame = 0;
  let pointerX = 0;
  let pointerY = 0;
  const motionAllowed = () => !systemMotion.matches && !userPaused;
  const canAnimate = () => Boolean(gsap) && motionAllowed();
  if (gsap && scrollTrigger) gsap.registerPlugin(scrollTrigger);

  const promoVideo = document.getElementById("promo-video");
  const filmPlay = document.getElementById("film-play");
  const filmLabel = document.getElementById("film-play-label");
  const filmStatus = document.getElementById("film-status");
  promoVideo.controls = false;
  filmPlay.hidden = false;
  function showFilmRetry() {
    filmPlay.hidden = false;
    filmPlay.disabled = false;
    filmLabel.textContent = "重新播放";
    filmStatus.hidden = false;
  }
  filmPlay.addEventListener("click", async () => {
    filmPlay.disabled = true;
    filmLabel.textContent = "正在加载…";
    filmStatus.hidden = true;
    promoVideo.controls = true;
    try {
      if (promoVideo.error) promoVideo.load();
      await promoVideo.play();
      // 自定义播放入口隐藏后，把焦点交给原生控件，便于继续用键盘操作。
      promoVideo.focus({ preventScroll: true });
    } catch {
      showFilmRetry();
    }
  });
  promoVideo.addEventListener("playing", () => {
    filmPlay.hidden = true;
    filmPlay.disabled = false;
    filmStatus.hidden = true;
  });
  promoVideo.addEventListener("ended", () => {
    filmPlay.hidden = false;
    filmPlay.disabled = false;
    filmLabel.textContent = "再看一次";
  });
  promoVideo.addEventListener("error", showFilmRetry);

  function openPanel(index, moveFocus = false) {
    const target = panels[index];
    if (!target) return;
    // 统一同步 aria 与可见性；快速点击时终止旧动效，避免迟到回调重新显示旧面板。
    if (gsap) gsap.killTweensOf(document.querySelectorAll(".demo-piece"));
    tabs.forEach((tab, tabIndex) => {
      const selected = tabIndex === index;
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
      tab.classList.toggle("is-active", selected);
      panels[tabIndex].hidden = !selected;
    });
    if (moveFocus) tabs[index].focus();
    if (canAnimate()) {
      gsap.fromTo(
        target.querySelectorAll(".demo-piece"),
        { opacity: 0, y: 12 },
        {
          opacity: 1,
          y: 0,
          duration: 0.42,
          stagger: 0.055,
          ease: "power2.out",
          clearProps: "opacity,transform",
        },
      );
    } else if (gsap) {
      gsap.set(target.querySelectorAll(".demo-piece"), {
        clearProps: "opacity,transform",
      });
    }
    if (scrollTrigger) scrollTrigger.refresh();
  }
  document.querySelector(".demo-tabs").hidden = false;
  tabs.forEach((tab, index) => {
    tab.addEventListener("click", () => openPanel(index));
    tab.addEventListener("keydown", (event) => {
      let target;
      if (event.key === "ArrowRight" || event.key === "ArrowDown")
        target = (index + 1) % tabs.length;
      else if (event.key === "ArrowLeft" || event.key === "ArrowUp")
        target = (index + tabs.length - 1) % tabs.length;
      else if (event.key === "Home") target = 0;
      else if (event.key === "End") target = tabs.length - 1;
      else return;
      event.preventDefault();
      openPanel(target, true);
    });
  });
  doorButton.disabled = false;
  doorButton.addEventListener("click", () => {
    const from = gsap
      ? Number(gsap.getProperty(doorLeaf, "rotationY"))
      : doorOpen
        ? -108
        : 0;
    doorOpen = !doorOpen;
    doorSpace.classList.toggle("is-open", doorOpen);
    doorButton.setAttribute("aria-pressed", String(doorOpen));
    doorButton.setAttribute(
      "aria-label",
      doorOpen ? "合上门道之门" : "打开门道之门",
    );
    doorHint.textContent = doorOpen
      ? "你好，新想法。"
      : "再推开一次，看看新的可能";
    if (!canAnimate()) return;
    gsap.fromTo(
      doorLeaf,
      { rotationY: from },
      {
        rotationY: doorOpen ? -108 : 0,
        duration: 1.05,
        ease: "power3.inOut",
        overwrite: true,
        onComplete: () => gsap.set(doorLeaf, { clearProps: "transform" }),
      },
    );
    if (doorOpen) {
      gsap.fromTo(
        doorCat,
        { y: 17, scale: 0.92 },
        {
          y: 0,
          scale: 1,
          duration: 0.85,
          delay: 0.2,
          ease: "back.out(1.8)",
          overwrite: true,
          clearProps: "transform",
        },
      );
      gsap.fromTo(
        ".scene-chip",
        { y: 9 },
        {
          y: 0,
          duration: 0.65,
          stagger: 0.07,
          ease: "back.out(2)",
          overwrite: true,
          clearProps: "transform",
        },
      );
    }
  });
  function clearPointer() {
    cancelAnimationFrame(pointerFrame);
    pointerFrame = 0;
    scene.style.removeProperty("--scene-x");
    scene.style.removeProperty("--scene-y");
  }
  scene.addEventListener("pointermove", (event) => {
    if (!motionAllowed() || !finePointer.matches) return;
    const box = scene.getBoundingClientRect();
    pointerX = (event.clientX - box.left) / box.width - 0.5;
    pointerY = (event.clientY - box.top) / box.height - 0.5;
    if (pointerFrame) return;
    pointerFrame = requestAnimationFrame(() => {
      scene.style.setProperty("--scene-y", `${pointerX * 6}deg`);
      scene.style.setProperty("--scene-x", `${-pointerY * 4}deg`);
      pointerFrame = 0;
    });
  });
  scene.addEventListener("pointerleave", clearPointer);
  finePointer.addEventListener("change", clearPointer);
  function configureMotion(initial = false) {
    motionContext?.revert();
    motionContext = undefined;
    if (gsap) {
      gsap.killTweensOf([
        doorLeaf,
        doorCat,
        ...document.querySelectorAll(".demo-piece,.scene-chip"),
      ]);
      gsap.set(
        [
          doorLeaf,
          doorCat,
          ...document.querySelectorAll(".demo-piece,.scene-chip"),
        ],
        { clearProps: "opacity,transform" },
      );
    }
    clearPointer();
    const paused = !motionAllowed();
    root.classList.toggle("motion-paused", paused);
    motionButton.setAttribute("aria-pressed", String(paused));
    motionButton.disabled = systemMotion.matches;
    motionButton.title = systemMotion.matches
      ? "已跟随系统的减少动态效果设置"
      : "";
    motionButton.querySelector(".motion-label").textContent = paused
      ? "动态已暂停"
      : "暂停动态";
    motionButton.querySelector("span:first-child").textContent = paused
      ? "▷"
      : "Ⅱ";
    if (!canAnimate()) return;
    motionContext = gsap.context(() => {
      if (initial) {
        gsap
          .timeline({ defaults: { ease: "power3.out" } })
          .from(".site-header", { opacity: 0, y: -12, duration: 0.65 })
          .from(
            ".title-word",
            { yPercent: 105, duration: 0.95, stagger: 0.12 },
            0.1,
          )
          .from(
            ".hero-enter",
            { opacity: 0, y: 20, duration: 0.85, stagger: 0.08 },
            0.27,
          )
          .fromTo(
            doorLeaf,
            { rotationY: 0 },
            {
              rotationY: -108,
              duration: 1.5,
              ease: "power3.inOut",
              clearProps: "transform",
            },
            0.8,
          )
          .from(".hero-bottom", { opacity: 0, y: 10, duration: 0.6 }, 0.8);
      }
      if (scrollTrigger) {
        gsap.utils.toArray(".reveal").forEach((element) => {
          gsap.from(element, {
            opacity: 0,
            y: 30,
            duration: 0.85,
            ease: "power2.out",
            scrollTrigger: {
              trigger: element,
              start: "top 94%",
              toggleActions: "play none none none",
              once: true,
            },
            clearProps: "opacity,transform",
          });
        });
        const path = document.querySelector(".flow-draw");
        const length = path.getTotalLength();
        gsap.fromTo(
          path,
          { strokeDasharray: length, strokeDashoffset: length },
          {
            strokeDashoffset: 0,
            ease: "none",
            scrollTrigger: {
              trigger: ".connection-flow",
              start: "top 82%",
              end: "top 30%",
              scrub: 0.7,
            },
          },
        );
        gsap.from(".giant-wordmark", {
          yPercent: 17,
          ease: "none",
          scrollTrigger: {
            trigger: ".invitation",
            start: "top bottom",
            end: "bottom bottom",
            scrub: 0.7,
          },
        });
      }
    });
  }
  if (gsap) {
    motionButton.hidden = false;
    motionButton.addEventListener("click", () => {
      userPaused = !userPaused;
      configureMotion();
    });
  }
  systemMotion.addEventListener("change", () => configureMotion());
  configureMotion(true);
})();

// 在首屏绘制前读取主题；使用同源外部脚本，兼容控制台 script-src 'self'。
(() => {
  let preference = "auto";
  try {
    const saved = localStorage.getItem("console-theme");
    if (saved === "auto" || saved === "light" || saved === "dark" || saved === "system") preference = saved;
  } catch {
    // 浏览器禁用本地存储时仍按上海昼夜显示首屏。
  }
  // 首屏与运行时使用同一上海墙钟边界，避免自动模式先闪现另一套配色。
  const hour = new Date(Date.now() + 8 * 60 * 60 * 1000).getUTCHours();
  const theme = preference === "auto"
    ? (hour >= 6 && hour < 18 ? "light" : "dark")
    : preference === "system"
      ? (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")
      : preference;
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
})();

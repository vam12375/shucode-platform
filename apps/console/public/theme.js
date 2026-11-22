// 在首屏绘制前读取主题；使用同源外部脚本，兼容控制台 script-src 'self'。
(() => {
  let preference = "system";
  try {
    const saved = localStorage.getItem("console-theme");
    if (saved === "light" || saved === "dark") preference = saved;
  } catch {
    // 浏览器禁用本地存储时仍按系统主题显示。
  }
  const theme = preference === "system"
    ? (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")
    : preference;
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
})();

export interface AppVersion {
  tag: string;
  subject: string;
}

// CI 在构建控制台时注入当前提交的 tag 与提交标题；本地 dev/build 未注入时为空。
export const appVersion: AppVersion = {
  tag: String(import.meta.env.VITE_APP_VERSION ?? "").trim(),
  subject: String(import.meta.env.VITE_APP_SUBJECT ?? "").trim(),
};

export function versionText(version: AppVersion): string {
  if (!version.tag) return "";
  return `(tag: ${version.tag})${version.subject ? ` ${version.subject}` : ""}`;
}

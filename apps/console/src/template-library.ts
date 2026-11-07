export type TemplateKind =
  | "template"
  | "video"
  | "automation"
  | "prompt"
  | "skill";
export interface PackageInfo {
  sha256: string;
  size: number;
  files: { path: string; size: number; sha256: string; executable: boolean }[];
}
export interface TemplateFields {
  packageDigest?: string;
  name: string;
  nameEn: string;
  description: string;
  descriptionEn: string;
  content: string;
  contentEn: string;
  tags: string[];
  scene: string;
  cronExpr: string;
}
export interface LibraryEntry extends TemplateFields {
  id: string;
  preview?: { cover: string; video: string };
  kind: TemplateKind;
  source: "builtin-video" | "client-scenes" | "custom" | "dineops" | "curated";
  package?: PackageInfo;
  enabled: boolean;
  expectedVersion: string;
}
export const kindLabels: Record<TemplateKind, string> = {
  template: "通用模板",
  video: "视频模板",
  automation: "自动化",
  prompt: "提示词",
  skill: "Skill",
};
export const sceneLabels: Record<string, string> = {
  "draft-suggestion": "对话推荐",
  "scheduled-task": "定时任务",
  "off-peak-task": "闲时任务",
  "ai-slides": "演示文稿",
  artifact: "创意作品",
  "data-insight": "数据分析",
  "full-stack": "全栈开发",
  "ai-writing": "写作",
};
export function emptyTemplate(): TemplateFields {
  return {
    name: "",
    nameEn: "",
    description: "",
    descriptionEn: "",
    content: "",
    contentEn: "",
    tags: [],
    scene: "draft-suggestion",
    cronExpr: "",
  };
}
export function entryFields(entry: TemplateFields): TemplateFields {
  const {
    packageDigest,
    name,
    nameEn,
    description,
    descriptionEn,
    content,
    contentEn,
    tags,
    scene,
    cronExpr,
  } = entry;
  return {
    ...(packageDigest ? { packageDigest } : {}),
    name,
    nameEn,
    description,
    descriptionEn,
    content,
    contentEn,
    tags: [...(tags ?? [])],
    scene,
    cronExpr,
  };
}
export function filterTemplates(
  entries: LibraryEntry[],
  kind: string,
  status: string,
  query: string,
): LibraryEntry[] {
  const search = query.trim().toLocaleLowerCase();
  return entries.filter(
    (entry) =>
      (kind === "all" || entry.kind === kind) &&
      (status === "all" || entry.enabled === (status === "enabled")) &&
      (!search ||
        [
          entry.name,
          entry.nameEn,
          entry.description,
          entry.id,
          entry.scene,
          ...entry.tags,
        ]
          .join(" ")
          .toLocaleLowerCase()
          .includes(search)),
  );
}

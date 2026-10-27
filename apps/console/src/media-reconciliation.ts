import type { SelectOption } from "./components/Select.js";

export type ReconciliationOutcome =
  | "video_task"
  | "video_usage"
  | "image_task"
  | "image_result"
  | "narration_result"
  | "narration_not_accepted"
  | "not_accepted";

const NARRATION_OUTCOMES: ReconciliationOutcome[] = ["narration_result", "narration_not_accepted"];

const IMAGE_OUTCOMES: ReconciliationOutcome[] = ["image_result", "not_accepted"];
const ASYNC_IMAGE_OUTCOMES: ReconciliationOutcome[] = ["image_task", ...IMAGE_OUTCOMES];
const VIDEO_OUTCOMES: ReconciliationOutcome[] = [
  "video_task",
  "video_usage",
  "not_accepted",
];

const OUTCOME_OPTIONS: Record<"image" | "video" | "narration", SelectOption[]> = {
  narration: [
    { value: "narration_result", label: "独白已生成，补录音频结果 URL" },
    { value: "narration_not_accepted", label: "确认配音未受理，允许显式重试" },
  ],
  image: [
    { value: "image_result", label: "图片已生成，补录结果 URL" },
    { value: "not_accepted", label: "确认未受理，零扣关闭" },
  ],
  video: [
    { value: "video_task", label: "视频已受理，补录 task ID" },
    { value: "video_usage", label: "视频已完成但缺 usage，补录 token" },
    { value: "not_accepted", label: "确认未受理，零扣关闭" },
  ],
};
const ASYNC_IMAGE_OPTIONS: SelectOption[] = [
  { value: "image_task", label: "图片已受理，补录 task ID" },
  ...OUTCOME_OPTIONS.image,
];
const ASYNC_IMAGE_MODELS = new Set([
  "gpt-image-2",
  "gpt-image-2.5-flare",
  "gpt-image-2.5-sunburst",
]);

export interface ImageReconciliationProject {
  brief?: { imageModel?: string };
  canvas?: { kind: string; modelId: string };
  models?: readonly {
    id: string;
    kind: string;
    adapter?: string;
    upstreamModel: string;
  }[];
}

export function hasAsyncImageReconciliation(
  project: ImageReconciliationProject,
): boolean {
  // 恢复能力必须来自本次已冻结的 SKU，不能随当前模型目录变化或误用另一个图片型号。
  const modelID = project.canvas
    ? project.canvas.kind === "image" ? project.canvas.modelId : undefined
    : project.brief?.imageModel;
  if (!modelID) return false;
  const model = project.models?.find((item) => item.id === modelID);
  return model?.kind === "image" && model.adapter === "toapis" && ASYNC_IMAGE_MODELS.has(model.upstreamModel);
}

export function reconciliationOutcomesForStage(
  stage?: string,
  asyncImage = false,
): ReconciliationOutcome[] {
  if (stage === "image") return asyncImage ? ASYNC_IMAGE_OUTCOMES : IMAGE_OUTCOMES;
  if (stage === "video") return VIDEO_OUTCOMES;
  if (stage === "narration") return NARRATION_OUTCOMES;
  return [];
}

export function reconciliationOptionsForStage(stage?: string, asyncImage = false): SelectOption[] {
  if (stage === "image" && asyncImage) return ASYNC_IMAGE_OPTIONS;
  if (stage === "image" || stage === "video" || stage === "narration") return OUTCOME_OPTIONS[stage];
  return [];
}

export function defaultReconciliationOutcome(
  stage?: string,
  asyncImage = false,
): ReconciliationOutcome | "" {
  if (stage === "image") return asyncImage ? "image_task" : "image_result";
  if (stage === "video") return "video_task";
  if (stage === "narration") return "narration_result";
  return "";
}

export function reconciliationValueLabel(
  outcome: ReconciliationOutcome | "",
): string {
  if (outcome === "video_task") return "视频 Task ID";
  if (outcome === "video_usage") return "Ark completion_tokens";
  if (outcome === "image_task") return "图片 Task ID";
  if (outcome === "image_result") return "图片结果 URL";
  if (outcome === "narration_result") return "全片独白音频 URL";
  return "";
}

export function reconciliationValuePlaceholder(
  outcome: ReconciliationOutcome | "",
): string {
  if (outcome === "video_task") return "供应商返回的视频任务 ID";
  if (outcome === "video_usage") return "例如 108900";
  if (outcome === "image_task") return "供应商返回的图片任务 ID";
  if (outcome === "image_result") return "可直接下载的图片结果地址";
  if (outcome === "narration_result") return "经供应商核实可直接下载的音频结果地址";
  return "";
}

export function isReconciliationOutcomeForStage(
  stage: string | undefined,
  outcome: ReconciliationOutcome | "",
  asyncImage = false,
): outcome is ReconciliationOutcome {
  return reconciliationOutcomesForStage(stage, asyncImage).includes(
    outcome as ReconciliationOutcome,
  );
}

// 历史项目把阶段售价写在 shot.reservedMicro；账本升级归零不应抹掉恢复价格。
export function pendingMediaChargeMicro(shot: {
  pendingChargeMicro?: number;
  reservedMicro?: number;
}): number {
  return (shot.pendingChargeMicro ?? 0) > 0
    ? shot.pendingChargeMicro!
    : shot.reservedMicro ?? 0;
}

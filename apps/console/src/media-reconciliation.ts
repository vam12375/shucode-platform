import type { SelectOption } from "./components/Select.js";

export type ReconciliationOutcome = "video_task" | "image_result" | "not_accepted";

const IMAGE_OUTCOMES: ReconciliationOutcome[] = ["image_result", "not_accepted"];
const VIDEO_OUTCOMES: ReconciliationOutcome[] = ["video_task", "not_accepted"];

const OUTCOME_OPTIONS: Record<"image" | "video", SelectOption[]> = {
  image: [
    { value: "image_result", label: "图片已生成，补录结果 URL" },
    { value: "not_accepted", label: "确认未受理，释放预留" },
  ],
  video: [
    { value: "video_task", label: "视频已受理，补录 task ID" },
    { value: "not_accepted", label: "确认未受理，释放预留" },
  ],
};

export function reconciliationOutcomesForStage(
  stage?: string,
): ReconciliationOutcome[] {
  if (stage === "image") return IMAGE_OUTCOMES;
  if (stage === "video") return VIDEO_OUTCOMES;
  return [];
}

export function reconciliationOptionsForStage(stage?: string): SelectOption[] {
  if (stage === "image" || stage === "video") return OUTCOME_OPTIONS[stage];
  return [];
}

export function defaultReconciliationOutcome(
  stage?: string,
): ReconciliationOutcome | "" {
  if (stage === "image") return "image_result";
  if (stage === "video") return "video_task";
  return "";
}

export function reconciliationValueLabel(
  outcome: ReconciliationOutcome | "",
): string {
  if (outcome === "video_task") return "视频 Task ID";
  if (outcome === "image_result") return "图片结果 URL";
  return "";
}

export function reconciliationValuePlaceholder(
  outcome: ReconciliationOutcome | "",
): string {
  if (outcome === "video_task") return "供应商返回的视频任务 ID";
  if (outcome === "image_result") return "可直接下载的图片结果地址";
  return "";
}

export function isReconciliationOutcomeForStage(
  stage: string | undefined,
  outcome: ReconciliationOutcome | "",
): outcome is ReconciliationOutcome {
  return reconciliationOutcomesForStage(stage).includes(
    outcome as ReconciliationOutcome,
  );
}

/** 网关记录的实际请求参数；历史记录缺字段不代表关闭思考。 */
export interface UsageReasoningParameters {
  model: string;
  modelDisplayName?: string;
  modelKind?: "text" | "image" | "video" | "media" | "tool";
  reasoningEffort?: string | null;
  reasoningBudgetTokens?: number | null;
}

function missingText(record: UsageReasoningParameters): string {
  return record.modelKind === "image" || record.modelKind === "video" || record.modelKind === "media" || record.modelKind === "tool" || record.model.startsWith("media:") || record.model === "deepseek:web-search" || record.model === "deepseek:web-fetch"
    ? "不适用"
    : "未记录";
}

function effort(record: UsageReasoningParameters): string {
  return record.reasoningEffort?.trim() ?? "";
}

function hasBudget(record: UsageReasoningParameters): boolean {
  return typeof record.reasoningBudgetTokens === "number" &&
    Number.isSafeInteger(record.reasoningBudgetTokens) &&
    record.reasoningBudgetTokens >= 0;
}

export function reasoningEffortText(record: UsageReasoningParameters): string {
  return effort(record) || missingText(record);
}

export function reasoningBudgetText(record: UsageReasoningParameters): string {
  return hasBudget(record)
    ? `${record.reasoningBudgetTokens!.toLocaleString("en-US")} tokens`
    : missingText(record);
}

export function reasoningSummary(record: UsageReasoningParameters): string {
  const parts = [effort(record)];
  if (hasBudget(record)) parts.push(`预算 ${reasoningBudgetText(record)}`);
  return parts.filter(Boolean).join(" · ") || missingText(record);
}

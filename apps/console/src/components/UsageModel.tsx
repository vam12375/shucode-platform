import {
  reasoningBudgetText,
  reasoningEffortText,
  reasoningSummary,
  type UsageReasoningParameters,
} from "../usage-reasoning.js";
import { Chip } from "./StatusChip.js";

export function UsageModel({ record }: { record: UsageReasoningParameters }) {
  return (
    <span className="usage-model">
      <span>{record.modelDisplayName || record.model}</span>
      <span aria-hidden="true">·</span>
      <Chip title={`思考强度：${reasoningEffortText(record)}；思考预算：${reasoningBudgetText(record)}`}>
        {reasoningSummary(record)}
      </Chip>
    </span>
  );
}

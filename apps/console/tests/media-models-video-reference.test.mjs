import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import * as credits from "@shucode-platform/contracts/credits";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/MediaModels.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    jsx: ts.JsxEmit.ReactJSX,
    target: ts.ScriptTarget.ES2022,
  },
}).outputText;
const output = {};
runInNewContext(compiled, {
  exports: output,
  require(name) {
    if (name === "@shucode-platform/contracts/credits") return credits;
    if (name === "react") return { useCallback() {}, useEffect() {}, useState() {} };
    if (name === "react/jsx-runtime") return { jsx() {}, jsxs() {} };
    if (name === "../api.js") return { api() {} };
    if (name === "../components/Feedback.js") return { toastErr() {}, toastOk() {} };
    if (name === "../components/Select.js") return { Select: "Select" };
    if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
    if (name === "./MediaReconciliation.js") return { MediaReconciliation: "MediaReconciliation" };
    if (name === "./ReferencePricing.js") return { ReferencePricing: "ReferencePricing" };
    if (name === "./SourceParserStatus.js") return { SourceParserStatus: "SourceParserStatus" };
    throw new Error(`Unexpected import ${name}`);
  },
});

const provider = { id: "ark", name: "Ark", baseUrl: "https://ark.example", enabled: true };
const model = {
  id: "video-model-720p",
  name: "Video model 720p",
  kind: "video",
  providerId: provider.id,
  upstreamModel: "upstream-video-model",
  adapter: "ark",
  resolutions: ["720p"],
  durations: [4, 5],
  costMicro: 200_000,
  generateAudio: false,
  priceMicro: 300_000,
  priceVersion: "v1",
};
const capability = {
  maxDurationSeconds: 15,
  maxImages: 3,
  costMicro: 125_000,
  priceMicro: 250_000,
  tokenCostMicroPerMillion: 14_000_000,
  tokenPriceMicroPerMillion: 20_000_000,
};
const validDraft = () => ({
  ...output.toDraft({ ...model, videoReference: capability }),
  familyId: "family-test",
  videoReference: {
    enabled: true,
    maxDurationSeconds: "15",
    maxImages: "3",
    tokenCostCredits: "14",
    tokenPriceCredits: "20",
  },
});

test("legacy duration lists become a continuous range on load", () => {
  const draft = output.toDraft(model);
  assert.equal(draft.minDuration, "4");
  assert.equal(draft.maxDuration, "5");

  const ranged = output.toDraft({ ...model, minDuration: 4, maxDuration: 15 });
  assert.equal(ranged.minDuration, "4");
  assert.equal(ranged.maxDuration, "15");
});

test("video reference keeps shared token rates and mirrors legacy per-second prices", () => {
  const imported = output.draftsFromJson(
    JSON.stringify([{ ...model, videoReference: capability }]),
  )[0];
  const saved = output.toApiModel(imported);
  assert.equal(saved.minDuration, 4);
  assert.equal(saved.maxDuration, 5);
  assert.equal(saved.durations, undefined);
  assert.deepEqual(JSON.parse(JSON.stringify(saved.videoReference)), {
    maxDurationSeconds: 15,
    maxImages: 3,
    // 旧按秒口径跟随档位主价格，不再单独维护。
    costMicro: 200_000,
    priceMicro: 300_000,
    tokenCostMicroPerMillion: 14_000_000,
    tokenPriceMicroPerMillion: 20_000_000,
  });

  const draft = output.toDraft({ ...model, videoReference: capability });
  assert.deepEqual(JSON.parse(JSON.stringify(draft.videoReference)), {
    enabled: true,
    maxDurationSeconds: "15",
    maxImages: "3",
    tokenCostCredits: "14",
    tokenPriceCredits: "20",
  });
});

test("models without reference capability stay absent from catalog JSON", () => {
  const draft = output.toDraft(model);
  assert.equal(draft.videoReference, undefined);
  assert.equal("videoReference" in output.toApiModel(draft), false);
});

test("duration range and token rates are validated before save", () => {
  assert.equal(output.validateDrafts([validDraft()], [provider]), null);

  for (const [patch, message] of [
    [{ maxDurationSeconds: "0" }, /1–90 秒/],
    [{ maxDurationSeconds: "91" }, /1–90 秒/],
    [{ maxDurationSeconds: "1.5" }, /1–90 秒/],
    [{ maxImages: "0" }, /1–8 张/],
    [{ maxImages: "9" }, /1–8 张/],
    [{ maxImages: "2.5" }, /1–8 张/],
    [{ tokenCostCredits: "1.0000001" }, /token 成本 格式/],
    [{ tokenPriceCredits: "-1" }, /token 售价/],
  ]) {
    const draft = validDraft();
    draft.videoReference = { ...draft.videoReference, ...patch };
    assert.match(output.validateDrafts([draft], [provider]), message);
  }

  for (const [patch, message] of [
    [{ minDuration: "1" }, /2–30 秒/],
    [{ maxDuration: "31" }, /2–30 秒/],
    [{ minDuration: "10", maxDuration: "9" }, /起始不大于结束/],
    [{ minDuration: "4.5" }, /整数秒/],
  ]) {
    const draft = validDraft();
    Object.assign(draft, patch);
    assert.match(output.validateDrafts([draft], [provider]), message);
  }
});

test("JSON rejects incomplete capability records and image models cannot enable it", () => {
  assert.throws(
    () =>
      output.draftsFromJson(
        JSON.stringify([{ ...model, videoReference: { maxDurationSeconds: 15 } }]),
      ),
    /字段不完整/,
  );
  assert.throws(
    () =>
      output.draftsFromJson(
        JSON.stringify([{ ...model, videoReference: { ...capability, maxImages: 1.5 } }]),
      ),
    /字段不完整/,
  );
  const image = { ...validDraft(), kind: "image", resolutions: [] };
  assert.match(output.validateDrafts([image], [provider]), /只有视频模型/);
});

test("one card groups all tiers of the same upstream model", () => {
  const drafts = [
    { ...validDraft(), familyId: "" },
    {
      ...validDraft(),
      familyId: "",
      id: "video-model-480p",
      name: "Video model 480p",
      resolutions: ["480p"],
      priceVersion: "v1",
    },
  ];
  const grouped = output.assignFamilyIds(drafts);
  assert.equal(grouped[0].familyId, grouped[1].familyId);
  const families = output.groupFamilies(grouped);
  assert.equal(families.length, 1);
  assert.deepEqual([...families[0].indexes], [0, 1]);
  assert.equal(output.familyNameOf(grouped[0]), "Video model");

  const other = output.assignFamilyIds([
    { ...validDraft(), familyId: "", upstreamModel: "another-upstream" },
  ]);
  assert.notEqual(other[0].familyId, grouped[0].familyId);
});

test("shared configuration must stay identical across tiers of one card", () => {
  const drafts = [
    { ...validDraft(), familyId: "same", id: "a" },
    { ...validDraft(), familyId: "same", id: "b", priceVersion: "v2" },
  ];
  assert.match(output.validateDrafts(drafts, [provider]), /共享配置不一致/);
});

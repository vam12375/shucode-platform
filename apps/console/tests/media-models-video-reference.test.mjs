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
    if (name === "react") return { useCallback() {}, useEffect() {}, useRef() {}, useState() {} };
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
  id: "video-model",
  name: "Video model",
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
};
const validDraft = () => ({
  ...output.toDraft({ ...model, videoReference: capability }),
  videoReference: {
    enabled: true,
    maxDurationSeconds: "15",
    maxImages: "3",
    costCredits: "0.125",
    priceCredits: "0.25",
  },
});

test("video reference settings round-trip through JSON and keep independent credit prices", () => {
  const imported = output.draftsFromJson(JSON.stringify([{ ...model, videoReference: capability }]))[0];
  assert.deepEqual(JSON.parse(JSON.stringify(output.toApiModel(imported))), { ...model, videoReference: capability });

  const draft = output.toDraft({ ...model, videoReference: capability });
  assert.deepEqual(JSON.parse(JSON.stringify(draft.videoReference)), {
    enabled: true,
    maxDurationSeconds: "15",
    maxImages: "3",
    costCredits: "0.125",
    priceCredits: "0.25",
  });
});

test("legacy models and disabled reference capability remain absent from catalog JSON", () => {
  const draft = output.toDraft(model);
  assert.equal(draft.videoReference, undefined);
  assert.equal("videoReference" in output.toApiModel(draft), false);

  const imported = output.draftsFromJson(JSON.stringify([model]))[0];
  assert.equal(imported.videoReference, undefined);
  assert.equal("videoReference" in output.toApiModel(imported), false);
});

test("capability bounds and separate reference prices are validated before save", () => {
  assert.equal(output.validateDrafts([validDraft()], [provider]), null);

  for (const [patch, message] of [
    [{ maxDurationSeconds: "0" }, /1–90 秒/],
    [{ maxDurationSeconds: "91" }, /1–90 秒/],
    [{ maxDurationSeconds: "1.5" }, /1–90 秒/],
    [{ maxImages: "0" }, /1–8 张/],
    [{ maxImages: "9" }, /1–8 张/],
    [{ maxImages: "2.5" }, /1–8 张/],
    [{ costCredits: "100.000001" }, /参考成本价/],
    [{ priceCredits: "0" }, /参考售价/],
    [{ priceCredits: "1.0000001" }, /参考售价格式/],
  ]) {
    const draft = validDraft();
    draft.videoReference = { ...draft.videoReference, ...patch };
    assert.match(output.validateDrafts([draft], [provider]), message);
  }
});

test("JSON rejects incomplete or non-integral capability records and image models cannot enable it", () => {
  assert.throws(
    () => output.draftsFromJson(JSON.stringify([{ ...model, videoReference: { maxDurationSeconds: 15 } }])),
    /字段不完整/,
  );
  assert.throws(
    () => output.draftsFromJson(JSON.stringify([{ ...model, videoReference: { ...capability, maxImages: 1.5 } }])),
    /字段不完整/,
  );
  const image = {
    ...validDraft(),
    kind: "image",
  };
  assert.match(output.validateDrafts([image], [provider]), /只有视频模型/);
});

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
  ...output.toDraft({ ...model, minDuration: 4, maxDuration: 5, videoReference: capability }),
  familyId: "family-test",
  videoReference: {
    enabled: true,
    maxDurationSeconds: "15",
    maxImages: "3",
    tokenCostCredits: "14",
    tokenPriceCredits: "20",
  },
});

test("legacy sparse duration lists retain their saved semantics on load and JSON round trip", () => {
  const legacy = { ...model, durations: [4, 5, 10, 15] };
  const draft = output.toDraft(legacy);
  assert.equal(draft.minDuration, "4");
  assert.equal(draft.maxDuration, "15");
  const saved = output.toApiModel({ ...draft, priceCredits: "0.5" });
  assert.deepEqual(Array.from(saved.durations), [4, 5, 10, 15]);
  assert.equal(saved.minDuration, undefined);
  assert.equal(saved.maxDuration, undefined);

  const imported = output.draftsFromJson(JSON.stringify([saved]))[0];
  assert.deepEqual(Array.from(output.toApiModel(imported).durations), [4, 5, 10, 15]);

  const ranged = output.toDraft({ ...model, minDuration: 4, maxDuration: 15 });
  const rangeModel = output.toApiModel(ranged);
  assert.equal(rangeModel.minDuration, 4);
  assert.equal(rangeModel.maxDuration, 15);
  assert.equal(rangeModel.durations, undefined);
});

test("video reference keeps shared token rates and mirrors legacy per-second prices", () => {
  const imported = output.draftsFromJson(
    JSON.stringify([{ ...model, videoReference: capability }]),
  )[0];
  const saved = output.toApiModel(imported);
  assert.deepEqual(Array.from(saved.durations), [4, 5]);
  assert.equal(saved.minDuration, undefined);
  assert.equal(saved.maxDuration, undefined);
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

async function mountModels(models) {
  const states = new Map();
  const effects = [];
  const calls = [];
  let context;
  let mutationError;
  const jsx = (type, props) => ({ type, props });
  const page = {};
  runInNewContext(compiled, {
    exports: page,
    require(name) {
      if (name === "@shucode-platform/contracts/credits") return credits;
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) {
          const state = context;
          const index = state.cursor++;
          if (!(index in state.slots)) state.slots[index] = initial;
          return [state.slots[index], (value) => {
            state.slots[index] = typeof value === "function" ? value(state.slots[index]) : value;
          }];
        },
        useCallback(callback) { return callback; },
        useEffect(effect) {
          const index = context.cursor++;
          if (!(index in context.slots)) {
            context.slots[index] = true;
            effects.push(effect);
          }
        },
      };
      if (name === "../api.js") return { api: async (path, options) => {
        calls.push({ path, options });
        if (options) {
          if (mutationError) throw mutationError;
          return JSON.parse(options.body);
        }
        if (path === "/providers") return { providers: [provider] };
        return models;
      } };
      if (name === "../components/Feedback.js") return { toastErr() {}, toastOk() {} };
      if (name === "../components/Select.js") return { Select: "Select" };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      if (name === "./MediaReconciliation.js") return { MediaReconciliation: "MediaReconciliation" };
      if (name === "./ReferencePricing.js") return { ReferencePricing: "ReferencePricing" };
      if (name === "./SourceParserStatus.js") return { SourceParserStatus: "SourceParserStatus" };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  function expand(node) {
    if (Array.isArray(node)) return node.map(expand);
    if (!node || typeof node !== "object") return node;
    if (typeof node.type === "function") {
      if (!states.has(node.type)) states.set(node.type, { slots: [], cursor: 0 });
      context = states.get(node.type);
      context.cursor = 0;
      return expand(node.type(node.props));
    }
    return { ...node, props: { ...node.props, children: expand(node.props?.children) } };
  }
  function render() {
    const tree = expand(jsx(page.MediaModelsPage, {}));
    effects.splice(0).forEach((effect) => effect());
    return tree;
  }
  render();
  await new Promise(setImmediate);
  return {
    render,
    calls,
    failSave(error) { mutationError = error; },
    async settle() { await new Promise(setImmediate); return render(); },
  };
}

function nodes(tree, predicate) {
  if (Array.isArray(tree)) return tree.flatMap((node) => nodes(node, predicate));
  if (!tree || typeof tree !== "object") return [];
  return [...(predicate(tree) ? [tree] : []), ...nodes(tree.props?.children, predicate)];
}

function nodeText(tree) {
  if (Array.isArray(tree)) return tree.map(nodeText).join("");
  if (!tree || typeof tree !== "object") return tree ?? "";
  return (tree.type === "Notice" ? tree.props.k ?? "" : "") + nodeText(tree.props?.children);
}

test("legacy duration conversion requires an explicit action then the ordinary save", async () => {
  const f = await mountModels([
    { ...model, durations: [4, 5, 10, 15] },
    { ...model, id: "video-model-480p", name: "Video model 480p", resolutions: ["480p"], durations: [4, 5, 10, 15] },
  ]);
  let tree = f.render();
  assert.match(nodeText(tree), /旧枚举/);
  assert.match(nodeText(tree), /4、5、10、15 秒/);
  assert.equal(nodes(tree, (node) => node.props?.type === "range").length, 0);
  assert.equal(nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.disabled, true);

  const conversion = nodes(tree, (node) => node.type === "button" && /转换为连续区间 4–15 秒/.test(nodeText(node)))[0];
  assert.ok(conversion, "the saved enum must expose an explicit conversion action");
  conversion.props.onClick();
  tree = f.render();
  assert.equal(f.calls.filter((call) => call.options).length, 0);
  assert.match(nodeText(tree), /有未保存修改/);
  const save = nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0];
  assert.equal(save.props.disabled, false);
  save.props.onClick();
  tree = await f.settle();
  const payload = JSON.parse(f.calls.find((call) => call.options).options.body);
  assert.equal(payload.length, 2);
  for (const tier of payload) {
    assert.equal(tier.minDuration, 4);
    assert.equal(tier.maxDuration, 15);
    assert.equal(tier.durations, undefined);
  }
  assert.match(nodeText(tree), /配置已同步/);
  assert.equal(nodes(tree, (node) => node.props?.type === "range").length, 1);
});

test("loading JSON or saving another field does not convert a legacy catalog", async () => {
  const f = await mountModels([{ ...model, durations: [4, 5, 10, 15] }]);
  let tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "JSON 批量编辑")[0].props.onClick();
  tree = f.render();
  const json = nodes(tree, (node) => node.type === "textarea" && node.props["aria-label"] === "媒体模型 JSON")[0];
  assert.deepEqual(JSON.parse(json.props.value)[0].durations, [4, 5, 10, 15]);
  assert.equal(JSON.parse(json.props.value)[0].minDuration, undefined);
  nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("售价"))[0].props.onChange({ target: { value: "0.5" } });
  tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  await f.settle();
  const payload = JSON.parse(f.calls.find((call) => call.options).options.body)[0];
  assert.deepEqual(payload.durations, [4, 5, 10, 15]);
  assert.equal(payload.minDuration, undefined);
  assert.equal(payload.priceMicro, 500_000);
});

test("failed duration conversion save remains dirty and can be discarded", async () => {
  const f = await mountModels([{ ...model, durations: [4, 5, 10, 15] }]);
  let tree = f.render();
  nodes(tree, (node) => node.type === "button" && /转换为连续区间 4–15 秒/.test(nodeText(node)))[0].props.onClick();
  tree = f.render();
  f.failSave(new Error("save failed"));
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  tree = await f.settle();
  assert.match(nodeText(tree), /有未保存修改/);
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "放弃修改")[0].props.onClick();
  tree = await f.settle();
  assert.match(nodeText(tree), /旧枚举/);
  assert.equal(nodes(tree, (node) => node.props?.type === "range").length, 0);
});

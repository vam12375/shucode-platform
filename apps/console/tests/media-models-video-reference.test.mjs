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

async function mountModels(models, providers = [provider]) {
  const states = new Map();
  const effects = [];
  const calls = [];
  let context;
  let mutationError;
  const jsx = (type, props) => ({ type, props });
  const page = {};
  runInNewContext(compiled, {
    exports: page,
    URL,
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
        if (path === "/providers") return { providers };
        if (path.includes("/models?")) return { models: ["fixture-video"] };
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

test("mixed shared fields are named and explicitly repaired without changing tier identities or prices", async () => {
  const f = await mountModels([
    { ...model, durations: undefined, minDuration: 4, maxDuration: 15 },
    { ...model, id: "video-model-480p", name: "Video model 480p", resolutions: ["480p"], durations: undefined, minDuration: 4, maxDuration: 15, priceVersion: "v2", generateAudio: true, costMicro: 400_000, priceMicro: 600_000 },
  ]);
  let tree = f.render();
  assert.match(nodeText(tree), /共享配置冲突/);
  assert.match(nodeText(tree), /价格版本、原生音频/);
  nodes(tree, (node) => node.props?.["aria-label"] === "模型 Video model 最小时长")[0].props.onChange({ target: { value: "4" } });
  tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  tree = await f.settle();
  assert.equal(f.calls.filter((call) => call.options).length, 0);
  assert.match(nodeText(tree), /价格版本、原生音频/);

  const repair = nodes(tree, (node) => node.type === "button" && nodeText(node) === "将当前卡片配置应用到所有档位")[0];
  assert.ok(repair);
  repair.props.onClick();
  tree = f.render();
  assert.doesNotMatch(nodeText(tree), /共享配置冲突/);
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  await f.settle();
  const payload = JSON.parse(f.calls.find((call) => call.options).options.body);
  assert.deepEqual(payload.map((tier) => [tier.id, tier.resolutions, tier.costMicro, tier.priceMicro]), [
    ["video-model-720p", ["720p"], 200_000, 300_000],
    ["video-model-480p", ["480p"], 400_000, 600_000],
  ]);
  assert.ok(payload.every((tier) => tier.priceVersion === "v1" && tier.generateAudio === false && tier.minDuration === 4 && tier.maxDuration === 15));
});

test("repair identifies duration semantics and video reference conflicts", () => {
  const first = validDraft();
  const second = { ...first, id: "other", legacyDurations: [4, 5], videoReference: { ...first.videoReference, maxImages: "5", tokenPriceCredits: "25" } };
  const problem = output.validateDrafts([first, second], [provider]);
  assert.match(problem, /时长能力/);
  assert.match(problem, /视频参考图上限/);
  assert.match(problem, /视频参考 token 售价/);
});

test("adding preset tiers inherits the existing family shared configuration", async () => {
  const f = await mountModels([{
    ...model,
    id: "seedance-2.0-mini-480p",
    name: "Seedance 2.0 mini 480p",
    upstreamModel: "doubao-seedance-2-0-mini-260615",
    resolutions: ["480p"],
    durations: [4, 5, 10, 15],
    priceVersion: "custom-version",
    videoReference: capability,
  }]);
  let tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "Seedance 2.0 系列")[0].props.onClick();
  tree = f.render();
  for (const input of nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("售价") && node.props.value === "")) {
    input.props.onChange({ target: { value: "0.5" } });
  }
  tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "JSON 批量编辑")[0].props.onClick();
  tree = f.render();
  const text = nodes(tree, (node) => node.type === "textarea" && node.props["aria-label"] === "媒体模型 JSON")[0].props.value;
  const added = JSON.parse(text).find((tier) => tier.id === "seedance-2.0-mini-720p");
  assert.equal(added.priceVersion, "custom-version");
  assert.equal(added.generateAudio, false);
  assert.deepEqual(added.durations, [4, 5, 10, 15]);
  assert.equal(added.minDuration, undefined);
  assert.equal(added.videoReference.maxImages, capability.maxImages);
  assert.equal(added.videoReference.tokenPriceMicroPerMillion, capability.tokenPriceMicroPerMillion);
});

test("platform aspect ratios are shown as read-only capabilities and never saved as configuration", async () => {
  const ratios = ["auto", "16:9", "4:3", "1:1", "3:4", "9:16", "21:9"];
  const f = await mountModels([{ ...model, aspectRatios: ratios }]);
  let tree = f.render();
  const readout = nodes(tree, (node) => node.props?.["aria-label"] === "模型 Video model 平台支持比例")[0];
  assert.equal(nodeText(readout), "智能 / 16:9 / 4:3 / 1:1 / 3:4 / 9:16 / 21:9");
  nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("售价"))[0].props.onChange({ target: { value: "0.5" } });
  tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  await f.settle();
  const payload = JSON.parse(f.calls.find((call) => call.options).options.body)[0];
  assert.equal(payload.aspectRatios, undefined);
  assert.equal(payload.supportedAspectRatios, undefined);

  const changed = await mountModels([{ ...model, aspectRatios: ratios }]);
  tree = changed.render();
  nodes(tree, (node) => node.props?.["aria-label"] === "模型 Video model 上游模型")[0].props.onChange({ target: { value: "unknown" } });
  tree = changed.render();
  assert.match(nodeText(tree), /保存后由平台同步支持比例/);
  assert.doesNotMatch(nodeText(nodes(tree, (node) => node.props?.["aria-label"] === "模型 Video model 平台支持比例")[0]), /21:9/);
});

const toapisModel = () => ({
  ...model, id: "toapis-seedance-2-mini-720p", name: "ToAPIs Seedance 2.0 mini 720p",
  adapter: "toapis", upstreamModel: "seedance-2-mini", durations: undefined,
  minDuration: 4, maxDuration: 15,
  videoReference: { ...capability, maxImages: 8, tokenCostMicroPerMillion: 0, tokenPriceMicroPerMillion: 0 },
});

test("ToAPIs adapter survives load, JSON and save while legacy models default to Ark", () => {
  const original = toapisModel();
  const draft = output.toDraft(original);
  assert.equal(draft.adapter, "toapis");
  const imported = output.draftsFromJson(JSON.stringify([output.toApiModel(draft)]))[0];
  assert.equal(output.toApiModel(imported).adapter, "toapis");
  const legacy = { ...model };
  delete legacy.adapter;
  assert.equal(output.toApiModel(output.toDraft(legacy)).adapter, "ark");
  assert.equal(output.toApiModel(output.draftsFromJson(JSON.stringify([legacy]))[0]).adapter, "ark");
  assert.equal(output.validateDrafts([draft], [provider]), null);
});

test("different media adapters never merge into one model family", () => {
  const draft = output.toDraft(toapisModel());
  const groups = output.assignFamilyIds([draft, { ...draft, id: "ark-alias", adapter: "ark" }]);
  assert.equal(output.groupFamilies(groups).length, 2);
});

test("ToAPIs validation rejects unsupported capabilities and token billing", () => {
  const draft = output.toDraft(toapisModel());
  for (const patch of [
    { adapter: "unknown" }, { kind: "image" }, { upstreamModel: "seedance-3" },
    { resolutions: ["1080p"] }, { minDuration: "3" }, { maxDuration: "16" },
    { legacyDurations: [4, 16] },
    { videoReference: { ...draft.videoReference, maxDurationSeconds: "16" } },
    { videoReference: { ...draft.videoReference, tokenCostCredits: "1" } },
    { videoReference: { ...draft.videoReference, tokenPriceCredits: "1" } },
  ]) assert.ok(output.validateDrafts([{ ...draft, ...patch }], [provider]), JSON.stringify(patch));
});

test("ToAPIs preset adds eight distinct unpriced tiers and preserves Ark entries", async () => {
  const f = await mountModels([model], [provider, { ...provider, id: "toapis", name: "ToAPIs", baseUrl: "https://toapis.com/v1" }]);
  let tree = f.render();
  const preset = nodes(tree, (node) => node.type === "button" && nodeText(node) === "ToAPIs Seedance 2.0")[0];
  assert.ok(preset, "explicit ToAPIs preset action is available");
  preset.props.onClick();
  tree = f.render();
  const emptyPrices = nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("售价") && node.props.value === "");
  assert.equal(emptyPrices.length, 8, "presets must not invent prices");
  for (const price of emptyPrices) price.props.onChange({ target: { value: "0.5" } });
  tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  tree = await f.settle();
  const saved = JSON.parse(f.calls.find((call) => call.options).options.body);
  assert.equal(saved.length, 9);
  assert.equal(saved[0].adapter, "ark");
  const added = saved.filter((item) => item.adapter === "toapis");
  assert.equal(added.length, 8);
  assert.deepEqual([...new Set(added.map((item) => item.upstreamModel))], ["seedance-2-mini", "seedance-2-fast", "seedance-2"]);
  assert.ok(added.every((item) => item.id.startsWith("toapis-") && item.minDuration === 4 && item.maxDuration === 15));
  assert.ok(added.every((item) => item.generateAudio === true));
  assert.equal(nodes(tree, (node) => (node.props?.["aria-label"] ?? "").includes("视频参考 token")).length, 0);
  assert.match(nodeText(tree), /配置已同步/);
});

test("ToAPIs reference controls expose second pricing and adapter changes invalidate saved capabilities", async () => {
  const f = await mountModels([{ ...toapisModel(), aspectRatios: ["auto", "21:9"] }]);
  let tree = f.render();
  assert.match(nodeText(tree), /按输出秒数/);
  assert.equal(nodes(tree, (node) => (node.props?.["aria-label"] ?? "").includes("视频参考 token")).length, 0);
  const adapter = nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("适配器"))[0];
  assert.ok(adapter);
  adapter.props.onChange("ark");
  tree = f.render();
  assert.match(nodeText(tree), /保存后由平台同步支持比例/);
});

test("ToAPIs model discovery requests video and caches separately from Ark", async () => {
  const f = await mountModels([toapisModel()]);
  let tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "获取模型列表")[0].props.onClick();
  tree = await f.settle();
  assert.ok(f.calls.some((call) => call.path === "/providers/ark/models?type=video&adapter=toapis"));
  assert.equal(nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("从供应商列表选择")).length, 1);
  const adapter = nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("适配器"))[0];
  adapter.props.onChange("ark");
  tree = f.render();
  assert.equal(nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("从供应商列表选择")).length, 0);
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "获取模型列表")[0].props.onClick();
  await f.settle();
  assert.ok(f.calls.some((call) => call.path === "/providers/ark/models?base=prefix"));
});

test("ToAPIs preset does not bind an unrelated Ark supplier", async () => {
  const f = await mountModels([model]);
  let tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "ToAPIs Seedance 2.0")[0].props.onClick();
  tree = f.render();
  assert.equal(nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("适配器")).length, 1);
  assert.match(nodeText(tree), /1 个上游模型/);
});

const minimaxModel = (upstreamModel = "MiniMax-H3") => ({
  ...toapisModel(), id: `toapis-${upstreamModel}-768p`, name: `ToAPIs ${upstreamModel} 768p`,
  upstreamModel, resolutions: ["768p"], minDuration: upstreamModel === "MiniMax-H3" ? 4 : 5,
  videoReference: undefined, generateAudio: false,
});

test("MiniMax H3 validates exact family capabilities without expanding Seedance", () => {
  for (const [upstream, resolutions, min] of [
    ["MiniMax-H3", ["768p", "2K"], 4], ["MiniMax-H3-Max", ["480p", "768p"], 5],
    ["MiniMax-H3-Max-Turbo", ["480p", "768p"], 5],
  ]) {
    for (const resolution of resolutions) {
      const draft = output.toDraft({ ...minimaxModel(upstream), resolutions: [resolution] });
      assert.equal(output.validateDrafts([draft], [provider]), null, `${upstream}/${resolution}`);
      for (const patch of [{ resolutions: ["720p"] }, { minDuration: String(min - 1) }, { maxDuration: "16" }, { generateAudio: true }]) {
        assert.ok(output.validateDrafts([{ ...draft, ...patch }], [provider]), JSON.stringify(patch));
      }
    }
  }
  const h3 = output.toDraft(minimaxModel());
  const reference = { ...output.toDraft(toapisModel()).videoReference };
  assert.equal(output.validateDrafts([{ ...h3, videoReference: reference }], [provider]), null);
  assert.ok(output.validateDrafts([{ ...h3, upstreamModel: "MiniMax-H3-Max-Turbo", minDuration: "5", videoReference: reference }], [provider]));
  assert.ok(output.validateDrafts([{ ...h3, upstreamModel: "minimax-h3" }], [provider]));
  assert.ok(output.validateDrafts([{ ...h3, upstreamModel: "toString" }], [provider]));
  assert.ok(output.validateDrafts([{ ...h3, resolutions: ["2k"] }], [provider]));
  assert.ok(output.validateDrafts([{ ...output.toDraft(toapisModel()), resolutions: ["768p"] }], [provider]));
});

test("MiniMax H3 preset adds six unpriced tiers, preserves Seedance and round-trips JSON", async () => {
  const f = await mountModels([toapisModel()], [provider, { ...provider, id: "toapis", baseUrl: "https://toapis.cn/v1" }]);
  let tree = f.render();
  const preset = nodes(tree, (node) => node.type === "button" && nodeText(node) === "ToAPIs MiniMax H3")[0];
  assert.ok(preset);
  preset.props.onClick();
  tree = f.render();
  const emptyPrices = nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("售价") && node.props.value === "");
  assert.equal(emptyPrices.length, 6);
  for (const price of emptyPrices) price.props.onChange({ target: { value: "0.5" } });
  tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  await f.settle();
  const saved = JSON.parse(f.calls.find((call) => call.options).options.body);
  const added = saved.filter((item) => item.upstreamModel.startsWith("MiniMax-"));
  assert.equal(saved.length, 7);
  assert.equal(added.length, 6);
  assert.deepEqual(added.map((item) => item.resolutions[0]), ["768p", "2K", "480p", "768p", "480p", "768p"]);
  assert.ok(added.every((item) => item.adapter === "toapis" && item.generateAudio === false && item.maxDuration === 15));
  assert.deepEqual(added.map((item) => item.minDuration), [4, 4, 5, 5, 5, 5]);
  const imported = output.draftsFromJson(JSON.stringify(added));
  assert.ok(imported.every((draft) => output.toApiModel(draft).adapter === "toapis"));
});

test("MiniMax controls explain audio and adaptive frames, block Turbo references and keep invalid import evidence", async () => {
  const f = await mountModels([minimaxModel("MiniMax-H3-Max-Turbo")]);
  let tree = f.render();
  assert.match(nodeText(tree), /由模型决定/);
  assert.match(nodeText(tree), /首尾帧.*自适应/);
  assert.equal(nodes(tree, (node) => node.type === "button" && nodeText(node) === "生成音频").length, 0);
  assert.equal(nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("启用视频参考")).length, 0);
  const adapter = nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("适配器"))[0];
  assert.equal(adapter.props.options.find((option) => option.value === "toapis").label, "ToAPIs");
  const invalid = { ...minimaxModel("MiniMax-H3-Max-Turbo"), generateAudio: true, videoReference: toapisModel().videoReference };
  const imported = output.draftsFromJson(JSON.stringify([invalid]))[0];
  assert.equal(output.toApiModel(imported).generateAudio, true);
  assert.ok(output.toApiModel(imported).videoReference);
  assert.ok(output.validateDrafts([imported], [provider]));
  tree = f.render();
  nodes(tree, (node) => node.props?.["aria-label"] === "模型 ToAPIs MiniMax-H3-Max-Turbo 上游模型")[0].props.onChange({ target: { value: "MiniMax-H3" } });
  tree = f.render();
  assert.match(nodeText(tree), /保存后由平台同步支持比例/);
  assert.equal(nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("启用视频参考")).length, 1);
});

const gptImageModel = (upstreamModel = "gpt-image-2", resolution = "1K") => ({
  ...model, id: `toapis-${upstreamModel}-${resolution}`, name: `ToAPIs ${upstreamModel} ${resolution}`,
  kind: "image", adapter: "toapis", upstreamModel, resolutions: [resolution], durations: undefined,
  generateAudio: false,
});

test("GPT image SKUs preserve one canonical resolution through load, save and JSON", () => {
  for (const upstream of ["gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"]) {
    for (const resolution of ["1K", "2K", "4K"]) {
      const draft = output.toDraft(gptImageModel(upstream, resolution));
      assert.equal(output.validateDrafts([draft], [provider]), null);
      const saved = output.toApiModel(draft);
      assert.deepEqual(Array.from(saved.resolutions), [resolution]);
      assert.equal(saved.minDuration, undefined);
      assert.equal(saved.generateAudio, false);
      const imported = output.draftsFromJson(JSON.stringify([saved]))[0];
      assert.deepEqual(Array.from(output.toApiModel(imported).resolutions), [resolution]);
      assert.equal(output.familyNameOf(draft), `ToAPIs ${upstream}`);
      for (const patch of [{ resolutions: [] }, { resolutions: ["1K", "2K"] }, { resolutions: ["3K"] }, { resolutions: ["1k"] }, { generateAudio: true }, { kind: "video" }]) {
        assert.ok(output.validateDrafts([{ ...draft, ...patch }], [provider]), `${upstream}: ${JSON.stringify(patch)}`);
      }
    }
  }
  const unsupported = output.toDraft(gptImageModel("gpt-image-2.5"));
  assert.ok(output.validateDrafts([unsupported], [provider]));
  const legacy = output.toDraft({ ...gptImageModel(), adapter: "ark", resolutions: [] });
  assert.deepEqual(Array.from(output.toApiModel(legacy).resolutions), []);
});

test("GPT image preset adds nine unpriced independent image tiers without replacing old models", async () => {
  const f = await mountModels([model, toapisModel()], [provider, { ...provider, id: "toapis", baseUrl: "https://api.toapis.com/v1" }]);
  let tree = f.render();
  const preset = nodes(tree, (node) => node.type === "button" && nodeText(node) === "ToAPIs GPT Image")[0];
  assert.ok(preset);
  preset.props.onClick();
  tree = f.render();
  const prices = nodes(tree, (node) => (node.props?.["aria-label"] ?? "").endsWith("售价") && node.props.value === "");
  assert.equal(prices.length, 9, "presets require explicit administrator prices");
  prices.forEach((price, index) => price.props.onChange({ target: { value: String((index + 1) / 10) } }));
  tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "保存配置")[0].props.onClick();
  tree = await f.settle();
  const saved = JSON.parse(f.calls.find((call) => call.options).options.body);
  assert.equal(saved.length, 11);
  assert.equal(saved[0].id, model.id);
  assert.equal(saved[1].id, toapisModel().id);
  const images = saved.filter((item) => item.kind === "image");
  assert.equal(images.length, 9);
  assert.deepEqual([...new Set(images.map((item) => item.upstreamModel))], ["gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"]);
  assert.deepEqual(images.map((item) => item.resolutions), Array.from({ length: 3 }, () => [["1K"], ["2K"], ["4K"]]).flat());
  assert.ok(images.every((item) => item.adapter === "toapis" && item.costMicro === 0 && item.priceMicro > 0 && item.minDuration === undefined));
  assert.equal(nodes(tree, (node) => node.props?.["aria-label"] === "模型 ToAPIs GPT Image 2 1K 售价").length, 1);
  assert.match(nodeText(tree), /credit\/张/);

  const reloaded = await mountModels(images);
  let loaded = reloaded.render();
  assert.equal(nodes(loaded, (node) => node.type === "article").length, 3);
  assert.equal(nodes(loaded, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("档分辨率")).length, 9);
  nodes(loaded, (node) => node.type === "button" && nodeText(node) === "JSON 批量编辑")[0].props.onClick();
  loaded = reloaded.render();
  const json = nodes(loaded, (node) => node.type === "textarea")[0].props.value;
  assert.deepEqual(JSON.parse(json).map((item) => item.resolutions), images.map((item) => item.resolutions));
});

test("GPT image card supports resolution preview and adding tiers with stable SKU ids", async () => {
  const f = await mountModels([gptImageModel()]);
  let tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "+ 2K")[0].props.onClick();
  tree = f.render();
  const addedPrice = nodes(tree, (node) => node.props?.["aria-label"] === "模型 ToAPIs gpt-image-2 2K 售价")[0];
  addedPrice.props.onChange({ target: { value: "0.8" } });
  tree = f.render();
  nodes(tree, (node) => node.props?.["aria-label"] === "预览 2K 档")[0].props.onChange();
  tree = f.render();
  assert.match(nodeText(tree), /2K.*0.8 credit\/张/);
  assert.equal(nodes(tree, (node) => node.props?.["aria-label"] === "模型 ToAPIs gpt-image-2 2K 平台模型 ID")[0].props.value, "toapis-gpt-image-2-2K");
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "移除")[1].props.onClick();
  tree = f.render();
  assert.equal(nodes(tree, (node) => node.type === "button" && nodeText(node) === "移除")[0].props.disabled, true);
});

test("ToAPIs image discovery uses image type and isolates cache from video on the same provider", async () => {
  const f = await mountModels([gptImageModel()]);
  let tree = f.render();
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "获取模型列表")[0].props.onClick();
  tree = await f.settle();
  assert.ok(f.calls.some((call) => call.path === "/providers/ark/models?type=image&adapter=toapis"));
  assert.equal(nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("从供应商列表选择")).length, 1);
  nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("类型"))[0].props.onChange("video");
  tree = f.render();
  assert.equal(nodes(tree, (node) => node.type === "Select" && (node.props.ariaLabel ?? "").endsWith("从供应商列表选择")).length, 0);
  nodes(tree, (node) => node.type === "button" && nodeText(node) === "获取模型列表")[0].props.onClick();
  await f.settle();
  assert.ok(f.calls.some((call) => call.path === "/providers/ark/models?type=video&adapter=toapis"));
});

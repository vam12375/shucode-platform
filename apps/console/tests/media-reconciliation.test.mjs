import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import * as credits from "@shucode-platform/contracts/credits";
import ts from "typescript";
import * as reconciliation from "../src/media-reconciliation.ts";
import {
  defaultReconciliationOutcome,
  hasAsyncImageReconciliation,
  isReconciliationOutcomeForStage,
  pendingMediaChargeMicro,
  reconciliationOptionsForStage,
  reconciliationValueLabel,
  reconciliationValuePlaceholder,
} from "../src/media-reconciliation.ts";

test("image stage defaults to image recovery and never offers a video task ID", () => {
  const options = reconciliationOptionsForStage("image");
  assert.equal(defaultReconciliationOutcome("image"), "image_result");
  assert.deepEqual(options.map(({ value }) => value), ["image_result", "not_accepted"]);
  assert.equal(isReconciliationOutcomeForStage("image", "image_result"), true);
  assert.equal(isReconciliationOutcomeForStage("image", "video_task"), false);
});

test("video stage defaults to video recovery and never offers an image result URL", () => {
  const options = reconciliationOptionsForStage("video");
  assert.equal(defaultReconciliationOutcome("video"), "video_task");
  assert.deepEqual(options.map(({ value }) => value), [
    "video_task",
    "video_usage",
    "not_accepted",
  ]);
  assert.equal(isReconciliationOutcomeForStage("video", "video_task"), true);
  assert.equal(isReconciliationOutcomeForStage("video", "video_usage"), true);
  assert.equal(isReconciliationOutcomeForStage("video", "image_result"), false);
});

test("unknown stage has no default or recovery options and cannot close a request identity", () => {
  for (const stage of [undefined, "", "other"]) {
    assert.equal(defaultReconciliationOutcome(stage), "");
    assert.deepEqual(reconciliationOptionsForStage(stage), []);
    assert.equal(isReconciliationOutcomeForStage(stage, "not_accepted"), false);
  }
});

test("narration recovery only accepts verified audio or explicit non-admission", () => {
  assert.equal(defaultReconciliationOutcome("narration"), "narration_result");
  assert.deepEqual(reconciliationOptionsForStage("narration").map(({ value }) => value), ["narration_result", "narration_not_accepted"]);
  assert.equal(isReconciliationOutcomeForStage("narration", "video_task"), false);
  assert.equal(isReconciliationOutcomeForStage("narration", "not_accepted"), false);
  assert.equal(isReconciliationOutcomeForStage("video", "narration_result"), false);
  assert.equal(reconciliationValueLabel("narration_result"), "全片独白音频 URL");
});

test("recovery fields are labeled for the selected upstream result", () => {
  assert.equal(reconciliationValueLabel("image_result"), "图片结果 URL");
  assert.equal(reconciliationValuePlaceholder("image_result"), "可直接下载的图片结果地址");
  assert.equal(reconciliationValueLabel("video_task"), "视频 Task ID");
  assert.equal(reconciliationValuePlaceholder("video_task"), "供应商返回的视频任务 ID");
});

test("media reconciliation restores a locked stage price without treating it as a balance hold", () => {
  assert.equal(pendingMediaChargeMicro({ pendingChargeMicro: 125000, reservedMicro: 999999 }), 125000);
  assert.equal(pendingMediaChargeMicro({ reservedMicro: 75000 }), 75000);
  assert.equal(pendingMediaChargeMicro({ pendingChargeMicro: 0, reservedMicro: 75000 }), 75000);
  assert.equal(pendingMediaChargeMicro({}), 0);
  assert.equal(reconciliationOptionsForStage("image").find(({ value }) => value === "not_accepted").label, "确认未受理，零扣关闭");
});

test("only an exact frozen ToAPIs image SKU enables asynchronous image recovery", () => {
  const frozen = { id: "image-sku", kind: "image", adapter: "toapis", upstreamModel: "gpt-image-2" };
  for (const upstreamModel of ["gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"]) {
    assert.equal(hasAsyncImageReconciliation({ brief: { imageModel: frozen.id }, models: [{ ...frozen, upstreamModel }] }), true);
    assert.equal(hasAsyncImageReconciliation({ canvas: { kind: "image", modelId: frozen.id }, models: [{ ...frozen, upstreamModel }] }), true);
  }
  for (const patch of [
    { adapter: "ark" }, { adapter: undefined }, { kind: "video" },
    { upstreamModel: "gpt-image-2.5" }, { upstreamModel: "gpt-image-2.5-flare-official" },
    { id: "another-sku" },
  ]) assert.equal(hasAsyncImageReconciliation({ brief: { imageModel: frozen.id }, models: [{ ...frozen, ...patch }] }), false);
  assert.equal(hasAsyncImageReconciliation({ brief: { imageModel: frozen.id } }), false);
  assert.equal(hasAsyncImageReconciliation({ models: [frozen] }), false);
  assert.equal(hasAsyncImageReconciliation({ canvas: { kind: "video", modelId: frozen.id }, brief: { imageModel: frozen.id }, models: [frozen] }), false);
  assert.equal(hasAsyncImageReconciliation({ brief: { imageModel: "ark-image" }, models: [frozen, { ...frozen, id: "ark-image", adapter: "ark" }] }), false);
});

test("asynchronous image task recovery preserves result URL and stage boundaries", () => {
  assert.equal(defaultReconciliationOutcome("image", true), "image_task");
  assert.deepEqual(reconciliationOptionsForStage("image", true).map(({ value }) => value), ["image_task", "image_result", "not_accepted"]);
  assert.equal(isReconciliationOutcomeForStage("image", "image_task", true), true);
  assert.equal(isReconciliationOutcomeForStage("image", "image_task"), false);
  assert.equal(isReconciliationOutcomeForStage("image", "image_result", true), true);
  assert.equal(isReconciliationOutcomeForStage("video", "image_task", true), false);
  assert.equal(isReconciliationOutcomeForStage(undefined, "image_task", true), false);
  assert.equal(defaultReconciliationOutcome("video", true), "video_task");
  assert.deepEqual(reconciliationOptionsForStage(undefined, true), []);
  assert.equal(reconciliationValueLabel("image_task"), "图片 Task ID");
  assert.equal(reconciliationValuePlaceholder("image_task"), "供应商返回的图片任务 ID");
});

const pageSource = await readFile(new URL("../src/pages/MediaReconciliation.tsx", import.meta.url), "utf8");
const pageCompiled = ts.transpileModule(pageSource, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;

async function mountProject(project) {
  const states = new Map();
  const effects = [];
  const calls = [];
  let context;
  const jsx = (type, props) => ({ type, props });
  const output = {};
  runInNewContext(pageCompiled, {
    exports: output,
    window: { addEventListener() {}, removeEventListener() {} },
    require(name) {
      if (name === "@shucode-platform/contracts/credits") return credits;
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) {
          const state = context;
          const index = state.cursor++;
          if (!(index in state.slots)) state.slots[index] = typeof initial === "function" ? initial() : initial;
          return [state.slots[index], (value) => { state.slots[index] = typeof value === "function" ? value(state.slots[index]) : value; }];
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
        return options ? project : [project];
      } };
      if (name === "../components/Feedback.js") return { toastOk() {} };
      if (name === "../components/Select.js") return { Select: "Select" };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      if (name === "../media-reconciliation.js") return reconciliation;
      if (name === "../time.js") return { formatShanghaiTime: (value) => value };
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
    const tree = expand(jsx(output.MediaReconciliation, {}));
    effects.splice(0).forEach((effect) => effect());
    return tree;
  }
  const settle = async () => { await new Promise(setImmediate); return render(); };
  render();
  await settle();
  return { render, settle, calls };
}

function nodes(tree, predicate) {
  if (Array.isArray(tree)) return tree.flatMap((node) => nodes(node, predicate));
  if (!tree || typeof tree !== "object") return [];
  return [...(predicate(tree) ? [tree] : []), ...nodes(tree.props?.children, predicate)];
}

function nodeText(tree) {
  if (Array.isArray(tree)) return tree.map(nodeText).join("");
  if (!tree || typeof tree !== "object") return tree ?? "";
  return nodeText(tree.props?.children);
}

const projectFixture = () => ({
  id: "project-fixture", teamId: "team-fixture", version: 7, status: "reconciliation_required",
  brief: { product: "测试图片", imageModel: "image-sku", videoModel: "video-sku", resolution: "720p", duration: 5 },
  models: [{ id: "image-sku", kind: "image", adapter: "toapis", upstreamModel: "gpt-image-2" }],
  shots: [{ status: "reconciliation_required", stage: "image", attempt: 1, pendingChargeMicro: 100000 }],
  updatedAt: "2026-10-25T01:00:00Z", quoteMicro: 100000,
});

test("the drawer submits an image task only for its frozen asynchronous model", async () => {
  const project = { ...projectFixture(), canvas: { kind: "image", modelId: "image-sku" } };
  project.brief.imageModel = "";
  const ui = await mountProject(project);
  nodes(ui.render(), (node) => node.type === "button" && nodeText(node) === "核对")[0].props.onClick();
  let tree = ui.render();
  const select = nodes(tree, (node) => node.type === "Select")[0];
  assert.equal(select.props.value, "image_task");
  assert.deepEqual(select.props.options.map(({ value }) => value), ["image_task", "image_result", "not_accepted"]);
  assert.match(nodeText(tree), /图片 Task ID/);
  nodes(tree, (node) => node.type === "input")[0].props.onChange({ target: { value: "task_img_fixture" } });
  nodes(tree, (node) => node.type === "textarea")[0].props.onChange({ target: { value: "已核实供应商受理" } });
  tree = ui.render();
  nodes(tree, (node) => node.type === "form")[0].props.onSubmit({ preventDefault() {} });
  await ui.settle();
  const call = ui.calls.find(({ options }) => options);
  assert.equal(call.path, "/media/projects/team-fixture/project-fixture/reconcile");
  assert.deepEqual(JSON.parse(call.options.body), {
    version: 7, shot: 0, outcome: "image_task", value: "task_img_fixture", tokens: 0, reason: "已核实供应商受理",
  });
});

test("the Ark drawer rejects a forged image_task selection before submitting", async () => {
  const project = projectFixture();
  project.models[0].adapter = "ark";
  const ui = await mountProject(project);
  nodes(ui.render(), (node) => node.type === "button" && nodeText(node) === "核对")[0].props.onClick();
  let tree = ui.render();
  const select = nodes(tree, (node) => node.type === "Select")[0];
  assert.equal(select.props.value, "image_result");
  assert.deepEqual(select.props.options.map(({ value }) => value), ["image_result", "not_accepted"]);
  select.props.onChange("image_task");
  tree = ui.render();
  nodes(tree, (node) => node.type === "form")[0].props.onSubmit({ preventDefault() {} });
  tree = await ui.settle();
  assert.equal(ui.calls.some(({ options }) => options), false);
  assert.match(nodeText(tree), /核对结果与阶段不匹配/);
});

test("unknown narration renders one full-film row and submits through the root reconcile path", async () => {
  const project = projectFixture();
  project.status = "failed";
  project.narration = { status: "unknown", requestId: "speech-request-fixture" };
  project.shots = [{ status: "pending" }, { status: "succeeded" }];
  const ui = await mountProject(project);
  const buttons = nodes(ui.render(), (node) => node.type === "button" && nodeText(node) === "核对");
  assert.equal(buttons.length, 1);
  assert.match(nodeText(ui.render()), /全片独白/);
  buttons[0].props.onClick();
  let tree = ui.render();
  assert.match(nodeText(tree), /speech-request-fixture/);
  const select = nodes(tree, (node) => node.type === "Select")[0];
  assert.equal(select.props.value, "narration_result");
  nodes(tree, (node) => node.type === "input")[0].props.onChange({ target: { value: "https://media.example.test/confirmed.wav" } });
  nodes(tree, (node) => node.type === "textarea")[0].props.onChange({ target: { value: "已核实同一配音请求结果" } });
  tree = ui.render();
  nodes(tree, (node) => node.type === "form")[0].props.onSubmit({ preventDefault() {} });
  await ui.settle();
  const call = ui.calls.find(({ options }) => options);
  assert.deepEqual(JSON.parse(call.options.body), {
    version: 7, shot: -1, outcome: "narration_result", value: "https://media.example.test/confirmed.wav", tokens: 0, reason: "已核实同一配音请求结果",
  });
});

test("confirmed non-admission offers no audio field and never invokes generation", async () => {
  const project = projectFixture();
  project.narration = { status: "submitting", requestId: "speech-request-fixture" };
  project.shots = [{ status: "pending" }];
  const ui = await mountProject(project);
  nodes(ui.render(), (node) => node.type === "button" && nodeText(node) === "核对")[0].props.onClick();
  nodes(ui.render(), (node) => node.type === "input")[0].props.onChange({ target: { value: "https://media.example.test/previous.wav" } });
  nodes(ui.render(), (node) => node.type === "Select")[0].props.onChange("narration_not_accepted");
  let tree = ui.render();
  assert.equal(nodes(tree, (node) => node.type === "input").length, 0);
  assert.match(nodeText(tree), /不会在此自动发起配音/);
  nodes(tree, (node) => node.type === "textarea")[0].props.onChange({ target: { value: "已核实供应商没有受理" } });
  tree = ui.render();
  nodes(tree, (node) => node.type === "form")[0].props.onSubmit({ preventDefault() {} });
  await ui.settle();
  const changes = ui.calls.filter(({ options }) => options);
  assert.equal(changes.length, 1);
  assert.equal(JSON.parse(changes[0].options.body).outcome, "narration_not_accepted");
  assert.equal(JSON.parse(changes[0].options.body).value, "");
});

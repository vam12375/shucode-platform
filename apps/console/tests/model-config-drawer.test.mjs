import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";
import * as configModule from "../src/model-config.ts";

const source = await readFile(new URL("../src/components/ModelConfigDrawer.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    jsx: ts.JsxEmit.ReactJSX,
    target: ts.ScriptTarget.ES2022,
  },
}).outputText;

const manualConfig = {
  properties: {
    contextWindow: 270000,
    inputFormat: { supportsText: true, supportsImage: true, supportsVideo: false, supportsAudio: false, supportsPdf: true },
    outputFormat: { supportsText: true },
    supportsToolCall: true,
    supportsJsonSchemaOutput: true,
    supportsNativeWebSearch: false,
    supportsMidConversationSystem: false,
    requiresMfjsToolSchema: false,
  },
  optionSpecs: {
    maxOutputTokens: { max: 200000 },
    reasoningLevel: { values: ["none", "low", "high", "max"], map: '{"reasoning_effort": reasoningLevel}' },
  },
};
const analysisResult = {
  modelId: "gpt-6.1-sol",
  config: manualConfig,
  matchedRules: 2,
  complete: true,
  releaseRevision: 31,
};

async function mount() {
  const slots = [];
  const effects = [];
  const calls = [];
  let cursor = 0;
  const jsx = (type, props) => ({ type, props });
  const output = {};
  runInNewContext(compiled, {
    exports: output,
    Error,
    console,
    window: { addEventListener() {}, removeEventListener() {} },
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") {
        return {
          useState(initial) {
            const index = cursor++;
            if (!(index in slots)) {
              slots[index] = typeof initial === "function" ? initial() : initial;
            }
            return [
              slots[index],
              (value) => {
                slots[index] = typeof value === "function" ? value(slots[index]) : value;
              },
            ];
          },
          useRef(initial) {
            const index = cursor++;
            if (!(index in slots)) slots[index] = { current: initial };
            return slots[index];
          },
          useEffect(effect) {
            const index = cursor++;
            if (!(index in slots)) {
              slots[index] = true;
              effects.push(effect);
            }
          },
        };
      }
      if (name === "../api.js") {
        return {
          api: async (path, init) => {
            calls.push({ path, init });
            if (path === "/models/analyze") return analysisResult;
            return { model: { modelId: "gpt-6.1-sol", displayName: "GPT-6.1 Sol", ...JSON.parse(init.body) } };
          },
        };
      }
      if (name === "../model-config.js") return configModule;
      if (name === "./Feedback.js") return { toastOk() {}, toastErr() {} };
      if (name === "./StatusChip.js") return { Notice: "Notice" };
      // 图标只做视觉强化：VM 测试按空组件桩渲染，不引入真实 SVG 输出。
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      if (name === "../components/Icon.js") return { Glyph: () => null, PageTitle: (props) => props.children };
      throw new Error(`Unexpected import ${name}`);
    },
  });

  const render = () => {
    cursor = 0;
    const tree = output.ModelConfigDrawer({
      model: { modelId: "gpt-6.1-sol", displayName: "GPT-6.1 Sol", configMode: "manual", config: manualConfig },
      onClose() {},
      onSaved() {},
    });
    effects.splice(0).forEach((effect) => effect());
    return tree;
  };
  const settle = async () => {
    await new Promise(setImmediate);
    return render();
  };
  render();
  await settle();
  return { render, settle, calls };
}

function nodes(tree, type) {
  if (!tree || typeof tree !== "object") return [];
  if (Array.isArray(tree)) return tree.flatMap((item) => nodes(item, type));
  return [...(tree.type === type ? [tree] : []), ...nodes(tree.props?.children, type)];
}
function text(tree) {
  if (tree === null || tree === undefined) return "";
  if (Array.isArray(tree)) return tree.map(text).join("");
  return typeof tree === "object" ? text(tree.props?.children) : String(tree);
}
function byText(tree, label) {
  return nodes(tree, "button").find((node) => text(node) === label);
}

test("drawer exposes inherited reasoning levels and map in manual mode", async () => {
  const ui = await mount();
  const tree = ui.render();
  assert.match(text(tree), /思考强度/);
  assert.match(text(tree), /下发顺序：无 \/ 低 \/ 高 \/ 最高/);
  const map = nodes(tree, "textarea").find((node) => node.props["aria-label"] === "思考档位映射");
  assert.equal(map.props.value, '{"reasoning_effort": reasoningLevel}');
  assert.equal(map.props.readOnly, false);
});

test("toggling a level changes the saved option specs", async () => {
  const ui = await mount();
  let tree = ui.render();
  byText(tree, "高 · high").props.onClick();
  tree = ui.render();
  assert.match(text(tree), /下发顺序：无 \/ 低 \/ 最高/);
  byText(tree, "保存配置").props.onClick();
  await ui.settle();
  const saveCall = ui.calls.find((call) => call.path === "/models/gpt-6.1-sol/config");
  const body = JSON.parse(saveCall.init.body);
  assert.equal(body.mode, "manual");
  assert.deepEqual(body.config.optionSpecs.reasoningLevel.values, ["none", "low", "max"]);
  assert.equal(body.config.optionSpecs.reasoningLevel.map, '{"reasoning_effort": reasoningLevel}');
});

test("disabling reasoning omits the option so official rules stay inherited", async () => {
  const ui = await mount();
  let tree = ui.render();
  const toggle = nodes(tree, "input").find((node) => node.props["aria-label"] === "下发思考档位");
  toggle.props.onChange({ target: { checked: false } });
  tree = ui.render();
  assert.match(text(tree), /官方档位：无 \/ 低 \/ 高 \/ 最高/);
  byText(tree, "保存配置").props.onClick();
  await ui.settle();
  const saveCall = ui.calls.find((call) => call.path === "/models/gpt-6.1-sol/config");
  const body = JSON.parse(saveCall.init.body);
  assert.equal("reasoningLevel" in body.config.optionSpecs, false);
});

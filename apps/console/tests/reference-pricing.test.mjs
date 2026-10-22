import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import * as credits from "@shucode-platform/contracts/credits";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/ReferencePricing.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const initialPricing = { snapAnyImportMicro: 500000, analysisMicro: 1000000 };

// 直接执行组件的事件与实际 credit helpers，覆盖价格写入和回显的数值边界。
async function mount(initial = initialPricing, loadError = null) {
  const slots = [];
  const effects = [];
  const calls = [];
  let cursor = 0;
  let mutation = async (_path, options) => JSON.parse(options.body);
  const jsx = (type, props) => ({ type, props });
  const output = {};
  runInNewContext(compiled, {
    exports: output,
    Error,
    require(name) {
      if (name === "@shucode-platform/contracts/credits") return credits;
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initialValue) {
          const index = cursor++;
          if (!(index in slots)) slots[index] = initialValue;
          return [slots[index], (value) => { slots[index] = value; }];
        },
        useRef(value) {
          const index = cursor++;
          if (!(index in slots)) slots[index] = { current: value };
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
      if (name === "../api.js") return {
        api: async (path, options) => {
          calls.push({ path, options });
          if (options) return mutation(path, options);
          if (loadError) throw loadError;
          return initial;
        },
      };
      if (name === "../components/Feedback.js") return { toastOk() {} };
      if (name === "../components/StatusChip.js") return { Notice: "Notice" };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => {
    cursor = 0;
    const tree = output.ReferencePricing();
    effects.splice(0).forEach((effect) => effect());
    return tree;
  };
  const settle = async () => {
    await new Promise(setImmediate);
    return render();
  };
  render();
  await settle();
  return { render, settle, calls, setMutation(value) { mutation = value; } };
}

function elements(tree, type) {
  if (!tree || typeof tree !== "object") return [];
  if (Array.isArray(tree)) return tree.flatMap((item) => elements(item, type));
  return [...(tree.type === type ? [tree] : []), ...elements(tree.props?.children, type)];
}
function text(tree) {
  if (tree === null || tree === undefined) return "";
  if (Array.isArray(tree)) return tree.map(text).join("");
  return typeof tree === "object" ? text(tree.props?.children) : String(tree);
}
const inputs = (tree) => elements(tree, "input");
const submit = (tree) => elements(tree, "form")[0].props.onSubmit({ preventDefault() {} });
const edit = (ui, index, value) => inputs(ui.render())[index].props.onChange({ target: { value } });

test("reference prices come from GET and explain the inclusive analysis price and locked operations", async () => {
  const ui = await mount({ snapAnyImportMicro: 750000, analysisMicro: 2000000 });
  const tree = ui.render();
  assert.equal(ui.calls[0].path, "/media/reference-pricing");
  assert.deepEqual(inputs(tree).map((input) => input.props.value), ["0.75", "2"]);
  assert.equal(elements(tree, "button")[0].props.disabled, true);
  assert.match(text(tree), /总价已含文字模型，不再另计 token/);
  assert.match(text(tree), /已受理操作保留锁定价格/);
});

test("free and six-decimal prices save as integer micro and successful writes display the authority response", async () => {
  const ui = await mount();
  ui.setMutation(async () => ({ snapAnyImportMicro: 0, analysisMicro: 1234567 }));
  edit(ui, 0, "0");
  edit(ui, 1, " 2.123456 ");
  submit(ui.render());
  const tree = await ui.settle();
  assert.equal(ui.calls[1].options.method, "PUT");
  assert.deepEqual(JSON.parse(ui.calls[1].options.body), { snapAnyImportMicro: 0, analysisMicro: 2123456 });
  assert.deepEqual(inputs(tree).map((input) => input.props.value), ["0", "1.234567"]);
  assert.equal(elements(tree, "button")[0].props.disabled, true);
  assert.match(text(tree), /SnapAny 导入 0 credit/);
});

test("invalid, negative, over-precision, exponential and unsafe prices never issue a PUT", async () => {
  for (const value of ["", "-1", "0.0000001", "1e6", "9007199254.740992", "100000000000000000000"]) {
    const ui = await mount();
    edit(ui, 0, value);
    submit(ui.render());
    const tree = await ui.settle();
    assert.equal(ui.calls.length, 1, value);
    assert.equal(elements(tree, "Notice")[0].props.tone, "stop", value);
    assert.equal(inputs(tree)[0].props.value, value);
  }
});

test("the maximum safe micro price round-trips exactly despite floating-point helper rounding", async () => {
  const ui = await mount({ snapAnyImportMicro: Number.MAX_SAFE_INTEGER, analysisMicro: 1 });
  assert.deepEqual(inputs(ui.render()).map((input) => input.props.value), ["9007199254.740991", "0.000001"]);
  edit(ui, 1, "9007199254.740991");
  submit(ui.render());
  await ui.settle();
  assert.deepEqual(JSON.parse(ui.calls[1].options.body), {
    snapAnyImportMicro: Number.MAX_SAFE_INTEGER,
    analysisMicro: Number.MAX_SAFE_INTEGER,
  });
  assert.deepEqual(inputs(ui.render()).map((input) => input.props.value), ["9007199254.740991", "9007199254.740991"]);
});

test("pending writes disable editing and reject duplicate submission; failures retain the current price and retry draft", async () => {
  const ui = await mount();
  let fail;
  ui.setMutation(() => new Promise((_resolve, reject) => { fail = reject; }));
  edit(ui, 0, "0.25");
  const tree = ui.render();
  submit(tree);
  submit(tree);
  assert.equal(ui.calls.length, 2);
  assert.ok(inputs(ui.render()).every((input) => input.props.disabled));
  fail(new Error("价格暂时无法保存"));
  const failed = await ui.settle();
  assert.equal(inputs(failed)[0].props.value, "0.25");
  assert.equal(elements(failed, "button")[0].props.disabled, false);
  assert.match(text(failed), /当前价格：SnapAny 导入 0.5 credit/);
  assert.match(text(failed), /价格暂时无法保存/);
});

test("failed price loading does not expose editable fallback prices", async () => {
  const ui = await mount(initialPricing, new Error("无权读取定价"));
  assert.equal(inputs(ui.render()).length, 0);
  assert.match(text(ui.render()), /无权读取定价/);
});

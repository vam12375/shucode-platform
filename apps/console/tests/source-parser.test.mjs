import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/SourceParserStatus.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const configured = {
  configured: true,
  provider: "snapany",
  maxBytes: 31457280,
  maxSeconds: 90,
  source: "console",
  environmentConfigured: true,
  canConfigure: true,
};

// 用轻量 hook 环境执行真实组件事件，避免为一个配置表单增加浏览器测试依赖。
async function mount(initial = configured) {
  const slots = [];
  const effects = [];
  const calls = [];
  const confirmations = [];
  let cursor = 0;
  let result = initial;
  let accepted = true;
  let mutation = async () => result;
  const jsx = (type, props) => ({ type, props });
  const output = {};
  runInNewContext(compiled, {
    exports: output,
    Error,
    require(name) {
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
          return options ? mutation(path, options) : initial;
        },
      };
      if (name === "../components/Feedback.js") return {
        toastOk() {},
        confirmDialog: async (options) => {
          confirmations.push(options);
          return { confirmed: accepted };
        },
      };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      // 图标只做视觉强化：VM 测试按空组件桩渲染，不引入真实 SVG 输出。
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      if (name === "../components/Icon.js") return { Glyph: () => null, PageTitle: (props) => props.children };
      throw new Error(`Unexpected import ${name}`);
    },
  });
  const render = () => {
    cursor = 0;
    const tree = output.SourceParserStatus();
    effects.splice(0).forEach((effect) => effect());
    return tree;
  };
  const settle = async () => {
    await new Promise(setImmediate);
    return render();
  };
  render();
  await settle();
  return {
    render, settle, calls, confirmations,
    setResult(value) { result = value; },
    setAccepted(value) { accepted = value; },
    setMutation(value) { mutation = value; },
  };
}

function elements(tree, type) {
  if (!tree || typeof tree !== "object") return [];
  if (Array.isArray(tree)) return tree.flatMap((item) => elements(item, type));
  return [
    ...(tree.type === type ? [tree] : []),
    ...elements(tree.props?.children, type),
  ];
}
const input = (tree) => elements(tree, "input")[0];
const submit = (tree) => elements(tree, "form")[0].props.onSubmit({ preventDefault() {} });
const clear = (tree) => elements(tree, "button").find((button) => button.props.type === "button");

test("SnapAny saves a replacement without returning or retaining its password field", async () => {
  const ui = await mount();
  assert.equal(input(ui.render()).props.type, "password");
  assert.equal(input(ui.render()).props.value, "");
  assert.equal(elements(ui.render(), "button")[0].props.disabled, true);
  input(ui.render()).props.onChange({ target: { value: "  example-key  " } });
  submit(ui.render());
  const tree = await ui.settle();
  assert.equal(ui.calls[1].path, "/media/source-parser");
  assert.equal(ui.calls[1].options.method, "PUT");
  assert.deepEqual(JSON.parse(ui.calls[1].options.body), { apiKey: "example-key" });
  assert.equal(input(tree).props.value, "");
  assert.equal(JSON.stringify(tree).includes("example-key"), false);
});

test("failed save keeps configured status and allows retry without clearing entered input", async () => {
  const ui = await mount();
  ui.setMutation(async () => { throw new Error("服务暂不可用"); });
  input(ui.render()).props.onChange({ target: { value: "replacement-key" } });
  submit(ui.render());
  const tree = await ui.settle();
  assert.equal(input(tree).props.value, "replacement-key");
  assert.equal(input(tree).props.disabled, false);
  assert.ok(JSON.stringify(tree).includes("当前使用后台保存的 API Key"));
  assert.ok(JSON.stringify(tree).includes("服务暂不可用"));
});

test("clear confirms environment fallback, cancellation avoids mutation, busy blocks concurrent saves", async () => {
  const ui = await mount();
  ui.setAccepted(false);
  clear(ui.render()).props.onClick();
  await ui.settle();
  assert.equal(ui.calls.length, 1);
  assert.ok(ui.confirmations[0].message.includes("SNAPANY_API_KEY"));

  let finish;
  ui.setAccepted(true);
  ui.setMutation(() => new Promise((resolve) => { finish = resolve; }));
  input(ui.render()).props.onChange({ target: { value: "new-key" } });
  clear(ui.render()).props.onClick();
  submit(ui.render());
  await ui.settle();
  assert.equal(ui.calls.length, 2);
  assert.equal(ui.calls[1].options.method, "DELETE");
  assert.ok(elements(ui.render(), "button").every((button) => button.props.disabled));
  finish({ ...configured, source: "environment" });
  const tree = await ui.settle();
  assert.ok(JSON.stringify(tree).includes("当前使用服务器环境中的 SNAPANY_API_KEY"));
  assert.equal(clear(tree), undefined);
  assert.equal(input(tree).props.value, "");
});

test("missing encryption setup disables saves while allowing stored configuration to be cleared", async () => {
  const ui = await mount({ ...configured, canConfigure: false, environmentConfigured: false });
  const tree = ui.render();
  assert.ok(JSON.stringify(tree).includes("PLATFORM_SECRET_KEY"));
  assert.equal(input(tree).props.disabled, true);
  assert.equal(elements(tree, "button")[0].props.disabled, true);
  assert.equal(clear(tree).props.disabled, false);
  ui.setResult({ ...configured, configured: false, source: "none", canConfigure: false });
  clear(tree).props.onClick();
  await ui.settle();
  assert.ok(ui.confirmations[0].message.includes("视频分享链接解析将停用"));
  assert.equal(ui.calls[1].options.method, "DELETE");
});

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/DeepSeekWebTools.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const provider = { id: "provider-1", name: "工具供应商", enabled: true, hasKey: true, baseUrl: "https://tools.example.test" };
const configured = {
  version: "v1", enabled: true, providerId: provider.id, searchEnabled: true, fetchEnabled: true,
  searchSaleMicro: 5000, searchCostMicro: 3000, fetchSaleMicro: 7000, fetchCostMicro: 2000,
  source: "console", configured: true, providerName: provider.name, providerEnabled: true, hasKey: true, canConfigure: true,
};

// 执行真实表单事件，覆盖字符串金额到 micro 的边界和 CAS 重试，而非复刻转换实现。
async function mount(initial = configured, providers = [provider], loadError = null) {
  const slots = [];
  const effects = [];
  const calls = [];
  const confirmations = [];
  const toasts = [];
  let cursor = 0;
  let result = initial;
  let accepted = true;
  let mutation = async () => result;
  let failure = loadError;
  const jsx = (type, props) => ({ type, props });
  const output = {};
  runInNewContext(compiled, {
    exports: output, Error,
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
          if (!(index in slots)) { slots[index] = true; effects.push(effect); }
        },
      };
      if (name === "../api.js") return { api: async (path, options) => {
        calls.push({ path, options });
        if (options) return mutation(path, options);
        if (failure) throw failure;
        return result;
      } };
      if (name === "../components/Feedback.js") return {
        toastOk: (...args) => toasts.push(args), toastErr: (...args) => toasts.push(args),
        confirmDialog: async (options) => { confirmations.push(options); return { confirmed: accepted }; },
      };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      if (name === "../components/Select.js") return { Select: "Select" };
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      if (name === "../components/Icon.js") return { Glyph: () => null };
      throw new Error("Unexpected import " + name);
    },
  });
  const render = () => { cursor = 0; const tree = output.DeepSeekWebToolsPanel({ providers }); effects.splice(0).forEach((effect) => effect()); return tree; };
  const settle = async () => { await new Promise(setImmediate); return render(); };
  render(); await settle();
  return { render, settle, calls, confirmations, toasts,
    setResult(value) { result = value; failure = null; },
    setAccepted(value) { accepted = value; },
    setMutation(value) { mutation = value; },
    setProviders(value) { providers = value; },
  };
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
const prices = (tree) => elements(tree, "input").filter((input) => input.props.inputMode === "decimal");
const submit = (tree) => elements(tree, "form")[0].props.onSubmit({ preventDefault() {} });
const edit = (ui, index, value) => prices(ui.render())[index].props.onChange({ target: { value } });
const button = (tree, label) => elements(tree, "button").find((item) => text(item).includes(label));

test("web tools load independent request prices and save 0.005 plus free prices as exact micro integers", async () => {
  const ui = await mount();
  assert.equal(ui.calls[0].path, "/deepseek-web-tools");
  assert.deepEqual(prices(ui.render()).map((input) => input.props.value), ["0.005", "0.003", "0.007", "0.002"]);
  assert.match(text(ui.render()), /¥0.002 \/ 次/);
  assert.equal(elements(ui.render(), "input").some((input) => input.props.type === "password"), false);
  edit(ui, 0, " 0.005 "); edit(ui, 1, "0"); edit(ui, 2, "2.123456"); edit(ui, 3, "0.000001");
  ui.setMutation(async () => ({ ...configured, version: "v2", searchSaleMicro: 9000 }));
  submit(ui.render()); await ui.settle();
  assert.equal(ui.calls[1].options.method, "PUT");
  assert.deepEqual(JSON.parse(ui.calls[1].options.body), {
    expectedVersion: "v1", enabled: true, providerId: provider.id, searchEnabled: true, fetchEnabled: true,
    searchSaleMicro: 5000, searchCostMicro: 0, fetchSaleMicro: 2123456, fetchCostMicro: 1,
  });
  assert.equal(prices(ui.render())[0].props.value, "0.009");
});

test("web tools reject invalid, negative, exponential, over-precision and unsafe prices before PUT", async () => {
  for (const value of ["", "-0.1", "0.0000001", "1e3", "1.", "01", "NaN", "9007199254.740992"]) {
    const ui = await mount(); edit(ui, 0, value); submit(ui.render());
    const tree = await ui.settle();
    assert.equal(ui.calls.length, 1, value);
    assert.equal(prices(tree)[0].props.value, value);
    assert.match(text(tree), /最多六位小数/);
  }
});

test("web tool prices preserve the maximum safe integer and negative margins without floating rounding", async () => {
  const ui = await mount({ ...configured, searchSaleMicro: Number.MAX_SAFE_INTEGER, searchCostMicro: 1 });
  assert.equal(prices(ui.render())[0].props.value, "9007199254.740991");
  edit(ui, 2, "9007199254.740991"); submit(ui.render()); await ui.settle();
  const body = JSON.parse(ui.calls[1].options.body);
  assert.equal(body.searchSaleMicro, Number.MAX_SAFE_INTEGER);
  assert.equal(body.fetchSaleMicro, Number.MAX_SAFE_INTEGER);
  edit(ui, 0, "0"); edit(ui, 1, "0.005");
  assert.match(text(ui.render()), /¥-0.005 \/ 次/);
});

test("web tools retain a CAS-conflicted draft and explicitly reload its version before retry", async () => {
  const ui = await mount(); edit(ui, 0, "0.08");
  ui.setMutation(async () => { throw new Error("配置已被更新，请刷新后重试"); });
  submit(ui.render()); await ui.settle();
  assert.equal(prices(ui.render())[0].props.value, "0.08");
  assert.match(text(ui.render()), /配置已被更新/);
  ui.setResult({ ...configured, version: "authority-v3", searchSaleMicro: 12000 });
  button(ui.render(), "重新载入").props.onClick(); await ui.settle();
  assert.equal(prices(ui.render())[0].props.value, "0.012");
  edit(ui, 0, "0.09"); ui.setMutation(async () => ({ ...configured, version: "v4" }));
  submit(ui.render()); await ui.settle();
  assert.equal(JSON.parse(ui.calls.at(-1).options.body).expectedVersion, "authority-v3");
});

test("pending web tool writes disable editing and reject duplicate submission before rerender", async () => {
  const ui = await mount(); let finish;
  ui.setMutation(() => new Promise((resolve) => { finish = resolve; }));
  const tree = ui.render(); submit(tree); submit(tree);
  assert.equal(ui.calls.length, 2);
  assert.ok(elements(ui.render(), "input").every((input) => input.props.disabled));
  assert.ok(elements(ui.render(), "button").every((item) => item.props.disabled));
  finish(configured); await ui.settle();
});

test("web tool provider changes immediately show unavailable state and keep the explicit binding", async () => {
  const ui = await mount(); ui.setProviders([{ ...provider, enabled: false }]);
  let tree = ui.render();
  assert.match(text(tree), /配置不可用/); assert.match(text(tree), /所选供应商已停用/);
  assert.equal(button(tree, "保存工具配置").props.disabled, true);
  const select = elements(tree, "Select")[0];
  assert.equal(select.props.value, provider.id); assert.match(select.props.options[1].label, /已停用/);
  ui.setProviders([]); tree = ui.render();
  assert.equal(elements(tree, "Select")[0].props.value, provider.id);
  assert.match(text(tree), /网关不会回退/);
});

test("clear uses versioned DELETE only after confirmation and never promises environment fallback", async () => {
  const ui = await mount(); ui.setAccepted(false);
  button(ui.render(), "停用并清除绑定").props.onClick(); await ui.settle();
  assert.equal(ui.calls.length, 1);
  ui.setAccepted(true); ui.setMutation(async () => ({ ...configured, enabled: false, providerId: "", configured: false }));
  button(ui.render(), "停用并清除绑定").props.onClick(); await ui.settle();
  assert.equal(ui.calls[1].options.method, "DELETE");
  assert.deepEqual(JSON.parse(ui.calls[1].options.body), { expectedVersion: "v1" });
  assert.match(text(ui.render()), /已停用/);
  assert.match(ui.confirmations[0].message, /供应商及其密钥保留/);
  assert.doesNotMatch(ui.confirmations[0].message, /回退/);
});

test("connectivity test submits only an explicit provider and explains its scope", async () => {
  const ui = await mount(); ui.setMutation(async () => ({ ok: true, status: 200, latencyMs: 2 }));
  button(ui.render(), "测试连通与鉴权").props.onClick(); await ui.settle();
  assert.equal(ui.calls[1].path, "/deepseek-web-tools/test");
  assert.deepEqual(JSON.parse(ui.calls[1].options.body), { providerId: provider.id });
  assert.match(ui.toasts[0][0], /不验证 Search\/Fetch 权限/);
});

test("failed web tool loads expose a retry without editable fallback prices", async () => {
  const ui = await mount(configured, [provider], new Error("读取失败"));
  assert.equal(prices(ui.render()).length, 0);
  ui.setResult(configured); button(ui.render(), "重新载入").props.onClick(); await ui.settle();
  assert.equal(prices(ui.render())[0].props.value, "0.005");
});

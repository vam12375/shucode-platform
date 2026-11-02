import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";
import * as helpers from "../src/payments.ts";

const source = await readFile(new URL("../src/pages/Payments.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 } }).outputText;
const team = { id: "team-a", name: "共享团队", quotaCredits: "12.000000", usedCredits: "2.000000" };
const account = { mode: "team", enabled: true, personal: { id: "user-a", name: "本人余额", quotaCredits: "3.000000", usedCredits: "1.000000", balanceCredits: "2.000000" }, team: { ...team, balanceCredits: "10.000000" } };
const order = { id: "order-a", outTradeNo: "md_fixture_a", targetType: "personal", targetId: "user-a", teamId: "personal-ledger", userId: "user-a", amountYuan: "10.00", creditAmount: "10.00", status: "pending", testPayment: false, createdAt: "2026-11-02T00:00:00Z", expiresAt: "2026-11-02T00:30:00Z", paidAt: null };
const config = { enabled: true, appId: "app-fixture", sellerId: "seller-fixture", gateway: "production", privateKeyConfigured: true, publicKeyConfigured: true, notifyUrl: "https://example.test/notify", returnUrl: "https://example.test/return" };

async function mount({ root = false, manager = false, storage = new Map(), panel = false, loadFailure = false } = {}) {
  const slots = [], scheduled = [], calls = [], navigations = [], toasts = [], timers = new Map(), listeners = new Map();
  let cursor = 0, uuid = 0, timerId = 0, activeAccount = account, currentOrder = order, currentConfig = config;
  let ordersResponse = { orders: [], nextCursor: null };
  let mutation = async (path) => path === "/payments/config" ? currentConfig : path === "/payments/account" ? { mode: "personal" } : path.endsWith("/reconcile") ? { order: currentOrder } : { order: currentOrder, payUrl: "https://openapi.alipay.com/gateway.do?fixture=1" };
  let query = null, failure = loadFailure;
  const auth = { user: { id: "user-a", email: "member@example.test", displayName: "本人" }, teams: [team], team, isRoot: root, memberships: [{ team, role: manager ? "admin" : "member", memberDisabledAt: null }], refreshSession: async () => {} };
  const document = { visibilityState: "visible", addEventListener: (name, fn) => listeners.set(name, fn), removeEventListener: (name, fn) => { if (listeners.get(name) === fn) listeners.delete(name); } };
  const jsx = (type, props) => ({ type, props });
  const same = (a, b) => a?.length === b?.length && a.every((v, i) => Object.is(v, b[i]));
  const output = {};
  runInNewContext(compiled, {
    exports: output, Error, URL, URLSearchParams, AbortController, document,
    crypto: { randomUUID: () => "11111111-1111-4111-8111-" + String(++uuid).padStart(12, "0") },
    sessionStorage: { getItem: (key) => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: (key) => storage.delete(key) },
    window: { matchMedia: () => ({ matches: false }), setTimeout: (fn) => { const id = ++timerId; timers.set(id, fn); return id; }, clearTimeout: (id) => timers.delete(id), location: { assign: (url) => navigations.push(url), hash: "#/payments?status=success" } },
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = typeof initial === "function" ? initial() : initial; return [slots[i], (v) => { slots[i] = typeof v === "function" ? v(slots[i]) : v; }]; },
        useRef(initial) { const i = cursor++; if (!(i in slots)) slots[i] = { current: initial }; return slots[i]; },
        useCallback(fn, deps) { const i = cursor++; if (!(i in slots) || !same(slots[i].deps, deps)) slots[i] = { deps, fn }; return slots[i].fn; },
        useMemo(fn, deps) { const i = cursor++; if (!(i in slots) || !same(slots[i].deps, deps)) slots[i] = { deps, value: fn() }; return slots[i].value; },
        useEffect(effect, deps) { const i = cursor++; if (!(i in slots) || !same(slots[i].deps, deps)) { const old = slots[i]; slots[i] = { deps }; scheduled.push(() => { old?.cleanup?.(); slots[i].cleanup = effect(); }); } },
      };
      if (name === "../auth.js") return { useAuth: () => auth };
      if (name === "../payments.js") return helpers;
      if (name === "../time.js") return { formatShanghaiTime: (value) => value };
      if (name === "../api.js") return { api: async (path, init = {}) => {
        calls.push({ path, init });
        if (init.method) return mutation(path, init);
        if (failure) throw new Error("fixture读取失败");
        if (query) return query(path, init);
        if (path === "/payments/account") return activeAccount;
        if (path === "/payments/config") return currentConfig;
        if (path.startsWith("/payments/orders?")) return ordersResponse;
        return { order: currentOrder };
      } };
      if (name === "../components/Feedback.js") return { toastOk: (...args) => toasts.push(args) };
      if (name === "../components/Select.js") return { Select: "Select" };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      if (name === "lucide-react") return new Proxy({}, { get: () => () => null });
      if (name === "../components/Icon.js") return { Glyph: () => null, PageTitle: "PageTitle" };
      throw new Error("Unexpected import " + name);
    },
  });
  const render = () => { cursor = 0; const tree = panel ? output.PaymentConfigPanel() : output.PaymentsPage(); scheduled.splice(0).forEach((fn) => fn()); return tree; };
  const settle = async () => { await new Promise(setImmediate); return render(); };
  render(); await settle();
  return { render, settle, calls, navigations, toasts, storage, timers, listeners,
    setMutation: (fn) => { mutation = fn; }, setQuery: (fn) => { query = fn; }, setOrder: (value) => { currentOrder = value; }, setAccount: (value) => { activeAccount = value; }, setConfig: (value) => { currentConfig = value; }, setFailure: (value) => { failure = value; }, setOrders: (value) => { ordersResponse = value; },
    async tick() { const entries = [...timers]; timers.clear(); for (const [, fn] of entries) await fn(); return settle(); },
    visibility(value) { document.visibilityState = value; listeners.get("visibilitychange")?.(); },
    unmount() { slots.forEach((slot) => slot?.cleanup?.()); },
  };
}
function nodes(tree, type) { if (!tree || typeof tree !== "object") return []; if (Array.isArray(tree)) return tree.flatMap((item) => nodes(item, type)); return [...(tree.type === type ? [tree] : []), ...nodes(tree.props?.children, type)]; }
function text(tree) { if (tree === null || tree === undefined) return ""; if (Array.isArray(tree)) return tree.map(text).join(""); return typeof tree === "object" ? text(tree.props?.children) : String(tree); }
const button = (tree, label) => nodes(tree, "button").find((item) => text(item).includes(label));
const select = (tree, label) => nodes(tree, "Select").find((item) => item.props.ariaLabel === label);
const submit = (ui) => nodes(ui.render(), "form")[0].props.onSubmit({ preventDefault() {} });
const amount = (ui, value) => nodes(ui.render(), "input").find((item) => item.props["aria-label"] === "充值金额（元）").props.onChange({ target: { value } });
const writes = (ui) => ui.calls.filter((call) => call.init.method);

test("普通成员仅有本人充值入口，普通表单拒绝0.01且不挂载root配置", async () => {
  const ui = await mount();
  assert.deepEqual([...select(ui.render(), "充值目标").props.options].map((item) => item.value), ["personal"]);
  assert.equal(button(ui.render(), "0.01 元支付测试"), undefined);
  assert.equal(ui.calls.some((call) => call.path === "/payments/config"), false);
  amount(ui, "0.01"); submit(ui); await ui.settle();
  assert.equal(writes(ui).length, 0); assert.match(text(ui.render()), /充值金额为 1–100000/);
});

test("网络失败保留同一请求编号，快速重复提交被同步阻止", async () => {
  const ui = await mount(); let reject;
  ui.setMutation(() => new Promise((_resolve, fail) => { reject = fail; }));
  const tree = ui.render(); nodes(tree, "form")[0].props.onSubmit({ preventDefault() {} }); nodes(tree, "form")[0].props.onSubmit({ preventDefault() {} });
  assert.equal(writes(ui).length, 1);
  assert.equal(nodes(ui.render(), "input").every((item) => item.props.disabled), true);
  const first = JSON.parse(writes(ui)[0].init.body);
  reject(new Error("fixture网络中断")); await ui.settle();
  assert.match(text(ui.render()), /请求编号已保留/);
  ui.setMutation(async () => ({ order, payUrl: "https://openapi.alipay.com/gateway.do?fixture=retry" }));
  submit(ui); await ui.settle();
  assert.equal(JSON.parse(writes(ui)[1].init.body).requestId, first.requestId);
  assert.equal(ui.navigations.length, 1); assert.equal(ui.storage.get("mendao:payments:v1:user-a:order"), order.outTradeNo);
  amount(ui, "20"); submit(ui); await ui.settle();
  assert.notEqual(JSON.parse(writes(ui)[2].init.body).requestId, first.requestId);
});

test("异常支付URL保存待核对订单且不导航，返回页query不触发成功", async () => {
  const ui = await mount();
  ui.setMutation(async () => ({ order, payUrl: "https://evil.test/gateway.do" }));
  submit(ui); await ui.settle();
  assert.equal(ui.navigations.length, 0); assert.match(text(ui.render()), /不是支付宝官方网关/);
  assert.match(text(ui.render()), /待付款 \/ 待确认/); assert.doesNotMatch(text(ui.render()), /该订单已增加/);
  assert.equal(writes(ui).filter((call) => call.path.endsWith("/reconcile")).length, 0);
});

test("root测试直接提交当前团队0.01实付订单，普通金额草稿不改变", async () => {
  const ui = await mount({ root: true });
  select(ui.render(), "充值目标").props.onChange("team"); await ui.settle();
  button(ui.render(), "0.01 元支付测试").props.onClick(); await ui.settle();
  const body = JSON.parse(writes(ui)[0].init.body);
  assert.equal(body.amountYuan, "0.01"); assert.equal(body.testPayment, true); assert.equal(body.targetType, "team"); assert.equal(body.teamId, team.id);
  assert.equal(nodes(ui.render(), "input").find((item) => item.props["aria-label"] === "充值金额（元）").props.value, "10.00");
});

test("恢复pending订单只轮询本地状态，页面隐藏和卸载取消在途请求", async () => {
  const ui = await mount({ storage: new Map([["mendao:payments:v1:user-a:order", order.outTradeNo]]) });
  assert.match(text(ui.render()), /待付款 \/ 待确认/); assert.equal(ui.timers.size, 1);
  let signal;
  ui.setQuery(async (path, init) => { if (path.endsWith(order.outTradeNo)) { signal = init.signal; return new Promise(() => {}); } return path === "/payments/account" ? account : { orders: [], nextCursor: null }; });
  const timer = [...ui.timers.values()][0]; ui.timers.clear(); timer();
  assert.equal(signal.aborted, false); ui.visibility("hidden"); assert.equal(signal.aborted, true); assert.equal(ui.timers.size, 0);
  ui.visibility("visible"); assert.equal(signal.aborted, false); ui.unmount(); assert.equal(signal.aborted, true); assert.equal(ui.listeners.size, 0);
  assert.equal(writes(ui).length, 0);
});

test("可信paid查单后刷新余额；失败查单清楚显示而不改成支付成功", async () => {
  const ui = await mount({ storage: new Map([["mendao:payments:v1:user-a:order", order.outTradeNo]]) });
  ui.setMutation(async () => { throw new Error("fixture查询失败"); });
  button(ui.render(), "查询支付宝并核对").props.onClick(); await ui.settle();
  assert.match(text(ui.render()), /fixture查询失败/); assert.doesNotMatch(text(ui.render()), /该订单已增加/);
  ui.setAccount({ ...account, personal: { ...account.personal, balanceCredits: "12.000000" } });
  ui.setMutation(async () => ({ order: { ...order, status: "paid", paidAt: "2026-11-02T00:01:00Z" } }));
  button(ui.render(), "查询支付宝并核对").props.onClick(); await ui.settle();
  assert.match(text(ui.render()), /已支付并入账/); assert.match(text(ui.render()), /12.000000/); assert.equal(ui.timers.size, 0);
});

test("私钥只写入不回显，空密钥保留，保存失败保留草稿并防止重复写入", async () => {
  const ui = await mount({ panel: true });
  assert.deepEqual(nodes(ui.render(), "textarea").map((item) => item.props.value), ["", ""]);
  nodes(ui.render(), "textarea")[0].props.onChange({ target: { value: "fixture-private-key" } });
  let reject;
  ui.setMutation(() => new Promise((_resolve, fail) => { reject = fail; }));
  const tree = ui.render(); button(tree, "保存支付宝配置").props.onClick(); button(tree, "保存支付宝配置").props.onClick();
  assert.equal(writes(ui).length, 1); reject(new Error("fixture保存失败")); await ui.settle();
  assert.equal(nodes(ui.render(), "textarea")[0].props.value, "fixture-private-key");
  ui.setMutation(async () => config); button(ui.render(), "保存支付宝配置").props.onClick(); await ui.settle();
  assert.equal(JSON.parse(writes(ui)[1].init.body).privateKey, "fixture-private-key"); assert.equal("publicKey" in JSON.parse(writes(ui)[1].init.body), false);
  assert.deepEqual(nodes(ui.render(), "textarea").map((item) => item.props.value), ["", ""]);
  button(ui.render(), "保存支付宝配置").props.onClick(); await ui.settle(); assert.equal("privateKey" in JSON.parse(writes(ui)[2].init.body), false);
});

test("账户及配置载入失败有明确重试，尚无个人账本仍可充值", async () => {
  const failed = await mount({ loadFailure: true });
  assert.match(text(failed.render()), /fixture读取失败/); assert.equal(button(failed.render(), "前往支付宝付款").props.disabled, true);
  failed.setFailure(false); failed.setAccount({ ...account, personal: null }); button(failed.render(), "重新载入").props.onClick(); await failed.settle();
  assert.match(text(failed.render()), /0.000000/); assert.equal(button(failed.render(), "前往支付宝付款").props.disabled, false);
  const panel = await mount({ panel: true, loadFailure: true }); assert.ok(button(panel.render(), "重试读取支付配置"));
});

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/pages/MediaTemplateReviews.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
const template = {
  id: "texture", version: "1.0.0", family: "product",
  name: { zh: "材质细节", en: "Texture" }, description: { zh: "突出真实材质", en: "Show texture" },
  industries: ["服饰"], objectives: ["产品展示"], keywords: ["细节"], requiredFacts: ["真实材质"],
  assetRequirements: ["近景商品图"], avoidWhen: ["无纹理图片"], minAssets: 1,
  aspectRatios: ["9:16"], durationRange: { min: 12, max: 60 },
  beats: [{ purpose: "细节钩子", framing: "近景", action: "缓慢旋转", camera: "推近", weight: 1 }],
  qualityChecks: ["纹理一致"], verifiedModelIds: [],
};
const review = {
  templateId: template.id, templateVersion: template.version, projectTeamId: "team-evidence",
  projectId: "project-evidence", score: 91, approved: true, note: "人工观看通过",
  modelId: "video-model", modelSignature: "snapshot", outputAssetId: "output-evidence",
  reviewerId: "root-user", reviewedAt: "2026-10-30T01:00:00Z",
};

function mount({ root = true, handler, templates = [template], reviews = [] } = {}) {
  const states = new Map();
  const calls = [];
  const dialogs = [];
  const toasts = [];
  let context;
  let rootValue = root;
  let writes = 0;
  let effects = [];
  let used = new Set();
  const jsx = (type, props) => ({ type, props });
  const output = {};
  runInNewContext(compiled, {
    exports: output, Error,
    require(name) {
      if (name === "react/jsx-runtime") return { jsx, jsxs: jsx };
      if (name === "react") return {
        useState(initial) {
          const owner = context, index = owner.cursor++;
          if (!(index in owner.slots)) owner.slots[index] = typeof initial === "function" ? initial() : initial;
          return [owner.slots[index], (value) => {
            writes++;
            owner.slots[index] = typeof value === "function" ? value(owner.slots[index]) : value;
          }];
        },
        useRef(initial) {
          const owner = context, index = owner.cursor++;
          if (!(index in owner.slots)) owner.slots[index] = { current: initial };
          return owner.slots[index];
        },
        useCallback(callback, deps) {
          const owner = context, index = owner.cursor++, old = owner.slots[index];
          if (!old || deps.some((value, i) => !Object.is(value, old.deps[i]))) owner.slots[index] = { callback, deps };
          return owner.slots[index].callback;
        },
        useEffect(effect, deps) {
          const owner = context, index = owner.cursor++, old = owner.slots[index];
          if (!old || deps.some((value, i) => !Object.is(value, old.deps[i]))) {
            old?.cleanup?.();
            const state = { deps, effect: true };
            owner.slots[index] = state;
            effects.push(() => { state.cleanup = effect(); });
          }
        },
      };
      if (name === "../auth.js") return { useAuth: () => ({ isRoot: rootValue, user: { id: "root-user" } }) };
      if (name === "../api.js") return { api: async (path, options) => {
        calls.push({ path, options });
        if (handler) return handler(path, options);
        if (options) return JSON.parse(options.body);
        return path.endsWith("/reviews") ? reviews : templates;
      } };
      if (name === "../components/Feedback.js") return {
        toastOk: (message) => toasts.push(message),
        confirmDialog: async (options) => { dialogs.push(options); return { confirmed: true, value: "样本不再满足一致性" }; },
      };
      if (name === "../components/Select.js") return { Select: "Select" };
      if (name === "../components/StatusChip.js") return { Chip: "Chip", Notice: "Notice" };
      if (name === "../time.js") return { formatShanghaiTime: (value) => value };
      throw new Error("Unexpected import " + name);
    },
  });
  function expand(node) {
    if (Array.isArray(node)) return node.map(expand);
    if (!node || typeof node !== "object") return node;
    if (typeof node.type === "function") {
      used.add(node.type);
      if (!states.has(node.type)) states.set(node.type, { slots: [], cursor: 0 });
      context = states.get(node.type);
      context.cursor = 0;
      return expand(node.type(node.props));
    }
    return { ...node, props: { ...node.props, children: expand(node.props?.children) } };
  }
  function cleanup(owner) { owner.slots.forEach((slot) => { if (slot?.effect) slot.cleanup?.(); }); }
  function render() {
    used = new Set();
    const tree = expand(jsx(output.MediaTemplateReviews, {}));
    for (const [component, owner] of states) if (!used.has(component)) { cleanup(owner); states.delete(component); }
    effects.splice(0).forEach((effect) => effect());
    return tree;
  }
  const ui = { render, calls, dialogs, toasts,
    setRoot(value) { rootValue = value; return render(); },
    unmount() { for (const owner of states.values()) cleanup(owner); states.clear(); effects = []; },
    writes: () => writes,
    async settle() { await new Promise(setImmediate); return render(); },
  };
  render();
  return ui;
}

function nodes(tree, predicate) {
  if (Array.isArray(tree)) return tree.flatMap((node) => nodes(node, predicate));
  if (!tree || typeof tree !== "object") return [];
  return [...(predicate(tree) ? [tree] : []), ...nodes(tree.props?.children, predicate)];
}
function text(tree) {
  if (Array.isArray(tree)) return tree.map(text).join("");
  if (!tree || typeof tree !== "object") return tree ?? "";
  return (tree.type === "Notice" ? tree.props.k ?? "" : "") + text(tree.props?.children);
}
function field(ui, label, value, checked) {
  const node = nodes(ui.render(), (item) => (item.props?.["aria-label"] ?? item.props?.ariaLabel) === label)[0];
  assert.ok(node, label + " exists");
  node.props.onChange(node.type === "Select" ? value : { target: { value, checked } });
}
function submit(ui) { nodes(ui.render(), (node) => node.type === "form")[0].props.onSubmit({ preventDefault() {} }); }
function evidence(ui, score = "91") {
  field(ui, "证据团队 ID", " team-evidence ");
  field(ui, "证据制作单 ID", " project-evidence ");
  field(ui, "审核评分", score);
  field(ui, "审核结论", "approved");
  field(ui, "审核说明", " 人工观看通过 ");
  field(ui, "已观看成片", undefined, true);
}

test("non-root never mounts management requests or cached evidence", async () => {
  const ui = mount({ root: false });
  assert.match(text(await ui.settle()), /仅超级管理员/);
  assert.equal(ui.calls.length, 0);
});

test("catalog and history load together and expose recipe constraints without invented verification", async () => {
  const ui = mount();
  assert.match(text(ui.render()), /正在加载/);
  const tree = await ui.settle();
  assert.deepEqual(ui.calls.map((item) => item.path), ["/media/templates", "/media/templates/reviews"]);
  assert.match(text(tree), /材质细节/);
  assert.match(text(tree), /未验证/);
  assert.match(text(tree), /纹理一致/);
  assert.match(text(tree), /无纹理图片/);
});

test("timed recipes show nominal length, acts, tiers, style and sound so reviewers see the whole structure", async () => {
  const timed = {
    ...template, id: "gift", version: "2.0.0", durationRange: { min: 20, max: 60 }, nominalSeconds: 45,
    style: { look: "低饱和暖调", lens: "50毫米自然视角", motion: "一镜一动作", sound: "轻音乐托底", narration: "送礼人的短句" },
    beats: [
      { ...template.beats[0], weight: 3, seconds: 3, tier: "core", act: "收到一盒", sound: "无旁白，保留纸面摩擦" },
      { ...template.beats[0], weight: 2, seconds: 2, tier: "extra", act: "收到一盒", sound: "无旁白" },
    ],
  };
  const tree = text(await mount({ templates: [timed] }).settle());
  assert.match(tree, /标称 45 秒，可压缩到 20 秒、拉伸到 60 秒/);
  assert.match(tree, /整体风格：低饱和暖调；50毫米自然视角；一镜一动作；轻音乐托底；送礼人的短句/);
  assert.match(tree, /【收到一盒 · 3秒 · 核心】细节钩子/);
  assert.match(tree, /【收到一盒 · 2秒 · 可删】/);
  assert.match(tree, /声画：无旁白，保留纸面摩擦/);
  // 旧配方没有这些字段，页面不凭空补出标称时长或风格。
  const legacy = text(await mount().settle());
  assert.doesNotMatch(legacy, /标称|整体风格|声画：|【/);
});

test("load failures are visible and retry fetches fresh platform facts", async () => {
  let fail = true;
  const ui = mount({ handler: async (path) => {
    if (fail) throw new Error("fixture unavailable");
    return path.endsWith("/reviews") ? [] : [template];
  } });
  assert.match(text(await ui.settle()), /fixture unavailable/);
  fail = false;
  nodes(ui.render(), (node) => node.type === "button" && text(node) === "刷新")[0].props.onClick();
  assert.match(text(await ui.settle()), /材质细节/);
});

test("approval submits only evidence inputs and blocks duplicate mutation before rerender", async () => {
  const ui = mount();
  await ui.settle();
  evidence(ui);
  submit(ui);
  submit(ui);
  await ui.settle();
  const posts = ui.calls.filter((call) => call.options?.method === "POST");
  assert.equal(posts.length, 1);
  assert.deepEqual(JSON.parse(posts[0].options.body), {
    templateId: "texture", templateVersion: "1.0.0", projectTeamId: "team-evidence", projectId: "project-evidence",
    score: 91, approved: true, note: "人工观看通过",
  });
  assert.equal(ui.calls.filter((call) => !call.options).length, 4);
});

test("approval needs score threshold and explicit human viewing confirmation", async () => {
  const ui = mount();
  await ui.settle();
  evidence(ui, "84");
  submit(ui);
  assert.match(text(ui.render()), /85/);
  assert.equal(ui.calls.filter((call) => call.options).length, 0);
  field(ui, "审核评分", "91");
  field(ui, "已观看成片", undefined, false);
  submit(ui);
  assert.match(text(ui.render()), /观看/);
  assert.equal(ui.calls.filter((call) => call.options).length, 0);
});

test("latest approval can be revoked with an explicit reason through the same evidence API", async () => {
  const ui = mount({ templates: [{ ...template, verifiedModelIds: [review.modelId] }], reviews: [review] });
  const tree = await ui.settle();
  assert.match(text(tree), /video-model/);
  nodes(tree, (node) => node.type === "button" && text(node) === "撤销")[0].props.onClick();
  await ui.settle();
  assert.equal(ui.dialogs.length, 1);
  assert.equal(ui.dialogs[0].input.minLength, 1);
  const post = ui.calls.find((call) => call.options?.method === "POST");
  assert.deepEqual(JSON.parse(post.options.body), {
    templateId: review.templateId, templateVersion: review.templateVersion,
    projectTeamId: review.projectTeamId, projectId: review.projectId,
    score: review.score, approved: false, note: "样本不再满足一致性",
  });
});

test("permission loss and unmount discard late management responses", async () => {
  const pending = [];
  const ui = mount({ handler: (path) => new Promise((resolve) => pending.push({ path, resolve })) });
  ui.setRoot(false);
  const count = ui.writes();
  pending.forEach(({ path, resolve }) => resolve(path.endsWith("/reviews") ? [review] : [template]));
  await new Promise(setImmediate);
  assert.equal(ui.writes(), count);
  assert.doesNotMatch(text(ui.render()), /project-evidence/);
  const pending2 = [];
  const other = mount({ handler: (path) => new Promise((resolve) => pending2.push({ path, resolve })) });
  other.unmount();
  const unmountedCount = other.writes();
  pending2.forEach(({ path, resolve }) => resolve(path.endsWith("/reviews") ? [] : [template]));
  await new Promise(setImmediate);
  assert.equal(other.writes(), unmountedCount);
});

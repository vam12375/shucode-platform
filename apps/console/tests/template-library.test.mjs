import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import ts from "typescript";

const source = await readFile(new URL("../src/template-library.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
const library = {};
runInNewContext(compiled, { exports: library });
const entry = { ...library.emptyTemplate(), id: "legacy:sample", kind: "skill", source: "dineops", name: "门店体验", tags: ["餐饮"], enabled: false, expectedVersion: "version", packageDigest: "digest", package: { sha256: "digest", files: [{ path: "SKILL.md" }, { path: "references/体验流程.md" }] } };

test("组合筛选能找出已停用技能，搜索不依赖标题大小写", () => {
  assert.equal(library.filterTemplates([entry], "skill", "disabled", "餐饮").length, 1);
  assert.equal(library.filterTemplates([entry], "skill", "enabled", "").length, 0);
  assert.equal(library.filterTemplates([entry], "prompt", "all", "").length, 0);
  assert.equal(library.filterTemplates([entry], "all", "all", "LEGACY").length, 1);
});
test("编辑正文保留完整包身份，不把管理字段或包清单作为可写字段", () => {
  const fields = library.entryFields(entry);
  assert.equal(fields.packageDigest, "digest");
  assert.equal("package" in fields, false);
  assert.equal("expectedVersion" in fields, false);
  fields.tags.push("changed");
  assert.equal(entry.tags.length, 1);
});

test("旧视频提示词归入视频筛选并保留文本编辑语义", () => {
  const video = { ...entry, id: "dineops:video-template:original", kind: "video" };
  assert.equal(library.filterTemplates([video], "video", "all", "").length, 1);
  assert.equal(library.filterTemplates([video], "template", "all", "").length, 0);
  assert.equal(library.isVideoRecipe(video), false);
  assert.equal(library.isVideoRecipe({ ...video, source: "builtin-video" }), true);
});

import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const source = await readFile(new URL("../src/pages/Tenant.tsx", import.meta.url), "utf8");
const policyStart = source.indexOf("<h2>门道自然语言画布协作策略</h2>");
const policyEnd = source.indexOf("</section>", policyStart);
const policySection = source.slice(policyStart, policyEnd);

test("门道画布策略区分已执行的运行限制和待接入计数限制", () => {
  assert.notEqual(policyStart, -1);
  assert.match(policySection, /单次报价限制已在服务端执行/);
  assert.match(policySection, /单次媒体报价上限会在门道自然语言画布协作触发媒体报价与生成时由服务端复核/);
  assert.match(policySection, /每日操作数和并发上限目前只保存/);
  assert.match(policySection, /每日媒体操作上限（暂不执行）/);
  assert.match(policySection, /并发媒体操作上限（暂不执行）/);
  assert.doesNotMatch(policySection, /不会向模型开放画布写入/);
});

import assert from "node:assert/strict";
import test from "node:test";
import {
  buildPackageUpdate,
  mergePackageDraft,
  normalizePackage,
  packageAmount,
  publishedPackages,
  recommendedByTarget,
  resolvePackageConflict,
  validatePackages,
} from "../src/creditPackages.ts";

const draft = {
  sku: "personal-99",
  name: "常用包",
  description: "日常使用",
  targetType: "personal",
  amountYuan: "99",
  creditAmount: "99.000000",
  status: "draft",
  recommended: true,
  sortOrder: 20,
};

test("套餐字段规范化保留金额字符串与 1:1 credit", () => {
  assert.deepEqual(normalizePackage(draft), {
    ...draft,
    amountYuan: "99.00",
    creditAmount: "99.00",
    sortOrder: 20,
  });
  assert.equal(packageAmount("100000.00"), "100000.00");
  for (const value of ["0", "0.01", "100000.01", "1e2", "01"]) assert.equal(packageAmount(value), null, value);
});

test("保存请求只发送服务端字段，并把 credit 按 1:1 派生", () => {
  assert.deepEqual(buildPackageUpdate([draft], "r7"), {
    expectedRevision: "r7",
    packages: [{
      sku: "personal-99",
      name: "常用包",
      description: "日常使用",
      targetType: "personal",
      amountYuan: "99.00",
      creditAmount: "99.00",
      status: "draft",
      recommended: true,
      sortOrder: 20,
    }],
  });
});

test("推荐套餐按目标最多一个，客户端预览只展示已上架", () => {
  const packages = [
    draft,
    { ...draft, sku: "personal-29", recommended: true, status: "published", sortOrder: 10 },
    { ...draft, sku: "team-999", targetType: "team", recommended: true, status: "published", sortOrder: 1 },
    { ...draft, sku: "team-2999", targetType: "team", recommended: false, status: "archived", sortOrder: 2 },
  ];
  const recommendations = recommendedByTarget(packages);
  assert.equal(recommendations.personal, "personal-29");
  assert.equal(recommendations.team, "team-999");
  assert.deepEqual(publishedPackages(packages).map((item) => item.sku), ["personal-29", "team-999"]);
});

test("CAS 冲突三方合并保留本地改动，吸收远端独立修改并标记同字段冲突", () => {
  const base = [draft];
  const local = [{ ...draft, name: "本地名称", description: "本地说明" }];
  const remote = [{ ...draft, description: "远端说明", sortOrder: 5 }];
  const merged = mergePackageDraft(base, local, remote);
  assert.equal(merged.packages[0].name, "本地名称");
  assert.equal(merged.packages[0].description, "本地说明");
  assert.equal(merged.packages[0].sortOrder, 5);
  assert.deepEqual(merged.conflicts, [{ sku: "personal-99", field: "description" }]);
  assert.equal(resolvePackageConflict(merged.packages, remote, merged.conflicts[0], "remote")[0].description, "远端说明");
});

test("目录校验拒绝重复 SKU、无效价格、多个推荐与坏排序", () => {
  assert.equal(validatePackages([draft]), null);
  assert.match(validatePackages([draft, draft]), /SKU.*重复/);
  assert.match(validatePackages([{ ...draft, amountYuan: "0.01" }]), /金额/);
  assert.match(validatePackages([draft, { ...draft, sku: "team-99", targetType: "team" }, { ...draft, sku: "team-199", targetType: "team" }]), /最多设置一个/);
  assert.match(validatePackages([{ ...draft, sortOrder: 10000 }]), /排序/);
});

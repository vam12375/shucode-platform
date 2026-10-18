#!/usr/bin/env node
// 从 TS 实现(packages/db)导出一段固定的加密/口令互操作向量,
// 供 Go 侧 internal/crypto 测试验证「存量数据可读、格式逐位一致」。
// 运行:node scripts/export-crypto-vectors.ts(仓库根目录,Node 24)。
import { randomBytes } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";
import { createSecretBox } from "../packages/db/src/crypto.ts";
import { hashPassword } from "../packages/db/src/passwords.ts";

const key = randomBytes(32).toString("base64");
const box = createSecretBox(key);

const plains = [
  "",
  "hello",
  "mdp_AbCdEfGh1234567890",
  "中文密文支持 ✓ emoji🔐",
  "x".repeat(4096),
];

const secretbox = plains.map((plain) => ({
  plain,
  payload: box.encrypt(plain),
}));

const passwords = [];
for (const password of ["mendao-admin", "中文口令 ✓", "l".repeat(200)]) {
  passwords.push({ password, stored: await hashPassword(password) });
}

const out = {
  key,
  secretbox,
  passwords,
};

mkdirSync("server/internal/crypto/testdata", { recursive: true });
writeFileSync(
  "server/internal/crypto/testdata/vectors.json",
  JSON.stringify(out, null, 2) + "\n",
);
console.log(
  `已导出 ${secretbox.length} 条 secretbox 向量、${passwords.length} 条口令向量`,
);

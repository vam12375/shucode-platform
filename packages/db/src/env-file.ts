import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

/**
 * 极简 .env 加载：pnpm 脚本的 cwd 是包目录，仓库根 .env 需要向上查找。
 * 只填充尚未设置的环境变量，不覆盖调用方显式传入的值。
 */
export function loadEnvFilesUpwards(startDir = process.cwd(), maxLevels = 4): void {
  let dir = resolve(startDir);
  for (let level = 0; level < maxLevels; level += 1) {
    for (const name of [".env.local", ".env"]) {
      const filePath = join(dir, name);
      if (!existsSync(filePath)) {
        continue;
      }
      applyEnvFile(filePath);
    }
    const parent = dirname(dir);
    if (parent === dir) {
      break;
    }
    dir = parent;
  }
}

function applyEnvFile(filePath: string): void {
  const content = readFileSync(filePath, "utf8");
  for (const rawLine of content.split(/\r?\n/u)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) {
      continue;
    }
    const separator = line.indexOf("=");
    if (separator <= 0) {
      continue;
    }
    const key = line.slice(0, separator).trim();
    let value = line.slice(separator + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }
    if (key && process.env[key] === undefined) {
      process.env[key] = value;
    }
  }
}

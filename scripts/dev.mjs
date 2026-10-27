#!/usr/bin/env node
/**
 * 本地一键启动：.env → Postgres（docker compose）→ 迁移 → 种子 → api / gateway / console。
 *
 *   pnpm dev          # 读取根目录 .env（缺失时从 .env.example 生成并填好 PLATFORM_SECRET_KEY）
 *   pnpm dev:memory   # 进程内存储，跳过 Postgres / 迁移 / 种子
 *
 * 后端(api/gateway/迁移/种子)通过 make -C server 在 golang:1.26.10 容器内 go run，
 * 与服务器/CI 的构建口径一致;本机无需安装 Go。容器内访问宿主机 Postgres/Redis 时
 * DSN 会改写为 host.docker.internal。console 仍由 vite 在宿主机跑。
 * Ctrl-C 停止全部子进程（含 docker run / vite）。
 */
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { appendFileSync, copyFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { createConnection } from "node:net";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
/** 仅内存体验模式内置的演示 root；Postgres 模式的 root 由控制台首次初始化创建。 */
const DEMO_ROOT_EMAIL = "admin@mendao.chat";
const DEMO_ROOT_PASSWORD = "mendao-admin";
/** 服务在日志里报告就绪的判断：api/gateway 打印“已启动”，vite 打印 Local 地址。 */
// oxlint-disable-next-line no-control-regex -- 这里就是要匹配终端颜色转义序列
const ANSI = /\u001b\[[0-9;]*m/gu;

const COLOR = {
  step: "\u001b[36m",
  warn: "\u001b[33m",
  error: "\u001b[31m",
  api: "\u001b[36m",
  gateway: "\u001b[35m",
  console: "\u001b[33m",
  reset: "\u001b[0m",
};
const useColor = Boolean(process.stdout.isTTY) && !process.env.NO_COLOR;
const paint = (color, text) => (useColor ? `${COLOR[color]}${text}${COLOR.reset}` : text);

function step(message) {
  console.log(`\n${paint("step", "▸")} ${message}`);
}
function info(message) {
  console.log(`  ${message}`);
}
function warn(message) {
  console.log(`  ${paint("warn", "warning:")} ${message}`);
}
/** fail() 用异常终止 main()，避免失败后继续执行后续步骤。 */
class Stop extends Error {}

function fail(message) {
  console.error(`\n${paint("error", "error:")} ${message}`);
  shutdown("停止已启动的进程…", 1);
  throw new Stop(message);
}
function sleep(ms) {
  return new Promise((done) => setTimeout(done, ms));
}

// ---------- .env ----------

/**
 * 首次运行时从 .env.example 生成 .env；PLATFORM_SECRET_KEY 为空则就地填入新生成的密钥
 * （Postgres 模式下 API 强制要求该密钥，手填会打断一键启动）。
 */
function ensureEnvFile() {
  const envPath = join(ROOT, ".env");
  const examplePath = join(ROOT, ".env.example");
  if (!existsSync(envPath)) {
    copyFileSync(examplePath, envPath);
    step("已从 .env.example 生成 .env");
  }
  const content = readFileSync(envPath, "utf8");
  // 注意：\s 在 JS 里会跨行，这里用 [^\S\n] 只匹配行内空白，避免把下一行内容当作已填值。
  if (/^[^\S\n]*PLATFORM_SECRET_KEY[^\S\n]*=[^\S\n]*\S/mu.test(content)) {
    return;
  }
  const key = randomBytes(32).toString("base64");
  if (/^[^\S\n]*PLATFORM_SECRET_KEY[^\S\n]*=[^\S\n]*$/mu.test(content)) {
    writeFileSync(
      envPath,
      content.replace(
        /^[^\S\n]*PLATFORM_SECRET_KEY[^\S\n]*=[^\S\n]*$/mu,
        `PLATFORM_SECRET_KEY=${key}`,
      ),
    );
  } else {
    appendFileSync(envPath, `\n# 由 scripts/dev.mjs 生成（本地开发）\nPLATFORM_SECRET_KEY=${key}\n`);
  }
  step("已在 .env 填入生成的 PLATFORM_SECRET_KEY");
}

/** 与 packages/db 的 loadEnvFilesUpwards 同样的语义：只填充尚未设置的环境变量。 */
function loadRootEnv() {
  for (const name of [".env.local", ".env"]) {
    const filePath = join(ROOT, name);
    if (!existsSync(filePath)) {
      continue;
    }
    for (const rawLine of readFileSync(filePath, "utf8").split(/\r?\n/u)) {
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
}

function isTruthy(value) {
  const normalized = value?.trim().toLowerCase();
  return normalized === "1" || normalized === "true" || normalized === "yes";
}

/** 容器内访问宿主机:localhost/127.0.0.1 改写为 host.docker.internal。 */
function toDockerUrl(value) {
  if (!value) {
    return value;
  }
  return value
    .replace(/@localhost([:/])/u, "@host.docker.internal$1")
    .replace(/@127\.0\.0\.1([:/])/u, "@host.docker.internal$1");
}

/** 后端容器需要的环境(含 DSN 改写);空值不注入,避免覆盖容器内 .env 的默认值。 */
function backendEnv(extra = {}) {
  const base = {
    DATABASE_URL: toDockerUrl(process.env.DATABASE_URL?.trim() ?? ""),
    MEMORY_DB: process.env.MEMORY_DB ?? "",
    PLATFORM_SECRET_KEY: process.env.PLATFORM_SECRET_KEY ?? "",
    PUBLIC_API_ORIGIN: process.env.PUBLIC_API_ORIGIN ?? "",
    PUBLIC_MEDIA_CDN_ORIGIN: process.env.PUBLIC_MEDIA_CDN_ORIGIN ?? "",
    PUBLIC_APP_ORIGIN: process.env.PUBLIC_APP_ORIGIN ?? "",
    GATEWAY_BASE_URL: process.env.GATEWAY_BASE_URL ?? "",
    GATEWAY_API_TYPE: process.env.GATEWAY_API_TYPE ?? "",
    RELEASE_BASE_URL: process.env.RELEASE_BASE_URL ?? "",
    COOKIE_DOMAIN: process.env.COOKIE_DOMAIN ?? "",
    CONSOLE_DIST_DIR: process.env.CONSOLE_DIST_DIR ?? "",
    PUBLIC_CONSOLE_ORIGIN: process.env.PUBLIC_CONSOLE_ORIGIN ?? "",
    CONSOLE_ALLOWED_HOSTS: process.env.CONSOLE_ALLOWED_HOSTS ?? "",
    SETUP_ALLOWED_HOSTS: process.env.SETUP_ALLOWED_HOSTS ?? "",
    TURNSTILE_SITE_KEY: process.env.TURNSTILE_SITE_KEY ?? "",
    TURNSTILE_SECRET_KEY: process.env.TURNSTILE_SECRET_KEY ?? "",
    TURNSTILE_VERIFY_URL: process.env.TURNSTILE_VERIFY_URL ?? "",
    REDIS_URL: toDockerUrl(process.env.REDIS_URL?.trim() ?? ""),
    UPSTREAM_BASE_URL: process.env.UPSTREAM_BASE_URL ?? "",
    UPSTREAM_API_KEY: process.env.UPSTREAM_API_KEY ?? "",
    UPSTREAM_TIMEOUT_MS: process.env.UPSTREAM_TIMEOUT_MS ?? "",
    UPSTREAM_IDLE_TIMEOUT_MS: process.env.UPSTREAM_IDLE_TIMEOUT_MS ?? "",
    GATEWAY_TRUSTED_PROXY_IPS: process.env.GATEWAY_TRUSTED_PROXY_IPS ?? "",
    PROVIDER_AUTO_DISABLE_THRESHOLD: process.env.PROVIDER_AUTO_DISABLE_THRESHOLD ?? "",
    ...extra,
  };
  return Object.fromEntries(Object.entries(base).filter(([, value]) => value !== ""));
}

// ---------- 进程与端口 ----------

function tcpPing(host, port, timeoutMs = 800) {
  return new Promise((done) => {
    const socket = createConnection({ host, port });
    const finish = (ok) => {
      socket.destroy();
      done(ok);
    };
    socket.setTimeout(timeoutMs);
    socket.once("connect", () => finish(true));
    socket.once("timeout", () => finish(false));
    socket.once("error", () => finish(false));
  });
}

async function waitForPort(host, port, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await tcpPing(host, port)) {
      return true;
    }
    await sleep(400);
  }
  return false;
}

/** 顺序执行命令，输出直接继承到终端。error 为 spawn 本身的失败（如命令不存在）。 */
function run(command, args, options = {}) {
  return new Promise((done) => {
    const child = spawn(command, args, { cwd: ROOT, stdio: "inherit", ...options });
    child.once("error", (error) => done({ ok: false, code: null, error }));
    child.once("close", (code) => done({ ok: code === 0, code }));
  });
}

/** 捕获 stdout/stderr（迁移需要判断错误类型来决定是否重试）。 */
function runCapture(command, args, options = {}) {
  return new Promise((done) => {
    const child = spawn(command, args, { cwd: ROOT, env: process.env, ...options });
    let stdout = "";
    let stderr = "";
    child.stdout?.setEncoding("utf8");
    child.stderr?.setEncoding("utf8");
    child.stdout?.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr?.on("data", (chunk) => {
      stderr += chunk;
    });
    child.once("error", (error) => done({ ok: false, code: null, stdout, stderr, error }));
    child.once("close", (code) => done({ ok: code === 0, code, stdout, stderr }));
  });
}

// ---------- 启动步骤 ----------

async function ensurePostgres(databaseUrl) {
  let url;
  try {
    url = new URL(databaseUrl);
  } catch {
    fail(`DATABASE_URL 不是合法 URL: ${databaseUrl}`);
  }
  const host = url.hostname;
  const port = Number(url.port || 5432);
  const isLocal = host === "localhost" || host === "127.0.0.1" || host === "::1";

  if (!isLocal) {
    step(`使用远端 Postgres ${host}:${port}（跳过 docker compose）`);
    return;
  }
  if (await tcpPing(host, port)) {
    step(`Postgres 已在 ${host}:${port} 监听（跳过 docker compose）`);
    return;
  }
  step("启动 Postgres（docker compose up -d postgres）");
  const result = await run("docker", ["compose", "up", "-d", "postgres"]);
  if (result.error) {
    fail("未找到 docker 命令：请安装并启动 Docker Desktop，或使用 pnpm dev:memory（进程内存储，无需数据库）");
  }
  if (!result.ok) {
    fail("docker compose up postgres 失败：请确认 Docker Desktop 已启动；若 5432 被其他进程占用，先停止该进程或改用 pnpm dev:memory");
  }
  if (!(await waitForPort(host, port, 60_000))) {
    fail(`等待 Postgres ${host}:${port} 就绪超时`);
  }
  info(`Postgres 就绪：${host}:${port}`);
}

async function migrate() {
  step("应用数据库迁移（make -C server migrate）");
  const env = { ...process.env, ...backendEnv() };
  for (let attempt = 1; attempt <= 10; attempt += 1) {
    const result = await runCapture("make", ["-C", "server", "migrate"], { env });
    process.stdout.write(result.stdout);
    if (result.ok) {
      return;
    }
    process.stderr.write(result.stderr);
    const transient = /ECONNREFUSED|Connection refused|starting up|ECONNRESET|ETIMEDOUT/iu.test(
      result.stderr,
    );
    if (!transient || attempt === 10) {
      fail("数据库迁移失败");
    }
    // TCP 已监听但尚未接受连接（容器初始化中）时重试。
    warn(`数据库尚未就绪，2 秒后重试（${attempt}/10）`);
    await sleep(2000);
  }
}

async function seed() {
  step("载入种子数据（内置配置 + 默认模型目录）");
  const result = await runCapture("make", ["-C", "server", "seed"], {
    env: { ...process.env, ...backendEnv() },
  });
  process.stdout.write(result.stdout);
  if (!result.ok) {
    process.stderr.write(result.stderr);
    fail("种子数据失败");
  }
  return true;
}

// ---------- 子进程管理 ----------

const children = [];
let shuttingDown = false;

function killGroup(child, signal) {
  try {
    process.kill(-child.pid, signal);
  } catch {
    // 子进程已退出。
  }
}

/** 停止全部子进程后退出；tsx watch / vite 的子进程随进程组一起收到信号。 */
function shutdown(reason, code = 0) {
  if (shuttingDown) {
    return;
  }
  shuttingDown = true;
  if (reason) {
    console.log(`\n${paint("step", "▸")} ${reason}`);
  }
  if (children.length === 0) {
    process.exit(code);
  }
  // 兜底：4 秒内没退干净则强杀。
  const force = setTimeout(() => {
    for (const child of children) {
      killGroup(child, "SIGKILL");
    }
    process.exit(code);
  }, 4000);
  force.unref();
  let remaining = children.length;
  for (const child of children) {
    child.once("close", () => {
      remaining -= 1;
      if (remaining === 0) {
        clearTimeout(force);
        console.log(`${paint("step", "▸")} 已停止全部进程`);
        process.exit(code);
      }
    });
    killGroup(child, "SIGINT");
  }
}

function pipeWithPrefix(stream, name, onLine) {
  let buffer = "";
  stream.setEncoding("utf8");
  stream.on("data", (chunk) => {
    buffer += chunk;
    const lines = buffer.split(/\r?\n/u);
    buffer = lines.pop() ?? "";
    for (const line of lines) {
      console.log(`${paint(name, `${name.padEnd(7)}|`)} ${line}`);
      onLine?.(line.replace(ANSI, ""));
    }
  });
}

function startService(service) {
  const child = spawn(service.command[0], service.command.slice(1), {
    cwd: ROOT,
    env: { ...process.env, ...service.env },
    // 独立进程组：Ctrl-C 时整组（make → docker run → go run / vite）都能收到信号。
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const onLine = (line) => {
    if (shuttingDown) {
      return;
    }
    // 容器/开发服务器端口冲突要靠日志识别，否则要等满就绪超时。
    if (/EADDRINUSE|port is already allocated/iu.test(line)) {
      console.error(
        `\n${paint("error", "error:")} ${service.name} 端口被占用（${service.port}）：先停止占用进程，或在 .env 里换端口（PORT / GATEWAY_PORT）`,
      );
      shutdown("停止其余进程…", 1);
      return;
    }
    if (service.ready) {
      return;
    }
    const localUrl = service.match(line);
    if (localUrl) {
      service.ready = true;
      service.url = localUrl;
    }
  };
  child.once("error", (error) => {
    console.error(`\n${paint("error", "error:")} ${service.name} 启动失败: ${error.message}`);
    shutdown("停止其余进程…", 1);
  });
  child.once("close", (code, signal) => {
    // 先把自己移出清单，shutdown 只需要等还活着的进程。
    const index = children.indexOf(child);
    if (index >= 0) {
      children.splice(index, 1);
    }
    if (shuttingDown) {
      return;
    }
    console.error(
      `\n${paint("error", "error:")} ${service.name} 已退出（${signal ?? `code ${code}`}）`,
    );
    shutdown("停止其余进程…", code === 0 ? 1 : (code ?? 1));
  });
  pipeWithPrefix(child.stdout, service.name, onLine);
  pipeWithPrefix(child.stderr, service.name, onLine);
  children.push(child);
  return child;
}

function consoleConfigPort() {
  try {
    const config = readFileSync(join(ROOT, "apps/console/vite.config.ts"), "utf8");
    const match = config.match(/port:\s*(\d+)/u);
    return match ? Number(match[1]) : 5177;
  } catch {
    return 5177;
  }
}

/** 从日志行提取“可用地址”；返回 null 表示该行不是就绪日志。 */
function matchLocalUrl(name, fallbackPort) {
  return (line) => {
    if (name === "console") {
      const match = line.match(/Local:\s+(https?:\/\/\S+)/u);
      return match ? match[1].replace(/\/+$/u, "") : null;
    }
    const started = name === "api" ? "平台 API 已启动" : "模型网关已启动";
    if (!line.includes(started)) {
      return null;
    }
    const match = line.match(/http:\/\/[0-9a-z.]+:(\d+)/iu);
    return `http://localhost:${match ? match[1] : fallbackPort}`;
  };
}

async function startAll({ memoryDb, seeded }) {
  const apiPort = Number.parseInt(process.env.PORT?.trim() || "8787", 10);
  const gatewayPort = Number.parseInt(process.env.GATEWAY_PORT?.trim() || "8788", 10);
  const consolePort = consoleConfigPort();
  if (apiPort === gatewayPort) {
    warn(`PORT 与 GATEWAY_PORT 相同（${apiPort}）：api 与 gateway 会抢同一个端口，请在 .env 中改开`);
  }
  const services = [
    {
      name: "api",
      command: ["make", "-C", "server", "run-api"],
      env: backendEnv({ PORT: String(apiPort) }),
      port: apiPort,
      match: matchLocalUrl("api", apiPort),
      ready: false,
      url: `http://localhost:${apiPort}`,
    },
    {
      name: "gateway",
      command: ["make", "-C", "server", "run-gateway"],
      env: backendEnv({ PORT: String(apiPort), GATEWAY_PORT: String(gatewayPort) }),
      port: gatewayPort,
      match: matchLocalUrl("gateway", gatewayPort),
      ready: false,
      url: `http://localhost:${gatewayPort}`,
    },
    {
      name: "console",
      command: ["pnpm", "dev:console"],
      port: consolePort,
      match: matchLocalUrl("console", consolePort),
      ready: false,
      url: `http://localhost:${consolePort}/console`,
    },
  ];

  step("启动 api / gateway / console");
  for (const service of services) {
    if (await tcpPing("127.0.0.1", service.port)) {
      if (service.name === "console") {
        warn(`${service.port} 已被占用：vite 会自动换端口，实际地址见下方日志`);
      } else {
        warn(
          `${service.port} 已被占用：${service.name} 可能启动失败（先停止占用进程，或改用其它端口）`,
        );
      }
    }
    startService(service);
  }

  // 首次运行需在容器内编译 Go 代码(模块/构建缓存卷命中后约数秒),给足 2 分钟。
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline && services.some((service) => !service.ready)) {
    await sleep(300);
  }
  for (const service of services) {
    if (service.ready || (await tcpPing("127.0.0.1", service.port))) {
      continue;
    }
    fail(`${service.name} 未在 120 秒内就绪（端口 ${service.port} 未监听，日志见上方输出）`);
  }

  const byName = (name) => services.find((service) => service.name === name);
  const lines = [
    "",
    "────────────────────────────────────────────────",
    ` 控制台      ${byName("console").url}/`,
    ` 控制面 API  ${byName("api").url}`,
    ` 平台登录页  ${byName("api").url}/app/platform/login（由客户端发起，需 flow_id）`,
    ` 模型网关    ${byName("gateway").url}`,
    "────────────────────────────────────────────────",
  ];
  if (memoryDb) {
    lines.push(` 演示 root   ${DEMO_ROOT_EMAIL} / ${DEMO_ROOT_PASSWORD}（内存模式内置）`);
  } else if (seeded) {
    lines.push(" 超级管理员  首次打开控制台按引导创建；已有 root 则用原密码登录");
  }
  lines.push(" 按 Ctrl-C 停止全部");
  console.log(paint("step", lines.join("\n")));
}

// ---------- 入口 ----------

process.on("SIGINT", () => shutdown("收到停止信号…"));
process.on("SIGTERM", () => shutdown("收到停止信号…"));
process.on("SIGHUP", () => shutdown("终端已关闭，停止全部进程…"));

async function main() {
  ensureEnvFile();
  loadRootEnv();
  if (process.argv.includes("--memory")) {
    process.env.MEMORY_DB = "1";
  }

  const memoryDb = isTruthy(process.env.MEMORY_DB);
  const databaseUrl = process.env.DATABASE_URL?.trim() || "";
  if (!memoryDb && !databaseUrl) {
    console.error(
      `\n${paint("error", "error:")} 缺少 DATABASE_URL：在 .env 中配置（参考 .env.example），或使用 pnpm dev:memory（进程内存储）`,
    );
    process.exit(1);
  }

  if (!existsSync(join(ROOT, "node_modules", ".pnpm"))) {
    step("安装依赖（pnpm install）");
    const result = await run("pnpm", ["install"]);
    if (!result.ok) {
      console.error(`\n${paint("error", "error:")} pnpm install 失败`);
      process.exit(1);
    }
  }

  let seeded = null;
  if (memoryDb) {
    step("内存模式（MEMORY_DB=1）：跳过 Postgres / 迁移 / 种子");
  } else {
    await ensurePostgres(databaseUrl);
    await migrate();
    seeded = await seed();
  }

  await startAll({ memoryDb, seeded });
}

main().catch((error) => {
  if (error instanceof Stop) {
    // fail() 已输出错误并开始退出。
    return;
  }
  console.error(`\n${paint("error", "error:")} 启动失败`, error);
  shutdown("停止已启动的进程…", 1);
});

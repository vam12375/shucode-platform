import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import JavaScriptObfuscator from "javascript-obfuscator";

export function consoleObfuscation() {
  return {
    name: "console-obfuscation",
    apply: "build",
    renderChunk(code) {
      // 仅增加静态分析成本，不使用 eval、反调试或自毁代码，兼容 CSP 与故障排查。
      return { code: JavaScriptObfuscator.obfuscate(code, {
        compact: true,
        seed: 7319,
        identifierNamesGenerator: "hexadecimal",
        renameGlobals: false,
        // 构建器稍后才替换资源哈希占位符，不能把模块 URL 编进字符串数组。
        reservedStrings: [".*\\.js.*", ".*\\.css.*"],
        stringArray: true,
        stringArrayEncoding: ["base64"],
        stringArrayThreshold: 0.75,
        stringArrayCallsTransform: false,
        controlFlowFlattening: false,
        deadCodeInjection: false,
        debugProtection: false,
        selfDefending: false,
        sourceMap: false,
        target: "browser-no-eval",
      }).getObfuscatedCode(), map: null };
    },
  };
}

// 白名单来自实际构建依赖图，不能把“随机文件名”或前端菜单判断当作授权。
export function consoleAssetAccess() {
  return {
    name: "console-asset-access",
    apply: "build",
    async writeBundle(options, bundle) {
      const dir = resolve(options.dir);
      const manifest = JSON.parse(await readFile(resolve(dir, ".vite/manifest.json"), "utf8"));
      const files = {};
      const visit = (key, access) => {
        const item = manifest[key];
        if (!item) throw new Error(`Missing console entry: ${key}`);
        if (files[item.file]) return;
        files[item.file] = access;
        for (const file of [...(item.css ?? []), ...(item.assets ?? [])]) files[file] ??= access;
        for (const imported of item.imports ?? []) visit(imported, access);
      };
      visit("index.html", "public");
      visit("src/App.tsx", "session");
      visit("src/RootConsole.tsx", "platform");
      visit("src/pages/PaymentConfig.tsx", "platform");
      // 初始化模块仅在部署凭据验证后交付，与已登录的管理会话分开授权。
      visit("src/pages/RootSetup.tsx", "setup");
      for (const [key, item] of Object.entries(manifest)) {
        if (!files[item.file]) throw new Error(`Unclassified console asset: ${key}`);
      }
      for (const chunk of Object.values(bundle)) {
        if (chunk.type !== "chunk") continue;
        const access = files[chunk.fileName];
        const modules = Object.keys(chunk.modules);
        if (access === "public" && modules.some((id) => /\/src\/(App|RootConsole)\.tsx$/.test(id) || /\/src\/pages\/(?!Login\.|Invite\.)/.test(id))) {
          throw new Error("Private console module included in public assets");
        }
        if (access === "session" && modules.some((id) => id.endsWith("/src/RootConsole.tsx") || /\/src\/pages\/(Providers|Pricing|Models|ModelDisplay|MediaModels|Overview|Ops|Org|ConfigPage|PaymentConfig|RootSetup|Releases|CreditPackages|TeamDetail|TemplateLibrary|UiLibrary)\.tsx$/.test(id))) {
          throw new Error("Management module included in session assets");
        }
      }
      files["favicon.png"] = "public";
      files["theme.js"] = "public";
      await writeFile(resolve(dir, "asset-access.json"), JSON.stringify({ version: 1, files }));
    },
  };
}

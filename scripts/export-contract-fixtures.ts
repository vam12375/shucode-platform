#!/usr/bin/env node
// 从 TS 参考实现(packages/contracts 与 scripts/reference)导出契约 fixtures,
// 供 Go 侧 internal/contracts、internal/pricing、internal/credits 的对齐测试使用。
// 运行:node scripts/export-contract-fixtures.ts(仓库根目录,Node 24+)。
import { mkdirSync, writeFileSync } from "node:fs";
import { platformDeviceTokenSchema, buildPlatformDeepLinkCallbackUrl, platformAuthInitRequestSchema } from "../packages/contracts/src/platform-auth.ts";
import { buildClientConfigsEnvelope } from "../packages/contracts/src/client-configs.ts";
import { parseBuiltinReleaseLoose, buildBuiltinReleaseDownloadUrl } from "../packages/contracts/src/release.ts";
import { computePricingDocVersion } from "./reference/pricing-doc.ts";
import {
  computeUsageMicroCredits,
  estimateMaxMicroCredits,
  formatMicroCredits,
  parseCreditsToMicro,
} from "../packages/contracts/src/credits.ts";

// ---- 定价版本哈希(内容寻址,排序口径必须一致) ----
const pricingVersions = [
  { entries: [], version: computePricingDocVersion([]) },
  {
    entries: [
      { modelId: "claude-sonnet-4-5", displayName: "Claude Sonnet 4.5", inputCostPerMillion: "3", outputCostPerMillion: "15", cacheReadCostPerMillion: "0.3", cacheCreationCostPerMillion: "3.75" },
      { modelId: "claude-haiku-4-5", displayName: "Claude Haiku 4.5", inputCostPerMillion: "1", outputCostPerMillion: "5", cacheReadCostPerMillion: "0.1", cacheCreationCostPerMillion: "1.25" },
    ],
    version: null,
  },
];
pricingVersions[1].version = computePricingDocVersion(pricingVersions[1].entries);
// 插入乱序与大小写混合、数字后缀、中文,验证排序口径后重算。
const shuffled = [
  { modelId: "GLM-4.7", displayName: "中文模型 ✓", inputCostPerMillion: "0.5", outputCostPerMillion: "2", cacheReadCostPerMillion: "0.05", cacheCreationCostPerMillion: "0.5" },
  { modelId: "claude-haiku-4-5", displayName: "Haiku", inputCostPerMillion: "1", outputCostPerMillion: "5", cacheReadCostPerMillion: "0.1", cacheCreationCostPerMillion: "1.25" },
  { modelId: "gpt-5.2-turbo", displayName: "GPT", inputCostPerMillion: "10", outputCostPerMillion: "40", cacheReadCostPerMillion: "2.5", cacheCreationCostPerMillion: "12.5" },
  { modelId: "claude-sonnet-4-5", displayName: "Sonnet", inputCostPerMillion: "3", outputCostPerMillion: "15", cacheReadCostPerMillion: "0.3", cacheCreationCostPerMillion: "3.75" },
];
pricingVersions.push({ entries: shuffled, version: computePricingDocVersion(shuffled) });

// ---- Release 宽松校验(合法/非法与错误文案) ----
const validRelease = {
  schemaVersion: 1,
  revision: 3,
  config: { providerConfigRules: { templateRules: [], providerRules: [{ id: "x" }] }, modelConfigRules: null },
  extraTop: "passthrough ✓",
};
const tryLoose = (input) => {
  try {
    const r = parseBuiltinReleaseLoose(input);
    return { input, ok: true, revision: r.revision, error: null };
  } catch (err) {
    return { input, ok: false, revision: null, error: err.message };
  }
};
const builtinReleases = [
  tryLoose(validRelease),
  tryLoose(null),
  tryLoose({ revision: 3, config: { providerConfigRules: { templateRules: [], providerRules: [] } } }),
  tryLoose({ schemaVersion: 2, revision: 3, config: { providerConfigRules: { templateRules: [], providerRules: [] } } }),
  tryLoose({ schemaVersion: 1, revision: -1, config: { providerConfigRules: { templateRules: [], providerRules: [] } } }),
  tryLoose({ schemaVersion: 1, revision: 1.5, config: { providerConfigRules: { templateRules: [], providerRules: [] } } }),
  tryLoose({ schemaVersion: 1, revision: 3, config: {} }),
  tryLoose({ schemaVersion: 1, revision: 3, config: { providerConfigRules: { templateRules: "no" } } }),
];

// ---- 设备 token / deep link / 下载 URL ----
const deviceTokens = [
  "mdp_AbCdEfGh1234567890_-", "mdp_short", "mdpx", "MDP_UPPER-CASE_1234", "mdp_" + "a".repeat(64),
].map((token) => ({ token, valid: platformDeviceTokenSchema.safeParse(token).success }));
const deepLinks = ["abc-flow-uuid-0001", "流量参数 ✓ &=?"].map((flowId) => ({ flowId, url: buildPlatformDeepLinkCallbackUrl(flowId) }));
const downloadUrls = [
  { base: "https://api.example.com", revision: 3, url: buildBuiltinReleaseDownloadUrl("https://api.example.com", 3) },
  { base: "https://api.example.com/", revision: 0, url: buildBuiltinReleaseDownloadUrl("https://api.example.com/", 0) },
  { base: "https://api.example.com///", revision: 12, url: buildBuiltinReleaseDownloadUrl("https://api.example.com///", 12) },
];

// ---- 公开配置信封 ----
const configsEnvelopes = [
  { input: {}, output: buildClientConfigsEnvelope({}) },
  { input: { help: { feedback_url: "https://example.com" }, builtinReleaseUrl: "https://api.example.com/", forceUpdateMinimalVersion: "1.2.3", pluginStoreOrder: ["a", null] }, output: null },
];
configsEnvelopes[1].output = buildClientConfigsEnvelope(configsEnvelopes[1].input);
// ---- 登录流 init 请求(strict/trim/长度,zod 实际裁决) ----
const initRequests = [
  {},
  { device_name: "  笔记本  ", platform: "darwin" },
  { unknown: 1 },
  { device_name: "x".repeat(121) },
  { device_name: null },
].map((body) => ({ body, ok: platformAuthInitRequestSchema.safeParse(body).success }));
// ---- credits ----
const credits = {
  format: [
    { micro: 0, s: formatMicroCredits(0) },
    { micro: 1234567, s: formatMicroCredits(1234567) },
    { micro: -1500, s: formatMicroCredits(-1500) },
  ],
  parse: ["0.000000", "1000.000000", "0.1", "3.75", "123.456789"].map((v) => ({ v, micro: parseCreditsToMicro(v) })),
  usage: [
    {
      usage: { inputTokens: 1000, outputTokens: 500, cacheReadTokens: 200, cacheCreationTokens: 80 },
      rates: { inputPerMtok: 3, outputPerMtok: 15, cacheReadPerMtok: 0.3, cacheWritePerMtok: 3.75 },
      micro: computeUsageMicroCredits({ inputTokens: 1000, outputTokens: 500, cacheReadTokens: 200, cacheCreationTokens: 80 }, { inputPerMtok: 3, outputPerMtok: 15, cacheReadPerMtok: 0.3, cacheWritePerMtok: 3.75 }),
    },
    {
      usage: { inputTokens: 1, outputTokens: 1, cacheReadTokens: 0, cacheCreationTokens: 0 },
      rates: { inputPerMtok: 0.1, outputPerMtok: 0.1, cacheReadPerMtok: 0, cacheWritePerMtok: 0 },
      micro: computeUsageMicroCredits({ inputTokens: 1, outputTokens: 1, cacheReadTokens: 0, cacheCreationTokens: 0 }, { inputPerMtok: 0.1, outputPerMtok: 0.1, cacheReadPerMtok: 0, cacheWritePerMtok: 0 }),
    },
  ],
  estimate: [
    { maxTokens: 8192, rate: 15, micro: estimateMaxMicroCredits(8192, 15) },
    { maxTokens: -5, rate: 15, micro: estimateMaxMicroCredits(-5, 15) },
  ],
};

mkdirSync("server/internal/contracts/testdata", { recursive: true });
writeFileSync(
  "server/internal/contracts/testdata/fixtures.json",
  JSON.stringify({ pricingVersions, builtinReleases, deviceTokens, deepLinks, downloadUrls, configsEnvelopes, initRequests, credits }, null, 2) + "\n",
);
console.log("契约 fixtures 导出完成");

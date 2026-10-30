import {
  MICRO_CREDITS_PER_CREDIT,
  parseCreditsToMicro,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState } from "react";
import { Clapperboard, FileJson, Plus } from "lucide-react";
import { api } from "../api.js";
import { toastErr, toastOk } from "../components/Feedback.js";
import { Glyph, PageTitle } from "../components/Icon.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { MediaReconciliation } from "./MediaReconciliation.js";
import { ReferencePricing } from "./ReferencePricing.js";
import { SourceParserStatus } from "./SourceParserStatus.js";

const KIND_OPTIONS = [
  { value: "video", label: "视频" },
  { value: "image", label: "图片" },
];
const ADAPTER_OPTIONS = [
  { value: "ark", label: "Ark" },
  { value: "toapis", label: "ToAPIs" },
];
const BILLING_UNIT_OPTIONS = [
  { value: "second", label: "按秒" },
  { value: "request", label: "按次" },
  { value: "token", label: "按 Token" },
];
const RESOLUTIONS = ["480p", "720p", "1080p", "4k"];
const DEFAULT_MIN_DURATION = 4;
const DEFAULT_MAX_DURATION = 15;

// 一卡 = 一个上游模型；分辨率档位是卡内价格行，保存时仍按每档一条模型下发，
// 因此服务端目录与结算口径保持不变。视频参考的 token 费率随上游模型共享，
// 旧按秒口径跟随档位主价格，仅作兼容字段。
const SEEDANCE_PRESETS = [
  {
    key: "seedance-mini",
    name: "Seedance 2.0 mini",
    upstreamModel: "doubao-seedance-2-0-mini-260615",
    tiers: [
      { id: "seedance-2.0-mini-480p", resolution: "480p" },
      { id: "seedance-2.0-mini-720p", resolution: "720p" },
    ],
  },
  {
    key: "seedance-fast",
    name: "Seedance 2.0 fast",
    upstreamModel: "doubao-seedance-2-0-fast-260128",
    tiers: [
      { id: "seedance-2.0-fast-480p", resolution: "480p" },
      { id: "seedance-2.0-fast-720p", resolution: "720p" },
    ],
  },
  {
    key: "seedance-base",
    name: "Seedance 2.0",
    upstreamModel: "doubao-seedance-2-0-260128",
    tiers: [
      { id: "seedance-2.0-480p", resolution: "480p" },
      { id: "seedance-2.0-720p", resolution: "720p" },
      { id: "seedance-2.0-1080p", resolution: "1080p" },
      { id: "seedance-2.0-4k", resolution: "4k" },
    ],
  },
] as const;

const TOAPIS_SEEDANCE_PRESETS = SEEDANCE_PRESETS.map((preset) => ({
  ...preset,
  name: `ToAPIs ${preset.name}`,
  upstreamModel: preset.key === "seedance-base" ? "seedance-2" : `seedance-2-${preset.key.slice("seedance-".length)}`,
  tiers: preset.tiers.map((tier) => ({ ...tier, id: `toapis-${tier.id}` })),
}));

type MediaBillingUnit = "second" | "request" | "token";

const TOAPIS_SEEDANCE_25_PRESETS = [
  {
    upstreamModel: "seedance-2-5",
    name: "ToAPIs Seedance 2.5",
    billingUnit: "token" as const,
    tiers: [
      { resolution: "480p", tokenCostCredits: "70", tokenPriceCredits: "70", videoReferenceTokenCostCredits: "42", videoReferenceTokenPriceCredits: "42" },
      { resolution: "720p", tokenCostCredits: "70", tokenPriceCredits: "70", videoReferenceTokenCostCredits: "42", videoReferenceTokenPriceCredits: "42" },
      { resolution: "1080p", tokenCostCredits: "77", tokenPriceCredits: "77", videoReferenceTokenCostCredits: "46", videoReferenceTokenPriceCredits: "46" },
    ].map((tier) => ({ ...tier, id: `toapis-seedance-2-5-${tier.resolution}` })),
  },
  {
    upstreamModel: "seedance-2.5-ext",
    name: "ToAPIs Seedance 2.5 Ext",
    billingUnit: "request" as const,
    tiers: [{ id: "toapis-seedance-2.5-ext-720p", resolution: "720p", costCredits: "8.75", priceCredits: "8.75", videoReferenceTokenCostCredits: "0", videoReferenceTokenPriceCredits: "0" }],
  },
] as const;

const TOAPIS_KLING_PRESETS = [
  {
    upstreamModel: "kling-4.0",
    name: "ToAPIs Kling 4.0",
    tiers: [
      {
        id: "toapis-kling-4.0-720p",
        resolution: "720p",
        costCredits: "1.1",
        priceCredits: "1.1",
        videoReferenceCostCredits: "1.32",
        videoReferencePriceCredits: "1.32",
      },
      {
        id: "toapis-kling-4.0-1080p",
        resolution: "1080p",
        costCredits: "1.65",
        priceCredits: "1.65",
        videoReferenceCostCredits: "1.97",
        videoReferencePriceCredits: "1.97",
      },
    ],
  },
] as const;

const TOAPIS_MINIMAX_CAPABILITIES: Record<string, { resolutions: readonly string[]; minDuration: number; videoReference: boolean }> = {
  "MiniMax-H3": { resolutions: ["768p", "2K"], minDuration: 4, videoReference: true },
  "MiniMax-H3-Max": { resolutions: ["480p", "768p"], minDuration: 4, videoReference: true },
  "MiniMax-H3-Max-Turbo": { resolutions: ["480p", "768p"], minDuration: 4, videoReference: false },
};
const TOAPIS_KLING_CAPABILITY = { minDuration: 3, maxDuration: 30, maxImages: 10 } as const;
const TOAPIS_MINIMAX_PRESETS = Object.entries(TOAPIS_MINIMAX_CAPABILITIES).map(([upstreamModel, capability]) => ({
  name: `ToAPIs ${upstreamModel.replace("MiniMax-", "MiniMax ")}`,
  upstreamModel,
  tiers: capability.resolutions.map((resolution) => ({ id: `toapis-${upstreamModel.toLowerCase()}-${resolution}`, resolution })),
}));

const TOAPIS_IMAGE_RESOLUTIONS = ["1K", "2K", "4K"];
const TOAPIS_GPT_IMAGE_PRESETS = [
  { upstreamModel: "gpt-image-2", name: "ToAPIs GPT Image 2" },
  { upstreamModel: "gpt-image-2.5-flare", name: "ToAPIs GPT Image 2.5 Flare" },
  { upstreamModel: "gpt-image-2.5-sunburst", name: "ToAPIs GPT Image 2.5 Sunburst" },
];

function minimaxCapability(upstreamModel: string) {
  return Object.hasOwn(TOAPIS_MINIMAX_CAPABILITIES, upstreamModel)
    ? TOAPIS_MINIMAX_CAPABILITIES[upstreamModel]
    : undefined;
}

const SEEDREAM_PRESETS = [
  { id: "seedream-5.0-pro", name: "Seedream 5.0 Pro", upstreamModel: "doubao-seedream-5-0-pro-260628" },
  { id: "seedream-5.0-flash", name: "Seedream 5.0 Flash", upstreamModel: "doubao-seedream-5-0-flash-260915" },
  { id: "seedream-5.0-lite", name: "Seedream 5.0 Lite", upstreamModel: "doubao-seedream-5-0-lite-260128" },
  { id: "seedream-4.5", name: "Seedream 4.5", upstreamModel: "doubao-seedream-4-5-251128" },
  { id: "seedream-4.0", name: "Seedream 4.0", upstreamModel: "doubao-seedream-4-0-250828" },
] as const;

interface MediaModelRoute {
  providerId: string;
  priority: number;
  weight: number;
  enabled: boolean;
}

export interface MediaRouteDraft {
  providerId: string;
  priority: string;
  weight: string;
  enabled: boolean;
}

interface MediaModel {
  id: string;
  name: string;
  displayName?: string;
  kind: "image" | "video";
  providerId: string;
  routes?: MediaModelRoute[];
  upstreamModel: string;
  adapter: string;
  resolutions: string[];
  aspectRatios?: string[];
  minDuration?: number;
  maxDuration?: number;
  // 旧目录的枚举时长；仅显式转换后才改为连续区间。
  durations?: number[];
  costMicro: number;
  billingUnit?: MediaBillingUnit;
  tokenCostMicroPerMillion?: number;
  tokenPriceMicroPerMillion?: number;
  generateAudio: boolean;
  priceMicro: number;
  priceVersion: string;
  videoReference?: {
    maxDurationSeconds: number;
    maxImages: number;
    maxSegments?: number;
    maxAudioSeconds?: number;
    costMicro: number;
    priceMicro: number;
    tokenCostMicroPerMillion: number;
    tokenPriceMicroPerMillion: number;
  };
}

interface ProviderOption {
  id: string;
  name: string;
  baseUrl: string;
  enabled: boolean;
}

export interface MediaModelDraft {
  // 本地分组标识，只用于「一卡多档」的渲染，不下发 API。
  familyId: string;
  id: string;
  name: string;
  displayName?: string;
  kind: "image" | "video";
  providerId: string;
  routes?: MediaRouteDraft[];
  upstreamModel: string;
  adapter?: string;
  resolutions: string[];
  // 平台按 adapter/upstream 派生的只读能力，不回写目录配置。
  supportedAspectRatios?: string[];
  minDuration: string;
  maxDuration: string;
  legacyDurations?: number[];
  costCredits: string;
  billingUnit?: MediaBillingUnit;
  tokenCostCredits?: string;
  tokenPriceCredits?: string;
  generateAudio: boolean;
  priceCredits: string;
  priceVersion: string;
  videoReference?: {
    enabled: boolean;
    maxDurationSeconds: string;
    maxImages: string;
    maxSegments?: string;
    // 0 = 不发送参考音频；非零为单段音频上限秒数。缺省与 0 等价。
    maxAudioSeconds?: string;
    tokenCostCredits: string;
    tokenPriceCredits: string;
    // 按秒/按次的输入视频独立价格；缺省时回退到档位主价格。
    costCredits?: string;
    priceCredits?: string;
  };
}

export interface MediaFamilyGroup {
  id: string;
  indexes: number[];
}

// Seedance 的 4–15 秒能力是连续整数区间。历史目录可能还带有 [4, 5, 10, 15]
// 之类的旧枚举；加载和保存时统一成连续区间，避免控制台继续传播过时说法。
function isSeedanceVideoModel(model: Pick<MediaModel, "kind" | "upstreamModel"> | Pick<MediaModelDraft, "kind" | "upstreamModel">): boolean {
  return model.kind === "video" && model.upstreamModel.trim().toLowerCase().includes("seedance");
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "操作失败";
}

/** micro-credit → 紧凑的 credit 输入值（最多 6 位小数，去掉尾零）。 */
function priceInputValue(micro: number): string {
  return (micro / MICRO_CREDITS_PER_CREDIT)
    .toFixed(6)
    .replace(/\.?0+$/, "");
}

/** 仅用于展示的宽松解析：非法或空输入按 0，避免边输入边抛错。 */
function previewMicro(value: string | undefined): number {
  const numeric = Number.parseFloat(value ?? "");
  return Number.isFinite(numeric) && numeric > 0
    ? Math.round(numeric * MICRO_CREDITS_PER_CREDIT)
    : 0;
}

function trimNumber(value: number): string {
  return String(Number(value.toFixed(6)));
}

let familySequence = 0;
function nextFamilyId(): string {
  familySequence += 1;
  return `family-${familySequence}`;
}

function familyKey(
  draft: Pick<MediaModelDraft, "kind" | "providerId" | "upstreamModel" | "adapter">,
): string {
  return `${draft.kind}\u0000${draft.adapter ?? "ark"}\u0000${draft.providerId}\u0000${draft.upstreamModel.trim()}`;
}

function isToapisGptImage(draft: Pick<MediaModelDraft, "adapter" | "kind" | "upstreamModel">): boolean {
  return draft.adapter === "toapis" && draft.kind === "image" &&
    TOAPIS_GPT_IMAGE_PRESETS.some((preset) => preset.upstreamModel === draft.upstreamModel.trim());
}

function hasResolutionTiers(draft: MediaModelDraft): boolean {
  return draft.kind === "video" || isToapisGptImage(draft);
}

function modelResolutions(draft: MediaModelDraft): readonly string[] {
  if (isToapisGptImage(draft)) return TOAPIS_IMAGE_RESOLUTIONS;
  if (draft.adapter === "toapis") {
    const minimax = minimaxCapability(draft.upstreamModel.trim());
    if (minimax) return minimax.resolutions;
    const seedance25 = TOAPIS_SEEDANCE_25_PRESETS.find((preset) => preset.upstreamModel === draft.upstreamModel.trim());
    if (seedance25) return seedance25.tiers.map((tier) => tier.resolution);
    if (draft.upstreamModel.trim() === "kling-4.0") return ["720p", "1080p"];
  }
  return draft.adapter === "toapis" && (draft.upstreamModel.trim() === "seedance-2-mini" || draft.upstreamModel.trim() === "seedance-2-fast")
    ? ["480p", "720p"]
    : RESOLUTIONS;
}

/** 给导入/加载的条目补充分组标识；同一上游模型归入同一张卡。 */
export function assignFamilyIds(drafts: MediaModelDraft[]): MediaModelDraft[] {
  const assigned = new Map<string, string>();
  return drafts.map((draft) => {
    if (draft.familyId) return draft;
    const key = familyKey(draft);
    const existing = assigned.get(key);
    if (existing) return { ...draft, familyId: existing };
    const id = nextFamilyId();
    assigned.set(key, id);
    return { ...draft, familyId: id };
  });
}

export function groupFamilies(drafts: MediaModelDraft[]): MediaFamilyGroup[] {
  const groups = new Map<string, number[]>();
  drafts.forEach((draft, index) => {
    const list = groups.get(draft.familyId);
    if (list) list.push(index);
    else groups.set(draft.familyId, [index]);
  });
  return [...groups.entries()].map(([id, indexes]) => ({ id, indexes }));
}

function tierLabel(draft: MediaModelDraft): string {
  return hasResolutionTiers(draft) ? (draft.resolutions[0] ?? "") : "";
}

/** 卡片展示名 = 去掉档位后缀的条目名（不含兜底，供输入框直接编辑）。 */
function stripTierSuffix(draft: MediaModelDraft): string {
  const label = tierLabel(draft);
  const name = draft.name.trim();
  if (label && name.toLowerCase().endsWith(` ${label.toLowerCase()}`)) {
    return name.slice(0, name.length - label.length - 1).trim();
  }
  return name;
}

export function familyNameOf(draft: MediaModelDraft): string {
  return stripTierSuffix(draft) || draft.upstreamModel.trim() || "未命名模型";
}

function tierName(familyName: string, draft: MediaModelDraft): string {
  const label = tierLabel(draft);
  return label ? `${familyName} ${label}`.trim() : familyName;
}

function durationBounds(model: MediaModel): { min: number; max: number } {
  if (model.minDuration && model.maxDuration && model.maxDuration >= model.minDuration) {
    return { min: model.minDuration, max: model.maxDuration };
  }
  if (model.durations?.length) {
    return {
      min: Math.min(...model.durations),
      max: Math.max(...model.durations),
    };
  }
  return { min: DEFAULT_MIN_DURATION, max: DEFAULT_MAX_DURATION };
}

export function toDraft(model: MediaModel): MediaModelDraft {
  const bounds = durationBounds(model);
  const seedanceContinuous = isSeedanceVideoModel(model);
  const hasRange = Boolean(
    model.minDuration && model.maxDuration && model.maxDuration >= model.minDuration,
  );
  return {
    familyId: "",
    id: model.id,
    name: model.name,
    // 对外名称由模型展示页管理；编辑价格必须原样保留，不能被本页字段白名单丢弃。
    ...(model.displayName !== undefined ? { displayName: model.displayName } : {}),
    kind: model.kind,
    providerId: model.providerId,
    ...(model.routes !== undefined ? { routes: model.routes.map((route) => ({ ...route, priority: String(route.priority), weight: String(route.weight) })) } : {}),
    upstreamModel: model.upstreamModel,
    adapter: model.adapter ?? "ark",
    resolutions: [...(model.resolutions ?? [])],
    ...(model.aspectRatios?.length ? { supportedAspectRatios: [...model.aspectRatios] } : {}),
    minDuration: String(seedanceContinuous ? DEFAULT_MIN_DURATION : bounds.min),
    maxDuration: String(seedanceContinuous ? DEFAULT_MAX_DURATION : bounds.max),
    // 非 Seedance 的旧稀疏枚举保留兼容；Seedance 已统一为连续 4–15 秒。
    ...(model.kind === "video" && !seedanceContinuous && !hasRange && model.durations?.length
      ? { legacyDurations: [...model.durations] }
      : {}),
    costCredits: priceInputValue(model.costMicro ?? 0),
    billingUnit: model.billingUnit ?? "second",
    tokenCostCredits: priceInputValue(model.tokenCostMicroPerMillion ?? 0),
    tokenPriceCredits: priceInputValue(model.tokenPriceMicroPerMillion ?? 0),
    generateAudio: model.generateAudio === true,
    priceCredits: priceInputValue(model.priceMicro),
    priceVersion: model.priceVersion,
    ...(model.videoReference
      ? {
          videoReference: {
            enabled: true,
            maxDurationSeconds: String(model.videoReference.maxDurationSeconds),
            maxImages: String(model.videoReference.maxImages),
            ...(model.videoReference.maxSegments ? { maxSegments: String(model.videoReference.maxSegments) } : {}),
            // 只在后台已声明时回填，避免给没有音频能力的型号凭空补字段。
            ...(model.videoReference.maxAudioSeconds
              ? { maxAudioSeconds: String(model.videoReference.maxAudioSeconds) }
              : {}),
            tokenCostCredits: priceInputValue(model.videoReference.tokenCostMicroPerMillion ?? 0),
            tokenPriceCredits: priceInputValue(model.videoReference.tokenPriceMicroPerMillion ?? 0),
            // Kling 4.0 的 ToAPIs SKU 按「是否输入视频」独立定价；历史 Ark/ToAPIs
            // 条目仍沿用主档位价格，避免旧目录被加载后凭空出现第二套价格来源。
            ...(model.adapter === "toapis" && model.upstreamModel.trim() === "kling-4.0"
              ? {
                  costCredits: priceInputValue(model.videoReference.costMicro ?? model.costMicro ?? 0),
                  priceCredits: priceInputValue(model.videoReference.priceMicro ?? model.priceMicro ?? 0),
                }
              : {}),
          },
        }
      : {}),
  };
}

export function toApiModel(draft: MediaModelDraft): MediaModel {
  const upstreamModel = draft.upstreamModel.trim();
  const costMicro = parseCreditsToMicro(draft.costCredits || "0");
  const priceMicro = draft.priceCredits.trim() ? parseCreditsToMicro(draft.priceCredits) : 0;
  const isVideo = draft.kind === "video";
  const seedanceContinuous = isSeedanceVideoModel({ ...draft, upstreamModel });
  return {
    // 平台模型 ID 默认等于上游模型；手动填写后才作为独立别名保存。
    id: draft.id.trim() || upstreamModel,
    name: draft.name.trim(),
    ...(draft.displayName !== undefined ? { displayName: draft.displayName } : {}),
    kind: draft.kind,
    providerId: draft.providerId,
    ...(draft.routes?.length ? { routes: draft.routes.map((route) => {
      const priority = mediaRouteInteger(route.priority, 0, 1_000_000);
      const weight = mediaRouteInteger(route.weight, 1, 10_000);
      if (priority === null || weight === null) throw new Error("路由优先级或权重不是有效整数，请先修正路由池。");
      return { ...route, priority, weight };
    }) } : {}),
    upstreamModel,
    adapter: draft.adapter ?? "ark",
    // GPT 图片按分辨率独立锁价，丢弃档位会把三个 SKU 都回退为默认清晰度。
    resolutions: hasResolutionTiers(draft) ? draft.resolutions : [],
    ...(isVideo
      ? seedanceContinuous
        ? { minDuration: DEFAULT_MIN_DURATION, maxDuration: DEFAULT_MAX_DURATION }
        : draft.legacyDurations !== undefined
        ? { durations: [...draft.legacyDurations] }
        : {
            minDuration: Number(draft.minDuration),
            maxDuration: Number(draft.maxDuration),
          }
      : {}),
    costMicro,
    ...(draft.billingUnit !== "second" ? { billingUnit: draft.billingUnit } : {}),
    ...(draft.billingUnit === "token" || parseCreditsToMicro(draft.tokenCostCredits || "0") > 0 || parseCreditsToMicro(draft.tokenPriceCredits || "0") > 0
      ? {
          tokenCostMicroPerMillion: parseCreditsToMicro(draft.tokenCostCredits || "0"),
          tokenPriceMicroPerMillion: parseCreditsToMicro(draft.tokenPriceCredits || "0"),
        }
      : {}),
    generateAudio: isVideo ? draft.generateAudio : false,
    priceMicro,
    priceVersion: draft.priceVersion.trim(),
    ...(isVideo && draft.videoReference?.enabled
      ? {
          videoReference: {
            maxDurationSeconds: Number(draft.videoReference.maxDurationSeconds),
            maxImages: Number(draft.videoReference.maxImages),
            ...(Number(draft.videoReference.maxSegments || "1") > 1 ? { maxSegments: Number(draft.videoReference.maxSegments) } : {}),
            ...(Number(draft.videoReference.maxAudioSeconds || "0") > 0
              ? { maxAudioSeconds: Number(draft.videoReference.maxAudioSeconds) }
              : {}),
            // 输入视频旧目录沿用档位主价格；Kling 分支在上方写入独立参考费率。
            costMicro: draft.adapter === "toapis" && upstreamModel === "kling-4.0"
              ? parseCreditsToMicro(draft.videoReference.costCredits ?? draft.costCredits ?? "0")
              : costMicro,
            priceMicro: draft.adapter === "toapis" && upstreamModel === "kling-4.0"
              ? parseCreditsToMicro(draft.videoReference.priceCredits ?? draft.priceCredits ?? "0")
              : priceMicro,
            tokenCostMicroPerMillion: parseCreditsToMicro(
              draft.videoReference.tokenCostCredits || "0",
            ),
            tokenPriceMicroPerMillion: parseCreditsToMicro(
              draft.videoReference.tokenPriceCredits || "0",
            ),
          },
        }
      : {}),
  };
}

// 共享字段只有卡片一个编辑入口；逐档副本仅用于兼容现有目录 API 和保留导入冲突证据。
function sharedConfiguration(draft: MediaModelDraft): Partial<MediaModelDraft> {
  const videoReference = draft.videoReference ? { ...draft.videoReference } : undefined;
  return {
    displayName: draft.displayName,
    kind: draft.kind,
    providerId: draft.providerId,
    upstreamModel: draft.upstreamModel,
    adapter: draft.adapter ?? "ark",
    ...(draft.billingUnit !== undefined ? { billingUnit: draft.billingUnit } : {}),
    ...(draft.billingUnit !== "token" && draft.tokenCostCredits !== undefined ? { tokenCostCredits: draft.tokenCostCredits } : {}),
    ...(draft.billingUnit !== "token" && draft.tokenPriceCredits !== undefined ? { tokenPriceCredits: draft.tokenPriceCredits } : {}),
    priceVersion: draft.priceVersion,
    generateAudio: draft.generateAudio,
    minDuration: draft.minDuration,
    maxDuration: draft.maxDuration,
    legacyDurations: draft.legacyDurations ? [...draft.legacyDurations] : undefined,
    videoReference,
  };
}

function sharedFields(draft: MediaModelDraft): Record<string, unknown> {
  const vr = draft.videoReference?.enabled ? draft.videoReference : null;
  const fields: Record<string, unknown> = {
    类型: draft.kind,
    上游供应商: draft.providerId,
    上游模型: draft.upstreamModel.trim(),
    适配器: draft.adapter ?? "ark",
    价格版本: draft.priceVersion.trim(),
    原生音频: draft.kind === "video" ? draft.generateAudio : false,
    时长能力: draft.kind === "video"
      ? [draft.minDuration.trim(), draft.maxDuration.trim(), draft.legacyDurations ?? null]
      : null,
    视频参考开关: vr !== null,
    视频参考时长上限: vr?.maxDurationSeconds.trim() ?? null,
    视频参考图上限: vr?.maxImages.trim() ?? null,
    参考音频时长上限: vr ? (vr.maxAudioSeconds ?? "0").trim() : null,
  };
  // Legacy/Ark reference token rates remain shared. Seedance 2.5 rates are SKU-specific.
  if (draft.billingUnit !== "token") {
    fields["视频参考 token 成本"] = vr?.tokenCostCredits.trim() ?? null;
    fields["视频参考 token 售价"] = vr?.tokenPriceCredits.trim() ?? null;
  }
  return fields;
}

function sharedConflicts(drafts: MediaModelDraft[]): string[] {
  const lead = drafts[0];
  if (!lead) return [];
  const fields = sharedFields(lead);
  const others = drafts.slice(1).map(sharedFields);
  return Object.keys(fields).filter((field) =>
    others.some((other) => JSON.stringify(other[field]) !== JSON.stringify(fields[field])),
  );
}

export interface DraftProblem {
  message: string;
  // 出错条目所在的卡片，供折叠清单展开定位。
  familyId: string;
}

export function findDraftProblem(
  drafts: MediaModelDraft[],
  providers: ProviderOption[],
): DraftProblem | null {
  const seen = new Set<string>();
  for (const [index, draft] of drafts.entries()) {
    const message = tierProblem(draft, index, seen, providers);
    if (message) return { message, familyId: draft.familyId };
  }
  // 旧配置或 JSON 导入会保留逐档差异；指出真实冲突，避免把合法时长误报为无法保存。
  for (const family of groupFamilies(drafts)) {
    const tiers = family.indexes.map((index) => drafts[index]);
    const conflicts = sharedConflicts(tiers);
    if (conflicts.length) {
      return {
        message: `「${familyNameOf(tiers[0])}」共享配置不一致：${conflicts.join("、")}。请在卡片中统一后再保存。`,
        familyId: family.id,
      };
    }
  }
  return null;
}

function mediaRouteInteger(value: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(value.trim())) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= min && parsed <= max ? parsed : null;
}

export function mediaRouteProblem(routes: MediaRouteDraft[] | undefined, providers: ProviderOption[]): string | null {
  if (!routes?.length) return null;
  if (routes.length > 32) return "每个档位最多配置 32 条路由。";
  const seen = new Set<string>();
  for (const route of routes) {
    if (!providers.some((provider) => provider.id === route.providerId)) return "路由池存在未选择或已删除的供应商。";
    if (seen.has(route.providerId)) return "路由池不能重复绑定同一供应商。";
    seen.add(route.providerId);
    if (mediaRouteInteger(route.priority, 0, 1_000_000) === null) return "路由优先级必须是 0–1000000 的整数。";
    if (mediaRouteInteger(route.weight, 1, 10_000) === null) return "路由权重必须是 1–10000 的整数。";
    if (typeof route.enabled !== "boolean") return "路由状态必须是布尔值。";
  }
  return null;
}

function importedMediaRoutes(value: unknown, index: number): MediaRouteDraft[] | undefined {
  if (value === undefined) return undefined;
  if (!Array.isArray(value)) throw new Error(`第 ${index + 1} 项的路由池必须是数组。`);
  return value.map((raw) => {
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) throw new Error(`第 ${index + 1} 项的路由必须是对象。`);
    const route = raw as Record<string, unknown>;
    if (typeof route.providerId !== "string" || typeof route.priority !== "number" || !Number.isSafeInteger(route.priority) || typeof route.weight !== "number" || !Number.isSafeInteger(route.weight) || typeof route.enabled !== "boolean" || Object.keys(route).some((key) => !["providerId", "priority", "weight", "enabled"].includes(key))) {
      throw new Error(`第 ${index + 1} 项的路由需提供供应商、整数优先级、整数权重和布尔启用状态；上游模型与适配器由档位共享。`);
    }
    return { providerId: route.providerId, priority: String(route.priority), weight: String(route.weight), enabled: route.enabled };
  });
}

export function validateDrafts(
  drafts: MediaModelDraft[],
  providers: ProviderOption[],
): string | null {
  return findDraftProblem(drafts, providers)?.message ?? null;
}

function tierProblem(
  draft: MediaModelDraft,
  index: number,
  seen: Set<string>,
  providers: ProviderOption[],
): string | null {
  const upstreamModel = draft.upstreamModel.trim();
  const modelID = draft.id.trim() || upstreamModel;
  const label = familyNameOf(draft) || modelID || `第 ${index + 1} 个模型`;
  if (!modelID) return `「${label}」缺少模型 ID，请先填写上游模型。`;
  if (seen.has(modelID)) return `模型 ID「${modelID}」重复，请改为唯一值。`;
  seen.add(modelID);
  if (!draft.name.trim()) return `「${label}」缺少展示名称。`;
  if (!draft.providerId) return `「${label}」未选择上游供应商。`;
  if (!providers.some((provider) => provider.id === draft.providerId)) {
    return `「${label}」引用的上游供应商已不存在，请重新选择。`;
  }
  const routeProblem = mediaRouteProblem(draft.routes, providers);
  if (routeProblem) return `「${label}」${routeProblem}`;
  if (!upstreamModel) return `「${label}」缺少上游模型 ID。`;
  const adapter = draft.adapter ?? "ark";
  if (adapter !== "ark" && adapter !== "toapis") return `「${label}」适配器不受支持。`;
  if (draft.billingUnit !== "second" && draft.billingUnit !== "request" && draft.billingUnit !== "token") {
    return `「${label}」计费单位不受支持。`;
  }
  if (adapter === "toapis") {
    if (draft.kind === "image") {
      if (!isToapisGptImage(draft)) return `「${label}」ToAPIs 仅支持已接入的 GPT Image 2 / 2.5 图片模型。`;
      if (draft.resolutions.length !== 1 || !TOAPIS_IMAGE_RESOLUTIONS.includes(draft.resolutions[0])) {
        return `「${label}」GPT Image 每个档位必须配置一个 1K、2K 或 4K 分辨率。`;
      }
      if (draft.generateAudio) return `「${label}」图片模型不支持生成音频。`;
    } else {
      const minimax = minimaxCapability(upstreamModel);
      const kling = upstreamModel === "kling-4.0";
      if (!minimax && !kling && !TOAPIS_SEEDANCE_PRESETS.some((preset) => preset.upstreamModel === upstreamModel) && !TOAPIS_SEEDANCE_25_PRESETS.some((preset) => preset.upstreamModel === upstreamModel)) {
        return `「${label}」ToAPIs 仅支持已接入的 Seedance 2.0 / 2.5、Kling 4.0 和 MiniMax H3 系列视频模型。`;
      }
      if (draft.resolutions.some((resolution) => !modelResolutions(draft).includes(resolution))) {
        return `「${label}」分辨率不受该 ToAPIs 模型支持。`;
      }
      const minDuration = kling ? TOAPIS_KLING_CAPABILITY.minDuration : minimax?.minDuration ?? 4;
      const maxDuration = kling ? TOAPIS_KLING_CAPABILITY.maxDuration : 15;
      if (Number(draft.minDuration) < minDuration || Number(draft.maxDuration) > maxDuration || draft.legacyDurations?.some((duration) => duration < minDuration || duration > maxDuration)) {
        return `「${label}」该 ToAPIs 模型镜头时长需在 ${minDuration}–${maxDuration} 秒内。`;
      }
      if ((minimax || kling) && draft.generateAudio) return `「${label}」该模型音频由模型决定，不支持生成音频开关。`;
      if (minimax && !minimax.videoReference && draft.videoReference?.enabled) return `「${label}」MiniMax H3 Max Turbo 不支持视频参考。`;
      if (draft.videoReference?.enabled && Number(draft.videoReference.maxDurationSeconds) > maxDuration) {
        return `「${label}」ToAPIs 视频参考片段上限为 ${maxDuration} 秒。`;
      }
      if (draft.videoReference?.enabled && draft.billingUnit !== "token" && (
        Number(draft.videoReference.tokenCostCredits || "0") !== 0 ||
        Number(draft.videoReference.tokenPriceCredits || "0") !== 0
      )) {
        return `「${label}」ToAPIs 按秒或按次计费模型不能配置视频参考 token 费率。`;
      }
    }
  }
  if (!draft.priceVersion.trim()) return `「${label}」缺少价格版本。`;
  if (!/^\d+(\.\d{1,6})?$/.test(draft.costCredits.trim())) {
    return `「${label}」成本价格式不正确（credit，最多 6 位小数）。`;
  }
  const costMicro = parseCreditsToMicro(draft.costCredits);
  if (costMicro < 0 || costMicro > 100_000_000) {
    return `「${label}」成本价需在 0–100 credit 之间。`;
  }
  if (draft.billingUnit === "token") {
    for (const [field, rawValue] of [["Token 成本", draft.tokenCostCredits], ["Token 售价", draft.tokenPriceCredits]] as const) {
      const value = rawValue ?? "";
      if (!/^\d+(\.\d{1,6})?$/.test(value.trim())) {
        return `「${label}」${field}格式不正确（credit/1M token，最多 6 位小数）。`;
      }
      const rate = parseCreditsToMicro(value);
      if (rate < 0 || rate > 1_000_000_000 || field === "Token 售价" && rate <= 0) {
        return `「${label}」${field}需在${field === "Token 售价" ? "大于 0 且" : ""}0–1000 credit/1M token 之间。`;
      }
    }
  }
  if (!/^\d+(\.\d{1,6})?$/.test(draft.priceCredits.trim()) && !(draft.billingUnit === "token" && draft.priceCredits.trim() === "")) {
    return `「${label}」单价格式不正确（credit，最多 6 位小数）。`;
  }
  const micro = draft.priceCredits.trim() ? parseCreditsToMicro(draft.priceCredits) : 0;
  if ((micro <= 0 && draft.billingUnit !== "token") || micro > 100_000_000) {
    return `「${label}」单价需在 0–100 credit 之间。`;
  }
  if (draft.kind === "video") {
    if (draft.resolutions.length === 0) return `「${label}」至少选择一个分辨率。`;
    if (
      draft.legacyDurations !== undefined &&
      (draft.legacyDurations.length === 0 ||
        draft.legacyDurations.some(
          (duration) => !Number.isInteger(duration) || duration < 2 || duration > 30,
        ))
    ) return `「${label}」旧枚举时长必须为 2–30 秒的整数。`;
    const min = draft.minDuration.trim();
    const max = draft.maxDuration.trim();
    if (!/^\d+$/.test(min) || !/^\d+$/.test(max)) {
      return `「${label}」镜头时长区间必须为整数秒。`;
    }
    const minDuration = Number(min);
    const maxDuration = Number(max);
    if (minDuration < 2 || maxDuration > 30 || minDuration > maxDuration) {
      return `「${label}」镜头时长区间需在 2–30 秒内，且起始不大于结束。`;
    }
  }
  if (draft.videoReference?.enabled) {
    if (draft.kind !== "video") return `「${label}」只有视频模型可以启用视频参考。`;
    const klingReference = draft.adapter === "toapis" && draft.upstreamModel.trim() === "kling-4.0";
    const referenceMaxDuration = klingReference ? 30 : 90;
    const referenceMaxImages = klingReference ? 10 : 8;
    if (!/^\d+$/.test(draft.videoReference.maxDurationSeconds.trim())) {
      return `「${label}」视频参考片段上限必须为 1–${referenceMaxDuration} 秒的整数。`;
    }
    const maxDurationSeconds = Number(draft.videoReference.maxDurationSeconds);
    if (
      !Number.isSafeInteger(maxDurationSeconds) ||
      maxDurationSeconds < 1 ||
      maxDurationSeconds > referenceMaxDuration
    ) {
      return `「${label}」视频参考片段上限必须为 1–${referenceMaxDuration} 秒的整数。`;
    }
    if (!/^\d+$/.test(draft.videoReference.maxImages.trim())) {
      return `「${label}」视频参考图上限必须为 1–${referenceMaxImages} 张的整数。`;
    }
    const maxImages = Number(draft.videoReference.maxImages);
    if (!Number.isSafeInteger(maxImages) || maxImages < 1 || maxImages > referenceMaxImages) {
      return `「${label}」视频参考图上限必须为 1–${referenceMaxImages} 张的整数。`;
    }
    const segmentsRaw = (draft.videoReference.maxSegments ?? "1").trim();
    const maxSegments = Number(segmentsRaw);
    if (!/^\d+$/.test(segmentsRaw) || !Number.isSafeInteger(maxSegments) || maxSegments < 1 || maxSegments > 30) {
      return `「${label}」视频参考执行段上限必须为 1–30 段的整数。`;
    }
    // 0 表示该型号不发送参考音频；非零时必须 ≥2 秒且不超过片段上限，与平台校验同口径。
    const audioRaw = (draft.videoReference.maxAudioSeconds ?? "0").trim();
    if (!/^\d+$/.test(audioRaw)) {
      return `「${label}」参考音频上限必须为 0 或 2–90 的整数。`;
    }
    const maxAudioSeconds = Number(audioRaw);
    if (
      !Number.isSafeInteger(maxAudioSeconds) ||
      maxAudioSeconds === 1 ||
      maxAudioSeconds > maxDurationSeconds
    ) {
      return `「${label}」参考音频上限必须为 0 或 2–${maxDurationSeconds} 的整数。`;
    }
    const rates = [
      ["token 成本", draft.videoReference.tokenCostCredits || "0"],
      ["token 售价", draft.videoReference.tokenPriceCredits || "0"],
    ] as const;
    for (const [field, value] of rates) {
      if (!/^\d+(\.\d{1,6})?$/.test(value.trim())) {
        return `「${label}」视频参考 ${field} 格式不正确（credit，最多 6 位小数）。`;
      }
      const rate = parseCreditsToMicro(value);
      if (rate < 0 || rate > 1_000_000_000) {
        return `「${label}」视频参考 ${field} 超出允许范围。`;
      }
    }
  }
  return null;
}

function exampleJson(providerId: string): string {
  return JSON.stringify(
    [
      {
        id: "seedance-2.0-mini-480p",
        name: "Seedance 2.0 mini 480p",
        kind: "video",
        providerId,
        upstreamModel: "your-upstream-video-model",
        adapter: "ark",
        resolutions: ["480p"],
        minDuration: 4,
        maxDuration: 15,
        costMicro: 232_000,
        generateAudio: true,
        priceMicro: 332_000,
        priceVersion: "v1",
      },
      {
        id: "seedance-2.0-mini-720p",
        name: "Seedance 2.0 mini 720p",
        kind: "video",
        providerId,
        upstreamModel: "your-upstream-video-model",
        adapter: "ark",
        resolutions: ["720p"],
        minDuration: 4,
        maxDuration: 15,
        costMicro: 500_000,
        generateAudio: true,
        priceMicro: 750_000,
        priceVersion: "v1",
      },
      {
        id: "product-image",
        name: "商品图片",
        kind: "image",
        providerId,
        upstreamModel: "your-upstream-image-model",
        adapter: "ark",
        resolutions: [],
        costMicro: 30_000,
        generateAudio: false,
        priceMicro: 50_000,
        priceVersion: "v1",
      },
    ],
    null,
    2,
  );
}

/** 高级 JSON 导入：宽松读取字段，具体合法性交给保存前校验与服务端。 */
export function draftsFromJson(raw: string): MediaModelDraft[] {
  const parsed: unknown = JSON.parse(raw);
  if (!Array.isArray(parsed)) throw new Error("JSON 顶层必须是模型数组。");
  return parsed.map((value, index) => {
    if (!value || typeof value !== "object" || Array.isArray(value)) {
      throw new Error(`第 ${index + 1} 项不是对象。`);
    }
    const record = value as Record<string, unknown>;
    const kind =
      record.kind === "image" || record.kind === "video" ? record.kind : null;
    if (!kind) throw new Error(`第 ${index + 1} 项的 kind 必须是 image 或 video。`);
    const upstreamModel = typeof record.upstreamModel === "string" ? record.upstreamModel : "";
    const seedanceContinuous = isSeedanceVideoModel({ kind, upstreamModel });
    const priceMicro = Number(record.priceMicro);
    const costMicro = Number(record.costMicro);
    const legacyDurations = Array.isArray(record.durations)
      ? record.durations
          .map((item) => Number(item))
          .filter((item) => Number.isInteger(item) && item > 0)
      : [];
    const minDuration =
      seedanceContinuous
        ? DEFAULT_MIN_DURATION
        : Number.isInteger(Number(record.minDuration)) && Number(record.minDuration) > 0
        ? Number(record.minDuration)
        : legacyDurations.length
          ? Math.min(...legacyDurations)
          : DEFAULT_MIN_DURATION;
    const maxDuration =
      seedanceContinuous
        ? DEFAULT_MAX_DURATION
        : Number.isInteger(Number(record.maxDuration)) && Number(record.maxDuration) > 0
        ? Number(record.maxDuration)
        : legacyDurations.length
          ? Math.max(...legacyDurations)
          : DEFAULT_MAX_DURATION;
    const hasRange =
      Number.isInteger(Number(record.minDuration)) && Number(record.minDuration) > 0 &&
      Number.isInteger(Number(record.maxDuration)) &&
      Number(record.maxDuration) >= Number(record.minDuration);
    let videoReference: MediaModelDraft["videoReference"];
    if (record.videoReference !== undefined) {
      if (!record.videoReference || typeof record.videoReference !== "object" || Array.isArray(record.videoReference)) {
        throw new Error(`第 ${index + 1} 项的视频参考配置必须是对象。`);
      }
      const capability = record.videoReference as Record<string, unknown>;
      const klingReferencePricing = record.adapter === "toapis" && upstreamModel.trim() === "kling-4.0";
      const referencePrice = klingReferencePricing ? Number(capability.priceMicro ?? 0) : 0;
      const referenceCost = klingReferencePricing ? Number(capability.costMicro ?? 0) : 0;
      const tokenPrice = Number(capability.tokenPriceMicroPerMillion ?? 0);
      const tokenCost = Number(capability.tokenCostMicroPerMillion ?? 0);
      if (
        typeof capability.maxDurationSeconds !== "number" ||
        typeof capability.maxImages !== "number" ||
        !Number.isSafeInteger(capability.maxDurationSeconds) ||
        !Number.isSafeInteger(capability.maxImages) ||
        (capability.maxSegments !== undefined &&
          (typeof capability.maxSegments !== "number" || !Number.isSafeInteger(capability.maxSegments) || capability.maxSegments < 1 || capability.maxSegments > 30)) ||
        !Number.isSafeInteger(referencePrice) || referencePrice < 0 ||
        !Number.isSafeInteger(referenceCost) || referenceCost < 0 ||
        !Number.isSafeInteger(tokenPrice) ||
        !Number.isSafeInteger(tokenCost)
      ) {
        throw new Error(`第 ${index + 1} 项的视频参考配置字段不完整。`);
      }
      videoReference = {
        enabled: true,
        maxDurationSeconds: String(capability.maxDurationSeconds),
        maxImages: String(capability.maxImages),
        ...(typeof capability.maxSegments === "number" && capability.maxSegments > 0 ? { maxSegments: String(capability.maxSegments) } : {}),
        tokenCostCredits: priceInputValue(tokenCost),
        tokenPriceCredits: priceInputValue(tokenPrice),
        ...(referenceCost > 0 ? { costCredits: priceInputValue(referenceCost) } : {}),
        ...(referencePrice > 0 ? { priceCredits: priceInputValue(referencePrice) } : {}),
      };
    }
    return {
      familyId: "",
      id: typeof record.id === "string" ? record.id : "",
      name: typeof record.name === "string" ? record.name : "",
      ...(typeof record.displayName === "string" ? { displayName: record.displayName } : {}),
      kind,
      providerId: typeof record.providerId === "string" ? record.providerId : "",
      ...(record.routes !== undefined ? { routes: importedMediaRoutes(record.routes, index) } : {}),
      upstreamModel,
      adapter: typeof record.adapter === "string" ? record.adapter : "ark",
      resolutions: Array.isArray(record.resolutions)
        ? record.resolutions.filter(
            (item): item is string => typeof item === "string",
          )
        : [],
      minDuration: String(minDuration),
      maxDuration: String(maxDuration),
      ...(kind === "video" && !seedanceContinuous && !hasRange && legacyDurations.length ? { legacyDurations } : {}),
      costCredits:
        Number.isFinite(costMicro) && costMicro >= 0
          ? priceInputValue(costMicro)
          : "0",
      billingUnit: record.billingUnit === "request" || record.billingUnit === "token"
        ? record.billingUnit
        : "second",
      tokenCostCredits: Number.isSafeInteger(Number(record.tokenCostMicroPerMillion)) && Number(record.tokenCostMicroPerMillion) >= 0
        ? priceInputValue(Number(record.tokenCostMicroPerMillion))
        : "0",
      tokenPriceCredits: Number.isSafeInteger(Number(record.tokenPriceMicroPerMillion)) && Number(record.tokenPriceMicroPerMillion) >= 0
        ? priceInputValue(Number(record.tokenPriceMicroPerMillion))
        : "0",
      generateAudio: kind === "video" && record.generateAudio === true,
      priceCredits:
        Number.isFinite(priceMicro) && priceMicro > 0
          ? priceInputValue(priceMicro)
          : "",
      priceVersion:
        typeof record.priceVersion === "string" ? record.priceVersion : "",
      ...(videoReference ? { videoReference } : {}),
    };
  });
}

const PREVIEW_PLACEHOLDER = 10;

type MediaSection = "models" | "source" | "reconcile";

const MEDIA_SECTIONS: { id: MediaSection; label: string }[] = [
  { id: "models", label: "模型清单" },
  { id: "source", label: "原片解析与定价" },
  { id: "reconcile", label: "提交核对" },
];

type FamilyChange = "new" | "modified" | "unchanged";

/** 卡片基线：加载/保存成功时的档位快照，用于在折叠清单上标出「新增 / 已修改」。 */
interface FamilySnapshot {
  key: string;
  value: string;
}

/** 卡片身份（类型 + 适配器 + 供应商 + 上游模型）：导入后 familyId 会重排，只能按内容认回。 */
function familyKeyOf(drafts: MediaModelDraft[]): string {
  const lead = drafts[0];
  return lead
    ? familyKey(lead)
    : "";
}

/** 搜索命中范围：卡片名、上游模型、平台 ID 与供应商名。 */
function matchesQuery(
  tiers: MediaModelDraft[],
  query: string,
  providerName: string,
  adapter: string,
): boolean {
  if (!query) return true;
  const haystack = [
    familyNameOf(tiers[0]),
    ...tiers.flatMap((tier) => [tier.upstreamModel, tier.id, tier.displayName ?? ""]),
    providerName,
    adapter,
  ]
    .join("\n")
    .toLowerCase();
  return query.toLowerCase().split(/\s+/).every((term) => haystack.includes(term));
}

function familySnapshots(drafts: MediaModelDraft[]): Record<string, FamilySnapshot> {
  const snapshots: Record<string, FamilySnapshot> = {};
  for (const family of groupFamilies(drafts)) {
    const tiers = family.indexes.map((index) => drafts[index]);
    snapshots[family.id] = {
      key: familyKeyOf(tiers),
      value: JSON.stringify(tiers, (field, value) =>
        field === "familyId" ? undefined : value,
      ),
    };
  }
  return snapshots;
}

function expandedKeysOf(drafts: MediaModelDraft[], expanded: Set<string>): Set<string> {
  return new Set(
    groupFamilies(drafts)
      .filter((family) => expanded.has(family.id))
      .map((family) => familyKeyOf(family.indexes.map((index) => drafts[index]))),
  );
}

/** 保存/重新加载后保留仍存在的卡片展开状态；新条目由调用方决定。 */
function expandMatching(
  drafts: MediaModelDraft[],
  keys: Set<string>,
): Set<string> {
  return new Set(
    groupFamilies(drafts)
      .filter((family) =>
        keys.has(familyKeyOf(family.indexes.map((index) => drafts[index]))),
      )
      .map((family) => family.id),
  );
}

function familyChange(
  tiers: MediaModelDraft[],
  familyId: string,
  baseline: Record<string, FamilySnapshot>,
): FamilyChange {
  const previous = baseline[familyId] ?? Object.values(baseline).find(
    (entry) => entry.key === familyKeyOf(tiers),
  );
  if (!previous) return "new";
  const current = JSON.stringify(tiers, (field, value) =>
    field === "familyId" ? undefined : value,
  );
  return current === previous.value ? "unchanged" : "modified";
}

export function MediaModelsPage() {
  const [drafts, setDrafts] = useState<MediaModelDraft[]>([]);
  const [providers, setProviders] = useState<ProviderOption[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [jsonText, setJsonText] = useState("");
  const [jsonError, setJsonError] = useState("");
  const [providerModels, setProviderModels] = useState<Record<string, string[]>>({});
  const [fetchingProvider, setFetchingProvider] = useState("");
  // 折叠清单：一次只展开正在编辑的卡片，卡片多时不必一路滚动。
  const [expandedFamilies, setExpandedFamilies] = useState<Set<string>>(new Set());
  const [baseline, setBaseline] = useState<Record<string, FamilySnapshot>>({});
  const [section, setSection] = useState<MediaSection>("models");
  const [presetsOpen, setPresetsOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [kindFilter, setKindFilter] = useState<"all" | "video" | "image">("all");
  const [attentionOnly, setAttentionOnly] = useState(false);
  // 新增/校验失败后需要滚动到视野的卡片；滚动完成后清空。
  const [focusedFamily, setFocusedFamily] = useState("");
  const [pendingReconcile, setPendingReconcile] = useState(0);

  // 展开状态由调用方传入：挂载时无既往状态，放弃修改时带上当前展开的卡片。
  const load = useCallback(async (previousExpanded: Set<string> = new Set()) => {
    setLoading(true);
    try {
      const [models, providerData] = await Promise.all([
        api<MediaModel[]>("/media/models"),
        api<{ providers: ProviderOption[] }>("/providers"),
      ]);
      const next = assignFamilyIds(models.map(toDraft));
      const families = groupFamilies(next);
      setDrafts(next);
      setProviders(providerData.providers);
      // 只有一张卡时直接展开；多卡时保持折叠，展开过的卡片按身份认回。
      setExpandedFamilies(
        families.length === 1 ? new Set([families[0].id]) : expandMatching(next, previousExpanded),
      );
      setBaseline(familySnapshots(next));
      setDirty(false);
      setError("");
      setJsonText("");
      setJsonError("");
    } catch (loadError) {
      setError(errorMessage(loadError));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // 只在挂载时加载；放弃修改由按钮显式调用 load，并传入当前展开状态。
    void load();
  }, [load]);

  // 新增或校验失败的卡片需要出现在视野里；等它渲染完再滚动，期间不动输入焦点。
  useEffect(() => {
    if (!focusedFamily) return;
    const reduceMotion = typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    document
      .getElementById(`media-family-${focusedFamily}`)
      ?.scrollIntoView({ block: "start", behavior: reduceMotion ? "auto" : "smooth" });
    setFocusedFamily("");
  }, [focusedFamily]);

  const updateTier = (index: number, patch: Partial<MediaModelDraft>): void => {
    setDrafts((previous) =>
      previous.map((draft, itemIndex) =>
        itemIndex === index ? { ...draft, ...patch } : draft,
      ),
    );
    setDirty(true);
    setError("");
  };

  const updateFamily = (
    indexes: number[],
    patch: Partial<MediaModelDraft>,
  ): void => {
    const target = new Set(indexes);
    setDrafts((previous) =>
      previous.map((draft, index) =>
        target.has(index)
          ? {
              ...draft,
              ...patch,
              ...(patch.adapter === undefined && patch.videoReference?.enabled && draft.videoReference?.enabled
                ? {
                    videoReference: {
                      ...patch.videoReference,
                      tokenCostCredits: draft.videoReference.tokenCostCredits,
                      tokenPriceCredits: draft.videoReference.tokenPriceCredits,
                    },
                  }
                : {}),
            }
          : draft,
      ),
    );
    setDirty(true);
    setError("");
  };

  // 卡片名写回每个档位条目：展示名仍保持「系列名 + 档位」，客户端目录不变。
  const updateFamilyName = (indexes: number[], name: string): void => {
    const target = new Set(indexes);
    setDrafts((previous) =>
      previous.map((draft, index) =>
        target.has(index) ? { ...draft, name: tierName(name, draft) } : draft,
      ),
    );
    setDirty(true);
    setError("");
  };

  const updateFamilyKind = (indexes: number[], kind: "image" | "video"): void => {
    const target = new Set(indexes);
    setDrafts((previous) => {
      if (kind === "image") {
        const lead = previous[indexes[0]];
        const keepImageTiers = lead && isToapisGptImage({ ...lead, kind });
        if (keepImageTiers && lead.kind === "image") return previous;
        // 旧 Ark 图片保持单行空档位；GPT 图片从 1K 开始配置独立 SKU。
        return previous
          .map((draft, index) =>
            index === indexes[0]
              ? {
                  ...draft,
                  kind,
                  name: keepImageTiers ? `${familyNameOf(draft)} 1K` : familyNameOf(draft),
                  resolutions: keepImageTiers ? ["1K"] : [],
                  generateAudio: false,
                  legacyDurations: undefined,
                  supportedAspectRatios: undefined,
                  videoReference: undefined,
                }
              : draft,
          )
          .filter((_, index) => index === indexes[0] || !target.has(index));
      }
      return previous.map((draft, index) =>
        target.has(index)
          ? {
              ...draft,
              kind,
              resolutions:
                draft.resolutions.length > 0 ? draft.resolutions : ["720p"],
              supportedAspectRatios: undefined,
            }
          : draft,
      );
    });
    setDirty(true);
    setError("");
  };

  const addTier = (indexes: number[], resolution: string): void => {
    const target = new Set(indexes);
    setDrafts((previous) => {
      const leadIndex = previous.findIndex((_, index) => target.has(index));
      const lead = previous[leadIndex];
      if (!lead) return previous;
      // 默认 ID 沿用既有档位 ID 的前缀（如 seedance-2.0-mini-480p → seedance-2.0-mini-1080p）。
      const idBase = lead.id.trim().replace(/-(480p|720p|768p|1080p|1k|2k|4k)$/i, "");
      const next: MediaModelDraft = {
        ...lead,
        id: idBase ? `${idBase}-${resolution}` : "",
        name: tierName(familyNameOf(lead), { ...lead, resolutions: [resolution] }),
        resolutions: [resolution],
        costCredits: "0",
        tokenCostCredits: "0",
        tokenPriceCredits: "",
        priceCredits: "",
        videoReference: lead.videoReference
          ? { ...lead.videoReference, enabled: true, tokenCostCredits: "0", tokenPriceCredits: "0" }
          : undefined,
      };
      const copy = [...previous];
      copy.splice(leadIndex + 1, 0, next);
      return copy;
    });
    setDirty(true);
    setError("");
  };

  const removeTier = (index: number): void => {
    setDrafts((previous) => previous.filter((_, itemIndex) => itemIndex !== index));
    setDirty(true);
    setError("");
  };

  const removeFamily = (indexes: number[]): void => {
    const target = new Set(indexes);
    setDrafts((previous) => previous.filter((_, index) => !target.has(index)));
    setDirty(true);
    setError("");
  };

  const toggleFamily = (familyId: string): void => {
    setExpandedFamilies((previous) => {
      const next = new Set(previous);
      if (next.has(familyId)) next.delete(familyId);
      else next.add(familyId);
      return next;
    });
  };

  // 新增或出错定位：清掉筛选让卡片可见，再展开并滚动过去。
  const revealFamilies = (familyIds: string[], focus: boolean): void => {
    const target = new Set(familyIds);
    if (!target.size) return;
    setQuery("");
    setKindFilter("all");
    setAttentionOnly(false);
    setExpandedFamilies((previous) => {
      const next = new Set(previous);
      for (const familyId of target) next.add(familyId);
      return next;
    });
    if (focus) setFocusedFamily(familyIds[0]);
  };

  const addModel = (): void => {
    const fallbackProvider =
      providers.find((provider) => provider.enabled)?.id ?? providers[0]?.id ?? "";
    const familyId = nextFamilyId();
    setDrafts((previous) => [
      ...previous,
      {
        familyId,
        id: "",
        name: "",
        kind: "video",
        providerId: fallbackProvider,
        upstreamModel: "",
        resolutions: ["720p"],
        minDuration: String(DEFAULT_MIN_DURATION),
        maxDuration: String(DEFAULT_MAX_DURATION),
        costCredits: "0",
        billingUnit: "second",
        tokenCostCredits: "0",
        tokenPriceCredits: "0",
        generateAudio: false,
        priceCredits: "",
        priceVersion: "v1",
      },
    ]);
    setDirty(true);
    setError("");
    revealFamilies([familyId], true);
  };

  const preferredProvider = (): string => {
    const ark = providers.find((provider) =>
      provider.baseUrl?.includes("ark.cn-beijing.volces.com"),
    );
    return ark?.id ?? providers.find((provider) => provider.enabled)?.id ?? providers[0]?.id ?? "";
  };

  const addVideoPresets = (adapter: "ark" | "toapis", family: "seedance" | "minimax" | "seedance25" | "seedance25ext" | "kling" = "seedance"): void => {
    const familyName = family === "minimax" ? "MiniMax H3" : family === "seedance25" ? "Seedance 2.5" : family === "seedance25ext" ? "Seedance 2.5 Ext" : family === "kling" ? "Kling 4.0" : "Seedance 2.0";
    const toapisProvider = providers.find((provider) => {
      try {
        return provider.enabled && ["toapis.com", "toapis.cn"].includes(new URL(provider.baseUrl).hostname);
      } catch {
        return false;
      }
    });
    const providerId = adapter === "ark" ? preferredProvider() : toapisProvider?.id;
    if (!providerId) {
      toastErr(adapter === "ark"
        ? "请先在「上游供应商」创建并配置 Ark API 前缀"
        : "请先在「上游供应商」新增 ToAPIs，填写 https://toapis.com/v1 或 https://toapis.cn/v1 与密钥；自定义代理可通过新增模型选择 ToAPIs 适配器。", "缺少供应商");
      return;
    }
    const existing = new Set(
      drafts.map((draft) => draft.id.trim() || draft.upstreamModel.trim()),
    );
    const additions: MediaModelDraft[] = [];
    const touched: string[] = [];
    const presets = family === "minimax"
      ? TOAPIS_MINIMAX_PRESETS
      : family === "seedance25"
        ? TOAPIS_SEEDANCE_25_PRESETS.filter((preset) => preset.upstreamModel === "seedance-2-5")
        : family === "seedance25ext"
          ? TOAPIS_SEEDANCE_25_PRESETS.filter((preset) => preset.upstreamModel === "seedance-2.5-ext")
          : family === "kling"
            ? TOAPIS_KLING_PRESETS
            : adapter === "toapis" ? TOAPIS_SEEDANCE_PRESETS : SEEDANCE_PRESETS;
    for (const preset of presets) {
      const existingFamily = drafts.find(
        (draft) =>
          draft.kind === "video" &&
          (draft.adapter ?? "ark") === adapter &&
          draft.providerId === providerId &&
          draft.upstreamModel.trim() === preset.upstreamModel,
      );
      const familyId = existingFamily?.familyId ?? nextFamilyId();
      const before = additions.length;
      for (const tier of preset.tiers) {
        if (existing.has(tier.id)) continue;
        const tierPricing = tier as typeof tier & {
          costCredits?: string;
          priceCredits?: string;
          tokenCostCredits?: string;
          tokenPriceCredits?: string;
          videoReferenceTokenCostCredits?: string;
          videoReferenceTokenPriceCredits?: string;
          videoReferenceCostCredits?: string;
          videoReferencePriceCredits?: string;
        };
        additions.push({
          familyId,
          id: tier.id,
          name: `${preset.name} ${tier.resolution}`,
          kind: "video",
          providerId,
          upstreamModel: preset.upstreamModel,
          adapter,
          resolutions: [tier.resolution],
          minDuration: String(family === "minimax" ? TOAPIS_MINIMAX_CAPABILITIES[preset.upstreamModel]!.minDuration : DEFAULT_MIN_DURATION),
          maxDuration: String(family === "kling" ? 30 : DEFAULT_MAX_DURATION),
          costCredits: tierPricing.costCredits ?? "0",
          billingUnit: "billingUnit" in preset ? preset.billingUnit : "second",
          tokenCostCredits: tierPricing.tokenCostCredits ?? "0",
          tokenPriceCredits: tierPricing.tokenPriceCredits ?? "0",
          generateAudio: family !== "minimax" && family !== "kling",
          priceCredits: tierPricing.priceCredits ?? "",
          priceVersion: "v1",
          ...("videoReferenceTokenPriceCredits" in tierPricing ? {
            videoReference: {
              enabled: true,
              maxDurationSeconds: "15",
              maxImages: "8",
              maxSegments: "1",
              tokenCostCredits: tierPricing.videoReferenceTokenCostCredits ?? "0",
              tokenPriceCredits: tierPricing.videoReferenceTokenPriceCredits ?? "0",
            },
          } : {}),
          ...(family === "kling" ? {
            videoReference: {
              enabled: true,
              maxDurationSeconds: "30",
              maxImages: "10",
              maxSegments: "1",
              tokenCostCredits: "0",
              tokenPriceCredits: "0",
              costCredits: tierPricing.videoReferenceCostCredits ?? "0",
              priceCredits: tierPricing.videoReferencePriceCredits ?? "0",
            },
          } : {}),
          // 补档必须继承卡片共享配置；默认值会与旧枚举、音频或价格版本冲突并阻塞保存。
          ...(existingFamily ? sharedConfiguration(existingFamily) : {}),
          ...(existingFamily && "videoReferenceTokenPriceCredits" in tierPricing ? {
            videoReference: {
              ...(existingFamily.videoReference ?? {
                enabled: true,
                maxDurationSeconds: "15",
                maxImages: "8",
                maxSegments: "1",
              }),
              enabled: true,
              tokenCostCredits: tierPricing.videoReferenceTokenCostCredits ?? "0",
              tokenPriceCredits: tierPricing.videoReferenceTokenPriceCredits ?? "0",
            },
          } : {}),
        });
      }
      if (additions.length > before) touched.push(familyId);
    }
    if (additions.length === 0) {
      toastErr(`${adapter === "toapis" ? "ToAPIs " : ""}${familyName} 条目都已在目录中，请直接编辑现有条目`, "无需重复添加");
      return;
    }
    setDrafts((previous) => [...previous, ...additions]);
    setDirty(true);
    setError("");
    revealFamilies(touched, true);
    setPresetsOpen(false);
    toastOk(
      `已加入 ${additions.length} 个档位条目，请确认供应商并填写成本和售价`,
      `已加入 ${adapter === "toapis" ? "ToAPIs " : ""}${familyName} 系列`,
    );
  };

  const addSeedreamModels = (): void => {
    const providerId = preferredProvider();
    if (!providerId) {
      toastErr("请先在「上游供应商」创建并配置 Ark API 前缀", "缺少供应商");
      return;
    }
    const existing = new Set(
      drafts.map((draft) => draft.id.trim() || draft.upstreamModel.trim()),
    );
    const additions = SEEDREAM_PRESETS.filter((preset) => !existing.has(preset.id));
    if (additions.length === 0) {
      toastErr("Seedream 条目都已在目录中，请直接编辑现有条目", "无需重复添加");
      return;
    }
    const families = additions.map(() => nextFamilyId());
    setDrafts((previous) => [
      ...previous,
      ...additions.map((preset, index) => ({
        familyId: families[index],
        id: preset.id,
        name: preset.name,
        kind: "image" as const,
        providerId,
        upstreamModel: preset.upstreamModel,
        resolutions: [],
        minDuration: String(DEFAULT_MIN_DURATION),
        maxDuration: String(DEFAULT_MAX_DURATION),
        costCredits: "0",
        billingUnit: "second" as const,
        tokenCostCredits: "0",
        tokenPriceCredits: "0",
        generateAudio: false,
        priceCredits: "",
        priceVersion: "v1",
      })),
    ]);
    setDirty(true);
    setError("");
    revealFamilies(families, true);
    setPresetsOpen(false);
    toastOk(
      `已加入 ${additions.length} 个图片模型，请填写成本和售价`,
      "已加入 Seedream",
    );
  };

  const addGptImageModels = (): void => {
    const providerId = providers.find((provider) => {
      try {
        return provider.enabled && ["toapis.com", "toapis.cn", "api.toapis.com", "api.toapis.cn"].includes(new URL(provider.baseUrl).hostname);
      } catch {
        return false;
      }
    })?.id;
    if (!providerId) {
      toastErr("请先新增 ToAPIs 供应商并填写账号支持的 API 地址与密钥；自定义代理可通过新增模型选择 ToAPIs 适配器。", "缺少供应商");
      return;
    }
    const existing = new Set(drafts.map((draft) => draft.id.trim() || draft.upstreamModel.trim()));
    const additions: MediaModelDraft[] = [];
    const touched: string[] = [];
    for (const preset of TOAPIS_GPT_IMAGE_PRESETS) {
      const existingFamily = drafts.find((draft) => isToapisGptImage(draft) && draft.providerId === providerId && draft.upstreamModel.trim() === preset.upstreamModel);
      const familyId = existingFamily?.familyId ?? nextFamilyId();
      const before = additions.length;
      for (const resolution of TOAPIS_IMAGE_RESOLUTIONS) {
        const id = `toapis-${preset.upstreamModel}-${resolution.toLowerCase()}`;
        if (existing.has(id)) continue;
        additions.push({
          familyId, id, name: `${preset.name} ${resolution}`, kind: "image",
          providerId, upstreamModel: preset.upstreamModel, adapter: "toapis",
          resolutions: [resolution], minDuration: String(DEFAULT_MIN_DURATION),
          maxDuration: String(DEFAULT_MAX_DURATION), costCredits: "0", generateAudio: false,
          billingUnit: "second" as const, tokenCostCredits: "0", tokenPriceCredits: "0",
          priceCredits: "", priceVersion: "v1",
          ...(existingFamily ? sharedConfiguration(existingFamily) : {}),
        });
      }
      if (additions.length > before) touched.push(familyId);
    }
    if (!additions.length) {
      toastErr("GPT Image 条目都已在目录中，请直接编辑现有档位", "无需重复添加");
      return;
    }
    setDrafts((previous) => [...previous, ...additions]);
    setDirty(true);
    setError("");
    revealFamilies(touched, true);
    setPresetsOpen(false);
    toastOk(`已加入 ${additions.length} 个图片档位，请确认供应商地址并填写每张成本和售价`, "已加入 ToAPIs GPT Image");
  };

  // 平台模型 ID 默认跟随上游模型；用户手动改过 ID 后不再覆盖。
  const updateUpstreamModel = (indexes: number[], value: string): void => {
    const target = new Set(indexes);
    setDrafts((previous) =>
      previous.map((draft, index) => {
        if (!target.has(index)) return draft;
        const currentID = draft.id.trim();
        const mirrorID = currentID === "" || currentID === draft.upstreamModel.trim();
        return {
          ...draft,
          upstreamModel: value,
          supportedAspectRatios: undefined,
          ...(mirrorID ? { id: value } : {}),
        };
      }),
    );
    setDirty(true);
    setError("");
  };

  const fetchModels = async (providerId: string, indexes: number[]): Promise<void> => {
    if (!providerId) {
      toastErr("请先为模型选择上游供应商", "无法获取模型");
      return;
    }
    const adapter = drafts[indexes[0]]?.adapter ?? "ark";
    const kind = drafts[indexes[0]]?.kind ?? "video";
    const cacheKey = `${adapter}:${kind}:${providerId}`;
    setFetchingProvider(cacheKey);
    try {
      // ToAPIs 文档只保证 /v1/models，不能先请求可能返回站点 HTML 的根 /models。
      const data = await api<{ models: string[] }>(
        `/providers/${encodeURIComponent(providerId)}/models?${adapter === "toapis" ? `type=${kind}&adapter=toapis` : "base=prefix"}`,
      );
      setProviderModels((previous) => ({
        ...previous,
        [cacheKey]: data.models,
      }));
      const current = drafts[indexes[0]]?.upstreamModel.trim() ?? "";
      if (data.models.length === 1 && !current) {
        updateUpstreamModel(indexes, data.models[0]);
        toastOk(`已自动填入唯一模型「${data.models[0]}」`, "已获取");
      } else if (data.models.length === 0) {
        toastErr("供应商返回的模型列表为空，请手动填写上游模型 ID", "未获取到模型");
      } else {
        toastOk(`获取到 ${data.models.length} 个模型，请在列表中选择`, "已获取");
      }
    } catch (fetchError) {
      toastErr(errorMessage(fetchError), "获取模型失败");
    } finally {
      setFetchingProvider("");
    }
  };

  const save = async (): Promise<void> => {
    const problem = findDraftProblem(drafts, providers);
    if (problem) {
      setError(problem.message);
      // 折叠清单里看不到出错的卡片：展开并滚动过去，省去逐个翻找。
      revealFamilies([problem.familyId], true);
      toastErr(problem.message, "无法保存");
      return;
    }
    setBusy(true);
    setError("");
    const previousExpanded = expandedKeysOf(drafts, expandedFamilies);
    try {
      const saved = await api<MediaModel[]>("/media/models", {
        method: "PUT",
        body: JSON.stringify(drafts.map(toApiModel)),
      });
      const next = assignFamilyIds(saved.map(toDraft));
      setDrafts(next);
      setExpandedFamilies(expandMatching(next, previousExpanded));
      setBaseline(familySnapshots(next));
      setDirty(false);
      setAdvancedOpen(false);
      toastOk("新制作单将使用本次媒体模型配置", "已保存");
    } catch (saveError) {
      const message = errorMessage(saveError);
      setError(message);
      toastErr(message, "保存失败");
    } finally {
      setBusy(false);
    }
  };

  const openJson = (): void => {
    if (!advancedOpen && !jsonText) {
      try {
        setJsonText(JSON.stringify(drafts.map(toApiModel), null, 2));
        setJsonError("");
      } catch (formatError) {
        setJsonError(errorMessage(formatError));
      }
    }
    setAdvancedOpen((previous) => !previous);
  };

  const importJson = (): void => {
    try {
      const imported = assignFamilyIds(draftsFromJson(jsonText));
      setDrafts(imported);
      setDirty(true);
      setError("");
      setJsonError("");
      toastOk(`已导入 ${imported.length} 个模型，请点击「保存配置」`, "已导入");
    } catch (importError) {
      setJsonError(errorMessage(importError));
    }
  };

  const videoCount = drafts.filter((draft) => draft.kind === "video").length;
  const imageCount = drafts.length - videoCount;
  const families = groupFamilies(drafts);
  const providerOptions = [
    { value: "", label: "选择供应商…" },
    ...providers.map((provider) => ({
      value: provider.id,
      label: provider.enabled ? provider.name : `${provider.name}（已停用）`,
    })),
  ];

  const providerNameOf = (providerId: string): string => {
    const provider = providers.find((item) => item.id === providerId);
    return provider
      ? provider.enabled
        ? provider.name
        : `${provider.name}（已停用）`
      : providerId;
  };
  const summaries = new Map(
    families.map((family) => {
      const tiers = family.indexes.map((index) => drafts[index]);
      return [family.id, {
        tiers,
        missing: tiers.filter((tier) => tier.billingUnit === "token"
          ? previewMicro(tier.tokenPriceCredits) <= 0
          : previewMicro(tier.priceCredits) <= 0).length,
        conflicted: sharedConflicts(tiers).length > 0,
        change: familyChange(tiers, family.id, baseline),
      }] as const;
    }),
  );
  const attentionCount = [...summaries.values()].filter(
    (summary) => summary.missing > 0 || summary.conflicted || summary.change !== "unchanged",
  ).length;
  const changedCount = [...summaries.values()].filter(
    (summary) => summary.change !== "unchanged",
  ).length;
  // 基线里按 id 或身份都认不回的卡片视为已删除。
  const liveKeys = new Set(
    families.map((family) =>
      familyKeyOf(family.indexes.map((index) => drafts[index])),
    ),
  );
  const removedCount = Object.entries(baseline).filter(
    ([id, snapshot]) => !summaries.has(id) && !liveKeys.has(snapshot.key),
  ).length;
  const visibleFamilies = families.filter((family) => {
    const summary = summaries.get(family.id);
    if (!summary?.tiers.length) return false;
    if (kindFilter !== "all" && summary.tiers[0].kind !== kindFilter) return false;
    if (
      attentionOnly &&
      summary.missing === 0 &&
      !summary.conflicted &&
      summary.change === "unchanged"
    ) return false;
    return matchesQuery(
      summary.tiers,
      query.trim(),
      providerNameOf(summary.tiers[0].providerId),
      summary.tiers[0].adapter ?? "ark",
    );
  });
  const allVisibleExpanded = visibleFamilies.length > 0 &&
    visibleFamilies.every((family) => expandedFamilies.has(family.id));
  const filtered = visibleFamilies.length !== families.length;
  const changeSummary = [
    dirty ? "有未保存修改" : "配置已同步",
    ...(changedCount ? [`${changedCount} 张卡片已改动`] : []),
    ...(removedCount ? [`${removedCount} 张已删除`] : []),
  ].join(" · ");

  const presetActions: { label: string; title: string; run: () => void }[] = [
    {
      label: "Seedance 2.0 系列",
      title: "补齐 mini / fast / 基础版的 Seedance 2.0 档位",
      run: () => addVideoPresets("ark"),
    },
    {
      label: "ToAPIs Seedance 2.0",
      title: "加入 ToAPIs 的 mini / fast / 标准版八档，请确认供应商并填写价格",
      run: () => addVideoPresets("toapis"),
    },
    {
      label: "ToAPIs Seedance 2.5",
      title: "加入 Seedance 2.5 的 480p / 720p / 1080p token 计费档位",
      run: () => addVideoPresets("toapis", "seedance25"),
    },
    {
      label: "ToAPIs Seedance 2.5 Ext",
      title: "加入 Seedance 2.5 Ext 720p 按次计费档位",
      run: () => addVideoPresets("toapis", "seedance25ext"),
    },
    {
      label: "ToAPIs MiniMax H3",
      title: "加入 MiniMax H3 / Max / Max Turbo 三款六档，请确认供应商并填写价格",
      run: () => addVideoPresets("toapis", "minimax"),
    },
    {
      label: "ToAPIs Kling 4.0",
      title: "加入 Kling 4.0 的 720p / 1080p 档位（3–30 秒，含视频参考价格）",
      run: () => addVideoPresets("toapis", "kling"),
    },
    {
      label: "ToAPIs GPT Image",
      title: "加入 GPT Image 2 / 2.5 Flare / Sunburst 三款九档，请确认供应商地址并填写每张价格",
      run: addGptImageModels,
    },
    {
      label: "Seedream 图像",
      title: "加入 Seedream 5.0 Pro / Flash / Lite / 4.5 / 4.0 图片模型",
      run: addSeedreamModels,
    },
  ];
  const presetsDisabled = loading || busy || providers.length === 0;

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">接入 / 媒体创作</p>
          <PageTitle icon={Clapperboard}>媒体模型</PageTitle>
        </div>
        <span className="readout">
          {loading
            ? "加载中…"
            : `${families.length} 个上游模型 · ${videoCount} 视频档位 · ${imageCount} 图片`}
        </span>
      </header>

      {/* 三段配置各管一摊：默认落在模型清单，设置类内容不再把清单顶出首屏。 */}
      <div className="page-tabs" role="tablist" aria-label="媒体模型页面分区">
        {MEDIA_SECTIONS.map((item) => (
          <button
            key={item.id}
            id={`media-tab-${item.id}`}
            type="button"
            role="tab"
            className="page-tab"
            aria-selected={section === item.id}
            aria-controls={`media-panel-${item.id}`}
            tabIndex={section === item.id ? 0 : -1}
            onClick={() => setSection(item.id)}
            onKeyDown={(event) => {
              const step = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
              if (!step) return;
              event.preventDefault();
              const index = MEDIA_SECTIONS.findIndex((entry) => entry.id === section);
              const next = MEDIA_SECTIONS[(index + step + MEDIA_SECTIONS.length) % MEDIA_SECTIONS.length];
              setSection(next.id);
              document.getElementById(`media-tab-${next.id}`)?.focus();
            }}
          >
            {item.label}
            {item.id === "reconcile" && pendingReconcile > 0 ? (
              <span className="page-tab-count">{pendingReconcile}</span>
            ) : null}
          </button>
        ))}
      </div>

      <div
        id="media-panel-models"
        role="tabpanel"
        aria-labelledby="media-tab-models"
        hidden={section !== "models"}
      >
        <Notice k="计费口径">
          一个上游模型一张卡：分辨率价格条目在卡内维护，镜头时长统一按连续 4–15 秒区间配置，共享配置只填一次。保存后仍按每个分辨率条目下发一条模型；视频按模型计费单位（按秒、按次或按 Token）结算，图片按张计价，1 credit = ¥1。Token 模型以供应商返回的实际 usage 结算，按次模型与输出时长无关。
        </Notice>
        {!loading && providers.length === 0 ? (
          <Notice
            k="缺少上游供应商"
            action={{ href: "#/providers", label: "去配置" }}
          >
            媒体模型需要先绑定一个上游供应商，请先在「上游供应商」中创建并填入对应的完整 API 前缀与密钥。
          </Notice>
        ) : null}
        {error ? (
          <Notice tone="stop" k="操作未完成">
            {error}
          </Notice>
        ) : null}

        <section className="pane d1">
          <div className="pane-h">
            <h2>模型清单</h2>
            <div className="form-row">
              <button
                className="btn btn-ghost btn-sm"
                type="button"
                aria-expanded={presetsOpen}
                aria-controls="media-presets"
                onClick={() => setPresetsOpen((previous) => !previous)}
                disabled={loading || busy}
                title="按供应商预设批量补齐已接入的 Seedance / MiniMax / GPT Image / Seedream 档位"
              >
                {presetsOpen ? "收起预设" : "从预设添加"}
              </button>
              <button
                className="btn btn-ghost btn-sm"
                type="button"
                onClick={openJson}
                disabled={loading || busy}
              >
                <Glyph icon={FileJson} size={14} strokeWidth={2} />
                {advancedOpen ? "收起 JSON" : "JSON 批量编辑"}
              </button>
              <button
                className="btn btn-sm"
                type="button"
                onClick={addModel}
                disabled={loading || busy}
              >
                <Glyph icon={Plus} size={14} strokeWidth={2} />
                新增模型
              </button>
            </div>
          </div>

          {presetsOpen ? (
            <div className="pane-b media-presets" id="media-presets">
              <span className="readout">预设会追加尚未存在的分辨率价格条目，已存在的条目保持不动；价格仍需逐条填写。</span>
              <div className="preset-actions">
                {presetActions.map((preset) => (
                  <button
                    key={preset.label}
                    className="btn btn-ghost btn-sm"
                    type="button"
                    disabled={presetsDisabled}
                    title={
                      providers.length === 0
                        ? "请先创建上游供应商"
                        : preset.title
                    }
                    onClick={preset.run}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
            </div>
          ) : null}

          {!loading && drafts.length > 0 ? (
            <div className="pane-b media-filter-bar">
              <input
                className="input media-search"
                type="search"
                aria-label="搜索媒体模型"
                placeholder="搜索模型名、上游模型 ID 或供应商…"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              <div className="choice-row">
                {([
                  ["all", `全部 ${families.length}`],
                  ["video", `视频 ${families.filter((family) => summaries.get(family.id)?.tiers[0]?.kind === "video").length}`],
                  ["image", `图片 ${families.filter((family) => summaries.get(family.id)?.tiers[0]?.kind === "image").length}`],
                ] as const).map(([value, label]) => (
                  <button
                    key={value}
                    className={"choice-chip" + (kindFilter === value ? " selected" : "")}
                    type="button"
                    aria-pressed={kindFilter === value}
                    onClick={() => setKindFilter(value)}
                  >
                    {label}
                  </button>
                ))}
                <button
                  className={"choice-chip" + (attentionOnly ? " selected" : "")}
                  type="button"
                  aria-pressed={attentionOnly}
                  title="只看缺价、共享配置冲突或本次改动过的卡片"
                  onClick={() => setAttentionOnly((previous) => !previous)}
                >
                  待处理 {attentionCount}
                </button>
              </div>
              <div className="media-list-actions">
                <span className="readout">
                  {filtered ? `${visibleFamilies.length} / ${families.length} 张` : `${families.length} 张卡片`}
                </span>
                <button
                  className="btn btn-ghost btn-sm"
                  type="button"
                  disabled={visibleFamilies.length === 0}
                  onClick={() => {
                    setExpandedFamilies(
                      allVisibleExpanded
                        ? new Set()
                        : new Set(visibleFamilies.map((family) => family.id)),
                    );
                  }}
                >
                  {allVisibleExpanded ? "全部收起" : "全部展开"}
                </button>
              </div>
            </div>
          ) : null}

          <div className="pane-b media-list-body">
            {loading ? (
              <p className="readout">正在加载媒体模型与供应商…</p>
            ) : drafts.length === 0 ? (
              <div className="empty" style={{ margin: "8px auto 0" }}>
                <h2>还没有媒体模型</h2>
                <p>
                  添加后，客户端媒体创作页才会出现对应的视频或图片模型。
                  <br />
                  一个上游模型一张卡，分辨率档位在卡内维护价格。
                </p>
                <button className="btn" type="button" onClick={addModel}>
                  新增第一个模型
                </button>
              </div>
            ) : visibleFamilies.length === 0 ? (
              <div className="empty" style={{ margin: "8px auto 0" }}>
                <h2>没有匹配的卡片</h2>
                <p>换个关键词，或清空类型与「待处理」筛选。</p>
                <button
                  className="btn btn-ghost"
                  type="button"
                  onClick={() => {
                    setQuery("");
                    setKindFilter("all");
                    setAttentionOnly(false);
                  }}
                >
                  清空筛选
                </button>
              </div>
            ) : (
              <div className="media-family-list">
                {visibleFamilies.map((family) => {
                  const summary = summaries.get(family.id);
                  return (
                    <MediaFamilyCard
                      key={family.id}
                      familyId={family.id}
                      indexes={family.indexes}
                      drafts={drafts}
                      providerOptions={providerOptions}
                      providerModels={providerModels}
                      fetchingProvider={fetchingProvider}
                      busy={busy}
                      providerName={
                        summary?.tiers[0] ? providerNameOf(summary.tiers[0].providerId) : ""
                      }
                      missingCount={summary?.missing ?? 0}
                      conflicted={summary?.conflicted ?? false}
                      change={summary?.change ?? "unchanged"}
                      expanded={expandedFamilies.has(family.id)}
                      onToggle={() => toggleFamily(family.id)}
                      onUpdateTier={updateTier}
                      onUpdateFamily={updateFamily}
                      onUpdateName={updateFamilyName}
                      onUpdateKind={updateFamilyKind}
                      onUpdateUpstream={updateUpstreamModel}
                      onAddTier={addTier}
                      onRemoveTier={removeTier}
                      onRemoveFamily={removeFamily}
                      onFetchModels={(providerId) => void fetchModels(providerId, family.indexes)}
                    />
                  );
                })}
              </div>
            )}

            {!loading ? (
              // 底部常驻保存条：卡片再长也不必滚回清单末尾才能保存。
              <div className={"media-save-bar" + (dirty ? " is-dirty" : "")}>
                <button
                  className="btn"
                  type="button"
                  disabled={busy || !dirty}
                  onClick={() => void save()}
                >
                  {busy ? "保存中…" : "保存配置"}
                </button>
                <button
                  className="btn btn-ghost"
                  type="button"
                  disabled={busy || !dirty}
                  onClick={() => {
                    void load(expandedKeysOf(drafts, expandedFamilies));
                  }}
                >
                  放弃修改
                </button>
                <span className="readout">{changeSummary} · 价格版本只影响新制作单</span>
              </div>
            ) : null}
          </div>

          {advancedOpen ? (
            <div
              className="pane-b"
              style={{ borderTop: "1px solid var(--line-soft)" }}
            >
              <div
                className="form-row"
                style={{ justifyContent: "space-between", marginBottom: 10 }}
              >
                <span className="readout">批量导入后仍需点击「保存配置」</span>
                <div className="form-row">
                  <button
                    className="btn btn-ghost btn-sm"
                    type="button"
                    onClick={() => {
                      try {
                        setJsonText(
                          JSON.stringify(drafts.map(toApiModel), null, 2),
                        );
                        setJsonError("");
                      } catch (formatError) {
                        setJsonError(errorMessage(formatError));
                      }
                    }}
                  >
                    载入当前配置
                  </button>
                  <button
                    className="btn btn-ghost btn-sm"
                    type="button"
                    onClick={() => {
                      setJsonText(exampleJson(providers[0]?.id ?? ""));
                      setJsonError("");
                    }}
                  >
                    填入示例
                  </button>
                  <button
                    className="btn btn-sm"
                    type="button"
                    disabled={!jsonText.trim()}
                    onClick={importJson}
                  >
                    从 JSON 导入
                  </button>
                </div>
              </div>
              {jsonError ? <Notice tone="stop">{jsonError}</Notice> : null}
              <textarea
                className="media-json"
                aria-label="媒体模型 JSON"
                spellCheck={false}
                value={jsonText}
                onChange={(event) => {
                  setJsonText(event.target.value);
                  setJsonError("");
                }}
              />
            </div>
          ) : null}
        </section>
      </div>

      <div
        id="media-panel-source"
        role="tabpanel"
        aria-labelledby="media-tab-source"
        hidden={section !== "source"}
      >
        <SourceParserStatus />
        <ReferencePricing />
      </div>

      <div
        id="media-panel-reconcile"
        role="tabpanel"
        aria-labelledby="media-tab-reconcile"
        hidden={section !== "reconcile"}
      >
        <MediaReconciliation onPendingCount={setPendingReconcile} />
      </div>
    </div>
  );
}

function MediaFamilyCard({
  familyId,
  indexes,
  drafts,
  providerOptions,
  providerModels,
  fetchingProvider,
  busy,
  providerName,
  missingCount,
  conflicted,
  change,
  expanded,
  onToggle,
  onUpdateTier,
  onUpdateFamily,
  onUpdateName,
  onUpdateKind,
  onUpdateUpstream,
  onAddTier,
  onRemoveTier,
  onRemoveFamily,
  onFetchModels,
}: {
  familyId: string;
  indexes: number[];
  drafts: MediaModelDraft[];
  providerOptions: { value: string; label: string }[];
  providerModels: Record<string, string[]>;
  fetchingProvider: string;
  busy: boolean;
  /** 折叠清单上展示的供应商名与状态摘要，均由整张卡片汇总。 */
  providerName: string;
  missingCount: number;
  conflicted: boolean;
  change: FamilyChange;
  expanded: boolean;
  onToggle: () => void;
  onUpdateTier: (index: number, patch: Partial<MediaModelDraft>) => void;
  onUpdateFamily: (indexes: number[], patch: Partial<MediaModelDraft>) => void;
  onUpdateName: (indexes: number[], name: string) => void;
  onUpdateKind: (indexes: number[], kind: "image" | "video") => void;
  onUpdateUpstream: (indexes: number[], value: string) => void;
  onAddTier: (indexes: number[], resolution: string) => void;
  onRemoveTier: (index: number) => void;
  onRemoveFamily: (indexes: number[]) => void;
  onFetchModels: (providerId: string) => void;
}) {
  const lead = drafts[indexes[0]];
  const [previewTier, setPreviewTier] = useState(lead.resolutions[0] ?? "");
  const [previewDuration, setPreviewDuration] = useState(PREVIEW_PLACEHOLDER);
  if (!lead) return null;

  const isVideo = lead.kind === "video";
  const hasTiers = hasResolutionTiers(lead);
  const isToapis = lead.adapter === "toapis";
  const minimax = isToapis ? minimaxCapability(lead.upstreamModel.trim()) : undefined;
  const kling = isToapis && lead.upstreamModel.trim() === "kling-4.0";
  const resolutions = modelResolutions(lead);
  const name = familyNameOf(lead);
  const tiers = indexes.map((index) => drafts[index]);
  const preview =
    tiers.find((draft) => tierLabel(draft) === previewTier) ??
    lead;
  const minDuration = Math.max(
    2,
    Math.min(30, Number.parseInt(lead.minDuration, 10) || DEFAULT_MIN_DURATION),
  );
  const maxDuration = Math.max(
    minDuration,
    Math.min(30, Number.parseInt(lead.maxDuration, 10) || DEFAULT_MAX_DURATION),
  );
  const legacyTiers = tiers.filter((draft) => draft.legacyDurations !== undefined);
  const conflicts = sharedConflicts(tiers);
  const legacyDurations = preview.legacyDurations;
  const duration = legacyDurations?.length
    ? (legacyDurations.includes(previewDuration) ? previewDuration : legacyDurations[0])
    : Math.min(maxDuration, Math.max(minDuration, previewDuration));
  const saleMicro = previewMicro(preview.priceCredits);
  const tokenSaleMicro = previewMicro(preview.tokenPriceCredits);
  const total = preview.billingUnit === "request"
    ? (saleMicro > 0 ? trimNumber(saleMicro / MICRO_CREDITS_PER_CREDIT) : "—")
    : preview.billingUnit === "token"
      ? "按实际 Token 结算"
      : (saleMicro > 0 ? trimNumber((saleMicro * duration) / MICRO_CREDITS_PER_CREDIT) : "—");
  const reference = preview.videoReference?.enabled ? preview.videoReference : null;
  const referenceCost = previewMicro(reference?.tokenCostCredits);
  const referencePrice = previewMicro(reference?.tokenPriceCredits);
  const referenceMargin =
    referencePrice > 0
      ? `${(((referencePrice - referenceCost) / referencePrice) * 100).toFixed(1)}%`
      : "—";
  const unusedResolutions = resolutions.filter(
    (resolution) => !tiers.some((draft) => draft.resolutions.includes(resolution)),
  );
  const providerCacheKey = `${lead.adapter ?? "ark"}:${lead.kind}:${lead.providerId}`;
  const fetchedModels = lead.providerId ? providerModels[providerCacheKey] : undefined;

  return (
    <article
      className={"model-card media-family-card" + (expanded ? " is-expanded" : "")}
      id={`media-family-${familyId}`}
    >
      {/* 折叠态的一行摘要：卡片再多也只占一行，点摘要行或标题即可展开编辑。 */}
      <div className="media-summary-row">
        <button
          className="media-summary-toggle"
          type="button"
          aria-expanded={expanded}
          aria-controls={`media-family-body-${familyId}`}
          onClick={onToggle}
        >
          <span className="media-summary-caret" aria-hidden="true">
            {expanded ? "▾" : "▸"}
          </span>
          <span className="media-summary-name" title={name}>{name}</span>
          <Chip tone={isVideo ? "info" : "mute"}>{isVideo ? "视频" : "图片"}</Chip>
          <span className="media-summary-adapter">{isToapis ? "ToAPIs" : "Ark"}</span>
          <span className="media-summary-provider" title={providerName}>{providerName || "未选供应商"}</span>
          <span className="media-summary-upstream" title={lead.upstreamModel}>
            {lead.upstreamModel || "未绑定上游模型"}
          </span>
          <span className="media-summary-tiers">
            {hasTiers
              ? tiers.map((tier) => {
                  const tierPrice = previewMicro(tier.billingUnit === "token" ? tier.tokenPriceCredits : tier.priceCredits);
                  const tierPriceValue = tier.billingUnit === "token" ? tier.tokenPriceCredits : tier.priceCredits;
                  const tierUnit = tier.billingUnit === "token"
                    ? "1M token"
                    : tier.billingUnit === "request" ? "req" : isVideo ? "秒" : "张";
                  const label = tier.resolutions[0] ?? "档位";
                  return (
                    <span
                      key={label}
                      className={"media-tier-pill" + (tierPrice > 0 ? "" : " is-missing")}
                      title={tierPrice > 0 ? `${label}：售价 ${tierPriceValue} credit/${tierUnit}` : `${label} 档尚未配置售价`}
                    >
                      {label}
                      <em>{tierPrice > 0 ? `${tierPriceValue}/${tierUnit}` : "缺价"}</em>
                    </span>
                  );
                })
              : <span className="media-tier-pill">按张计价</span>}
          </span>
          <span className="media-summary-state">
            {change !== "unchanged" ? (
              <span className="media-change-tag">{change === "new" ? "新增" : "已修改"}</span>
            ) : null}
            {conflicted ? (
              <Chip tone="stop">共享配置冲突</Chip>
            ) : missingCount > 0 ? (
              <Chip tone="stop">缺 {missingCount} 档价格</Chip>
            ) : (
              <Chip tone="ok">已配置</Chip>
            )}
          </span>
        </button>
        <div className="media-family-actions">
          <button
            className="btn btn-ghost btn-sm"
            type="button"
            disabled={busy || !lead.providerId || fetchingProvider === providerCacheKey}
            title={
              lead.providerId
                ? "从上游供应商拉取可用模型列表"
                : "请先选择上游供应商"
            }
            onClick={() => onFetchModels(lead.providerId)}
          >
            {fetchingProvider === providerCacheKey ? "获取中…" : "获取模型列表"}
          </button>
          <button
            className="btn btn-danger btn-sm"
            type="button"
            disabled={busy}
            onClick={() => onRemoveFamily(indexes)}
          >
            删除
          </button>
        </div>
      </div>

      <div
        className="media-family-body"
        id={`media-family-body-${familyId}`}
        hidden={!expanded}
      >
      <div className="model-card-head media-family-head">
        <input
          className="input media-family-name"
          aria-label={`模型 ${name} 展示名称`}
          placeholder="如 Seedance 2.0 mini"
          value={stripTierSuffix(lead)}
          onChange={(event) => onUpdateName(indexes, event.target.value)}
        />
        <span className="readout">
          {tiers.length} 个档位 · 共享配置只填一次，保存时按每档一条模型下发
        </span>
      </div>

      <div className="media-shared-grid">
        <label className="field">
          <span className="field-label">类型</span>
          <Select
            ariaLabel={`模型 ${name} 类型`}
            value={lead.kind}
            onChange={(value) =>
              onUpdateKind(indexes, value === "image" ? "image" : "video")
            }
            options={KIND_OPTIONS}
          />
        </label>
        <label className="field">
          <span className="field-label">适配器</span>
          <Select
            ariaLabel={`模型 ${name} 适配器`}
            value={lead.adapter ?? "ark"}
            disabled={busy}
            options={ADAPTER_OPTIONS}
            onChange={(adapter) => onUpdateFamily(indexes, {
              adapter,
              supportedAspectRatios: undefined,
              ...(adapter === "toapis" && lead.videoReference ? {
                videoReference: {
                  ...lead.videoReference, tokenCostCredits: "0", tokenPriceCredits: "0",
                },
              } : {}),
            })}
          />
        </label>
        <label className="field">
          <span className="field-label">上游供应商</span>
          <Select
            ariaLabel={`模型 ${name} 上游供应商`}
            value={lead.providerId}
            onChange={(value) => onUpdateFamily(indexes, { providerId: value })}
            options={providerOptions}
          />
        </label>
        <div className="field media-upstream-field">
          <label className="field-label" htmlFor={`media-upstream-${familyId}`}>
            上游模型（真实模型 ID）
          </label>
          <input
            id={`media-upstream-${familyId}`}
            className="input num"
            aria-label={`模型 ${name} 上游模型`}
            placeholder="供应商已开通的模型"
            value={lead.upstreamModel}
            onChange={(event) => onUpdateUpstream(indexes, event.target.value)}
          />
          {fetchedModels !== undefined ? (
            fetchedModels.length > 0 ? (
              <Select
                ariaLabel={`模型 ${name} 从供应商列表选择`}
                value={fetchedModels.includes(lead.upstreamModel) ? lead.upstreamModel : ""}
                onChange={(value) => onUpdateUpstream(indexes, value)}
                options={[
                  { value: "", label: `从 ${fetchedModels.length} 个模型中选择…` },
                  ...fetchedModels.map((modelID) => ({ value: modelID, label: modelID })),
                ]}
              />
            ) : (
              <span className="readout">供应商未返回模型，请手动填写</span>
            )
          ) : null}
        </div>
        <label className="field">
          <span className="field-label">价格版本</span>
          <input
            className="input num"
            aria-label={`模型 ${name} 价格版本`}
            placeholder="v1"
            value={lead.priceVersion}
            onChange={(event) =>
              onUpdateFamily(indexes, { priceVersion: event.target.value })
            }
          />
        </label>
        {isVideo ? (
          <div className="field">
            <span className="field-label">原生音频</span>
            {minimax || kling ? <span className="readout">由模型决定</span> : <div className="choice-row">
              <button
                className={"choice-chip" + (lead.generateAudio ? " selected" : "")}
                type="button"
                aria-pressed={lead.generateAudio}
                title="向供应商请求 generate_audio"
                onClick={() =>
                  onUpdateFamily(indexes, { generateAudio: !lead.generateAudio })
                }
              >
                生成音频
              </button>
            </div>}
          </div>
        ) : null}
        <div className="field">
          <span className="field-label">平台支持比例</span>
          <span className="readout" aria-label={`模型 ${name} 平台支持比例`}>
            {lead.supportedAspectRatios?.length
              ? lead.supportedAspectRatios.map((ratio) => ratio === "auto" ? "智能" : ratio).join(" / ")
              : "保存后由平台同步支持比例"}
          </span>
        </div>
      </div>

      {minimax ? <Notice k="首尾帧画幅">
        首尾帧按输入图片自适应，画布所选固定比例可能被上游忽略；制作单仍按固定画幅合成。
        {lead.generateAudio ? <p>当前配置开启了不支持的音频开关，请在 JSON 中修正后保存。</p> : null}
      </Notice> : null}

      {conflicts.length > 0 ? (
        <Notice k="共享配置冲突">
          <p>以下字段在档位间不同：{conflicts.join("、")}。当前卡片展示的是首档配置。</p>
          <button
            className="btn btn-ghost btn-sm"
            type="button"
            disabled={busy}
            onClick={() => onUpdateFamily(indexes, sharedConfiguration(lead))}
          >
            将当前卡片配置应用到所有档位
          </button>
        </Notice>
      ) : null}

      <div className="table-scroll">
        <table className="table media-tier-table">
          <thead>
            <tr>
              <th aria-label="预览档位" />
              <th>档位</th>
              <th>平台模型 ID</th>
              <th>计费单位</th>
              <th className="num">成本（credit/单位）</th>
              <th className="num">售价 / 预算价（credit/单位）</th>
              {tiers.some((tier) => tier.billingUnit === "token") ? <>
                <th className="num">Token 成本 / 1M</th>
                <th className="num">Token 售价 / 1M</th>
              </> : null}
              {tiers.some((tier) => tier.videoReference?.enabled) ? <>
                <th className="num">输入视频 Token 成本 / 1M</th>
                <th className="num">输入视频 Token 售价 / 1M</th>
              </> : null}
              <th className="num">毛利</th>
              <th>状态</th>
              <th aria-label="操作" />
            </tr>
          </thead>
          <tbody>
            {indexes.map((index, row) => {
              const tier = drafts[index];
              if (!tier) return null;
              const label = hasTiers ? (tier.resolutions[0] ?? `档位 ${row + 1}`) : "单张";
              const selected = (tier.resolutions[0] ?? "") === (preview.resolutions[0] ?? "");
              const billingUnit = tier.billingUnit ?? "second";
              const ratesPerToken = billingUnit === "token";
              // 毛利率始终按当前实际结算口径计算：Token 模型使用 Token 售价/成本，
              // 按秒和按次模型使用单位售价/成本。此前新增 Token 计费时误将其直接隐藏为“—”。
              const price = previewMicro(ratesPerToken ? tier.tokenPriceCredits : tier.priceCredits);
              const cost = previewMicro(ratesPerToken ? tier.tokenCostCredits : tier.costCredits);
              const videoReferenceEnabled = tier.videoReference?.enabled === true;
              const billingUnitName = billingUnit === "token" ? "token" : billingUnit === "request" ? "次" : isVideo ? "秒" : "张";
              return (
                <tr key={`${familyId}-${index}`} className={selected ? "is-preview" : ""}>
                  <td>
                    <input
                      type="radio"
                      className="media-row-radio"
                      name={`media-preview-${familyId}`}
                      aria-label={`预览 ${label} 档`}
                      checked={selected}
                      onChange={() => setPreviewTier(hasTiers ? label : "")}
                    />
                  </td>
                  <td>
                    {hasTiers ? (
                      <Select
                        ariaLabel={`模型 ${name} 第 ${row + 1} 档分辨率`}
                        value={tier.resolutions[0] ?? ""}
                        onChange={(value) =>
                          onUpdateTier(index, { resolutions: [value], name: tierName(name, { ...tier, resolutions: [value] }) })
                        }
                        options={resolutions.map((resolution) => ({
                          value: resolution,
                          label: resolution,
                        }))}
                      />
                    ) : (
                      <span className="readout">按张计价</span>
                    )}
                  </td>
                  <td>
                    <input
                      className="media-id-input num"
                      aria-label={`模型 ${name} ${label} 平台模型 ID`}
                      placeholder="默认同上游"
                      value={tier.id}
                      onChange={(event) => onUpdateTier(index, { id: event.target.value })}
                    />
                  </td>
                  <td>
                    <Select
                      ariaLabel={`模型 ${name} ${label} 计费单位`}
                      value={billingUnit}
                      disabled={busy}
                      onChange={(value) => onUpdateTier(index, { billingUnit: value as MediaBillingUnit })}
                      options={BILLING_UNIT_OPTIONS}
                    />
                  </td>
                  <td className="num">
                    <input
                      className="media-price-input num"
                      inputMode="decimal"
                      aria-label={`模型 ${name} ${label} 成本价`}
                      value={tier.costCredits}
                      onChange={(event) =>
                        onUpdateTier(index, { costCredits: event.target.value })
                      }
                    />
                  </td>
                  <td className="num">
                    <input
                      className="media-price-input num"
                      inputMode="decimal"
                      aria-label={billingUnit === "second"
                        ? `模型 ${name} ${label} 售价`
                        : `模型 ${name} ${label} 售价或预算参考 credit/${billingUnitName}`}
                      value={tier.priceCredits}
                      onChange={(event) =>
                        onUpdateTier(index, { priceCredits: event.target.value })
                      }
                    />
                  </td>
                  {tiers.some((item) => item.billingUnit === "token") ? <>
                    <td className="num">
                      {ratesPerToken ? <input className="media-price-input num" inputMode="decimal" aria-label={`模型 ${name} ${label} Token 成本 credit/1M token`} value={tier.tokenCostCredits} disabled={busy} onChange={(event) => onUpdateTier(index, { tokenCostCredits: event.target.value })} /> : <span className="readout">—</span>}
                    </td>
                    <td className="num">
                      {ratesPerToken ? <input className="media-price-input num" inputMode="decimal" aria-label={`模型 ${name} ${label} Token 售价 credit/1M token`} value={tier.tokenPriceCredits} disabled={busy} onChange={(event) => onUpdateTier(index, { tokenPriceCredits: event.target.value })} /> : <span className="readout">—</span>}
                    </td>
                  </> : null}
                  {tiers.some((item) => item.videoReference?.enabled) ? <>
                    <td className="num">
                      {videoReferenceEnabled && (ratesPerToken || !isToapis) ? <input className="media-price-input num" inputMode="decimal" aria-label={`模型 ${name} ${label} 输入视频 Token 成本 credit/1M token`} value={tier.videoReference?.tokenCostCredits ?? "0"} disabled={busy} onChange={(event) => onUpdateTier(index, { videoReference: { ...tier.videoReference!, tokenCostCredits: event.target.value } })} /> : <span className="readout">—</span>}
                    </td>
                    <td className="num">
                      {videoReferenceEnabled && (ratesPerToken || !isToapis) ? <input className="media-price-input num" inputMode="decimal" aria-label={`模型 ${name} ${label} 输入视频 Token 售价 credit/1M token`} value={tier.videoReference?.tokenPriceCredits ?? "0"} disabled={busy} onChange={(event) => onUpdateTier(index, { videoReference: { ...tier.videoReference!, tokenPriceCredits: event.target.value } })} /> : <span className="readout">—</span>}
                    </td>
                  </> : null}
                  <td className="num media-margin">
                    {price > 0
                      ? `${(((price - cost) / price) * 100).toFixed(1)}%`
                      : "—"}
                  </td>
                  <td>
                    {price > 0 || ratesPerToken && previewMicro(tier.tokenPriceCredits) > 0 ? (
                      <Chip tone="ok">已配置</Chip>
                    ) : (
                      <Chip tone="stop">缺价</Chip>
                    )}
                  </td>
                  <td>
                    <button
                      className="media-remove-tier"
                      type="button"
                      disabled={busy || (hasTiers && indexes.length <= 1)}
                      title={hasTiers && indexes.length <= 1 ? "至少保留一个档位" : "移除该档位"}
                      onClick={() => onRemoveTier(index)}
                    >
                      移除
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div style={{ margin: "18px 0" }}>
        <h3>路由池 · 每个档位独立配置</h3>
        <p className="readout">同一真实模型可绑定多个供应商，共用当前适配器、上游模型和能力。优先级小者先用，同优先级按权重轮转。</p>
        <p className="readout">媒体在生成阶段开始时选择供应商；任务受理后固定该供应商查询与结算，结果不明时进入核对，不跨供应商重复提交。</p>
        {indexes.map((index) => {
          const tier = drafts[index];
          const routes = tier.routes ?? [];
          const label = hasTiers ? (tier.resolutions[0] ?? "档位") : "单张";
          const routeLabel = `${name} ${label}`;
          const patchRoute = (row: number, patch: Partial<MediaRouteDraft>) => onUpdateTier(index, { routes: routes.map((route, i) => i === row ? { ...route, ...patch } : route) });
          return <div key={`routes-${index}`} style={{ marginBottom: 16 }}>
            <div className="form-row" style={{ justifyContent: "space-between", marginBottom: 8 }}>
              <strong>{label} · {tier.id || "未设平台模型 ID"}</strong>
              <button className="btn btn-ghost btn-sm" type="button" disabled={busy || routes.length >= 32} aria-label={`${routeLabel} 添加路由`} onClick={() => onUpdateTier(index, { routes: [...routes, { providerId: routes.length === 0 ? tier.providerId : "", priority: "100", weight: "1", enabled: true }] })}>添加供应商路由</button>
            </div>
            {routes.length === 0 ? <p className="readout">未配置路由池，使用卡片的单一供应商绑定。添加后由池内路由接管此档位。</p> : <>
              {!routes.some((route) => route.enabled) ? <Notice k="路由全部停用">此档位将拒绝新的生成请求，不会回退到单一供应商绑定。</Notice> : null}
              <div className="table-scroll"><table className="table" style={{ minWidth: 680, whiteSpace: "nowrap" }}><thead><tr><th>供应商</th><th>优先级 · 小者优先</th><th>权重 · 同级分流</th><th>启用</th><th>操作</th></tr></thead><tbody>
                {routes.map((route, row) => <tr key={row}>
                  <td><Select disabled={busy} ariaLabel={`${routeLabel} 路由 ${row + 1} 供应商`} value={route.providerId} onChange={(value) => patchRoute(row, { providerId: value })} options={[
                    { value: "", label: "选择供应商…" },
                    ...providerOptions.filter((option) => option.value && (option.value === route.providerId || !routes.some((existing) => existing.providerId === option.value))),
                  ]} /></td>
                  <td><input className="input num" style={{ width: 120 }} inputMode="numeric" disabled={busy} aria-label={`${routeLabel} 路由 ${row + 1} 优先级`} value={route.priority} onChange={(event) => patchRoute(row, { priority: event.target.value })} /></td>
                  <td><input className="input num" style={{ width: 100 }} inputMode="numeric" disabled={busy} aria-label={`${routeLabel} 路由 ${row + 1} 权重`} value={route.weight} onChange={(event) => patchRoute(row, { weight: event.target.value })} /></td>
                  <td><input type="checkbox" disabled={busy} aria-label={`${routeLabel} 路由 ${row + 1} 启用`} checked={route.enabled} onChange={(event) => patchRoute(row, { enabled: event.target.checked })} /></td>
                  <td><button className="btn btn-ghost btn-sm" type="button" disabled={busy} aria-label={`${routeLabel} 路由 ${row + 1} 移除`} onClick={() => onUpdateTier(index, { routes: routes.filter((_, i) => i !== row) })}>移除路由</button></td>
                </tr>)}
              </tbody></table></div>
              {routes.length === 1 ? <p className="readout">移除最后一条路由后恢复卡片的单一供应商绑定。修改后点击「保存配置」生效。</p> : null}
            </>}
          </div>;
        })}
      </div>

      {hasTiers && unusedResolutions.length > 0 ? (
        <div className="media-add-tier">
          <span className="readout">添加档位</span>
          {unusedResolutions.map((resolution) => (
            <button
              key={resolution}
              className="media-add-chip"
              type="button"
              disabled={busy}
              title={`添加 ${resolution} 档位；不需要的档位可在行内移除`}
              onClick={() => onAddTier(indexes, resolution)}
            >
              + {resolution}
            </button>
          ))}
        </div>
      ) : null}

      {isVideo && legacyTiers.length ? (
        <Notice k="旧枚举时长">
          <div>
            {legacyTiers.map((tier) => (
              <div key={tier.id}>
                {tier.resolutions.join(" / ")}：{tier.legacyDurations!.join("、")} 秒
              </div>
            ))}
            <p>当前仅支持上述档位。转换后需点击「保存配置」才会生效。</p>
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              disabled={busy}
              onClick={() =>
                onUpdateFamily(indexes, {
                  legacyDurations: undefined,
                  minDuration: lead.minDuration,
                  maxDuration: lead.maxDuration,
                })
              }
            >
              转换为连续区间 {lead.minDuration}–{lead.maxDuration} 秒
            </button>
          </div>
        </Notice>
      ) : null}

      {isVideo ? (
        <div className="media-trial">
          {legacyTiers.length ? (
            <Chip tone="hot">旧枚举时长试算</Chip>
          ) : (
            <div className="media-trial-config">
              <span className="media-trial-label">镜头时长</span>
              <input
                className="input num media-duration-input"
                inputMode="numeric"
                aria-label={`模型 ${name} 最小时长`}
                value={lead.minDuration}
                onChange={(event) =>
                  onUpdateFamily(indexes, { minDuration: event.target.value })
                }
              />
              <span className="media-range-dash">–</span>
              <input
                className="input num media-duration-input"
                inputMode="numeric"
                aria-label={`模型 ${name} 最大时长`}
                value={lead.maxDuration}
                onChange={(event) =>
                  onUpdateFamily(indexes, { maxDuration: event.target.value })
                }
              />
              <span className="media-trial-unit">秒</span>
            </div>
          )}
          <div className="media-trial-slider">
            {legacyDurations?.length ? (
              <Select
                ariaLabel={`模型 ${name} 试算时长`}
                value={String(duration)}
                onChange={(value) => setPreviewDuration(Number(value))}
                options={legacyDurations.map((value) => ({
                  value: String(value),
                  label: `${value} 秒`,
                }))}
              />
            ) : (
              <input
                className="media-duration-range"
                type="range"
                min={minDuration}
                max={maxDuration}
                step={1}
                aria-label={`模型 ${name} 试算时长`}
                value={duration}
                onChange={(event) => setPreviewDuration(Number(event.target.value))}
              />
            )}
            <span className="media-duration-value num">{duration}s</span>
          </div>
          <div className="media-trial-total">
            <span className="readout">
              {preview.resolutions[0] ?? "未设档位"} · {duration}s · {preview.billingUnit === "token"
                ? tokenSaleMicro > 0
                  ? `售价 ${preview.tokenPriceCredits} credit/1M token`
                  : "尚未配置 Token 售价"
                : preview.billingUnit === "request"
                  ? preview.priceCredits ? `固定售价 ${preview.priceCredits} credit/req` : "尚未配置按次售价"
                  : preview.priceCredits ? `预算售价 ${preview.priceCredits} credit/秒` : "尚未配置售价"}
            </span>
            <span className="media-total-line">
              <span className="media-total-num num">{total}</span>
              <span className="media-total-unit">{preview.billingUnit === "request" ? "credit / request" : preview.billingUnit === "token" ? "" : "credit / 镜头"}</span>
            </span>
          </div>
        </div>
      ) : null}

      {!isVideo && hasTiers ? <div className="media-trial">
        <span className="readout">{preview.resolutions[0]} · {preview.priceCredits ? `售价 ${preview.priceCredits} credit/张` : "尚未配置售价"}</span>
      </div> : null}

      {isVideo && minimax && !minimax.videoReference ? <Notice k="视频参考不可用">
        MiniMax H3 Max Turbo 仅支持文生视频和首尾帧，不支持参考媒体。
        {reference ? <button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => onUpdateFamily(indexes, { videoReference: undefined })}>移除不支持的视频参考配置</button> : null}
      </Notice> : isVideo ? (
        <div className="media-vr-panel">
          <label className="video-reference-toggle">
            <input
              type="checkbox"
              aria-label={`模型 ${name} 启用视频参考`}
              checked={reference !== null}
              disabled={busy}
              onChange={(event) =>
                onUpdateFamily(indexes, {
                  videoReference: event.target.checked
                    ? lead.videoReference ?? {
                        enabled: true,
                        maxDurationSeconds: "15",
                        maxImages: "8",
                        maxSegments: "1",
                        tokenCostCredits: isToapis ? "0" : "",
                        tokenPriceCredits: isToapis ? "0" : "",
                      }
                    : undefined,
                })
              }
            />
            <span>
              <strong>启用视频参考</strong>
              <span className="video-reference-note">
                {" "}整卡共享 · 原片段 + 替换图 · {isToapis
                  ? preview.billingUnit === "request"
                    ? "按每次请求固定价格结算"
                    : preview.billingUnit === "token"
                      ? "按实际 total_tokens 幂等结算"
                      : "按输出秒数和档位售价结算"
                  : "按实际 usage token 幂等实扣，不按秒预估"}
              </span>
            </span>
          </label>
          {reference ? (
            <>
              <div className="video-reference-grid">
                <label className="field">
                  <span className="field-label">片段上限（秒）</span>
                  <input
                    className="input num"
                    type="number"
                    inputMode="numeric"
                    min={2}
                    max={isToapis ? (kling ? 30 : 15) : 90}
                    step={1}
                    aria-label={`模型 ${name} 视频参考时长上限`}
                    value={reference.maxDurationSeconds}
                    disabled={busy}
                    onChange={(event) =>
                      onUpdateFamily(indexes, {
                        videoReference: {
                          ...reference,
                          maxDurationSeconds: event.target.value,
                        },
                      })
                    }
                  />
                </label>
                <label className="field">
                  <span className="field-label">替换图上限</span>
                  <input
                    className="input num"
                    type="number"
                    inputMode="numeric"
                    min={1}
                    max={kling ? 10 : 8}
                    step={1}
                    aria-label={`模型 ${name} 视频参考图上限`}
                    value={reference.maxImages}
                    disabled={busy}
                    onChange={(event) =>
                      onUpdateFamily(indexes, {
                        videoReference: { ...reference, maxImages: event.target.value },
                      })
                    }
                  />
                </label>
                <label className="field">
                  <span className="field-label">执行段上限</span>
                  <input
                    className="input num"
                    type="number"
                    inputMode="numeric"
                    min={1}
                    max={30}
                    step={1}
                    aria-label={`模型 ${name} 视频参考执行段上限`}
                    value={reference.maxSegments ?? "1"}
                    disabled={busy}
                    onChange={(event) =>
                      onUpdateFamily(indexes, {
                        videoReference: { ...reference, maxSegments: event.target.value },
                      })
                    }
                  />
                  <span className="video-reference-note">1 = 仅整段；多段只在事实镜头边界自动拆分</span>
                </label>
                {!isToapis ? <label className="field">
                  <span className="field-label">参考音频上限（秒）</span>
                  <input
                    className="input num"
                    type="number"
                    inputMode="numeric"
                    min={0}
                    max={Number(reference.maxDurationSeconds) || 90}
                    step={1}
                    aria-label={`模型 ${name} 参考音频时长上限`}
                    value={reference.maxAudioSeconds ?? "0"}
                    disabled={busy}
                    onChange={(event) =>
                      onUpdateFamily(indexes, {
                        videoReference: { ...reference, maxAudioSeconds: event.target.value },
                      })
                    }
                  />
                  <span className="video-reference-note">0 = 不发送原片音轨；Ark 2.0 单段上限 15 秒</span>
                </label> : null}
                {!isToapis ? <label className="field">
                  <span className="field-label">Token 成本（Credit/1M token）</span>
                  <input className="input num" inputMode="decimal" aria-label={`模型 ${name} 视频参考 token 成本`} value={reference.tokenCostCredits} disabled={busy} onChange={(event) => onUpdateTier(indexes[0], { videoReference: { ...reference, tokenCostCredits: event.target.value } })} />
                </label> : null}
                {!isToapis ? <label className="field">
                  <span className="field-label">Token 售价（Credit/1M token）</span>
                  <input className="input num" inputMode="decimal" aria-label={`模型 ${name} 视频参考 token 售价`} value={reference.tokenPriceCredits} disabled={busy} onChange={(event) => onUpdateTier(indexes[0], { videoReference: { ...reference, tokenPriceCredits: event.target.value } })} />
                </label> : null}
              </div>
              {isToapis ? (
                <div role="group" aria-label={`模型 ${name} 视频参考价格`}>
                  <p className="video-reference-note">
                    {preview.billingUnit === "request"
                      ? "无论镜头时长或是否输入视频，均按每次请求的固定价格结算。"
                      : preview.billingUnit === "token"
                        ? "输入视频的实际用量按本档输入视频 Token 费率结算；上方按秒价格仅作预算参考。"
                        : "视频参考按输出秒数计费，与上方档位共用成本价和售价。"}
                  </p>
                  <div className="table-scroll">
                    <table className="table media-tier-table">
                      <thead>
                        <tr>
                          <th>档位</th>
                          <th className="num">{preview.billingUnit === "request" ? "成本价（credit/req）" : preview.billingUnit === "token" ? "成本价（credit/1M token）" : "成本价（credit/秒）"}</th>
                          <th className="num">{preview.billingUnit === "request" ? "售价（credit/req）" : preview.billingUnit === "token" ? "输入视频参考价（credit/1M token）" : "售价（credit/秒）"}</th>
                          <th>状态</th>
                        </tr>
                      </thead>
                      <tbody>
                        {indexes.map((index) => {
                          const tier = drafts[index];
                          const label = tierLabel(tier);
                          const tokenReference = tier.billingUnit === "token";
                          const independentReferencePricing = tier.adapter === "toapis" && tier.upstreamModel.trim() === "kling-4.0";
                          const referenceCostValue = tokenReference
                            ? tier.videoReference?.tokenCostCredits ?? "0"
                            : independentReferencePricing
                              ? tier.videoReference?.costCredits ?? tier.costCredits
                              : tier.costCredits;
                          const referencePriceValue = tokenReference
                            ? tier.videoReference?.tokenPriceCredits ?? "0"
                            : independentReferencePricing
                              ? tier.videoReference?.priceCredits ?? tier.priceCredits
                              : tier.priceCredits;
                          return (
                            <tr key={`${familyId}-reference-${index}`}>
                              <td>{label}</td>
                              <td className="num">
                                <input
                                  className="media-price-input num"
                                  inputMode="decimal"
                                  aria-label={`模型 ${name} 视频参考 ${label} 成本价`}
                                  value={referenceCostValue}
                                  disabled={busy}
                                  onChange={(event) => onUpdateTier(index, tokenReference
                                    ? { videoReference: { ...tier.videoReference!, tokenCostCredits: event.target.value } }
                                    : independentReferencePricing
                                      ? { videoReference: { ...tier.videoReference!, costCredits: event.target.value } }
                                      : { costCredits: event.target.value })}
                                />
                              </td>
                              <td className="num">
                                <input
                                  className="media-price-input num"
                                  inputMode="decimal"
                                  aria-label={`模型 ${name} 视频参考 ${label} ${tier.billingUnit === "token" ? "预算参考价" : "售价"}`}
                                  placeholder="待配置"
                                  value={referencePriceValue}
                                  disabled={busy}
                                  onChange={(event) => onUpdateTier(index, tokenReference
                                    ? { videoReference: { ...tier.videoReference!, tokenPriceCredits: event.target.value } }
                                    : independentReferencePricing
                                      ? { videoReference: { ...tier.videoReference!, priceCredits: event.target.value } }
                                      : { priceCredits: event.target.value })}
                                />
                              </td>
                              <td>
                                {previewMicro(referencePriceValue) > 0
                                  ? <Chip tone="ok">已配置</Chip>
                                  : <Chip tone="stop">缺价</Chip>}
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                </div>
              ) : (
                <div className="media-vr-summary">
                  <span>
                    Token 毛利 <b className="media-margin">{referenceMargin}</b>
                  </span>
                  <span>
                    旧按秒口径（跟随档位，只读）：成本 <b>{preview.costCredits || "0"}</b> / 售价{" "}
                    <b>{preview.priceCredits || "0"}</b>
                  </span>
                </div>
              )}
            </>
          ) : null}
        </div>
      ) : null}
      </div>
    </article>
  );
}

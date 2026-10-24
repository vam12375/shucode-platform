import {
  MICRO_CREDITS_PER_CREDIT,
  parseCreditsToMicro,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useState } from "react";
import { api } from "../api.js";
import { toastErr, toastOk } from "../components/Feedback.js";
import { Select } from "../components/Select.js";
import { Chip, Notice } from "../components/StatusChip.js";
import { MediaReconciliation } from "./MediaReconciliation.js";
import { ReferencePricing } from "./ReferencePricing.js";
import { SourceParserStatus } from "./SourceParserStatus.js";

const KIND_OPTIONS = [
  { value: "video", label: "视频" },
  { value: "image", label: "图片" },
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

const SEEDREAM_PRESETS = [
  { id: "seedream-5.0-pro", name: "Seedream 5.0 Pro", upstreamModel: "doubao-seedream-5-0-pro-260628" },
  { id: "seedream-5.0-flash", name: "Seedream 5.0 Flash", upstreamModel: "doubao-seedream-5-0-flash-260915" },
  { id: "seedream-5.0-lite", name: "Seedream 5.0 Lite", upstreamModel: "doubao-seedream-5-0-lite-260128" },
  { id: "seedream-4.5", name: "Seedream 4.5", upstreamModel: "doubao-seedream-4-5-251128" },
  { id: "seedream-4.0", name: "Seedream 4.0", upstreamModel: "doubao-seedream-4-0-250828" },
] as const;

interface MediaModel {
  id: string;
  name: string;
  kind: "image" | "video";
  providerId: string;
  upstreamModel: string;
  adapter: string;
  resolutions: string[];
  minDuration?: number;
  maxDuration?: number;
  // 旧目录的枚举时长；仅显式转换后才改为连续区间。
  durations?: number[];
  costMicro: number;
  generateAudio: boolean;
  priceMicro: number;
  priceVersion: string;
  videoReference?: {
    maxDurationSeconds: number;
    maxImages: number;
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
  kind: "image" | "video";
  providerId: string;
  upstreamModel: string;
  resolutions: string[];
  minDuration: string;
  maxDuration: string;
  legacyDurations?: number[];
  costCredits: string;
  generateAudio: boolean;
  priceCredits: string;
  priceVersion: string;
  videoReference?: {
    enabled: boolean;
    maxDurationSeconds: string;
    maxImages: string;
    tokenCostCredits: string;
    tokenPriceCredits: string;
  };
}

export interface MediaFamilyGroup {
  id: string;
  indexes: number[];
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
  draft: Pick<MediaModelDraft, "kind" | "providerId" | "upstreamModel">,
): string {
  return `${draft.kind}\u0000${draft.providerId}\u0000${draft.upstreamModel.trim()}`;
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
  return draft.kind === "video" ? (draft.resolutions[0] ?? "") : "";
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
  const hasRange = Boolean(
    model.minDuration && model.maxDuration && model.maxDuration >= model.minDuration,
  );
  return {
    familyId: "",
    id: model.id,
    name: model.name,
    kind: model.kind,
    providerId: model.providerId,
    upstreamModel: model.upstreamModel,
    resolutions: [...(model.resolutions ?? [])],
    minDuration: String(bounds.min),
    maxDuration: String(bounds.max),
    // min/max 只是转换建议，不能把旧稀疏枚举在加载或修改价格时静默扩大为连续能力。
    ...(model.kind === "video" && !hasRange && model.durations?.length
      ? { legacyDurations: [...model.durations] }
      : {}),
    costCredits: priceInputValue(model.costMicro ?? 0),
    generateAudio: model.generateAudio === true,
    priceCredits: priceInputValue(model.priceMicro),
    priceVersion: model.priceVersion,
    ...(model.videoReference
      ? {
          videoReference: {
            enabled: true,
            maxDurationSeconds: String(model.videoReference.maxDurationSeconds),
            maxImages: String(model.videoReference.maxImages),
            tokenCostCredits: priceInputValue(model.videoReference.tokenCostMicroPerMillion ?? 0),
            tokenPriceCredits: priceInputValue(model.videoReference.tokenPriceMicroPerMillion ?? 0),
          },
        }
      : {}),
  };
}

export function toApiModel(draft: MediaModelDraft): MediaModel {
  const upstreamModel = draft.upstreamModel.trim();
  const costMicro = parseCreditsToMicro(draft.costCredits || "0");
  const priceMicro = parseCreditsToMicro(draft.priceCredits);
  const isVideo = draft.kind === "video";
  return {
    // 平台模型 ID 默认等于上游模型；手动填写后才作为独立别名保存。
    id: draft.id.trim() || upstreamModel,
    name: draft.name.trim(),
    kind: draft.kind,
    providerId: draft.providerId,
    upstreamModel,
    adapter: "ark",
    resolutions: isVideo ? draft.resolutions : [],
    ...(isVideo
      ? draft.legacyDurations !== undefined
        ? { durations: [...draft.legacyDurations] }
        : {
            minDuration: Number(draft.minDuration),
            maxDuration: Number(draft.maxDuration),
          }
      : {}),
    costMicro,
    generateAudio: isVideo ? draft.generateAudio : false,
    priceMicro,
    priceVersion: draft.priceVersion.trim(),
    ...(isVideo && draft.videoReference?.enabled
      ? {
          videoReference: {
            maxDurationSeconds: Number(draft.videoReference.maxDurationSeconds),
            maxImages: Number(draft.videoReference.maxImages),
            // 旧按秒口径跟随档位主价格，仅为兼容历史制作单保留。
            costMicro,
            priceMicro,
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
  return {
    kind: draft.kind,
    providerId: draft.providerId,
    upstreamModel: draft.upstreamModel,
    priceVersion: draft.priceVersion,
    generateAudio: draft.generateAudio,
    minDuration: draft.minDuration,
    maxDuration: draft.maxDuration,
    legacyDurations: draft.legacyDurations ? [...draft.legacyDurations] : undefined,
    videoReference: draft.videoReference ? { ...draft.videoReference } : undefined,
  };
}

function sharedFields(draft: MediaModelDraft): Record<string, unknown> {
  const vr = draft.videoReference?.enabled ? draft.videoReference : null;
  return {
    类型: draft.kind,
    上游供应商: draft.providerId,
    上游模型: draft.upstreamModel.trim(),
    价格版本: draft.priceVersion.trim(),
    原生音频: draft.kind === "video" ? draft.generateAudio : false,
    时长能力: draft.kind === "video"
      ? [draft.minDuration.trim(), draft.maxDuration.trim(), draft.legacyDurations ?? null]
      : null,
    视频参考开关: vr !== null,
    视频参考时长上限: vr?.maxDurationSeconds.trim() ?? null,
    视频参考图上限: vr?.maxImages.trim() ?? null,
    "视频参考 token 成本": vr?.tokenCostCredits.trim() ?? null,
    "视频参考 token 售价": vr?.tokenPriceCredits.trim() ?? null,
  };
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

export function validateDrafts(
  drafts: MediaModelDraft[],
  providers: ProviderOption[],
): string | null {
  const seen = new Set<string>();
  for (const [index, draft] of drafts.entries()) {
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
    if (!upstreamModel) return `「${label}」缺少上游模型 ID。`;
    if (!draft.priceVersion.trim()) return `「${label}」缺少价格版本。`;
    if (!/^\d+(\.\d{1,6})?$/.test(draft.costCredits.trim())) {
      return `「${label}」成本价格式不正确（credit，最多 6 位小数）。`;
    }
    const costMicro = parseCreditsToMicro(draft.costCredits);
    if (costMicro < 0 || costMicro > 100_000_000) {
      return `「${label}」成本价需在 0–100 credit 之间。`;
    }
    if (!/^\d+(\.\d{1,6})?$/.test(draft.priceCredits.trim())) {
      return `「${label}」单价格式不正确（credit，最多 6 位小数）。`;
    }
    const micro = parseCreditsToMicro(draft.priceCredits);
    if (micro <= 0 || micro > 100_000_000) {
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
      if (!/^\d+$/.test(draft.videoReference.maxDurationSeconds.trim())) {
        return `「${label}」视频参考片段上限必须为 1–90 秒的整数。`;
      }
      const maxDurationSeconds = Number(draft.videoReference.maxDurationSeconds);
      if (
        !Number.isSafeInteger(maxDurationSeconds) ||
        maxDurationSeconds < 1 ||
        maxDurationSeconds > 90
      ) {
        return `「${label}」视频参考片段上限必须为 1–90 秒的整数。`;
      }
      if (!/^\d+$/.test(draft.videoReference.maxImages.trim())) {
        return `「${label}」视频参考图上限必须为 1–8 张的整数。`;
      }
      const maxImages = Number(draft.videoReference.maxImages);
      if (!Number.isSafeInteger(maxImages) || maxImages < 1 || maxImages > 8) {
        return `「${label}」视频参考图上限必须为 1–8 张的整数。`;
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
  }
  // 旧配置或 JSON 导入会保留逐档差异；指出真实冲突，避免把合法时长误报为无法保存。
  for (const family of groupFamilies(drafts)) {
    const tiers = family.indexes.map((index) => drafts[index]);
    const conflicts = sharedConflicts(tiers);
    if (conflicts.length) {
      return `「${familyNameOf(tiers[0])}」共享配置不一致：${conflicts.join("、")}。请在卡片中统一后再保存。`;
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
    const priceMicro = Number(record.priceMicro);
    const costMicro = Number(record.costMicro);
    const legacyDurations = Array.isArray(record.durations)
      ? record.durations
          .map((item) => Number(item))
          .filter((item) => Number.isInteger(item) && item > 0)
      : [];
    const minDuration =
      Number.isInteger(Number(record.minDuration)) && Number(record.minDuration) > 0
        ? Number(record.minDuration)
        : legacyDurations.length
          ? Math.min(...legacyDurations)
          : DEFAULT_MIN_DURATION;
    const maxDuration =
      Number.isInteger(Number(record.maxDuration)) && Number(record.maxDuration) > 0
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
      const tokenPrice = Number(capability.tokenPriceMicroPerMillion ?? 0);
      const tokenCost = Number(capability.tokenCostMicroPerMillion ?? 0);
      if (
        typeof capability.maxDurationSeconds !== "number" ||
        typeof capability.maxImages !== "number" ||
        !Number.isSafeInteger(capability.maxDurationSeconds) ||
        !Number.isSafeInteger(capability.maxImages) ||
        !Number.isSafeInteger(tokenPrice) ||
        !Number.isSafeInteger(tokenCost)
      ) {
        throw new Error(`第 ${index + 1} 项的视频参考配置字段不完整。`);
      }
      videoReference = {
        enabled: true,
        maxDurationSeconds: String(capability.maxDurationSeconds),
        maxImages: String(capability.maxImages),
        tokenCostCredits: priceInputValue(tokenCost),
        tokenPriceCredits: priceInputValue(tokenPrice),
      };
    }
    return {
      familyId: "",
      id: typeof record.id === "string" ? record.id : "",
      name: typeof record.name === "string" ? record.name : "",
      kind,
      providerId: typeof record.providerId === "string" ? record.providerId : "",
      upstreamModel:
        typeof record.upstreamModel === "string" ? record.upstreamModel : "",
      resolutions: Array.isArray(record.resolutions)
        ? record.resolutions.filter(
            (item): item is string => typeof item === "string",
          )
        : [],
      minDuration: String(minDuration),
      maxDuration: String(maxDuration),
      ...(kind === "video" && !hasRange && legacyDurations.length ? { legacyDurations } : {}),
      costCredits:
        Number.isFinite(costMicro) && costMicro >= 0
          ? priceInputValue(costMicro)
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

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [models, providerData] = await Promise.all([
        api<MediaModel[]>("/media/models"),
        api<{ providers: ProviderOption[] }>("/providers"),
      ]);
      setDrafts(assignFamilyIds(models.map(toDraft)));
      setProviders(providerData.providers);
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
    void load();
  }, [load]);

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
        target.has(index) ? { ...draft, ...patch } : draft,
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
        // 图片模型没有档位：保留第一行，清空分辨率、音频与视频参考。
        return previous
          .map((draft, index) =>
            index === indexes[0]
              ? {
                  ...draft,
                  kind,
                  name: familyNameOf(draft),
                  resolutions: [],
                  generateAudio: false,
                  legacyDurations: undefined,
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
      const idBase = lead.id.trim().replace(/-(480p|720p|1080p|4k)$/i, "");
      const next: MediaModelDraft = {
        ...lead,
        id: idBase ? `${idBase}-${resolution}` : "",
        name: tierName(familyNameOf(lead), { ...lead, resolutions: [resolution] }),
        resolutions: [resolution],
        costCredits: "0",
        priceCredits: "",
        videoReference: lead.videoReference
          ? { ...lead.videoReference, enabled: true }
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

  const addModel = (): void => {
    const fallbackProvider =
      providers.find((provider) => provider.enabled)?.id ?? providers[0]?.id ?? "";
    setDrafts((previous) => [
      ...previous,
      {
        familyId: nextFamilyId(),
        id: "",
        name: "",
        kind: "video",
        providerId: fallbackProvider,
        upstreamModel: "",
        resolutions: ["720p"],
        minDuration: String(DEFAULT_MIN_DURATION),
        maxDuration: String(DEFAULT_MAX_DURATION),
        costCredits: "0",
        generateAudio: false,
        priceCredits: "",
        priceVersion: "v1",
      },
    ]);
    setDirty(true);
    setError("");
  };

  const preferredProvider = (): string => {
    const ark = providers.find((provider) =>
      provider.baseUrl?.includes("ark.cn-beijing.volces.com"),
    );
    return ark?.id ?? providers.find((provider) => provider.enabled)?.id ?? providers[0]?.id ?? "";
  };

  const addSeedanceModels = (): void => {
    const providerId = preferredProvider();
    if (!providerId) {
      toastErr("请先在「上游供应商」创建并配置 Ark API 前缀", "缺少供应商");
      return;
    }
    const existing = new Set(
      drafts.map((draft) => draft.id.trim() || draft.upstreamModel.trim()),
    );
    let added = 0;
    setDrafts((previous) => {
      const next = [...previous];
      for (const preset of SEEDANCE_PRESETS) {
        const existingFamily = next.find(
          (draft) =>
            draft.kind === "video" &&
            draft.providerId === providerId &&
            draft.upstreamModel.trim() === preset.upstreamModel,
        );
        const familyId = existingFamily?.familyId ?? nextFamilyId();
        for (const tier of preset.tiers) {
          if (existing.has(tier.id)) continue;
          next.push({
            familyId,
            id: tier.id,
            name: `${preset.name} ${tier.resolution}`,
            kind: "video",
            providerId,
            upstreamModel: preset.upstreamModel,
            resolutions: [tier.resolution],
            minDuration: String(DEFAULT_MIN_DURATION),
            maxDuration: String(DEFAULT_MAX_DURATION),
            costCredits: "0",
            generateAudio: true,
            priceCredits: "",
            priceVersion: "v1",
            // 补档必须继承卡片共享配置；默认值会与旧枚举、音频或价格版本冲突并阻塞保存。
            ...(existingFamily ? sharedConfiguration(existingFamily) : {}),
          });
          added += 1;
        }
      }
      return next;
    });
    if (added === 0) {
      toastErr("Seedance 2.0 条目都已在目录中，请直接编辑现有条目", "无需重复添加");
      return;
    }
    setDirty(true);
    setError("");
    toastOk(
      `已加入 ${added} 个档位条目，请确认供应商并填写成本和售价`,
      "已加入 Seedance 2.0 系列",
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
    setDrafts((previous) => [
      ...previous,
      ...additions.map((preset) => ({
        familyId: nextFamilyId(),
        id: preset.id,
        name: preset.name,
        kind: "image" as const,
        providerId,
        upstreamModel: preset.upstreamModel,
        resolutions: [],
        minDuration: String(DEFAULT_MIN_DURATION),
        maxDuration: String(DEFAULT_MAX_DURATION),
        costCredits: "0",
        generateAudio: false,
        priceCredits: "",
        priceVersion: "v1",
      })),
    ]);
    setDirty(true);
    setError("");
    toastOk(
      `已加入 ${additions.length} 个图片模型，请填写成本和售价`,
      "已加入 Seedream",
    );
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
    setFetchingProvider(providerId);
    try {
      // 媒体供应商的 baseUrl 是完整 API 前缀，先按 <prefix>/models 拉取。
      const data = await api<{ models: string[] }>(
        `/providers/${encodeURIComponent(providerId)}/models?base=prefix`,
      );
      setProviderModels((previous) => ({
        ...previous,
        [providerId]: data.models,
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
    const problem = validateDrafts(drafts, providers);
    if (problem) {
      setError(problem);
      toastErr(problem, "无法保存");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const saved = await api<MediaModel[]>("/media/models", {
        method: "PUT",
        body: JSON.stringify(drafts.map(toApiModel)),
      });
      setDrafts(assignFamilyIds(saved.map(toDraft)));
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

  return (
    <div>
      <header className="page-head">
        <div>
          <p className="eyebrow">接入 / 媒体创作</p>
          <h1>媒体模型</h1>
        </div>
        <span className="readout">
          {loading
            ? "加载中…"
            : `${families.length} 个上游模型 · ${videoCount} 视频档位 · ${imageCount} 图片`}
        </span>
      </header>

      <SourceParserStatus />
      <ReferencePricing />

      {error ? (
        <Notice tone="stop" k="操作未完成">
          {error}
        </Notice>
      ) : null}
      <Notice k="计费口径">
        一个上游模型一张卡：分辨率档位是卡内的价格行，镜头时长按区间（如 4–15 秒）配置，共享配置只填一次。保存后仍按每档一条模型下发，视频按镜头秒、图片按张计价，1 credit = ¥1。视频参考按 token 费率（Credit/1M token）结算，旧按秒价格跟随档位主价格保留兼容。
      </Notice>
      {!loading && providers.length === 0 ? (
        <Notice
          k="缺少上游供应商"
          action={{ href: "#/providers", label: "去配置" }}
        >
          媒体模型需要先绑定一个上游供应商，请先在「上游供应商」中创建并填入 Ark API 前缀。
        </Notice>
      ) : null}

      <section className="pane d1">
        <div className="pane-h">
          <h2>模型清单</h2>
          <div className="form-row">
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              onClick={addSeedanceModels}
              disabled={loading || busy || providers.length === 0}
              title={
                providers.length === 0
                  ? "请先创建上游供应商"
                  : "补齐 mini / fast / 基础版的 Seedance 2.0 档位"
              }
            >
              Seedance 2.0 系列
            </button>
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              onClick={addSeedreamModels}
              disabled={loading || busy || providers.length === 0}
              title={
                providers.length === 0
                  ? "请先创建上游供应商"
                  : "加入 Seedream 5.0 Pro / Flash / Lite / 4.5 / 4.0 图片模型"
              }
            >
              Seedream 图像
            </button>
            <button
              className="btn btn-ghost btn-sm"
              type="button"
              onClick={openJson}
              disabled={loading || busy}
            >
              {advancedOpen ? "收起 JSON" : "JSON 批量编辑"}
            </button>
            <button
              className="btn btn-sm"
              type="button"
              onClick={addModel}
              disabled={loading || busy}
            >
              新增模型
            </button>
          </div>
        </div>

        <div className="pane-b">
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
          ) : (
            <div className="media-family-list">
              {families.map((family) => (
                <MediaFamilyCard
                  key={family.id}
                  familyId={family.id}
                  indexes={family.indexes}
                  drafts={drafts}
                  providerOptions={providerOptions}
                  providerModels={providerModels}
                  fetchingProvider={fetchingProvider}
                  busy={busy}
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
              ))}
            </div>
          )}

          {!loading ? (
            <div className="form-row" style={{ marginTop: 16 }}>
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
                onClick={() => void load()}
              >
                放弃修改
              </button>
              <span className="readout">
                {dirty ? "有未保存修改" : "配置已同步"} · 价格版本只影响新制作单
              </span>
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

      <MediaReconciliation />
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
  const name = familyNameOf(lead);
  const tiers = indexes.map((index) => drafts[index]);
  const preview =
    tiers.find((draft) => (draft.kind === "video" ? draft.resolutions[0] ?? "" : "") === previewTier) ??
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
  const total = saleMicro > 0 ? trimNumber((saleMicro * duration) / MICRO_CREDITS_PER_CREDIT) : "—";
  const reference = lead.videoReference?.enabled ? lead.videoReference : null;
  const referenceCost = previewMicro(reference?.tokenCostCredits);
  const referencePrice = previewMicro(reference?.tokenPriceCredits);
  const referenceMargin =
    referencePrice > 0
      ? `${(((referencePrice - referenceCost) / referencePrice) * 100).toFixed(1)}%`
      : "—";
  const unusedResolutions = RESOLUTIONS.filter(
    (resolution) => !tiers.some((draft) => draft.resolutions.includes(resolution)),
  );
  const fetchedModels = lead.providerId ? providerModels[lead.providerId] : undefined;

  return (
    <article className="model-card media-family-card">
      <div className="model-card-head">
        <input
          className="input media-family-name"
          aria-label={`模型 ${name} 展示名称`}
          placeholder="如 Seedance 2.0 mini"
          value={stripTierSuffix(lead)}
          onChange={(event) => onUpdateName(indexes, event.target.value)}
        />
        <Chip tone={isVideo ? "info" : "mute"}>{isVideo ? "视频" : "图片"}</Chip>
        <span className="model-card-id" title={lead.upstreamModel}>
          {lead.upstreamModel || "未绑定上游模型"}
        </span>
        <div className="media-family-actions">
          <button
            className="btn btn-ghost btn-sm"
            type="button"
            disabled={busy || !lead.providerId || fetchingProvider === lead.providerId}
            title={
              lead.providerId
                ? "从上游供应商拉取可用模型列表"
                : "请先选择上游供应商"
            }
            onClick={() => onFetchModels(lead.providerId)}
          >
            {fetchingProvider === lead.providerId ? "获取中…" : "获取模型列表"}
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
            <div className="choice-row">
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
            </div>
          </div>
        ) : null}
      </div>

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
              <th className="num">成本（credit/秒）</th>
              <th className="num">售价（credit/秒）</th>
              <th className="num">毛利</th>
              <th>状态</th>
              <th aria-label="操作" />
            </tr>
          </thead>
          <tbody>
            {indexes.map((index, row) => {
              const tier = drafts[index];
              if (!tier) return null;
              const label = isVideo ? (tier.resolutions[0] ?? `档位 ${row + 1}`) : "单张";
              const selected = (tier.resolutions[0] ?? "") === (preview.resolutions[0] ?? "");
              const price = previewMicro(tier.priceCredits);
              return (
                <tr key={`${familyId}-${index}`} className={selected ? "is-preview" : ""}>
                  <td>
                    <input
                      type="radio"
                      className="media-row-radio"
                      name={`media-preview-${familyId}`}
                      aria-label={`预览 ${label} 档`}
                      checked={selected}
                      onChange={() => setPreviewTier(isVideo ? label : "")}
                    />
                  </td>
                  <td>
                    {isVideo ? (
                      <Select
                        ariaLabel={`模型 ${name} 第 ${row + 1} 档分辨率`}
                        value={tier.resolutions[0] ?? ""}
                        onChange={(value) =>
                          onUpdateTier(index, { resolutions: [value] })
                        }
                        options={RESOLUTIONS.map((resolution) => ({
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
                      aria-label={`模型 ${name} ${label} 售价`}
                      value={tier.priceCredits}
                      onChange={(event) =>
                        onUpdateTier(index, { priceCredits: event.target.value })
                      }
                    />
                  </td>
                  <td className="num media-margin">
                    {price > 0
                      ? `${(((price - previewMicro(tier.costCredits)) / price) * 100).toFixed(1)}%`
                      : "—"}
                  </td>
                  <td>
                    {price > 0 ? (
                      <Chip tone="ok">已配置</Chip>
                    ) : (
                      <Chip tone="stop">缺价</Chip>
                    )}
                  </td>
                  <td>
                    <button
                      className="media-remove-tier"
                      type="button"
                      disabled={busy || (isVideo && indexes.length <= 1)}
                      title={isVideo && indexes.length <= 1 ? "至少保留一个档位" : "移除该档位"}
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

      {isVideo && unusedResolutions.length > 0 ? (
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
              {preview.resolutions[0] ?? "未设档位"} · {duration}s ·{" "}
              {preview.priceCredits
                ? `售价 ${preview.priceCredits} credit/秒`
                : "尚未配置售价"}
            </span>
            <span className="media-total-line">
              <span className="media-total-num num">{total}</span>
              <span className="media-total-unit">credit / 镜头</span>
            </span>
          </div>
        </div>
      ) : null}

      {isVideo ? (
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
                        tokenCostCredits: "",
                        tokenPriceCredits: "",
                      }
                    : undefined,
                })
              }
            />
            <span>
              <strong>启用视频参考</strong>
              <span className="video-reference-note">
                {" "}整卡共享 · 原片段 + 替换图 · 按实际 usage token 幂等实扣，不按秒预估
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
                    max={90}
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
                    max={8}
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
                  <span className="field-label">Token 成本（Credit/1M token）</span>
                  <input
                    className="input num"
                    inputMode="decimal"
                    aria-label={`模型 ${name} 视频参考 token 成本`}
                    value={reference.tokenCostCredits}
                    disabled={busy}
                    onChange={(event) =>
                      onUpdateFamily(indexes, {
                        videoReference: {
                          ...reference,
                          tokenCostCredits: event.target.value,
                        },
                      })
                    }
                  />
                </label>
                <label className="field">
                  <span className="field-label">Token 售价（Credit/1M token）</span>
                  <input
                    className="input num"
                    inputMode="decimal"
                    aria-label={`模型 ${name} 视频参考 token 售价`}
                    value={reference.tokenPriceCredits}
                    disabled={busy}
                    onChange={(event) =>
                      onUpdateFamily(indexes, {
                        videoReference: {
                          ...reference,
                          tokenPriceCredits: event.target.value,
                        },
                      })
                    }
                  />
                </label>
              </div>
              <div className="media-vr-summary">
                <span>
                  Token 毛利 <b className="media-margin">{referenceMargin}</b>
                </span>
                <span>
                  旧按秒口径（跟随档位，只读）：成本{" "}
                  <b>{preview.costCredits || "0"}</b> / 售价{" "}
                  <b>{preview.priceCredits || "0"}</b>
                </span>
              </div>
            </>
          ) : null}
        </div>
      ) : null}
    </article>
  );
}

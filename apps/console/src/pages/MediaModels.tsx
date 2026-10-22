import {
  MICRO_CREDITS_PER_CREDIT,
  parseCreditsToMicro,
} from "@shucode-platform/contracts/credits";
import { useCallback, useEffect, useRef, useState } from "react";
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
const DURATIONS = [4, 5, 10, 15, 20, 30];

const SEEDANCE_DURATIONS = [4, 5, 10, 15];
// 2026-10-20 官方价（输入不含视频）各时长档的单秒最高成本，向上取整到 0.001 credit；
// 售价按约 30% 毛利率预填（售价 = 成本 ÷ 0.7）。平台每个媒体模型只有一个按秒售价，
// 不同分辨率成本不同，因此拆成独立目录条目。
const SEEDANCE_MODELS = [
  {
    id: "seedance-2.0-mini-480p",
    name: "Seedance 2.0 mini 480p",
    upstreamModel: "doubao-seedance-2-0-mini-260615",
    resolution: "480p",
    costCredits: "0.232",
    priceCredits: "0.332",
  },
  {
    id: "seedance-2.0-mini-720p",
    name: "Seedance 2.0 mini 720p",
    upstreamModel: "doubao-seedance-2-0-mini-260615",
    resolution: "720p",
    costCredits: "0.498",
    priceCredits: "0.711",
  },
  {
    id: "seedance-2.0-fast-480p",
    name: "Seedance 2.0 fast 480p",
    upstreamModel: "doubao-seedance-2-0-fast-260128",
    resolution: "480p",
    costCredits: "0.373",
    priceCredits: "0.533",
  },
  {
    id: "seedance-2.0-fast-720p",
    name: "Seedance 2.0 fast 720p",
    upstreamModel: "doubao-seedance-2-0-fast-260128",
    resolution: "720p",
    costCredits: "0.8",
    priceCredits: "1.143",
  },
  {
    id: "seedance-2.0-480p",
    name: "Seedance 2.0 480p",
    upstreamModel: "doubao-seedance-2-0-260128",
    resolution: "480p",
    costCredits: "0.4625",
    priceCredits: "0.661",
  },
  {
    id: "seedance-2.0-720p",
    name: "Seedance 2.0 720p",
    upstreamModel: "doubao-seedance-2-0-260128",
    resolution: "720p",
    costCredits: "0.994",
    priceCredits: "1.42",
  },
  {
    id: "seedance-2.0-1080p",
    name: "Seedance 2.0 1080p",
    upstreamModel: "doubao-seedance-2-0-260128",
    resolution: "1080p",
    costCredits: "2.479",
    priceCredits: "3.542",
  },
  {
    id: "seedance-2.0-4k",
    name: "Seedance 2.0 4k",
    upstreamModel: "doubao-seedance-2-0-260128",
    resolution: "4k",
    costCredits: "5.055",
    priceCredits: "7.222",
  },
] as const;

// Seedream 图片模型：平台固定单输入图、2K 输出、单张生成。
// pro 走「单图生成 >261 万像素」0.60 元/张；输入首张免费，其余模型按官方固定价。
const SEEDREAM_MODELS = [
  {
    id: "seedream-5.0-pro",
    name: "Seedream 5.0 Pro",
    upstreamModel: "doubao-seedream-5-0-pro-260628",
    costCredits: "0.6",
    priceCredits: "0.858",
  },
  {
    id: "seedream-5.0-flash",
    name: "Seedream 5.0 Flash",
    upstreamModel: "doubao-seedream-5-0-flash-260915",
    costCredits: "0.12",
    priceCredits: "0.172",
  },
  {
    id: "seedream-5.0-lite",
    name: "Seedream 5.0 Lite",
    upstreamModel: "doubao-seedream-5-0-lite-260128",
    costCredits: "0.22",
    priceCredits: "0.315",
  },
  {
    id: "seedream-4.5",
    name: "Seedream 4.5",
    upstreamModel: "doubao-seedream-4-5-251128",
    costCredits: "0.25",
    priceCredits: "0.358",
  },
  {
    id: "seedream-4.0",
    name: "Seedream 4.0",
    upstreamModel: "doubao-seedream-4-0-250828",
    costCredits: "0.2",
    priceCredits: "0.286",
  },
] as const;

interface MediaModel {
  id: string;
  name: string;
  kind: "image" | "video";
  providerId: string;
  upstreamModel: string;
  adapter: string;
  resolutions: string[];
  durations: number[];
  costMicro: number;
  generateAudio: boolean;
  priceMicro: number;
  priceVersion: string;
}

interface ProviderOption {
  id: string;
  name: string;
  baseUrl: string;
  enabled: boolean;
}

interface MediaModelDraft {
  id: string;
  name: string;
  kind: "image" | "video";
  providerId: string;
  upstreamModel: string;
  resolutions: string[];
  durations: number[];
  costCredits: string;
  generateAudio: boolean;
  priceCredits: string;
  priceVersion: string;
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

function toDraft(model: MediaModel): MediaModelDraft {
  return {
    id: model.id,
    name: model.name,
    kind: model.kind,
    providerId: model.providerId,
    upstreamModel: model.upstreamModel,
    resolutions: [...(model.resolutions ?? [])],
    durations: [...(model.durations ?? [])],
    costCredits: priceInputValue(model.costMicro ?? 0),
    generateAudio: model.generateAudio === true,
    priceCredits: priceInputValue(model.priceMicro),
    priceVersion: model.priceVersion,
  };
}

function toApiModel(draft: MediaModelDraft): MediaModel {
  const upstreamModel = draft.upstreamModel.trim();
  return {
    // 平台模型 ID 默认等于上游模型；手动填写后才作为独立别名保存。
    id: draft.id.trim() || upstreamModel,
    name: draft.name.trim(),
    kind: draft.kind,
    providerId: draft.providerId,
    upstreamModel,
    adapter: "ark",
    resolutions: draft.kind === "video" ? draft.resolutions : [],
    durations: draft.kind === "video" ? draft.durations : [],
    costMicro: parseCreditsToMicro(draft.costCredits),
    generateAudio: draft.kind === "video" ? draft.generateAudio : false,
    priceMicro: parseCreditsToMicro(draft.priceCredits),
    priceVersion: draft.priceVersion.trim(),
  };
}

function validateDrafts(
  drafts: MediaModelDraft[],
  providers: ProviderOption[],
): string | null {
  const seen = new Set<string>();
  for (const [index, draft] of drafts.entries()) {
    const upstreamModel = draft.upstreamModel.trim();
    const modelID = draft.id.trim() || upstreamModel;
    const label = draft.name.trim() || modelID || `第 ${index + 1} 个模型`;
    if (!modelID) return `「${label}」缺少模型 ID，请先填写上游模型。`;
    if (seen.has(modelID)) return `模型 ID「${modelID}」重复，请改为唯一值。`;
    seen.add(modelID);
    if (!draft.name.trim()) return `「${label}」缺少展示名称。`;
    if (!draft.providerId) return `「${label}」未选择上游供应商。`;
    if (!providers.some((provider) => provider.id === draft.providerId)) {
      return `「${label}」引用的上游供应商已不存在，请重新选择。`;
    }
    if (!draft.upstreamModel.trim()) return `「${label}」缺少上游模型 ID。`;
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
      if (draft.durations.length === 0) return `「${label}」至少选择一个镜头时长。`;
    }
  }
  return null;
}

function exampleJson(providerId: string): string {
  return JSON.stringify(
    [
      {
        id: "product-video",
        name: "商品视频",
        kind: "video",
        providerId,
        upstreamModel: "your-upstream-video-model",
        adapter: "ark",
        resolutions: ["720p", "1080p"],
        durations: [5, 10],
        costMicro: 300_000,
        generateAudio: true,
        priceMicro: 500_000,
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
        durations: [],
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
function draftsFromJson(raw: string): MediaModelDraft[] {
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
    return {
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
      durations: Array.isArray(record.durations)
        ? record.durations
            .map((item) => Number(item))
            .filter((item) => Number.isInteger(item))
        : [],
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
    };
  });
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
  const gridRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [models, providerData] = await Promise.all([
        api<MediaModel[]>("/media/models"),
        api<{ providers: ProviderOption[] }>("/providers"),
      ]);
      setDrafts(models.map(toDraft));
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

  const update = (index: number, patch: Partial<MediaModelDraft>): void => {
    setDrafts((previous) =>
      previous.map((draft, itemIndex) =>
        itemIndex === index ? { ...draft, ...patch } : draft,
      ),
    );
    setDirty(true);
    setError("");
  };

  const addModel = (): void => {
    const fallbackProvider =
      providers.find((provider) => provider.enabled)?.id ?? providers[0]?.id ?? "";
    setDrafts((previous) => [
      ...previous,
      {
        id: "",
        name: "",
        kind: "video",
        providerId: fallbackProvider,
        upstreamModel: "",
        resolutions: ["720p"],
        durations: [5],
        costCredits: "0",
        generateAudio: false,
        priceCredits: "",
        priceVersion: "v1",
      },
    ]);
    setDirty(true);
    setError("");
    requestAnimationFrame(() => {
      gridRef.current?.lastElementChild?.scrollIntoView({
        block: "nearest",
        behavior: "smooth",
      });
    });
  };

  const removeModel = (index: number): void => {
    setDrafts((previous) =>
      previous.filter((_, itemIndex) => itemIndex !== index),
    );
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
    const additions = SEEDANCE_MODELS.filter(
      (preset) => !existing.has(preset.id),
    );
    if (additions.length === 0) {
      toastErr("Seedance 2.0 条目都已在目录中，请直接编辑现有条目", "无需重复添加");
      return;
    }
    setDrafts((previous) => [
      ...previous,
      ...additions.map((preset) => ({
        id: preset.id,
        name: preset.name,
        kind: "video" as const,
        providerId,
        upstreamModel: preset.upstreamModel,
        resolutions: [preset.resolution],
        durations: [...SEEDANCE_DURATIONS],
        costCredits: preset.costCredits,
        generateAudio: true,
        priceCredits: preset.priceCredits,
        priceVersion: "ark-2026-10-20-margin30",
      })),
    ]);
    setDirty(true);
    setError("");
    toastOk(
      `已加入 ${additions.length} 个条目，请确认供应商；成本按官方价、售价按约 30% 毛利预填`,
      "已加入 Seedance 2.0 系列",
    );
    requestAnimationFrame(() => {
      gridRef.current?.lastElementChild?.scrollIntoView({
        block: "nearest",
        behavior: "smooth",
      });
    });
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
    const additions = SEEDREAM_MODELS.filter(
      (preset) => !existing.has(preset.id),
    );
    if (additions.length === 0) {
      toastErr("Seedream 条目都已在目录中，请直接编辑现有条目", "无需重复添加");
      return;
    }
    setDrafts((previous) => [
      ...previous,
      ...additions.map((preset) => ({
        id: preset.id,
        name: preset.name,
        kind: "image" as const,
        providerId,
        upstreamModel: preset.upstreamModel,
        resolutions: [],
        durations: [],
        costCredits: preset.costCredits,
        generateAudio: false,
        priceCredits: preset.priceCredits,
        priceVersion: "ark-2026-10-20-margin30",
      })),
    ]);
    setDirty(true);
    setError("");
    toastOk(
      `已加入 ${additions.length} 个图片模型，成本按官方价、售价按约 30% 毛利预填`,
      "已加入 Seedream",
    );
    requestAnimationFrame(() => {
      gridRef.current?.lastElementChild?.scrollIntoView({
        block: "nearest",
        behavior: "smooth",
      });
    });
  };

  const toggleResolution = (index: number, resolution: string): void => {
    const draft = drafts[index];
    if (!draft) return;
    const selected = draft.resolutions.includes(resolution);
    update(index, {
      resolutions: selected
        ? draft.resolutions.filter((item) => item !== resolution)
        : [...draft.resolutions, resolution].sort(),
    });
  };

  const toggleDuration = (index: number, duration: number): void => {
    const draft = drafts[index];
    if (!draft) return;
    const selected = draft.durations.includes(duration);
    update(index, {
      durations: selected
        ? draft.durations.filter((item) => item !== duration)
        : [...draft.durations, duration].sort((a, b) => a - b),
    });
  };

  // 平台模型 ID 默认跟随上游模型；用户手动改过 ID 后不再覆盖。
  const updateUpstreamModel = (index: number, value: string): void => {
    const draft = drafts[index];
    if (!draft) return;
    const currentID = draft.id.trim();
    const mirrorID = currentID === "" || currentID === draft.upstreamModel.trim();
    update(index, {
      upstreamModel: value,
      ...(mirrorID ? { id: value } : {}),
    });
  };

  const fetchModels = async (index: number, providerId: string): Promise<void> => {
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
      const current = drafts[index]?.upstreamModel.trim() ?? "";
      if (data.models.length === 1 && !current) {
        updateUpstreamModel(index, data.models[0]);
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
      setDrafts(saved.map(toDraft));
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
      const imported = draftsFromJson(jsonText);
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
          {loading ? "加载中…" : `${videoCount} 视频 · ${imageCount} 图片`}
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
        视频按镜头秒、图片按张计价；成本和售价均以 credit 计量，1 credit = ¥1 = 1,000,000 micro-credit。适配器固定 Ark，复用「上游供应商」的加密密钥与 Base URL。
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
                  : "补齐 mini / fast / 基础版的 Seedance 2.0 条目"
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
                媒体模型复用上游供应商的密钥，不会新建凭据。
              </p>
              <button className="btn" type="button" onClick={addModel}>
                新增第一个模型
              </button>
            </div>
          ) : (
            <div className="model-grid media-model-grid" ref={gridRef}>
              {drafts.map((draft, index) => (
                <article className="model-card" key={index}>
                  <div className="model-card-head">
                    <span className="model-card-id" title={draft.id}>
                      {draft.id || "未命名模型"}
                    </span>
                    <Chip tone={draft.kind === "video" ? "info" : "mute"}>
                      {draft.kind === "video" ? "视频" : "图片"}
                    </Chip>
                    <button
                      className="btn btn-ghost btn-sm"
                      type="button"
                      style={{ marginLeft: "auto" }}
                      disabled={
                        busy ||
                        !draft.providerId ||
                        fetchingProvider === draft.providerId
                      }
                      title={
                        draft.providerId
                          ? "从上游供应商拉取可用模型列表"
                          : "请先选择上游供应商"
                      }
                      onClick={() => void fetchModels(index, draft.providerId)}
                    >
                      {fetchingProvider === draft.providerId
                        ? "获取中…"
                        : "获取模型列表"}
                    </button>
                    <button
                      className="btn btn-danger btn-sm"
                      type="button"
                      disabled={busy}
                      onClick={() => removeModel(index)}
                    >
                      删除
                    </button>
                  </div>

                  <div className="model-card-row">
                    <label className="field" style={{ flex: "1.1 1 170px" }}>
                      <span className="field-label">平台模型 ID（默认同上游）</span>
                      <input
                        className="input num"
                        aria-label={`模型 ${index + 1} ID`}
                        placeholder="如 product-video"
                        value={draft.id}
                        onChange={(event) =>
                          update(index, { id: event.target.value })
                        }
                      />
                    </label>
                    <label className="field" style={{ flex: "1.2 1 180px" }}>
                      <span className="field-label">展示名称</span>
                      <input
                        className="input"
                        aria-label={`模型 ${index + 1} 展示名称`}
                        placeholder="如 商品视频"
                        value={draft.name}
                        onChange={(event) =>
                          update(index, { name: event.target.value })
                        }
                      />
                    </label>
                    <label className="field" style={{ flex: "0.8 1 110px" }}>
                      <span className="field-label">类型</span>
                      <Select
                        ariaLabel={`模型 ${index + 1} 类型`}
                        value={draft.kind}
                        onChange={(value) =>
                          update(index, {
                            kind: value === "image" ? "image" : "video",
                          })
                        }
                        options={KIND_OPTIONS}
                      />
                    </label>
                  </div>

                  <div className="model-card-row">
                    <label className="field" style={{ flex: "1.2 1 170px" }}>
                      <span className="field-label">上游供应商</span>
                      <Select
                        ariaLabel={`模型 ${index + 1} 上游供应商`}
                        value={draft.providerId}
                        onChange={(value) =>
                          update(index, { providerId: value })
                        }
                        options={providerOptions}
                      />
                    </label>
                    <div className="field" style={{ flex: "1.3 1 190px" }}>
                      <label className="field-label" htmlFor={`media-upstream-${index}`}>
                        上游模型（真实模型 ID）
                      </label>
                      <input
                        id={`media-upstream-${index}`}
                        className="input num"
                        aria-label={`模型 ${index + 1} 上游模型`}
                        placeholder="供应商已开通的模型"
                        value={draft.upstreamModel}
                        onChange={(event) =>
                          updateUpstreamModel(index, event.target.value)
                        }
                      />
                      {draft.providerId &&
                      providerModels[draft.providerId] !== undefined ? (
                        providerModels[draft.providerId].length > 0 ? (
                          <Select
                            ariaLabel={`模型 ${index + 1} 从供应商列表选择`}
                            value={
                              providerModels[draft.providerId].includes(
                                draft.upstreamModel,
                              )
                                ? draft.upstreamModel
                                : ""
                            }
                            onChange={(value) =>
                              updateUpstreamModel(index, value)
                            }
                            options={[
                              {
                                value: "",
                                label: `从 ${providerModels[draft.providerId].length} 个模型中选择…`,
                              },
                              ...providerModels[draft.providerId].map(
                                (modelID) => ({ value: modelID, label: modelID }),
                              ),
                            ]}
                          />
                        ) : (
                          <span className="readout">供应商未返回模型，请手动填写</span>
                        )
                      ) : null}
                    </div>
                    <label className="field" style={{ flex: "0.8 1 110px" }}>
                      <span className="field-label">价格版本</span>
                      <input
                        className="input num"
                        aria-label={`模型 ${index + 1} 价格版本`}
                        placeholder="v1"
                        value={draft.priceVersion}
                        onChange={(event) =>
                          update(index, { priceVersion: event.target.value })
                        }
                      />
                    </label>
                  </div>

                  <div className="model-card-row">
                    <label className="field" style={{ flex: "0.9 1 150px" }}>
                      <span className="field-label">
                        {draft.kind === "video"
                          ? "成本价（credit / 镜头秒）"
                          : "成本价（credit / 张）"}
                      </span>
                      <input
                        className="input num"
                        inputMode="decimal"
                        aria-label={`模型 ${index + 1} 成本价`}
                        placeholder="如 0.3"
                        value={draft.costCredits}
                        onChange={(event) =>
                          update(index, { costCredits: event.target.value })
                        }
                      />
                      <span className="readout">客户端展示用</span>
                    </label>
                    <label className="field" style={{ flex: "0.9 1 150px" }}>
                      <span className="field-label">
                        {draft.kind === "video"
                          ? "售价（credit / 镜头秒）"
                          : "售价（credit / 张）"}
                      </span>
                      <input
                        className="input num"
                        inputMode="decimal"
                        aria-label={`模型 ${index + 1} 售价`}
                        placeholder="如 0.5"
                        value={draft.priceCredits}
                        onChange={(event) =>
                          update(index, { priceCredits: event.target.value })
                        }
                      />
                      <span className="readout">1 credit = ¥1</span>
                    </label>
                    {draft.kind === "video" ? (
                      <>
                        <div className="field" style={{ flex: "1 1 150px" }}>
                          <span className="field-label">分辨率</span>
                          <div className="choice-row">
                            {RESOLUTIONS.map((resolution) => (
                              <button
                                key={resolution}
                                className={
                                  "choice-chip" +
                                  (draft.resolutions.includes(resolution)
                                    ? " selected"
                                    : "")
                                }
                                type="button"
                                aria-pressed={draft.resolutions.includes(
                                  resolution,
                                )}
                                onClick={() =>
                                  toggleResolution(index, resolution)
                                }
                              >
                                {resolution}
                              </button>
                            ))}
                          </div>
                        </div>
                        <div className="field" style={{ flex: "1.4 1 220px" }}>
                          <span className="field-label">镜头时长（秒）</span>
                          <div className="choice-row">
                            {DURATIONS.map((duration) => (
                              <button
                                key={duration}
                                className={
                                  "choice-chip" +
                                  (draft.durations.includes(duration)
                                    ? " selected"
                                    : "")
                                }
                                type="button"
                                aria-pressed={draft.durations.includes(
                                  duration,
                                )}
                                onClick={() => toggleDuration(index, duration)}
                              >
                                {duration}s
                              </button>
                            ))}
                          </div>
                        </div>
                        <div className="field" style={{ flex: "0.8 1 130px" }}>
                          <span className="field-label">原生音频</span>
                          <div className="choice-row">
                            <button
                              className={
                                "choice-chip" +
                                (draft.generateAudio ? " selected" : "")
                              }
                              type="button"
                              aria-pressed={draft.generateAudio}
                              title="向供应商请求 generate_audio"
                              onClick={() =>
                                update(index, {
                                  generateAudio: !draft.generateAudio,
                                })
                              }
                            >
                              生成音频
                            </button>
                          </div>
                        </div>
                      </>
                    ) : null}
                  </div>
                </article>
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

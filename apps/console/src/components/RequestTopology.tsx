import { useEffect, useId, useMemo, useRef, useState, type FocusEvent, type MouseEvent } from "react";
import { entityColor } from "./polar-colors.js";
import { PolarTooltip, type PolarTooltipData } from "./PolarTooltip.js";
import "./polar-signatures.css";

export interface TopologyRoute {
  providerId: string;
  enabled: boolean;
  priority: number;
  weight: number;
  providerEnabled?: boolean;
}

export interface TopologyModel {
  modelId: string;
  displayName: string;
  upstreamProviderId: string | null;
  enabled: boolean;
  routes?: TopologyRoute[] | null;
}

export interface TopologyProvider {
  id: string;
  name: string;
  enabled: boolean;
}

export interface TopologyProviderReading {
  providerId: string;
  requests: number;
  errors: number;
  p95LatencyMs?: number | null;
}

function seededRandom(seed: number): () => number {
  return () => {
    seed |= 0; seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() => window.matchMedia("(prefers-reduced-motion: reduce)").matches);
  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const change = () => setReduced(media.matches);
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, []);
  return reduced;
}

const count = (value: number) => value.toLocaleString("en-US", { maximumFractionDigits: 0 });

export function RequestTopology({ models, providers, requests, activeDevices, modelRequests = [], windowRequests }: {
  models: TopologyModel[];
  providers: TopologyProvider[];
  requests: TopologyProviderReading[];
  activeDevices: number;
  modelRequests?: { model: string; requests: number }[];
  windowRequests?: number;
}) {
  const container = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(880);
  const [tip, setTip] = useState<PolarTooltipData | null>(null);
  const reduced = useReducedMotion();
  const id = useId().replace(/[^a-zA-Z0-9_-]/gu, "");
  useEffect(() => {
    const element = container.current;
    if (!element) return;
    const resize = () => setWidth(Math.max(520, element.clientWidth));
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const visibleModels = models.filter((model) => model.enabled).slice(0, 6);
  const modelTotal = modelRequests.reduce((sum, row) => sum + Math.max(0, row.requests), 0);
  const gatewayRequests = windowRequests ?? requests.reduce((sum, row) => sum + row.requests, 0);
  const providerTotal = requests.reduce((sum, row) => sum + Math.max(0, row.requests), 0);
  const routeRows = visibleModels.flatMap((model, mi) => {
    if (model.routes === null) return [];
    if (model.routes?.length) {
      const active = model.routes.filter((route) => route.enabled);
      const weights = active.reduce((sum, route) => sum + route.weight, 0);
      return active.map((route) => ({ model: mi, providerId: route.providerId, share: weights > 0 ? route.weight / weights : 0, priority: route.priority }));
    }
    return [{ model: mi, providerId: model.upstreamProviderId || "default", share: 1, priority: 0 }];
  });
  const providerIds = new Set(routeRows.map((route) => route.providerId));
  const visibleProviders = providers.filter((provider) => provider.enabled || providerIds.has(provider.id)).slice(0, 6);
  if (providerIds.has("default")) visibleProviders.push({ id: "default", name: "默认上游", enabled: true });
  const height = 360;
  const cx = [width * .07, width * .3, width * .58, width * .86];
  const top = 40, bottom = height - 40, gy = (top + bottom) / 2;
  const modelY = (index: number) => top + (bottom - top) * (index + .5) / Math.max(visibleModels.length, 1);
  const providerY = (index: number) => top + (bottom - top) * (index + .5) / Math.max(visibleProviders.length, 1);
  const modelShare = (model: TopologyModel) => {
    const reading = modelRequests.find((row) => row.model === model.modelId);
    return modelTotal > 0 ? (reading?.requests ?? 0) / modelTotal : 0;
  };
  const providerState = (provider: TopologyProvider) => {
    const reading = requests.find((row) => row.providerId === (provider.id === "default" ? "" : provider.id));
    const rate = reading && reading.requests > 0 ? reading.errors / reading.requests : null;
    return !provider.enabled || (rate !== null && rate >= .03) ? "crit" : (reading?.p95LatencyMs ?? 0) > 6000 ? "warn" : "ok";
  };
  const links: { d: string; width: number; color: string; particles: number; key: string }[] = [];
  const addLink = (x0: number, y0: number, x1: number, y1: number, stroke: number, color: string, particles: number, key: string) => {
    const mx = (x0 + x1) / 2;
    links.push({ key, d: `M${x0},${y0} C${mx},${y0} ${mx},${y1} ${x1},${y1}`, width: stroke, color, particles });
  };
  for (let index = 0; index < 9; index++) addLink(cx[0] + 18, gy - 90 + index * 22, cx[1] - 34, gy - 16 + index * 4, 1.4, "var(--accent)", 1, `device-${index}`);
  visibleModels.forEach((model, index) => {
    const share = modelShare(model);
    addLink(cx[1] + 34, gy - 12 + index * 4.5, cx[2] - 14, modelY(index), 2 + share * 26, entityColor(model.modelId), Math.round(1 + share * 9), `model-${model.modelId}`);
  });
  routeRows.forEach((route, index) => {
    const model = visibleModels[route.model];
    const pi = visibleProviders.findIndex((provider) => provider.id === route.providerId);
    if (pi < 0) return;
    const provider = visibleProviders[pi], critical = providerState(provider) === "crit";
    const share = modelShare(model) * route.share;
    addLink(cx[2] + 14, modelY(route.model), cx[3] - 16, providerY(pi), 1 + share * 22, critical ? "var(--crit)" : entityColor(model.modelId), critical ? 1 : Math.round(1 + share * 6), `route-${index}`);
  });
  const dots = useMemo(() => {
    const random = seededRandom(5);
    return Array.from({ length: 64 }, () => {
      const angle = random() * Math.PI * 2, radius = Math.sqrt(random());
      return { x: Math.cos(angle) * radius * 30, y: Math.sin(angle) * radius * 104, radius: 1.2 + random() * 1.4, opacity: .25 + random() * .6 };
    });
  }, []);
  const pointer = (event: MouseEvent<SVGGElement>, title: string, rows: PolarTooltipData["rows"]) => setTip({ x: event.clientX, y: event.clientY, title, rows });
  const focus = (event: FocusEvent<SVGGElement>, title: string, rows: PolarTooltipData["rows"]) => {
    const bounds = event.currentTarget.getBoundingClientRect();
    setTip({ x: bounds.right, y: bounds.top, title, rows });
  };
  return <div className="request-topology" ref={container}>
    <svg className="request-topology-svg" width={width} height={height} role="img" aria-label="实时请求拓扑：设备经网关、模型路由到上游供应商，流动光点显示请求方向">
      {["DEVICES", "GATEWAY", "MODELS", "UPSTREAM"].map((label, index) => <text key={label} className="topology-column" x={cx[index]} y={16} textAnchor="middle">{label}</text>)}
      {links.map((link, index) => <path key={link.key} id={`${id}-path-${index}`} className="topology-link drawn" pathLength={1} d={link.d} stroke={link.color} strokeOpacity={.32} strokeWidth={link.width} style={{ animationDelay: `${(index + 1) * 40}ms` }} />)}
      {!reduced ? links.flatMap((link, index) => Array.from({ length: link.particles }, (_, particle) => {
        const duration = 2.4 + ((index + 1) % 5) * .35;
        return <circle className="topology-particle" key={`${link.key}-${particle}`} r={Math.min(2.6, 1.2 + link.width / 8)} fill={link.color} opacity={.95}>
          <animateMotion dur={`${duration.toFixed(2)}s`} begin={`${(1.2 + particle * duration / link.particles).toFixed(2)}s`} repeatCount="indefinite"><mpath href={`#${id}-path-${index}`} /></animateMotion>
        </circle>;
      })) : null}
      <g className="topology-node" tabIndex={0} onMouseMove={(event) => pointer(event, "客户端设备", [{ color: "var(--accent)", label: "活跃设备", value: `${count(activeDevices)} 台` }, { label: "统计窗口", value: "近 1 小时" }])} onMouseLeave={() => setTip(null)} onFocus={(event) => focus(event, "客户端设备", [{ color: "var(--accent)", label: "活跃设备", value: `${count(activeDevices)} 台` }])} onBlur={() => setTip(null)}>
        {dots.map((dot, index) => <circle key={index} cx={cx[0] + dot.x} cy={gy + dot.y} r={dot.radius} fill="var(--accent)" opacity={dot.opacity} />)}
        <rect x={cx[0] - 36} y={gy - 112} width={72} height={224} fill="transparent" />
      </g>
      <text className="topology-label" x={cx[0]} y={height - 14} textAnchor="middle">{count(activeDevices)} 台设备</text>
      <g className="topology-node" tabIndex={0} onMouseMove={(event) => pointer(event, "网关 gateway", [{ label: "近 1 小时请求", value: count(gatewayRequests) }, { label: "光点", value: "显示请求流向" }])} onMouseLeave={() => setTip(null)} onFocus={(event) => focus(event, "网关 gateway", [{ label: "近 1 小时请求", value: count(gatewayRequests) }])} onBlur={() => setTip(null)}>
        <circle className="topology-gateway-ring" cx={cx[1]} cy={gy} r={40} fill="none" stroke="var(--accent)" strokeOpacity={.5} />
        <circle cx={cx[1]} cy={gy} r={34} fill="var(--surface)" stroke="var(--accent)" strokeWidth={1.5} />
        <circle cx={cx[1]} cy={gy} r={26} fill="color-mix(in srgb, var(--accent) 8%, transparent)" />
        <text x={cx[1]} y={gy - 2} textAnchor="middle" className="topology-label">网关</text>
        <text x={cx[1]} y={gy + 13} textAnchor="middle" className="topology-caption">{count(gatewayRequests)}/h</text>
      </g>
      {visibleModels.map((model, index) => {
        const color = entityColor(model.modelId), share = modelShare(model);
        const routeNames = routeRows.filter((route) => route.model === index).map((route) => visibleProviders.find((provider) => provider.id === route.providerId)?.name ?? "不可用上游");
        const rows = [{ color, label: "流量占比", value: modelTotal ? `${(share * 100).toFixed(0)}%` : "暂无请求" }, { label: "首选上游", value: routeNames[0] || "未启用路由" }];
        const label = model.modelId.length <= 24 ? model.modelId : model.displayName;
        return <g className="topology-node" tabIndex={0} key={model.modelId} onMouseMove={(event) => pointer(event, model.displayName || model.modelId, rows)} onMouseLeave={() => setTip(null)} onFocus={(event) => focus(event, model.displayName || model.modelId, rows)} onBlur={() => setTip(null)}>
          <circle className="topology-core" cx={cx[2]} cy={modelY(index)} r={7} fill="var(--surface)" stroke={color} strokeWidth={2} /><circle cx={cx[2]} cy={modelY(index)} r={2.5} fill={color} />
          <text x={cx[2] - 14} y={modelY(index) + 4} textAnchor="end" className="topology-label topology-model-label">{label.length > 27 ? `${label.slice(0, 26)}…` : label}</text>
        </g>;
      })}
      {visibleProviders.map((provider, index) => {
        const reading = requests.find((row) => row.providerId === (provider.id === "default" ? "" : provider.id));
        const state = providerState(provider), color = `var(--${state})`;
        const radius = 6 + (providerTotal > 0 ? (reading?.requests ?? 0) / providerTotal : 0) * 26;
        const errorRate = reading && reading.requests > 0 ? reading.errors / reading.requests : null;
        const latency = reading?.p95LatencyMs;
        const rows = [{ color, label: "状态", value: !provider.enabled ? "已停用" : state === "crit" ? "错误率偏高" : state === "warn" ? "延迟偏高" : "已启用" }, { label: "非错误请求占比", value: errorRate === null ? "暂无请求" : `${((1 - errorRate) * 100).toFixed(2)}%` }, { label: "P95", value: latency === null || latency === undefined ? "—" : `${(latency / 1000).toFixed(2)}s` }, { label: "错误率", value: errorRate === null ? "—" : `${(errorRate * 100).toFixed(2)}%` }];
        return <g className="topology-node" tabIndex={0} key={provider.id} onMouseMove={(event) => pointer(event, provider.name, rows)} onMouseLeave={() => setTip(null)} onFocus={(event) => focus(event, provider.name, rows)} onBlur={() => setTip(null)}>
          {state !== "ok" ? <circle className="topology-gateway-ring" cx={cx[3]} cy={providerY(index)} r={radius + 4} fill="none" stroke={color} /> : null}
          <circle className="topology-core" cx={cx[3]} cy={providerY(index)} r={radius} fill="var(--surface)" stroke={color} strokeWidth={1.5} />
          <text x={cx[3] + radius + 10} y={providerY(index) + 1} className="topology-label topology-provider-label">{provider.name}</text>
          <text x={cx[3] + radius + 10} y={providerY(index) + 15} className="topology-caption">{count(reading?.requests ?? 0)}/h · {latency === null || latency === undefined ? "—" : `${Number((latency / 1000).toFixed(2))}s`}</text>
        </g>;
      })}
      {!visibleModels.length ? <text x={cx[2]} y={gy} textAnchor="middle" className="topology-caption">暂无启用模型</text> : null}
    </svg>
    <PolarTooltip data={tip} />
  </div>;
}

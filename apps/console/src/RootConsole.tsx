import type { ReactNode } from "react";
import { Activity, Boxes, ChartColumn, Clapperboard, HandCoins, LayoutDashboard, LayoutGrid, LayoutTemplate, PackageCheck, Palette, Plug, ScrollText, SlidersHorizontal, Smartphone, Tags, Users } from "lucide-react";
import { Glyph } from "./components/Icon.js";
import type { NavGroup } from "./navigation.js";
import { ConfigPage } from "./pages/ConfigPage.js";
import { ModelsPage } from "./pages/Models.js";
import { ModelDisplayPage } from "./pages/ModelDisplay.js";
import { MediaModelsPage } from "./pages/MediaModels.js";
import { OpsPage } from "./pages/Ops.js";
import { OverviewPage } from "./pages/Overview.js";
import { OrgPage } from "./pages/Org.js";
import { CreditPackagesPage } from "./pages/CreditPackages.js";
import { PricingPage } from "./pages/Pricing.js";
import { ProvidersPage } from "./pages/Providers.js";
import { ReleasesPage } from "./pages/Releases.js";
import { TeamDetailPage } from "./pages/TeamDetail.js";
import { TemplateLibraryPage } from "./pages/TemplateLibrary.js";
import { UiLibraryPage } from "./pages/UiLibrary.js";

const NAV_GROUPS = [
  { label: "总览", items: [{ path: "/overview", label: "总览", icon: LayoutDashboard }] },
  {
    label: "组织",
    items: [
      { path: "/org", label: "团队与账号", icon: Users },
      { path: "/audit", label: "审计日志", icon: ScrollText },
      { path: "/quota-requests", label: "额度申请审批", icon: HandCoins },
    ],
  },
  {
    label: "计量",
    items: [
      { path: "/usage", label: "用量", icon: ChartColumn },
      { path: "/payments", label: "充值与余额", icon: HandCoins },
      { path: "/credit-packages", label: "充值套餐", icon: PackageCheck },
      { path: "/ops", label: "运维大盘", icon: Activity },
    ],
  },
  {
    label: "下发",
    items: [
      { path: "/releases", label: "内置配置发布", icon: PackageCheck },
      { path: "/models", label: "模型目录", icon: Boxes },
      { path: "/pricing", label: "模型定价库", icon: Tags },
      { path: "/config", label: "平台配置", icon: SlidersHorizontal },
    ],
  },
  {
    label: "接入",
    items: [
      { path: "/providers", label: "上游供应商", icon: Plug },
      { path: "/media-models", label: "媒体模型", icon: Clapperboard },
      { path: "/model-display", label: "模型展示", icon: LayoutGrid },
      { path: "/devices", label: "设备凭据", icon: Smartphone },
    ],
  },
] satisfies NavGroup[];


export default function RootConsole({ section, param, userId, page, children }: {
  section: string;
  param: string;
  userId: string;
  page: ReactNode;
  children: (groups: NavGroup[], page: ReactNode, referenceNav: ReactNode) => ReactNode;
}) {
  switch (section) {
    case "overview": page = <OverviewPage />; break;
    case "credit-packages": page = <CreditPackagesPage />; break;
    case "releases": page = <ReleasesPage />; break;
    case "models": page = <ModelsPage />; break;
    case "model-display": page = <ModelDisplayPage />; break;
    case "media-models": page = <MediaModelsPage />; break;
    case "pricing": page = <PricingPage />; break;
    case "providers": page = <ProvidersPage />; break;
    case "config": page = <ConfigPage />; break;
    case "ops": page = <OpsPage />; break;
    case "template-library": page = <TemplateLibraryPage />; break;
    case "ui": page = <UiLibraryPage />; break;
    case "org":
    case "users": page = <OrgPage currentUserId={userId} />; break;
    case "teams": page = param ? <TeamDetailPage teamId={param} /> : <OrgPage currentUserId={userId} />; break;
    default: page ??= <OverviewPage />;
  }
  return children(NAV_GROUPS, page, (
    <div className="side-ref">
      <p className="nav-group">参考</p>
      <a href="#/ui" className={section === "ui" ? "nav-item active" : "nav-item"}>
        <Glyph icon={Palette} /><span>UI 组件库</span>
      </a>
      <a href="#/template-library" className={section === "template-library" ? "nav-item active" : "nav-item"}>
        <Glyph icon={LayoutTemplate} /><span>模板库</span>
      </a>
    </div>
  ));
}

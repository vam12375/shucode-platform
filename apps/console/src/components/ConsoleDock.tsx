import { useEffect, useRef, useState, type ReactNode } from "react";
import { Bell, ChevronDown, Command, CornerDownLeft, LogOut, Menu, Search, UserCog, X } from "lucide-react";
import { navigate } from "../api.js";
import { formatShanghaiTime } from "../time.js";
import { useAuth } from "../auth.js";
import type { NavGroup } from "../navigation.js";
import { Glyph } from "./Icon.js";
import { ReadoutRail } from "./ReadoutRail.js";
import { ThemePicker } from "./ThemePicker.js";
import "./console-dock.css";

function sectionMatches(section: string, path: string): boolean {
  const target = path.slice(1);
  return section === target || (target === "org" && ["teams", "users"].includes(section));
}

function ShanghaiClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  return <time className="console-clock" dateTime={now.toISOString()}>
    {formatShanghaiTime(now.toISOString()).slice(5, 16)} 上海
  </time>;
}

export function ConsoleDock({ groups, section, referenceNav, onSignOut }: {
  groups: NavGroup[];
  section: string;
  referenceNav?: ReactNode;
  onSignOut: () => void;
}) {
  const { user, canManagePlatform, canManageTeam, team } = useAuth();
  const [menu, setMenu] = useState<string | null>(null);
  const [mobile, setMobile] = useState(false);
  const [account, setAccount] = useState(false);
  const [alerts, setAlerts] = useState<number | null>(null);
  const [command, setCommand] = useState(false);
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState(0);
  const headerRef = useRef<HTMLDivElement>(null);
  const commandRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const flat = groups.flatMap((group) => group.items.map((item) => ({ ...item, group: group.label })));
  const search = query.trim().toLocaleLowerCase();
  const matches = flat.filter((item) => `${item.label} ${item.group} ${item.path}`.toLocaleLowerCase().includes(search));
  const selectedIndex = Math.min(selected, Math.max(matches.length - 1, 0));
  const activeItem = flat.find((item) => sectionMatches(section, item.path));
  const closeCommand = () => { setCommand(false); triggerRef.current?.focus(); };
  const go = (path: string) => { setMenu(null); setMobile(false); setAccount(false); setCommand(false); navigate(path); };

  useEffect(() => {
    setMenu(null); setMobile(false); setAccount(false);
  }, [section]);
  useEffect(() => {
    if (command) { setQuery(""); setSelected(0); searchRef.current?.focus(); }
  }, [command]);
  useEffect(() => {
    if (command) commandRef.current?.querySelector(`#command-result-${selectedIndex}`)?.scrollIntoView({ block: "nearest" });
  }, [command, selectedIndex, query]);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault(); setCommand((value) => !value);
      }
      if (event.key === "Escape") { setMenu(null); setMobile(false); setAccount(false); setCommand(false); }
    };
    const onPointer = (event: PointerEvent) => {
      const target = event.target as Element;
      const selectMenu = target.closest?.(".ui-select-menu");
      // 下拉选项挂到 body 后仍属于导航；按 aria-controls 识别，避免点击选项先关闭读数面板。
      const ownedMenu = selectMenu && [...(headerRef.current?.querySelectorAll("[aria-controls]") ?? [])]
        .some((trigger) => trigger.getAttribute("aria-controls") === selectMenu.id);
      if (!headerRef.current?.contains(target) && !ownedMenu) {
        setMenu(null); setMobile(false); setAccount(false);
        headerRef.current?.querySelectorAll("details[open]").forEach((element) => element.removeAttribute("open"));
      }
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onPointer);
    return () => { document.removeEventListener("keydown", onKey); document.removeEventListener("pointerdown", onPointer); };
  }, []);

  return <>
    <div className="aurora" aria-hidden="true"><i /><i /><i /></div>
    <div className="console-header" ref={headerRef}>
      <ShanghaiClock />
      <nav className="dock" aria-label="控制台导航">
        <a className="dk-logo" href={canManagePlatform ? "#/overview" : "#/usage"} aria-label="门道控制台首页"><span>门</span></a>
        <button className="dock-mobile-toggle" type="button" aria-label="打开导航" aria-expanded={mobile} onClick={() => setMobile((value) => !value)}>
          <Glyph icon={mobile ? X : Menu} size={17} /><span>{activeItem?.label ?? "控制台"}</span><Glyph icon={ChevronDown} size={12} />
        </button>
        <div className="dock-groups">
          {groups.map((group) => {
            const active = group.items.some((item) => sectionMatches(section, item.path));
            const direct = group.items.length === 1;
            return <div className={`dk${active ? " on" : ""}`} key={group.label}
              onMouseEnter={() => { if (!direct) setMenu(group.label); }}
              onMouseLeave={() => setMenu(null)}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.stopPropagation();
                  event.currentTarget.querySelector<HTMLButtonElement>("button")?.focus();
                  setMenu(null);
                }
              }}
              onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setMenu(null); }}>
              {direct ? <a href={`#${group.items[0].path}`} aria-current={active ? "page" : undefined}>{group.items[0].label}</a> : <>
                {/* 悬停已经打开菜单，点击应保持展开，避免点击把刚出现的菜单反向关闭。 */}
                <button type="button" aria-expanded={menu === group.label} aria-controls={`dock-${group.label}`} onFocus={() => setMenu(group.label)} onClick={() => setMenu(group.label)}>
                  {group.label}<Glyph icon={ChevronDown} size={11} />
                </button>
                {menu === group.label ? <div className="dk-menu" id={`dock-${group.label}`}>
                  {group.items.map((item) => <a key={item.path} href={`#${item.path}`} className={sectionMatches(section, item.path) ? "on" : undefined} aria-current={sectionMatches(section, item.path) ? "page" : undefined} onClick={() => setMenu(null)}>
                    <Glyph icon={item.icon} size={16} strokeWidth={1.6} /><span>{item.label}</span>
                  </a>)}
                </div> : null}
              </>}
            </div>;
          })}
        </div>
        <button className="kbtn" ref={triggerRef} type="button" aria-label="搜索页面，Command 或 Control K" onClick={() => setCommand(true)}><Glyph icon={Search} size={15} /><span>搜索或执行</span><kbd>⌘K</kbd></button>
        <ReadoutRail onAlerts={setAlerts} />
      </nav>
      <div className="corner">
        {canManagePlatform ? <a className="cbtn" href="#/ops" aria-label={alerts === null ? "查看运维告警" : `告警 ${alerts} 条`} title="运维告警"><Glyph icon={Bell} size={18} strokeWidth={1.6} />{alerts !== null && alerts > 0 ? <i className="dot" /> : null}</a> : null}
        <button className="cbtn me" type="button" aria-label="账号设置" aria-expanded={account} onClick={() => setAccount((value) => !value)}>{(user.displayName || user.email).slice(0, 1).toUpperCase()}</button>
      </div>
      {account ? <div className="account-menu">
        <div className="account-identity"><div className="user-avatar">{(user.displayName || user.email).slice(0, 1).toUpperCase()}</div><div><strong>{user.displayName || user.email}</strong><span>{user.email}</span></div></div>
        <p className="account-role">{canManagePlatform ? "平台管理员" : canManageTeam ? "团队管理员" : "团队成员"}{team ? ` · ${team.name}` : ""}</p>
        <a className="account-link" href="#/profile" onClick={() => setAccount(false)}><Glyph icon={UserCog} size={16} />个人设置</a>
        {referenceNav}
        <div className="account-theme"><span>界面</span><ThemePicker /></div>
        <button className="account-link account-logout" type="button" onClick={onSignOut}><Glyph icon={LogOut} size={16} />退出登录</button>
      </div> : null}
      {mobile ? <div className="dock-mobile-menu">
        {groups.map((group) => <div key={group.label}><p>{group.label}</p>{group.items.map((item) => <a key={item.path} href={`#${item.path}`} className={sectionMatches(section, item.path) ? "on" : undefined} onClick={() => setMobile(false)}><Glyph icon={item.icon} size={16} /><span>{item.label}</span></a>)}</div>)}
      </div> : null}
    </div>
    {command ? <div className="cmdk on" onMouseDown={(event) => { if (event.target === event.currentTarget) closeCommand(); }}>
      <div className="cmdk-box" role="dialog" aria-modal="true" aria-label="搜索控制台页面" ref={commandRef} onKeyDown={(event) => {
        if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeCommand(); }
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          event.preventDefault();
          if (matches.length) setSelected((value) => (value + (event.key === "ArrowDown" ? 1 : -1) + matches.length) % matches.length);
        }
        if (event.key === "Enter" && matches[selectedIndex]) { event.preventDefault(); go(matches[selectedIndex].path); }
        if (event.key === "Tab") {
          const buttons = commandRef.current?.querySelectorAll<HTMLElement>('input,button,[href]');
          if (!buttons?.length) return;
          const first = buttons[0], last = buttons[buttons.length - 1];
          if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
          else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
        }
      }}>
        <div className="cmdk-in"><Glyph icon={Search} size={20} /><input ref={searchRef} placeholder="前往页面…" value={query} onChange={(event) => { setQuery(event.target.value); setSelected(0); }} role="combobox" aria-label="搜索页面" aria-expanded="true" aria-controls="command-results" aria-activedescendant={matches.length ? `command-result-${selectedIndex}` : undefined} /><button className="cmdk-close" type="button" aria-label="关闭搜索" onClick={closeCommand}><kbd>esc</kbd></button></div>
        <div className="cmdk-list" id="command-results" role="listbox" aria-label="页面">
          {matches.length ? matches.map((item, index) => <button key={item.path} id={`command-result-${index}`} type="button" role="option" aria-selected={index === selectedIndex} className={`cmdk-it${index === selectedIndex ? " sel" : ""}`} onMouseEnter={() => setSelected(index)} onClick={() => go(item.path)}><Glyph icon={item.icon} size={17} /><span>{item.label}</span><em>{item.group}</em>{index === selectedIndex ? <Glyph icon={CornerDownLeft} size={13} /> : null}</button>) : <p className="command-empty">没有找到页面，请尝试其他名称。</p>}
        </div>
        <div className="cmdk-foot"><span><kbd>↑</kbd> <kbd>↓</kbd> 选择</span><span><kbd>↵</kbd> 前往</span><span><kbd>esc</kbd> 关闭</span><Glyph icon={Command} size={12} /></div>
      </div>
    </div> : null}
  </>;
}

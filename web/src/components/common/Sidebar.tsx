import React, { type RefObject } from "react";
import { NavLink } from "react-router-dom";
import { LayoutDashboard, Compass, Rss, ClipboardList, FolderSync, Activity, PackageOpen, Globe2, Settings, KeyRound, PanelLeftClose, PanelLeftOpen, X } from "lucide-react";

const groups = [
  { title: "开始", items: [{ to: "/", label: "仪表盘", icon: LayoutDashboard }] },
  { title: "发现", items: [{ to: "/discover", label: "探索发现", icon: Compass }] },
  { title: "订阅", items: [{ to: "/subscriptions", label: "影视订阅", icon: Rss }, { to: "/requests", label: "家庭请求", icon: ClipboardList }] },
  { title: "整理与存储", items: [
    { to: "/media", label: "媒体库", icon: FolderSync },
    { to: "/activity", label: "下载活动", icon: Activity },
    { to: "/reorganize", label: "历史整理", icon: PackageOpen },
  ] },
  { title: "站点与配置", items: [
    { to: "/sites", label: "站点管理", icon: Globe2 },
    { to: "/settings", label: "服务设置", icon: Settings },
    { to: "/account/tokens", label: "API 令牌", icon: KeyRound },
  ] },
];

type SidebarProps = {
  isCollapsed: boolean;
  isMobile: boolean;
  isOpen: boolean;
  navigationRef: RefObject<HTMLElement | null>;
  onToggle: () => void;
  onClose: () => void;
};

export const Sidebar: React.FC<SidebarProps> = ({ isCollapsed, isMobile, isOpen, navigationRef, onToggle, onClose }) => (
  <aside id="app-navigation" ref={navigationRef}
    className={`sidebar ${isCollapsed ? "sidebar-collapsed" : ""}`}
    inert={isMobile && !isOpen} role={isMobile && isOpen ? "dialog" : undefined}
    aria-modal={isMobile && isOpen ? true : undefined}
    aria-label={isMobile && isOpen ? "导航菜单" : undefined} tabIndex={-1}>
    <div className="sidebar-brand">
      {!isCollapsed && <div className="brand-left">
        <div className="logo-box-interactive"><span className="logo-icon" aria-hidden="true">⚡</span></div>
        <strong className="brand-text">PulseMedia</strong>
      </div>}
      <button type="button" className="collapse-btn" onClick={isMobile ? onClose : onToggle}
        title={isMobile ? "关闭导航菜单" : isCollapsed ? "展开侧边栏" : "收起侧边栏"}
        aria-label={isMobile ? "关闭导航菜单" : isCollapsed ? "展开侧边栏" : "收起侧边栏"}
        aria-controls="primary-navigation" aria-expanded={!isCollapsed}>
        {isMobile ? <X size={20} aria-hidden="true" /> : isCollapsed ? <PanelLeftOpen size={20} aria-hidden="true" /> : <PanelLeftClose size={20} aria-hidden="true" />}
      </button>
    </div>
    <nav id="primary-navigation" className="sidebar-nav" aria-label="主导航">
      {groups.map(group => <React.Fragment key={group.title}>
        {!isCollapsed && <div className="nav-section-title">{group.title}</div>}
        {group.items.map(item => <NavLink key={item.to} to={item.to} end={item.to === "/"}
          aria-label={item.label} title={isCollapsed ? item.label : undefined}
          className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <item.icon size={18} className="nav-icon" aria-hidden="true" />
          {!isCollapsed && <span className="nav-label">{item.label}</span>}
        </NavLink>)}
      </React.Fragment>)}
    </nav>
    {!isCollapsed && <div className="sidebar-footer"><span className="version-tag">PulseMedia v0.1.0-beta</span></div>}
  </aside>
);

import React from "react";
import { NavLink } from "react-router-dom";
import {
  LayoutDashboard,
  Compass,
  Rss,
  FolderSync,
  Activity,
  PackageOpen,
  Globe2,
  Settings,
  KeyRound,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";

interface SidebarProps {
  isCollapsed: boolean;
  onToggle: () => void;
}

export const Sidebar: React.FC<SidebarProps> = ({ isCollapsed, onToggle }) => {
  return (
    <aside className={`sidebar ${isCollapsed ? "sidebar-collapsed" : ""}`}>
      {/* Brand Header */}
      <div
        className="sidebar-brand"
        onClick={isCollapsed ? onToggle : undefined}
        title={isCollapsed ? "点击展开侧边栏" : undefined}
      >
        <div className="brand-left">
          {/* Logo Box with Hover-Expand Transition */}
          <div className="logo-box-interactive">
            <span className="logo-icon">⚡</span>
            {isCollapsed && (
              <span className="logo-expand-hover">
                <PanelLeftOpen size={20} />
              </span>
            )}
          </div>
          {!isCollapsed && <strong className="brand-text">PulseMedia</strong>}
        </div>

        {/* 展开时在图标右侧显示收起按钮 */}
        {!isCollapsed && (
          <button
            className="collapse-btn"
            onClick={(e) => {
              e.stopPropagation();
              onToggle();
            }}
            title="收起侧边栏"
          >
            <PanelLeftClose size={20} />
          </button>
        )}
      </div>

      {/* Grouped Navigation Links */}
      <nav className="sidebar-nav">
        {/* 开始 */}
        {!isCollapsed && <div className="nav-section-title">开始</div>}
        <NavLink to="/" end className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <LayoutDashboard size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">仪表盘</span>}
        </NavLink>

        {/* 发现 */}
        {!isCollapsed && <div className="nav-section-title">发现</div>}
        <NavLink to="/discover" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <Compass size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">探索发现</span>}
        </NavLink>

        {/* 订阅 */}
        {!isCollapsed && <div className="nav-section-title">订阅</div>}
        <NavLink to="/subscriptions" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <Rss size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">影视订阅</span>}
        </NavLink>

        {/* 整理与存储 */}
        {!isCollapsed && <div className="nav-section-title">整理与存储</div>}
        <NavLink to="/media" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <FolderSync size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">媒体库</span>}
        </NavLink>
        <NavLink to="/activity" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <Activity size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">下载活动</span>}
        </NavLink>
        <NavLink to="/reorganize" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <PackageOpen size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">历史整理</span>}
        </NavLink>

        {/* 站点与系统 */}
        {!isCollapsed && <div className="nav-section-title">站点与配置</div>}
        <NavLink to="/sites" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <Globe2 size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">站点管理</span>}
        </NavLink>
        <NavLink to="/settings" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <Settings size={18} className="nav-icon" />
          {!isCollapsed && <span className="nav-label">服务设置</span>}
        </NavLink>
        <NavLink to="/account/tokens" aria-label="API 令牌" title="API 令牌" className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}>
          <KeyRound size={18} className="nav-icon" aria-hidden="true" />
          {!isCollapsed && <span className="nav-label">API 令牌</span>}
        </NavLink>
      </nav>

      {/* Footer (底部不重复放置按钮，仅保留简洁版本标识) */}
      {!isCollapsed && (
        <div className="sidebar-footer">
          <span className="version-tag">PulseMedia v0.1.0-beta</span>
        </div>
      )}
    </aside>
  );
};

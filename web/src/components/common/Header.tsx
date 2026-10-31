import React, { useState } from "react";
import { Search, LogOut, Menu, Moon, Sun, ChevronRight } from "lucide-react";
import { useAuth } from "@/features/auth/AuthContext";
import { useNavigate, useLocation } from "react-router-dom";

const titles: Record<string, string> = {
  "/": "仪表盘", "/discover": "探索发现", "/subscriptions": "影视订阅",
  "/requests": "家庭请求", "/activity": "下载活动", "/media": "媒体库",
  "/sites": "站点管理", "/reorganize": "历史整理", "/settings": "服务设置",
  "/account/tokens": "账号与安全",
};
type HeaderProps = { onToggleMobile?: () => void; mobileDrawerOpen?: boolean; menuButtonRef?: React.Ref<HTMLButtonElement> };

export const Header: React.FC<HeaderProps> = ({ onToggleMobile, mobileDrawerOpen = false, menuButtonRef }) => {
  const { user, logout, loading } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [dark, setDark] = useState(() => document.documentElement.dataset.theme !== "light");
  const title = titles[location.pathname] ?? "PulseMedia";
  const toggleTheme = () => {
    const next = !dark;
    document.documentElement.dataset.theme = next ? "dark" : "light";
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content", next ? "#171c1b" : "#f4f6f5");
    try { localStorage.setItem("pulsemedia.theme", next ? "dark" : "light"); } catch { /* Theme still works when storage is unavailable. */ }
    setDark(next);
  };
  const handleLogout = async () => { if (await logout()) navigate("/login"); };
  return <header className="topbar">
    <div className="topbar-left">
      {onToggleMobile && <button ref={menuButtonRef} type="button" className="mobile-menu-btn" onClick={onToggleMobile}
        aria-label={mobileDrawerOpen ? "关闭导航菜单" : "打开导航菜单"}
        title={mobileDrawerOpen ? "关闭导航菜单" : "打开导航菜单"}
        aria-controls="app-navigation" aria-expanded={mobileDrawerOpen}>
        <Menu size={20} aria-hidden="true" />
      </button>}
      <span className="topbar-home">工作台</span><ChevronRight size={14} className="breadcrumb-separator" aria-hidden="true" />
      <span className="page-title" title={title}>{title}</span>
    </div>
    <div className="topbar-right">
      <button type="button" className="quick-search-pill" onClick={() => navigate("/discover")}>
        <Search size={17} aria-hidden="true" /><span>搜索电影、电视剧</span>
      </button>
      <button type="button" className="theme-toggle" onClick={toggleTheme} aria-label={dark ? "切换浅色主题" : "切换深色主题"} title={dark ? "切换浅色主题" : "切换深色主题"}>
        {dark ? <Sun size={18} aria-hidden="true" /> : <Moon size={18} aria-hidden="true" />}
      </button>
      {user && <div className="user-profile" role="group" aria-label={`当前用户：${user.username}（${user.role}）`}>
        <div className="avatar-ring" aria-hidden="true">{Array.from(user.username || "A")[0].toUpperCase()}</div>
        <div className="user-meta"><span className="username" title={user.username}>{user.username}</span><span className={`badge ${user.role}`}>{user.role === "admin" ? "管理员" : "成员"}</span></div>
        <button type="button" className="btn-logout" title="退出登录" aria-label="退出登录" onClick={handleLogout} disabled={loading}>
          <LogOut size={17} aria-hidden="true" /><span>退出</span>
        </button>
      </div>}
    </div>
  </header>;
};

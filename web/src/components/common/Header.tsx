import React from "react";
import { Search, LogOut, Menu } from "lucide-react";
import { useAuth } from "@/features/auth/AuthContext";
import { useNavigate, useLocation } from "react-router-dom";

const titles: Record<string, string> = {
  "/": "控制台仪表盘",
  "/discover": "影视探索与选季订阅",
  "/subscriptions": "媒体订阅与追更日历",
  "/requests": "家庭请求与审批",
  "/activity": "下载活动与任务时间线",
  "/media": "媒体库与存储管理",
  "/sites": "站点管理与探针测速",
  "/reorganize": "文件与映射",
  "/settings": "系统配置与服务集成",
  "/account/tokens": "账户 API 令牌",
};

type HeaderProps = { onToggleMobile?: () => void; mobileDrawerOpen?: boolean; menuButtonRef?: React.Ref<HTMLButtonElement> };
export const Header: React.FC<HeaderProps> = ({ onToggleMobile, mobileDrawerOpen = false, menuButtonRef }) => {
  const { user, logout, loading } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const title = titles[location.pathname] ?? "PulseMedia";
  const handleLogout = async () => { if (await logout()) navigate("/login"); };
  return <header className="topbar">
    <div className="topbar-left">
      {onToggleMobile && <button ref={menuButtonRef} type="button" className="mobile-menu-btn" onClick={onToggleMobile}
        aria-label={mobileDrawerOpen ? "关闭导航菜单" : "打开导航菜单"}
        title={mobileDrawerOpen ? "关闭导航菜单" : "打开导航菜单"}
        aria-controls="app-navigation" aria-expanded={mobileDrawerOpen}>
        <Menu size={18} aria-hidden="true" />
      </button>}
      <span className="page-title" title={title}>{title}</span>
    </div>
    <div className="topbar-right">
      <button type="button" className="quick-search-pill" onClick={() => navigate("/discover")}>
        <Search size={14} aria-hidden="true" /><span>搜索电影、电视剧...</span>
      </button>
      {user && <div className="user-profile" role="group" aria-label={`当前用户：${user.username}（${user.role}）`}>
        <div className="avatar-ring" aria-hidden="true">{(user.username || "A").slice(0, 1).toUpperCase()}</div>
        <div className="user-meta"><span className="username" title={user.username}>{user.username}</span><span className={`badge ${user.role}`}>{user.role}</span></div>
        <button type="button" className="btn-logout" title="退出登录" onClick={handleLogout} disabled={loading}>
          <LogOut size={14} aria-hidden="true" /><span>退出</span>
        </button>
      </div>}
    </div>
  </header>;
};

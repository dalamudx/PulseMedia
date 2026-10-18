import React from "react";
import { Search, LogOut, Menu } from "lucide-react";
import { useAuth } from "@/features/auth/AuthContext";
import { useNavigate, useLocation } from "react-router-dom";

interface HeaderProps {
  onToggleMobile?: () => void;
}

export const Header: React.FC<HeaderProps> = ({ onToggleMobile }) => {
  const { user, logout, loading } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();

  const getTitle = () => {
    switch (location.pathname) {
      case "/":
        return "控制台仪表盘";
      case "/discover":
        return "影视探索与选季订阅";
      case "/subscriptions":
        return "媒体订阅与追更日历";
      case "/activity":
        return "下载活动与任务时间线";
      case "/media":
        return "媒体库与存储管理";
      case "/sites":
        return "站点管理与探针测速";
      case "/reorganize":
        return "文件与映射";
      case "/settings":
        return "系统配置与服务集成";
      case "/account/tokens":
        return "账户 API 令牌";
      default:
        return "PulseMedia";
    }
  };

  const handleLogout = async () => {
    if (await logout()) navigate("/login");
  };

  return (
    <header className="topbar">
      <div className="topbar-left">
        {/* Mobile Hamburger Button (Visible on screens < 1024px) */}
        {onToggleMobile && (
          <button
            className="mobile-menu-btn"
            onClick={onToggleMobile}
            title="打开导航菜单"
          >
            <Menu size={18} />
          </button>
        )}
        <span className="page-title">{getTitle()}</span>
      </div>

      <div className="topbar-right">
        {/* Quick Search Pill */}
        <button className="quick-search-pill" onClick={() => navigate("/discover")}>
          <Search size={14} />
          <span>搜索电影、电视剧...</span>
          <kbd>Ctrl+K</kbd>
        </button>


        {/* User Profile */}
        {user && (
          <div className="user-profile">
            <div className="avatar-ring">
              {(user.username || "A").slice(0, 1).toUpperCase()}
            </div>
            <div className="user-meta">
              <span className="username">{user.username}</span>
              <span className={`badge ${user.role}`}>{user.role}</span>
            </div>
            <button className="btn-logout" title="退出登录" onClick={handleLogout} disabled={loading}>
              <LogOut size={14} />
              <span>退出</span>
            </button>
          </div>
        )}
      </div>
    </header>
  );
};

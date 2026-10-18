import React, { useState, useEffect } from "react";
import { Routes, Route, Navigate, useLocation } from "react-router-dom";
import { useAuth } from "@/features/auth/AuthContext";
import { Sidebar } from "@/components/common/Sidebar";
import { Header } from "@/components/common/Header";
import { DashboardView } from "@/views/DashboardView";
import { DiscoverView } from "@/views/DiscoverView";
import { SubscriptionsView } from "@/views/SubscriptionsView";
import { ActivityView } from "@/views/ActivityView";
import { MediaView } from "@/views/MediaView";
import { SitesView } from "@/views/SitesView";
import { ReorganizeView } from "@/views/ReorganizeView";
import { SettingsView } from "@/views/SettingsView";
import { AccountTokensView } from "@/views/AccountTokensView";
import { LoginView } from "@/views/LoginView";
import { SetupView } from "@/views/SetupView";

export const App: React.FC = () => {
  const { user, initialized, checking, statusError, checkStatus } = useAuth();
  const location = useLocation();
  const [isCollapsed, setIsCollapsed] = useState(false);
  const [mobileDrawerOpen, setMobileDrawerOpen] = useState(false);

  // Close mobile drawer on route navigation
  useEffect(() => {
    setMobileDrawerOpen(false);
  }, [location.pathname]);

  if (statusError) {
    return (
      <main className="auth-wrapper">
        <section className="auth-card" aria-labelledby="auth-status-title">
          <h1 id="auth-status-title">无法确认认证状态</h1>
          <p role="alert">{statusError}</p>
          <button className="btn-auth" onClick={() => void checkStatus()} disabled={checking}>
            重试读取认证状态
          </button>
        </section>
      </main>
    );
  }

  // Wait for a successful status read before selecting setup or authenticated routes.
  if (checking || initialized === null) {
    return (
      <div className="loading-fullscreen">
        <div className="spinner"></div>
      </div>
    );
  }

  // 1. Not initialized -> go to /setup
  if (!initialized && location.pathname !== "/setup") {
    return <Navigate to="/setup" replace />;
  }

  // 2. Initialized but visiting /setup -> go to /
  if (initialized && location.pathname === "/setup") {
    return <Navigate to={user ? "/" : "/login"} replace />;
  }

  // 3. Not logged in -> must go to /login
  if (!user && location.pathname !== "/login" && location.pathname !== "/setup") {
    return <Navigate to="/login" replace />;
  }

  // 4. Logged in visiting /login -> redirect to /
  if (user && location.pathname === "/login") {
    return <Navigate to="/" replace />;
  }

  // Auth pages (Login & Setup without sidebar)
  if (!user) {
    return (
      <Routes>
        <Route path="/login" element={<LoginView />} />
        <Route path="/setup" element={<SetupView />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  // Full Console Layout (with Responsive Adaptive Container & Mobile Drawer)
  return (
    <div
      className={`app-shell with-sidebar ${isCollapsed ? "sidebar-collapsed" : ""} ${
        mobileDrawerOpen ? "mobile-drawer-open" : ""
      }`}
    >
      {/* Mobile Drawer Dark Backdrop */}
      {mobileDrawerOpen && (
        <div
          className="mobile-drawer-backdrop"
          onClick={() => setMobileDrawerOpen(false)}
        />
      )}

      <Sidebar
        isCollapsed={isCollapsed}
        onToggle={() => setIsCollapsed(!isCollapsed)}
      />

      <div className="main-wrapper">
        <Header onToggleMobile={() => setMobileDrawerOpen(!mobileDrawerOpen)} />
        <main className="page-content">
          <Routes>
            <Route path="/" element={<DashboardView />} />
            <Route path="/discover" element={<DiscoverView />} />
            <Route path="/subscriptions" element={<SubscriptionsView />} />
            <Route path="/activity" element={<ActivityView />} />
            <Route path="/media" element={<MediaView />} />
            <Route path="/sites" element={<SitesView />} />
            <Route path="/reorganize" element={<ReorganizeView />} />
            <Route path="/settings" element={<SettingsView />} />
            <Route path="/account/tokens" element={<AccountTokensView />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
      </div>
    </div>
  );
};

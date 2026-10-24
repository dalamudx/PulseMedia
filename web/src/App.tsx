import React, { useState, useEffect, useRef } from "react";
import { Routes, Route, Navigate, useLocation } from "react-router-dom";
import { useAuth } from "@/features/auth/AuthContext";
import { Sidebar } from "@/components/common/Sidebar";
import { Header } from "@/components/common/Header";
import { DashboardView } from "@/views/DashboardView";
import { DiscoverView } from "@/views/DiscoverView";
import { SubscriptionsView } from "@/views/SubscriptionsView";
import { RequestsView } from "@/views/RequestsView";
import { ActivityView } from "@/views/ActivityView";
import { MediaView } from "@/views/MediaView";
import { SitesView } from "@/views/SitesView";
import { ReorganizeView } from "@/views/ReorganizeView";
import { SettingsView } from "@/views/SettingsView";
import { AccountTokensView } from "@/views/AccountTokensView";
import { LoginView } from "@/views/LoginView";
import { SetupView } from "@/views/SetupView";
import {DiscoveryReviewHandoffProvider} from "@/features/configuration/DiscoveryReviewHandoff";
import { useDialogFocus } from "@/features/configuration/useDialogFocus";

export const App: React.FC = () => {
  const { user, initialized, checking, statusError, checkStatus } = useAuth();
  const location = useLocation();
  const [isCollapsed, setIsCollapsed] = useState(false);
  const [mobileDrawerOpen, setMobileDrawerOpen] = useState(false);

  const [isCompact, setIsCompact] = useState(() => window.matchMedia("(max-width: 1024px)").matches);
  const navigation = useRef<HTMLElement>(null);
  const menuButton = useRef<HTMLButtonElement>(null);
  const drawerOpen = Boolean(user) && isCompact && mobileDrawerOpen;
  useDialogFocus(navigation, () => setMobileDrawerOpen(false), drawerOpen, menuButton);

  useEffect(() => {
    const query = window.matchMedia("(max-width: 1024px)");
    const update = () => { setIsCompact(query.matches); setMobileDrawerOpen(false); };
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    setMobileDrawerOpen(false);
  }, [location.key, user?.id, user?.role]);

  useEffect(() => {
    if (!drawerOpen) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = previous; };
  }, [drawerOpen]);

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
    <DiscoveryReviewHandoffProvider>
    <a className="skip-link" href="#main-content">跳到主要内容</a>
    <div
      className={`app-shell with-sidebar ${isCollapsed ? "sidebar-collapsed" : ""} ${
        drawerOpen ? "mobile-drawer-open" : ""
      }`}
    >
      {/* Mobile Drawer Dark Backdrop */}
      {drawerOpen && (
        <div
          className="mobile-drawer-backdrop"
          onClick={() => setMobileDrawerOpen(false)}
        />
      )}

      <Sidebar
        navigationRef={navigation}
        isMobile={isCompact}
        isOpen={drawerOpen}
        isCollapsed={!isCompact && isCollapsed}
        onToggle={() => setIsCollapsed(previous => !previous)}
        onClose={() => setMobileDrawerOpen(false)}
      />

      <div className="main-wrapper" inert={drawerOpen}>
        <Header menuButtonRef={menuButton} mobileDrawerOpen={drawerOpen} onToggleMobile={() => setMobileDrawerOpen(previous => !previous)} />
        <main id="main-content" tabIndex={-1} className="page-content">
          <Routes>
            <Route path="/" element={<DashboardView />} />
            <Route path="/discover" element={<DiscoverView />} />
            <Route path="/subscriptions" element={<SubscriptionsView />} />
            <Route path="/requests" element={<RequestsView />} />
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
    </DiscoveryReviewHandoffProvider>
  );
};

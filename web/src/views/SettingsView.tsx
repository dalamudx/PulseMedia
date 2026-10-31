import React, { useState, useEffect } from "react";
import { Key, Radio, Download, Tv, Terminal } from "lucide-react";
import { api } from "@/api/client";
import { LazyContent } from "@/components/common/LazyContent";
const DeliveryPanel = React.lazy(() => import("@/features/configuration/DeliveryPanel").then(module => ({ default: module.DeliveryPanel })));
const WebPushPanel = React.lazy(() => import("@/features/configuration/WebPushPanel").then(module => ({ default: module.WebPushPanel })));
const EndpointConfigurationPanel = React.lazy(() => import("@/features/configuration/EndpointConfigurationPanel").then(module => ({ default: module.EndpointConfigurationPanel })));
const LibraryRootsPanel = React.lazy(() => import("@/features/configuration/LibraryRootsPanel").then(module => ({ default: module.LibraryRootsPanel })));
const TMDBSettingsPanel = React.lazy(() => import("@/features/configuration/TMDBSettingsPanel").then(module => ({ default: module.TMDBSettingsPanel })));
const MetadataDiagnosticsPanel = React.lazy(() => import("@/features/configuration/MetadataDiagnosticsPanel").then(module => ({ default: module.MetadataDiagnosticsPanel })));
const PolicyPanel = React.lazy(() => import("@/features/configuration/PolicyPanel").then(module => ({ default: module.PolicyPanel })));
const NamingTemplatesPanel = React.lazy(() => import("@/features/configuration/NamingTemplatesPanel").then(module => ({ default: module.NamingTemplatesPanel })));
const LiveLogPanel = React.lazy(() => import("@/features/configuration/LiveLogPanel").then(module => ({ default: module.LiveLogPanel })));
const DoubanSyncSettingsPanel = React.lazy(() => import("@/features/configuration/DoubanSyncSettingsPanel").then(module => ({ default: module.DoubanSyncSettingsPanel })));
const CheckinSettingsPanel = React.lazy(() => import("@/features/configuration/CheckinSettingsPanel").then(module => ({ default: module.CheckinSettingsPanel })));
const CookieCloudSettingsPanel = React.lazy(() => import("@/features/configuration/CookieCloudSettingsPanel").then(module => ({ default: module.CookieCloudSettingsPanel })));
const SeedingSettingsPanel = React.lazy(() => import("@/features/configuration/SeedingSettingsPanel").then(module => ({ default: module.SeedingSettingsPanel })));
const FanartSettingsPanel = React.lazy(() => import("@/features/configuration/FanartSettingsPanel").then(module => ({ default: module.FanartSettingsPanel })));
const AuditTimelinePanel = React.lazy(() => import("@/features/configuration/AuditTimelinePanel").then(module => ({ default: module.AuditTimelinePanel })));
const ClassificationSettingsPanel = React.lazy(() => import("@/features/configuration/ClassificationSettingsPanel").then(module => ({ default: module.ClassificationSettingsPanel })));
const SeerrSettingsPanel = React.lazy(() => import("@/features/configuration/SeerrSettingsPanel").then(module => ({ default: module.SeerrSettingsPanel })));
import { useAuth } from "@/features/auth/AuthContext";
import type { paths } from "@/api/client";

type SystemStatus = paths["/api/v1/system/status"]["get"]["responses"]["200"]["content"]["application/json"];

export const SettingsView: React.FC = () => {
  const { user } = useAuth();
  const [activeTab, setActiveTab] = useState<
    "classification" | "seerr" | "fanart" | "audit" | "webpush" | "cookiecloud" | "seeding" | "roots" | "tmdb" | "douban" | "checkin" | "downloader" | "mediaserver" | "system" | "logs" | "policy" | "deliveries"
  >("tmdb");




  // System Status & Logs
  const [sysStatus, setSysStatus] = useState<SystemStatus | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    setSysStatus(null);
    void api.GET("/api/v1/system/status", { signal: controller.signal }).then(({ data }) => {
      if (!controller.signal.aborted && data) setSysStatus(data);
    }).catch(() => {});
    return () => { controller.abort(); };
  }, [user?.id, user?.role]);





  return (
    <div className="settings-container">
      <div className="page-header">
        <div>
          <h2>系统配置与服务集成</h2>
          <p className="subtitle">管理影视元数据、质量与过滤策略、下载器、媒体服务器与系统状态</p>
        </div>
      </div>

      <div className="settings-layout">
        {/* Left Category Tabs */}
        <aside className="settings-nav">
          <div className="settings-nav-group">元数据来源</div>
          <button className={`nav-tab-btn ${activeTab === "tmdb" ? "active" : ""}`} onClick={() => setActiveTab("tmdb")}>
            <Key size={16} />
            <span>TMDB 元数据</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "douban" ? "active" : ""}`} onClick={() => setActiveTab("douban")}>
            <Radio size={16} />
            <span>豆瓣想看同步</span>
          </button>
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "fanart" ? "active" : ""}`} onClick={() => setActiveTab("fanart")}><Key size={16}/><span>Fanart艺术画报</span></button>}
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "audit" ? "active" : ""}`} onClick={() => setActiveTab("audit")}><Terminal size={16}/><span>系统操作审计</span></button>}
          <div className="settings-nav-group">站点自动化</div>
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "checkin" ? "active" : ""}`} onClick={() => setActiveTab("checkin")}><Radio size={16} /><span>站点定时签到</span></button>}
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "cookiecloud" ? "active" : ""}`} onClick={() => setActiveTab("cookiecloud")}><Key size={16}/><span>CookieCloud同步</span></button>}
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "seeding" ? "active" : ""}`} onClick={() => setActiveTab("seeding")}><Download size={16}/><span>做种策略</span></button>}
          <div className="settings-nav-group">下载与整理</div>
          <button className={`nav-tab-btn ${activeTab === "downloader" ? "active" : ""}`} onClick={() => setActiveTab("downloader")}>
            <Download size={16} />
            <span>下载器配置</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "mediaserver" ? "active" : ""}`} onClick={() => setActiveTab("mediaserver")}>
            <Tv size={16} />
            <span>媒体服务器</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "policy" ? "active" : ""}`} onClick={() => setActiveTab("policy")}><Terminal size={16} /><span>质量与过滤策略</span></button>
          <div className="settings-nav-group">通知</div>
          <button className={`nav-tab-btn ${activeTab === "webpush" ? "active" : ""}`} onClick={() => setActiveTab("webpush")}><Radio size={16}/><span>浏览器通知</span></button>
          {user?.role==="admin"&&<button className={`nav-tab-btn ${activeTab==="deliveries"?"active":""}`} onClick={()=>setActiveTab("deliveries")}><Radio size={16}/><span>通知与投递</span></button>}
          <button className={`nav-tab-btn ${activeTab === "roots" ? "active" : ""}`} onClick={()=>setActiveTab("roots")}><Tv size={16}/><span>媒体根与基线</span></button>
{user?.role === "admin" && <><button className={`nav-tab-btn ${activeTab === "classification" ? "active" : ""}`} onClick={()=>setActiveTab("classification")}>媒体自动分类</button><button className={`nav-tab-btn ${activeTab === "seerr" ? "active" : ""}`} onClick={()=>setActiveTab("seerr")}>外部求片接入</button></>}
          <div className="settings-nav-group">系统</div>
        <button className={`nav-tab-btn ${activeTab === "system" ? "active" : ""}`} onClick={() => setActiveTab("system")}>
            <Terminal size={16} />
            <span>系统运行参数</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "logs" ? "active" : ""}`} onClick={() => setActiveTab("logs")}>
            <Terminal size={16} />
            <span>实时日志流</span>
          </button>

        </aside>

        {/* Right Main Content Panel */}
        <main className="settings-content">
          <LazyContent key={`${user?.id}:${user?.role}:${activeTab}`}>
{activeTab === "classification" && user?.role === "admin" && <ClassificationSettingsPanel />}
{activeTab === "seerr" && user?.role === "admin" && <SeerrSettingsPanel />}
          {activeTab === "fanart" && user?.role === "admin" && <FanartSettingsPanel />}
          {activeTab === "audit" && user?.role === "admin" && <AuditTimelinePanel />}
          {activeTab === "roots" && <LibraryRootsPanel />}
          {activeTab === "policy" && <><PolicyPanel /><NamingTemplatesPanel /></>}
          {activeTab === "webpush" && <WebPushPanel /> }
        {activeTab==="deliveries"&&user?.role==="admin"&&<DeliveryPanel/>}
          {/* 1. TMDB */}
          {activeTab === "tmdb" && <><TMDBSettingsPanel /><MetadataDiagnosticsPanel /></>}

          {/* 2. Douban */}
          {activeTab === "douban" && <DoubanSyncSettingsPanel />}
          {activeTab === "checkin" && user?.role === "admin" && <CheckinSettingsPanel />}
          {activeTab === "cookiecloud" && user?.role === "admin" && <CookieCloudSettingsPanel />}
          {activeTab === "seeding" && user?.role === "admin" && <SeedingSettingsPanel />}

          {/* 3. Downloader */}
          {activeTab === "downloader" && <EndpointConfigurationPanel family="download_client" />}
          {activeTab === "mediaserver" && <EndpointConfigurationPanel family="media_server" />}

          {/* 8. System Status */}
          {activeTab === "system" && (
            <div className="card">
              <div className="card-header">
                <h3>系统运行环境参数</h3>
              </div>
              {sysStatus && (
                <div className="sys-meta-list">
                  <div className="sys-row"><strong>微单体系统版本:</strong> PulseMedia v{sysStatus.version}</div>
                  <div className="sys-row">
                    <strong>数据库引擎:</strong> PostgreSQL 18.6 原生约束 ({sysStatus.db_ready ? "🟢 正常连接" : "🔴 异常"})
                  </div>
                  <div className="sys-row"><strong>应用持久化数据目录:</strong> {sysStatus.data_dir}</div>
                  <div className="sys-row"><strong>当前日志输出级别:</strong> {sysStatus.log_level}</div>
                </div>
              )}
            </div>
          )}

          {/* 9. Live Logs */}
          {activeTab === "logs" && <LiveLogPanel userID={user?.id ?? ""} role={user?.role ?? ""} />}


          </LazyContent>
        </main>
      </div>
    </div>
  );
};

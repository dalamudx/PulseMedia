import React, { useState, useEffect } from "react";
import { Key, Radio, Download, Tv, Terminal } from "lucide-react";
import { api } from "@/api/client";
import { DeliveryPanel } from "@/features/configuration/DeliveryPanel";
import { WebPushPanel } from "@/features/configuration/WebPushPanel";
import { EndpointConfigurationPanel } from "@/features/configuration/EndpointConfigurationPanel";
import {LibraryRootsPanel} from "@/features/configuration/LibraryRootsPanel";
import { TMDBSettingsPanel } from "@/features/configuration/TMDBSettingsPanel";
import { PolicyPanel } from "@/features/configuration/PolicyPanel";
import { NamingTemplatesPanel } from "@/features/configuration/NamingTemplatesPanel";
import { LiveLogPanel } from "@/features/configuration/LiveLogPanel";
import { DoubanSyncSettingsPanel } from "@/features/configuration/DoubanSyncSettingsPanel";
import { CheckinSettingsPanel } from "@/features/configuration/CheckinSettingsPanel";
import { CookieCloudSettingsPanel } from "@/features/configuration/CookieCloudSettingsPanel";
import { SeedingSettingsPanel } from "@/features/configuration/SeedingSettingsPanel";
import {FanartSettingsPanel} from "@/features/configuration/FanartSettingsPanel";
import {AuditTimelinePanel} from "@/features/configuration/AuditTimelinePanel";
import { useAuth } from "@/features/auth/AuthContext";
import type { paths } from "@/api/client";

type SystemStatus = paths["/api/v1/system/status"]["get"]["responses"]["200"]["content"]["application/json"];

export const SettingsView: React.FC = () => {
  const { user } = useAuth();
  const [activeTab, setActiveTab] = useState<
    "fanart" | "audit" | "webpush" | "cookiecloud" | "seeding" | "roots" | "tmdb" | "douban" | "checkin" | "downloader" | "mediaserver" | "system" | "logs" | "policy" | "deliveries"
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
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "checkin" ? "active" : ""}`} onClick={() => setActiveTab("checkin")}><Radio size={16} /><span>站点定时签到</span></button>}
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "cookiecloud" ? "active" : ""}`} onClick={() => setActiveTab("cookiecloud")}><Key size={16}/><span>CookieCloud同步</span></button>}
          {user?.role === "admin" && <button className={`nav-tab-btn ${activeTab === "seeding" ? "active" : ""}`} onClick={() => setActiveTab("seeding")}><Download size={16}/><span>做种策略</span></button>}
          <button className={`nav-tab-btn ${activeTab === "downloader" ? "active" : ""}`} onClick={() => setActiveTab("downloader")}>
            <Download size={16} />
            <span>下载器配置</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "mediaserver" ? "active" : ""}`} onClick={() => setActiveTab("mediaserver")}>
            <Tv size={16} />
            <span>媒体服务器</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "policy" ? "active" : ""}`} onClick={() => setActiveTab("policy")}><Terminal size={16} /><span>质量与过滤策略</span></button>
          <button className={`nav-tab-btn ${activeTab === "webpush" ? "active" : ""}`} onClick={() => setActiveTab("webpush")}><Radio size={16}/><span>浏览器通知</span></button>
          {user?.role==="admin"&&<button className={`nav-tab-btn ${activeTab==="deliveries"?"active":""}`} onClick={()=>setActiveTab("deliveries")}><Radio size={16}/><span>通知与投递</span></button>}
          <button className={`nav-tab-btn ${activeTab === "roots" ? "active" : ""}`} onClick={()=>setActiveTab("roots")}><Tv size={16}/><span>媒体根与基线</span></button>
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
          {activeTab === "fanart" && user?.role === "admin" && <FanartSettingsPanel />}
          {activeTab === "audit" && user?.role === "admin" && <AuditTimelinePanel />}
          {activeTab === "roots" && <LibraryRootsPanel />}
          {activeTab === "policy" && <><PolicyPanel /><NamingTemplatesPanel /></>}
          {activeTab === "webpush" && <WebPushPanel /> }
        {activeTab==="deliveries"&&user?.role==="admin"&&<DeliveryPanel/>}
          {/* 1. TMDB */}
          {activeTab === "tmdb" && <TMDBSettingsPanel />}

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


        </main>
      </div>
    </div>
  );
};

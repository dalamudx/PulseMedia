import React, { useState, useEffect } from "react";
import { Key, Radio, Download, Tv, Terminal } from "lucide-react";
import { api } from "@/api/client";
import { DeliveryPanel } from "@/features/configuration/DeliveryPanel";
import { EndpointConfigurationPanel } from "@/features/configuration/EndpointConfigurationPanel";
import {LibraryRootsPanel} from "@/features/configuration/LibraryRootsPanel";
import { TMDBSettingsPanel } from "@/features/configuration/TMDBSettingsPanel";
import { PolicyPanel } from "@/features/configuration/PolicyPanel";
import { LiveLogPanel } from "@/features/configuration/LiveLogPanel";
import { useToast } from "@/features/toast/ToastContext";
import { useAuth } from "@/features/auth/AuthContext";
import type { paths } from "@/api/client";

type SystemStatus = paths["/api/v1/system/status"]["get"]["responses"]["200"]["content"]["application/json"];

export const SettingsView: React.FC = () => {
  const toast = useToast();
  const { user } = useAuth();
  const [activeTab, setActiveTab] = useState<
    "roots" | "tmdb" | "douban" | "downloader" | "mediaserver" | "system" | "logs" | "policy" | "deliveries"
  >("tmdb");


  // Douban
  const [doubanUID, setDoubanUID] = useState("");
  const [syncingDouban, setSyncingDouban] = useState(false);


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



  const syncDoubanNow = async () => {
    if (!doubanUID.trim()) {
      toast.error("请输入豆瓣用户 ID");
      return;
    }
    setSyncingDouban(true);
    try {
      const { data, error } = await api.POST("/api/v1/sync/douban", {
        body: { douban_uid: doubanUID.trim() },
      });
      if (error) {
        toast.error("豆瓣同步失败", error.message || "请求异常");
        return;
      }
      toast.success("豆瓣想看同步完成", data?.message || "已成功同步想看影片并转入追更订阅！");
    } catch {
      toast.error("同步异常", "网络请求失败");
    } finally {
      setSyncingDouban(false);
    }
  };


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
          <button className={`nav-tab-btn ${activeTab === "downloader" ? "active" : ""}`} onClick={() => setActiveTab("downloader")}>
            <Download size={16} />
            <span>下载器配置</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "mediaserver" ? "active" : ""}`} onClick={() => setActiveTab("mediaserver")}>
            <Tv size={16} />
            <span>媒体服务器</span>
          </button>
          <button className={`nav-tab-btn ${activeTab === "policy" ? "active" : ""}`} onClick={() => setActiveTab("policy")}><Terminal size={16} /><span>质量与过滤策略</span></button>
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
          {activeTab === "roots" && <LibraryRootsPanel />}
          {activeTab === "policy" && <PolicyPanel />}
        {activeTab==="deliveries"&&user?.role==="admin"&&<DeliveryPanel/>}
          {/* 1. TMDB */}
          {activeTab === "tmdb" && <TMDBSettingsPanel />}

          {/* 2. Douban */}
          {activeTab === "douban" && (
            <div className="card">
              <div className="card-header">
                <h3>豆瓣「想看」/「在看」自动同步追更订阅</h3>
              </div>
              <p className="card-desc">绑定豆瓣账号后，系统将自动周期同步您的「想看」电影与电视剧，全自动转入 PulseMedia 发起追更下载。</p>
              <div className="form">
                <div className="form-group">
                  <label>豆瓣用户 ID / 个人主页唯一域名</label>
                  <input
                    type="text"
                    placeholder="例如: 123456789 或 your_douban_id..."
                    value={doubanUID}
                    onChange={(e) => setDoubanUID(e.target.value)}
                  />
                </div>
                <div className="form-actions">
                  <button className="btn-primary" onClick={syncDoubanNow} disabled={syncingDouban}>
                    {syncingDouban ? "正在同步想看列表中..." : "⚡ 立即同步豆瓣想看列表"}
                  </button>
                </div>
              </div>
            </div>
          )}

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

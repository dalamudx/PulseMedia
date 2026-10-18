import React, { useState, useEffect } from "react";
import { Key, Radio, Download, Tv, Terminal } from "lucide-react";
import { api } from "@/api/client";
import { DeliveryPanel } from "@/features/configuration/DeliveryPanel";
import {LibraryRootsPanel} from "@/features/configuration/LibraryRootsPanel";
import { TMDBSettingsPanel } from "@/features/configuration/TMDBSettingsPanel";
import { PolicyPanel } from "@/features/configuration/PolicyPanel";
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

  // Download Clients
  const [downloadClients, setDownloadClients] = useState<any[]>([]);
  const [savingDl, setSavingDl] = useState(false);
  const [testingDlId, setTestingDlId] = useState<string | null>(null);
  const [testResultDl, setTestResultDl] = useState<Record<string, string>>({});
  const [dlForm, setDlForm] = useState({
    client_type: "qbittorrent",
    name: "",
    url: "",
    username: "",
    password: "",
  });

  const [editingDlId, setEditingDlId] = useState<string | null>(null);
  const [editingDlUpdatedAt, setEditingDlUpdatedAt] = useState<string | undefined>(undefined);
  const [editingMsId, setEditingMsId] = useState<string | null>(null);
  const [editingMsUpdatedAt, setEditingMsUpdatedAt] = useState<string | undefined>(undefined);
  // Media Servers
  const [mediaServers, setMediaServers] = useState<any[]>([]);
  const [savingMs, setSavingMs] = useState(false);
  const [testingMsId, setTestingMsId] = useState<string | null>(null);
  const [testResultMs, setTestResultMs] = useState<Record<string, string>>({});
  const [msForm, setMsForm] = useState({
    server_type: "jellyfin",
    name: "",
    url: "",
    api_key: "",
  });

  // System Status & Logs
  const [sysStatus, setSysStatus] = useState<SystemStatus | null>(null);
  const [logLines, setLogLines] = useState<string[]>([]);
  const [sseConnected, setSseConnected] = useState(false);

  useEffect(() => {
    api.GET("/api/v1/system/status").then(({ data }) => {
      if (data) setSysStatus(data);
    });
    loadDownloadClients();
    loadMediaServers();

    if (user?.role !== "admin") return;
    const es = new EventSource("/api/v1/events");
    es.onopen = () => setSseConnected(true);
    es.onerror = () => setSseConnected(false);
    es.onmessage = (e) => {
      try {
        setLogLines((prev) => [...prev.slice(-200), `[${new Date().toLocaleTimeString()}] ${e.data}`]);
      } catch {}
    };
    return () => es.close();
  }, [user?.role]);

  const loadDownloadClients = async () => {
    const { data } = await api.GET("/api/v1/download-clients");
    if (data?.items) setDownloadClients(data.items);
  };

  const loadMediaServers = async () => {
    const { data } = await api.GET("/api/v1/media-servers");
    if (data?.items) setMediaServers(data.items);
  };


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

  const saveDlClient = async (e: React.FormEvent) => {
    e.preventDefault();
    setSavingDl(true);
    try {
      if (editingDlId) {
        const { data, error } = await api.PUT("/api/v1/download-clients/{id}", {
          params: { path: { id: editingDlId } },
          body: {
            name: dlForm.name,
            client_type: dlForm.client_type as "qbittorrent",
            url: dlForm.url,
            expected_updated_at: editingDlUpdatedAt,
            username: dlForm.username,
            password: dlForm.password,
          },
        });
        if (error) {
          toast.error("更新下载器失败", error.message);
          return;
        }
        toast.success("下载器配置已更新！", data?.name);
        setEditingDlId(null);
      } else {
        const { data, error } = await api.POST("/api/v1/download-clients", {
          body: {
            name: dlForm.name,
            client_type: dlForm.client_type as "qbittorrent",
            url: dlForm.url,
            username: dlForm.username,
            password: dlForm.password,
          },
        });
        if (error) {
          toast.error("添加下载器失败", error.message);
          return;
        }
        toast.success("下载器添加成功！", data?.name);
      }
      setDlForm({ client_type: "qbittorrent", name: "", url: "", username: "", password: "" });
      await loadDownloadClients();
    } finally {
      setSavingDl(false);
    }
  };

  const startEditDl = (c: any) => {
    setEditingDlId(c.id);
    setEditingDlUpdatedAt(c.updated_at);
    setDlForm({
      client_type: c.client_type,
      name: c.name,
      url: c.url,
      username: c.username,
      password: "",
    });
  };

  const cancelEditDl = () => {
    setEditingDlId(null);
    setDlForm({ client_type: "qbittorrent", name: "", url: "", username: "", password: "" });
  };

  const testDlClient = async (id: string) => {
    setTestingDlId(id);
    setTestResultDl((prev) => ({ ...prev, [id]: "正在连接下载器..." }));
    try {
      const { data, error } = await api.POST("/api/v1/download-clients/{id}/test", {
        params: { path: { id } },
      });
      if (error) {
        setTestResultDl((prev) => ({ ...prev, [id]: "❌ 探针失败: " + error.message }));
        toast.error("连接失败", error.message);
        return;
      }
      if (data) {
        if (data.alive) {
          setTestResultDl((prev) => ({ ...prev, [id]: "🟢 连通正常 (" + data.message + ")" }));
          toast.success("下载器连通正常", data.message);
        } else {
          setTestResultDl((prev) => ({ ...prev, [id]: "🔴 连接失败: " + data.message }));
          toast.error("连接失败", data.message);
        }
      }
    } finally {
      setTestingDlId(null);
    }
  };

  const deleteDlClient = async (id: string) => {
    if (!confirm("确定要删除该下载器吗？")) return;
    try {
      const { error } = await api.DELETE("/api/v1/download-clients/{id}", {
        params: { path: { id } },
      });
      if (error) {
        toast.error("无法删除下载器", error.message || "下载器仍被任务或订阅使用；请先处理关联对象。");
        return;
      }
      toast.success("下载器已删除");
      await loadDownloadClients();
    } catch {
      toast.error("删除失败", "连接异常，请稍后重试。");
    }
  };

  const saveMsServer = async (e: React.FormEvent) => {
    e.preventDefault();
    setSavingMs(true);
    try {
      if (editingMsId) {
        const { data, error } = await api.PUT("/api/v1/media-servers/{id}", {
          params: { path: { id: editingMsId } },
          body: {
            name: msForm.name,
            server_type: msForm.server_type as "jellyfin",
            url: msForm.url,
            expected_updated_at: editingMsUpdatedAt,
            api_key: msForm.api_key,
          },
        });
        if (error) {
          toast.error("更新媒体服务器失败", error.message);
          return;
        }
        toast.success("媒体服务器配置已更新！", data?.name);
        setEditingMsId(null);
      } else {
        const { data, error } = await api.POST("/api/v1/media-servers", {
          body: {
            name: msForm.name,
            server_type: msForm.server_type as "jellyfin",
            url: msForm.url,
            api_key: msForm.api_key,
          },
        });
        if (error) {
          toast.error("添加媒体服务器失败", error.message);
          return;
        }
        toast.success("媒体服务器添加成功！", data?.name);
      }
      setMsForm({ server_type: "jellyfin", name: "", url: "", api_key: "" });
      await loadMediaServers();
    } finally {
      setSavingMs(false);
    }
  };

  const startEditMs = (s: any) => {
    setEditingMsId(s.id);
    setEditingMsUpdatedAt(s.updated_at);
    setMsForm({
      server_type: s.server_type,
      name: s.name,
      url: s.url,
      api_key: "",
    });
  };

  const cancelEditMs = () => {
    setEditingMsId(null);
    setMsForm({ server_type: "jellyfin", name: "", url: "", api_key: "" });
  };

  const testMsServer = async (id: string) => {
    setTestingMsId(id);
    setTestResultMs((prev) => ({ ...prev, [id]: "正在连接媒体服务器..." }));
    try {
      const { data, error } = await api.POST("/api/v1/media-servers/{id}/test", {
        params: { path: { id } },
      });
      if (error) {
        setTestResultMs((prev) => ({ ...prev, [id]: "❌ 探针失败: " + error.message }));
        toast.error("连接失败", error.message);
        return;
      }
      if (data) {
        if (data.alive) {
          setTestResultMs((prev) => ({ ...prev, [id]: "🟢 连通正常 (" + data.message + ")" }));
          toast.success("媒体服务器连通正常", data.message);
        } else {
          setTestResultMs((prev) => ({ ...prev, [id]: "🔴 连接失败: " + data.message }));
          toast.error("连接失败", data.message);
        }
      }
    } finally {
      setTestingMsId(null);
    }
  };

  const deleteMsServer = async (id: string) => {
    if (!confirm("确定要删除该媒体服务器吗？")) return;
    try {
      const { error } = await api.DELETE("/api/v1/media-servers/{id}", {
        params: { path: { id } },
      });
      if (error) {
        toast.error("无法删除媒体服务器", error.message || "请检查关联任务后重试。");
        return;
      }
      toast.success("媒体服务器已删除");
      await loadMediaServers();
    } catch {
      toast.error("删除失败", "连接异常，请稍后重试。");
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
          {activeTab === "downloader" && (
            <div className="card">
              <div className="card-header">
                <h3>下载器配置 (qBittorrent / Transmission)</h3>
                <button className="btn-sm btn-secondary" onClick={loadDownloadClients}>刷新列表</button>
              </div>
              <p className="card-desc">管理对接的下载器。支持 qBittorrent 与 Transmission 双客户端，支持任务自动分流与丢响应安全对账。</p>

              {downloadClients.length > 0 ? (
                <div className="integration-list">
                  {downloadClients.map((c) => (
                    <div key={c.id} className="integration-row">
                      <div>
                        <strong>{c.name}</strong> <span className="badge client-badge">{c.client_type}</span>
                        <div className="item-meta">{c.url} · 用户名: {c.username}</div>
                        {testResultDl[c.id] && <div className="test-tip">{testResultDl[c.id]}</div>}
                      </div>
                      <div className="item-actions">
                        <button
                          className="btn-sm btn-primary"
                          disabled={testingDlId === c.id}
                          onClick={() => testDlClient(c.id)}
                        >
                          {testingDlId === c.id ? "测试中..." : "测试连接"}
                        </button>
                        <button className="btn-sm btn-secondary" onClick={() => startEditDl(c)}>编辑</button>
                        <button className="btn-sm btn-danger" onClick={() => deleteDlClient(c.id)}>删除</button>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="empty-hint">暂无配置下载器，请在下方添加。</div>
              )}

              <h4 className="sub-heading">{editingDlId ? `编辑下载器：${dlForm.name}` : "添加下载器"}</h4>
              <form onSubmit={saveDlClient} className="form-grid">
                <div className="form-group">
                  <label>下载器类型</label>
                  <select
                    value={dlForm.client_type}
                    onChange={(e) => setDlForm({ ...dlForm, client_type: e.target.value })}
                  >
                    <option value="qbittorrent">qBittorrent</option>
                    <option value="transmission">Transmission</option>
                  </select>
                </div>
                <div className="form-group">
                  <label>下载器名称</label>
                  <input
                    type="text"
                    required
                    placeholder="例如: qBittorrent Primary"
                    value={dlForm.name}
                    onChange={(e) => setDlForm({ ...dlForm, name: e.target.value })}
                  />
                </div>
                <div className="form-group">
                  <label>Web UI 地址</label>
                  <input
                    type="url"
                    required
                    placeholder="例如: http://192.168.1.100:8080 或 9091"
                    value={dlForm.url}
                    onChange={(e) => setDlForm({ ...dlForm, url: e.target.value })}
                  />
                </div>
                <div className="form-group">
                  <label>Web 账号</label>
                  <input
                    type="text"
                    required
                    placeholder="admin"
                    value={dlForm.username}
                    onChange={(e) => setDlForm({ ...dlForm, username: e.target.value })}
                  />
                </div>
                <div className="form-group">
                  <label>{editingDlId ? "Web 密码（留空保留已有值）" : "Web 密码"}</label>
                  <input
                    type="password"
                    required={!editingDlId}
                    autoComplete="new-password"
                    placeholder={editingDlId ? "留空保留已有密码" : "输入下载器密码"}
                    value={dlForm.password}
                    onChange={(e) => setDlForm({ ...dlForm, password: e.target.value })}
                  />
                </div>
                <div className="form-actions-full">
                  <button type="submit" className="btn-primary" disabled={savingDl}>
                    {savingDl ? "保存中..." : editingDlId ? "保存下载器修改" : "保存并启用下载器"}
                  </button>
                  {editingDlId && (
                    <button type="button" className="btn-secondary" onClick={cancelEditDl}>
                      取消编辑
                    </button>
                  )}
                </div>
              </form>
            </div>
          )}

          {/* 4. Media Server */}
          {activeTab === "mediaserver" && (
            <div className="card">
              <div className="card-header">
                <h3>媒体服务器配置 (Jellyfin / Plex / Emby)</h3>
                <button className="btn-sm btn-secondary" onClick={loadMediaServers}>刷新列表</button>
              </div>
              <p className="card-desc">文件整理入库完成后，自动调用媒体服务器 API 精准刷新对应影视库目录与刮削事实。</p>

              {mediaServers.length > 0 ? (
                <div className="integration-list">
                  {mediaServers.map((s) => (
                    <div key={s.id} className="integration-row">
                      <div>
                        <strong>{s.name}</strong> <span className="badge server-badge">{s.server_type}</span>
                        <span className={`badge ${s.health}`}>{s.health}</span>
                        <div className="item-meta">{s.url}</div>
                        {testResultMs[s.id] && <div className="test-tip">{testResultMs[s.id]}</div>}
                      </div>
                      <div className="item-actions">
                        <button
                          className="btn-sm btn-primary"
                          disabled={testingMsId === s.id}
                          onClick={() => testMsServer(s.id)}
                        >
                          {testingMsId === s.id ? "测试中..." : "测试连接"}
                        </button>
                        <button className="btn-sm btn-secondary" onClick={() => startEditMs(s)}>编辑</button>
                        <button className="btn-sm btn-danger" onClick={() => deleteMsServer(s.id)}>删除</button>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="empty-hint">暂无配置媒体服务器，请在下方添加。</div>
              )}

              <h4 className="sub-heading">{editingMsId ? `编辑媒体服务器：${msForm.name}` : "添加媒体服务器"}</h4>
              <form onSubmit={saveMsServer} className="form-grid">
                <div className="form-group">
                  <label>服务器类型</label>
                  <select
                    value={msForm.server_type}
                    onChange={(e) => setMsForm({ ...msForm, server_type: e.target.value })}
                  >
                    <option value="jellyfin">Jellyfin</option>
                    <option value="plex">Plex</option>
                    <option value="emby">Emby</option>
                  </select>
                </div>
                <div className="form-group">
                  <label>服务器名称</label>
                  <input
                    type="text"
                    required
                    placeholder="例如: Jellyfin Local"
                    value={msForm.name}
                    onChange={(e) => setMsForm({ ...msForm, name: e.target.value })}
                  />
                </div>
                <div className="form-group">
                  <label>服务地址 URL</label>
                  <input
                    type="url"
                    required
                    placeholder="例如: http://192.168.1.100:8096 或 32400"
                    value={msForm.url}
                    onChange={(e) => setMsForm({ ...msForm, url: e.target.value })}
                  />
                </div>
                <div className="form-group">
                  <label>{editingMsId ? "API Key / Token（留空保留已有值）" : "API Key / Token"}</label>
                  <input
                    type="password"
                    required={!editingMsId}
                    autoComplete="new-password"
                    placeholder={editingMsId ? "留空保留已有凭据" : "Jellyfin API Key 或 X-Plex-Token"}
                    value={msForm.api_key}
                    onChange={(e) => setMsForm({ ...msForm, api_key: e.target.value })}
                  />
                </div>
                <div className="form-actions-full">
                  <button type="submit" className="btn-primary" disabled={savingMs}>
                    {savingMs ? "保存中..." : editingMsId ? "保存媒体服务器修改" : "保存并启用媒体服务器"}
                  </button>
                  {editingMsId && (
                    <button type="button" className="btn-secondary" onClick={cancelEditMs}>
                      取消编辑
                    </button>
                  )}
                </div>
              </form>
            </div>
          )}

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
          {activeTab === "logs" && (
            <div className="card logs-card">
              <div className="card-header">
                <div className="logs-header-left">
                  <h3>📋 实时系统运行日志 (Live Log Viewer)</h3>
                  <span className={`badge ${sseConnected ? "online" : "offline"}`}>
                    {sseConnected ? "🟢 实时推送中" : "⚠️ 离线"}
                  </span>
                </div>
                <div className="logs-actions">
                  <button className="btn-sm btn-secondary" onClick={() => setLogLines([])}>清空屏幕</button>
                </div>
              </div>

              <div className="terminal-box">
                {logLines.length === 0 ? (
                  <div className="log-empty">正在等待系统日志输出...</div>
                ) : (
                  logLines.map((line, idx) => (
                    <div key={idx} className="log-line">
                      {line}
                    </div>
                  ))
                )}
              </div>
            </div>
          )}


        </main>
      </div>
    </div>
  );
};

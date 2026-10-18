import React, { useState, useEffect } from "react";
import { Zap, RefreshCw, Trash2, CheckCircle, Plus, Loader2, Sparkles } from "lucide-react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { RSSConfigEditor } from "@/features/configuration/RSSConfigEditor";
import { useToast } from "@/features/toast/ToastContext";
import type { paths } from "@/api/client";

type IndexerItem = paths["/api/v1/indexers"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];

export const SitesView: React.FC = () => {
  const toast = useToast();
  const { user } = useAuth();
  const [rssIndexerId, setRSSIndexerId] = useState<string | null>(null);
  const [indexers, setIndexers] = useState<IndexerItem[]>([]);
  const [loading, setLoading] = useState(false);
const [listError,setListError]=useState("");
  const [saving, setSaving] = useState(false);
  const [testingId, setTestingId] = useState<string | null>(null);
  const [testingAll, setTestingAll] = useState(false);
  const [testResults, setTestResults] = useState<Record<string, string>>({});
  const [latencyMap, setLatencyMap] = useState<Record<string, number>>({});

  const [form, setForm] = useState({
    site_type: "nexusphp" as "nexusphp" | "dmhy" | "torznab",
    name: "",
    url: "",
    api_key: "",
    passkey: "",
  });
  const [selectedPreset, setSelectedPreset] = useState("");
  const [editingSiteId, setEditingSiteId] = useState<string | null>(null);
  const [editingSiteVersion, setEditingSiteVersion] = useState<number | undefined>(undefined);

  const startEditSite = (idx: IndexerItem) => {
    setEditingSiteId(idx.id);
    setEditingSiteVersion(idx.config_version);
    const isPublic = idx.url.includes("dmhy") || idx.url.includes("mikan") || idx.url.includes("bangumi");
    const isTorznab = idx.url.includes("9696") || idx.url.includes("9117") || idx.url.includes("torznab");
    const sType = isPublic ? "dmhy" : isTorznab ? "torznab" : "nexusphp";
    setForm({
      site_type: sType,
      name: idx.name,
      url: idx.url,
      api_key: "",
      passkey: "",
    });
    document.querySelector(".form-card")?.scrollIntoView({ behavior: "smooth" });
  };

  const cancelEditSite = () => {
    setEditingSiteId(null);
    setForm({ site_type: "nexusphp", name: "", url: "", api_key: "", passkey: "" });
  };

  const loadIndexers = async () => {
    setLoading(true);setListError("");setLatencyMap({});setTestResults({});
    try {
      const { data, error } = await api.GET("/api/v1/indexers",{cache:"no-store"});
      if (error) {
        setIndexers([]);setListError("站点状态读取失败，未沿用旧的在线观测。请重试刷新。");
        return;
      }
      setIndexers(data?.items || []);
    } catch {setIndexers([]);setListError("站点状态连接失败，未沿用旧观测。请重试刷新。");} finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadIndexers();
  }, []);

  const applyPreset = (preset: string) => {
    setSelectedPreset(preset);
    switch (preset) {
      case "dmhy":
        setForm({
          site_type: "dmhy",
          name: "动漫花园 (DMHY)",
          url: "https://share.dmhy.org",
          api_key: "public",
          passkey: "",
        });
        break;
      case "mikan":
        setForm({
          site_type: "dmhy",
          name: "蜜柑计划 (Mikan)",
          url: "https://mikanani.me",
          api_key: "public",
          passkey: "",
        });
        break;
      case "bangumi_moe":
        setForm({
          site_type: "dmhy",
          name: "萌番组 (Bangumi.moe)",
          url: "https://bangumi.moe",
          api_key: "public",
          passkey: "",
        });
        break;
      case "acg_rip":
        setForm({
          site_type: "dmhy",
          name: "ACG.RIP",
          url: "https://acg.rip",
          api_key: "public",
          passkey: "",
        });
        break;
      case "custom_rss":
        setForm({
          site_type: "dmhy",
          name: "自定义公开 RSS 检索源",
          url: "https://your-anime-site.com/rss?q={query}",
          api_key: "public",
          passkey: "",
        });
        break;
      case "nyaa":
        setForm({
          site_type: "dmhy",
          name: "Nyaa (动漫BT)",
          url: "https://nyaa.si",
          api_key: "public",
          passkey: "",
        });
        break;
      case "springwood":
        setForm({
          site_type: "nexusphp",
          name: "SpringWood",
          url: "https://springwood.me",
          api_key: "",
          passkey: "",
        });
        break;
      case "mteam":
        setForm({
          site_type: "nexusphp",
          name: "M-Team (馒头)",
          url: "https://kp.m-team.cc",
          api_key: "",
          passkey: "",
        });
        break;
      case "hdsky":
        setForm({
          site_type: "nexusphp",
          name: "HDSky (天空)",
          url: "https://hdsky.me",
          api_key: "",
          passkey: "",
        });
        break;
      case "pthome":
        setForm({
          site_type: "nexusphp",
          name: "PTHome (家)",
          url: "https://pthome.net",
          api_key: "",
          passkey: "",
        });
        break;
      case "hhanclub":
        setForm({
          site_type: "nexusphp",
          name: "HhanClub (憨憨)",
          url: "https://hhanclub.top",
          api_key: "",
          passkey: "",
        });
        break;
      case "audiences":
        setForm({
          site_type: "nexusphp",
          name: "Audiences (观众)",
          url: "https://audiences.me",
          api_key: "",
          passkey: "",
        });
        break;
      case "prowlarr":
        setForm({
          site_type: "torznab",
          name: "Prowlarr 聚合器",
          url: "http://192.168.1.100:9696/1/api",
          api_key: "",
          passkey: "",
        });
        break;
      case "jackett":
        setForm({
          site_type: "torznab",
          name: "Jackett 聚合器",
          url: "http://192.168.1.100:9117/api/v2.0/indexers/all/results/torznab",
          api_key: "",
          passkey: "",
        });
        break;
      default:
        break;
    }
  };

  const createIndexer = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      if (form.site_type === "nexusphp" && form.passkey.trim() && !form.api_key.trim()) {
        toast.error("请输入完整 Cookie", "更新 Passkey 时必须同时输入完整 Cookie，不能与未回显的旧凭据拼接。");
        return;
      }
      let finalKey = form.api_key.trim();
      if (form.site_type === "dmhy") {
        finalKey = "public";
      } else if (form.site_type === "nexusphp" && form.passkey.trim()) {
        finalKey = `${finalKey}; passkey=${form.passkey.trim()}`;
      }

      if (editingSiteId) {
        const { data, error } = await api.PUT("/api/v1/indexers/{id}", {
          params: { path: { id: editingSiteId } },
          body: {
            name: form.name.trim(),
            url: form.url.trim(),
            expected_version: editingSiteVersion,
            api_key: finalKey,
          },
        });
        if (error) {
          toast.error("更新站点失败", error.message || "未知错误");
          return;
        }
        toast.success(`站点「${data?.name}」配置已更新！`);
        setEditingSiteId(null);
      } else {
        const { data, error } = await api.POST("/api/v1/indexers", {
          body: {
            name: form.name.trim(),
            url: form.url.trim(),
            api_key: finalKey || "public",
            categories: [2000, 5000],
          },
        });
        if (error) {
          toast.error("添加站点失败", error.message || "未知错误");
          return;
        }
        toast.success(`站点「${data?.name}」添加成功！`);
      }
      setForm({ site_type: "nexusphp", name: "", url: "", api_key: "", passkey: "" });
      await loadIndexers();
    } catch {
      toast.error("保存结果未确认", "请刷新列表确认状态，勿重复添加站点。输入尚未清除。");
    } finally {
      setSaving(false);
    }
  };

  const testIndexer = async (id: string) => {
    setTestingId(id);
    setTestResults((prev) => ({ ...prev, [id]: "正在发送连通探针..." }));
    try {
      const { data, error } = await api.POST("/api/v1/indexers/{id}/test", {
        params: { path: { id } },
      });
      if (error) {
        setTestResults((prev) => ({ ...prev, [id]: "❌ 探针失败: " + error.message }));
        toast.error("测速失败", error.message);
        return;
      }
      if (data) {
        if (data.alive) {
          const lat = data.latency_ms ?? null;
          if (lat !== null) setLatencyMap((prev) => ({ ...prev, [id]: lat }));
          setTestResults((prev) => ({ ...prev, [id]: lat === null ? "连通正常（未返回耗时）" : `连通正常 (${lat}ms)` }));
          toast.success("站点连通正常", lat === null ? "探测成功，未返回耗时。" : `响应耗时 ${lat}ms`);
        } else {
          setTestResults((prev) => ({ ...prev, [id]: `🔴 不可用: ${data.message}` }));
          toast.error("站点不可用", data.message);
        }
      }
    } finally {
      setTestingId(null);
      await loadIndexers();
    }
  };

  const testAllIndexers = async () => {
    if (indexers.length === 0) return;
    setTestingAll(true);
    try {
      let count = 0;
      await Promise.all(
        indexers.map(async (idx) => {
          const { data } = await api.POST("/api/v1/indexers/{id}/test", {
            params: { path: { id: idx.id } },
          });
          if (data?.alive) {
            count++;
            const lat = data.latency_ms ?? null;
            if (lat !== null) setLatencyMap((prev) => ({ ...prev, [idx.id]: lat }));
            setTestResults((prev) => ({ ...prev, [idx.id]: lat === null ? "正常（未返回耗时）" : `正常 (${lat}ms)` }));
          } else {
            setTestResults((prev) => ({ ...prev, [idx.id]: `🔴 异常: ${data?.message || "离线"}` }));
          }
        })
      );
      toast.success("批量测速完成", `共测速 ${indexers.length} 个站点，${count} 个在线可用！`);
      await loadIndexers();
    } finally {
      setTestingAll(false);
    }
  };

  const deleteIndexer = async (id: string) => {
    if (!confirm("确定要删除该站点吗？")) return;
    const { error } = await api.DELETE("/api/v1/indexers/{id}", {
      params: { path: { id } },
    });
    if (error) {
      toast.error("删除失败", error.message);
    } else {
      toast.info("站点已删除");
      setIndexers((prev) => prev.filter((i) => i.id !== id));
    }
  };

  const checkinSite = async (idxId: string, siteName: string) => {
    try {
      const { data, error } = await api.POST("/api/v1/indexers/{id}/checkin", {
        params: { path: { id: idxId } },
      });
      if (error) {
        toast.error("签到失败", error.message || "请求异常");
        return;
      }
      toast.success("站点签到完成", data?.message || `站点「${siteName}」连续签到活跃状态已刷新！`);
    } catch {
      toast.error("签到异常", "网络请求失败");
    }
  };

  const getLatencyClass = (lat: number) => {
    if (lat < 100) return "lat-fast";
    if (lat < 300) return "lat-medium";
    return "lat-slow";
  };

  const getProtocolBadge = (idx: IndexerItem) => {
    const url = idx.url.toLowerCase();
    if (url.includes("dmhy.org") || url.includes("mikanani.me") || url.includes("bangumi.moe") || url.includes("nyaa.si") || url.includes("acg.rip") || url.includes("{query}")) {
      return { label: "RSS 地址特征（待确认）", cls: "protocol-pill badge-dmhy" };
    }
    if (url.includes("9696") || url.includes("9117") || url.includes("torznab")) {
      return { label: "Torznab 地址特征（待确认）", cls: "protocol-pill badge-torznab" };
    }
    return { label: "协议未确认", cls: "protocol-pill" };
  };

  return (
    <div className="sites-container">
      <div className="page-header">
        <div>
          <h2>站点与索引器管理 (Indexers)</h2>
          <p className="subtitle">
            支持国内 NexusPHP 私有 PT 站（Cookie 网页爬虫直连）、DMHY 动漫花园公开免登 RSS、与 Prowlarr/Jackett 聚合器协议
          </p>
        </div>
        <div className="header-actions">
          <button className="btn-primary" disabled={testingAll} onClick={testAllIndexers}>
            {testingAll ? <Loader2 size={16} className="spinner-icon" /> : <Zap size={16} />}
            <span>{testingAll ? "正在批量测速..." : "⚡ 一键测速全部站点"}</span>
          </button>
        </div>
      </div>

      {/* Indexers List Card */}
      <div className="card">
        <div className="card-header">
          <h3>已配置做种与检索站点 (共 {indexers.length} 个)</h3>
          <button className="btn-sm btn-secondary" disabled={loading} onClick={loadIndexers}>
            <RefreshCw size={13} />
            <span>刷新列表</span>
          </button>
        </div>

        {loading ? (
          <div className="loading-state">
            <Loader2 className="spinner-icon" size={24} />
            <p>加载站点列表中...</p>
          </div>
        ) : listError ? (<div role="alert" className="config-error">{listError}</div>) : indexers.length === 0 ? (
          <div className="empty-state">
            <p>暂无配置站点。可在下方选择「🌸 DMHY 动漫花园 (免登录)」、「🌱 NexusPHP 私有 PT 站」或「☁️ Prowlarr 聚合器」一键添加。</p>
          </div>
        ) : (
          <div className="indexers-table">
            {indexers.map((idx) => {
              const proto = getProtocolBadge(idx);
              const isPublic = idx.url.includes("dmhy") || idx.url.includes("mikan") || idx.url.includes("nyaa");

              return (
                <div key={idx.id} className="indexer-row">
                  <div className="idx-info">
                    <div className="idx-name-box">
                      <strong>{idx.name}</strong>
                      <span className={proto.cls}>{proto.label}</span>
                      <span className={`badge ${idx.health_checked_at?idx.health:"unknown"}`}>{!idx.health_checked_at?"尚未确认":idx.health==="online"?"最近观测在线":"最近观测离线"}</span>
                      {latencyMap[idx.id] && (
                        <span className={`latency-pill ${getLatencyClass(latencyMap[idx.id])}`}>
                          {latencyMap[idx.id]}ms
                        </span>
                      )}
                    </div>
                    <div className="idx-url">{idx.url}</div>
 <p className="config-description">{idx.health_checked_at?`观测：${new Date(idx.health_observed_at??idx.health_checked_at).toLocaleString()}`:"尚无当前配置的成功或失败观测"} · {idx.health_reason==="ok"?"探针成功":idx.health_reason==="rate_limited"?"端点限流，预算内等待":idx.health_reason==="unavailable"?"探针失败，按退避重试":"未检测"}</p>
 {idx.health_next_check_at&&<p className="config-description">下次健康检测：{new Date(idx.health_next_check_at).toLocaleString()}（仅已绑定活动订阅或启用 RSS，仍受共享预算约束）</p>}

                    {testResults[idx.id] && <div className="test-res">{testResults[idx.id]}</div>}
                  </div>

                  <div className="idx-actions">
                    {user?.role === "admin" && <button type="button" className="btn-sm btn-secondary" aria-expanded={rssIndexerId === idx.id} onClick={() => setRSSIndexerId(idx.id)}>RSS 配置</button>}
                    {!isPublic && (
                      <button
                        className="btn-sm btn-checkin"
                        title="模拟签到保号"
                        onClick={() => checkinSite(idx.id, idx.name)}
                      >
                        <CheckCircle size={13} />
                        <span>签到</span>
                      </button>
                    )}
                    <button className="btn-sm btn-secondary" onClick={() => startEditSite(idx)}>编辑</button>
                    <button
                      className="btn-sm btn-primary"
                      disabled={testingId === idx.id}
                      onClick={() => testIndexer(idx.id)}
                    >
                      {testingId === idx.id ? "测试中..." : "测试连接"}
                    </button>
                    <button
                      className="btn-sm btn-danger"
                      title="删除站点"
                      onClick={() => deleteIndexer(idx.id)}
                    >
                      <Trash2 size={13} />
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {rssIndexerId && indexers.some(item => item.id === rssIndexerId) && <RSSConfigEditor key={rssIndexerId} id={rssIndexerId} name={indexers.find(item => item.id === rssIndexerId)!.name} onClose={() => setRSSIndexerId(null)} />}

      {/* Add Indexer Form Card with Protocol Selector */}
      <div className="card form-card">
        <h3>
          <Plus size={18} />
          <span>{editingSiteId ? `编辑做种与检索站点：${form.name}` : "添加新做种与检索站点 (PT / DMHY / 聚合器)"}</span>
        </h3>
        <p className="card-desc">
          支持国内 NexusPHP 私有 PT 站（网页爬取/Cookie 直连，彻底免除搭建 Prowlarr）、DMHY 动漫花园公开动漫 RSS 免登直连、以及标准 Torznab 聚合器协议。
        </p>

        <form onSubmit={createIndexer} className="form">
          {/* Quick Preset Selector */}
          <div className="form-group">
            <label>快速导入常见站点预设模板 (可选)</label>
            <select
              value={selectedPreset}
              onChange={(e) => applyPreset(e.target.value)}
            >
              <option value="">-- 选择预设模板自动填充 --</option>
              <optgroup label="🌸 公开二次元动漫发布站 (免登录、免 Cookie、通用 RSS 检索)">
                <option value="mikan">🌸 蜜柑计划 (mikanani.me)</option>
                <option value="bangumi_moe">🌸 萌番组 (bangumi.moe)</option>
                <option value="dmhy">🌸 动漫花园 (share.dmhy.org)</option>
                <option value="nyaa">🌸 Nyaa (nyaa.si)</option>
                <option value="acg_rip">🌸 ACG.RIP (acg.rip)</option>
                <option value="custom_rss">🌸 自定义公开 RSS 检索源 (模板占位符)</option>
              </optgroup>
              <optgroup label="🌱 国内 NexusPHP 原生私有 PT 站 (Cookie 网页爬虫直连)">
                <option value="springwood">SpringWood (春天 PT)</option>
                <option value="mteam">M-Team (馒头 PT)</option>
                <option value="hdsky">HDSky (天空 PT)</option>
                <option value="pthome">PTHome (家 PT)</option>
                <option value="hhanclub">HhanClub (憨憨 PT)</option>
                <option value="audiences">Audiences (观众 PT)</option>
              </optgroup>
              <optgroup label="☁️ 聚合器协议 (多站集中管理)">
                <option value="prowlarr">Prowlarr 聚合器 (默认 9696)</option>
                <option value="jackett">Jackett 聚合器 (默认 9117)</option>
              </optgroup>
            </select>
          </div>

          {/* Architecture / Protocol Type Selector Bar */}
          <div className="form-group">
            <label>站点类型与接入架构 *</label>
            <div className="site-type-selector-bar">
              <button
                type="button"
                className={`site-type-btn ${form.site_type === "nexusphp" ? "active" : ""}`}
                onClick={() => setForm({ ...form, site_type: "nexusphp" })}
              >
                <span className="type-btn-title">🌱 NexusPHP 原生私有 PT 站</span>
                <span className="type-btn-desc">网页爬虫 / Cookie 鉴权直连 · 免装 Prowlarr</span>
              </button>

              <button
                type="button"
                className={`site-type-btn ${form.site_type === "dmhy" ? "active" : ""}`}
                onClick={() => {
                  setForm({
                    ...form,
                    site_type: "dmhy",
                    url: form.url || "https://share.dmhy.org",
                    name: form.name || "动漫花园 (DMHY)",
                    api_key: "public",
                  });
                }}
              >
                <span className="type-btn-title">🌸 通用公开 BT / 动漫 RSS 站</span>
                <span className="type-btn-desc">Mikan / Bangumi.moe / DMHY / Nyaa / 模板 · 免登统一接入</span>
              </button>

              <button
                type="button"
                className={`site-type-btn ${form.site_type === "torznab" ? "active" : ""}`}
                onClick={() => setForm({ ...form, site_type: "torznab" })}
              >
                <span className="type-btn-title">☁️ Torznab 聚合器协议</span>
                <span className="type-btn-desc">Prowlarr / Jackett API 协议规范</span>
              </button>
            </div>
          </div>

          {/* DMHY Info Box */}
          {form.site_type === "dmhy" && (
            <div className="dmhy-info-box">
              <Sparkles size={18} color="#ec4899" className="dmhy-icon" />
              <div>
                <strong>🌸 通用公开 BT / 动漫 RSS 检索引擎已激活 (Universal RSS Indexer)</strong>
                <p>
                  PulseMedia 内置通用 RSS 2.0 / Atom 声明式检索解析引擎，统一支持 <strong>蜜柑计划 (mikanani.me)</strong>、<strong>萌番组 (bangumi.moe)</strong>、<strong>动漫花园 (dmhy.org)</strong>、<strong>Nyaa</strong>、<strong>ACG.RIP</strong> 等任意公开二次元 BT 站点。
                  无需登录、免 Cookie、无分享率考核，输入站点网址或包含 <code>{"{query}"}</code> 的 RSS 检索模板即可<strong>全自动接入，无需修改任何代码</strong>！
                </p>
              </div>
            </div>
          )}

          {/* Common Inputs */}
          <div className="form-group">
            <label htmlFor="site-name">站点名称 *</label>
            <input id="site-name"
              type="text"
              required
              placeholder="例如: 动漫花园 (DMHY), SpringWood, Prowlarr..."
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>

          <div className="form-group">
            <label>
              {form.site_type === "dmhy"
                ? "公开动漫站或 RSS 检索模板 URL (支持填入 {query} 占位符) *"
                : form.site_type === "nexusphp"
                ? "PT 站点网页 URL 地址 *"
                : "Torznab API URL 地址 *"}
            </label>
            <input
              type="url"
              required
              placeholder={
                form.site_type === "dmhy"
                  ? "例如: https://share.dmhy.org 或 https://mikanani.me"
                  : form.site_type === "nexusphp"
                  ? "例如: https://springwood.me 或 https://hdsky.me"
                  : "例如: http://192.168.1.100:9696/1/api"
              }
              value={form.url}
              onChange={(e) => setForm({ ...form, url: e.target.value })}
            />
          </div>

          {/* Conditional Credentials Inputs */}
          {form.site_type === "nexusphp" && (
            <>
              <div className="form-group">
                <label htmlFor="site-credential">{editingSiteId ? "凭据（留空保留已有值）" : "登录 Cookie（必填）"}</label>
                <input
                  id="site-credential" type="password"
                  required={!editingSiteId}
                  autoComplete="new-password"
                  placeholder="输入站点的 Cookie (如 nexus_uid=xxx; nexus_pass=xxx)..."
                  value={form.api_key}
                  onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                />
                <span className="field-hint">凭据加密保存且不回显；留空修改名称/地址不会覆盖旧凭据。当前不自动同步 CookieCloud。</span>
              </div>

              <div className="form-group">
                <label>Passkey (选填，用于构造 download.php 下载直链)</label>
                <input
                  type="password"
                  placeholder="可选：输入站点个人 Passkey..."
                  value={form.passkey}
                  onChange={(e) => setForm({ ...form, passkey: e.target.value })}
                />
              </div>
            </>
          )}

          {form.site_type === "torznab" && (
            <div className="form-group">
              <label htmlFor="site-credential">{editingSiteId ? "Torznab API Key（留空保留）" : "Torznab API Key (Token) *"}</label>
              <input
                id="site-credential" type="password"
                required={!editingSiteId}
                autoComplete="new-password"
                placeholder="输入 Prowlarr 或 Jackett 提供的 API Key (将强加密落库)..."
                value={form.api_key}
                onChange={(e) => setForm({ ...form, api_key: e.target.value })}
              />
            </div>
          )}

          <div className="form-actions">
            <button type="submit" className="btn-primary" disabled={saving}>
              {saving ? "保存中..." : editingSiteId ? "保存站点修改" : "保存并启用站点"}
            </button>
            {editingSiteId && (
              <button type="button" className="btn-secondary" onClick={cancelEditSite}>
                取消编辑
              </button>
            )}
          </div>
        </form>
      </div>
    </div>
  );
};

import { useCallback, useEffect, useEffectEvent, useRef, useState, type FormEvent } from "react";
import { Loader2, Plus, RefreshCw, Trash2, Zap } from "lucide-react";
import { api, type paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { RSSConfigEditor } from "@/features/configuration/RSSConfigEditor";
import { SiteAccountStatsPanel } from "@/features/configuration/SiteAccountStatsPanel";
import { useActivityInvalidation } from "@/features/configuration/useActivityInvalidation";
import { parseSitePending, serializeSitePending, sitePendingStorageKey, type SitePendingRecord } from "@/features/configuration/managedSiteRecovery";
import {createSiteProbeBatch, type SiteProbeBatchSnapshot, type SiteProbeEntry} from "@/features/configuration/siteProbeBatch";
import "@/features/configuration/site-probes.css";

import { type IndexerTemplate, applyTemplateToDraft, groupTemplatesByProtocol } from "@/features/configuration/indexerTemplates";
type Site = paths["/api/v1/indexers"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type CreateBody = paths["/api/v1/indexers"]["post"]["requestBody"]["content"]["application/json"];
type UpdateBody = paths["/api/v1/indexers/{id}"]["put"]["requestBody"]["content"]["application/json"];
type Protocol = "torznab" | "nexusphp" | "rss";
type Command = { action: "create"; body: CreateBody } | { action: "update"; id: string; body: UpdateBody } | { action: "delete"; id: string; version: number };
type Pending = { record: SitePendingRecord; command?: Command; phase: "unknown" | "absent" | "confirmed" | "rejected" };
type Draft = { name: string; protocol: Protocol; url: string; credential: string; categories: string; keepURL: boolean; clearCredential: boolean };
const emptyDraft = (): Draft => ({ name: "", protocol: "torznab", url: "", credential: "", categories: "", keepURL: true, clearCredential: false });
const protocolNames = { torznab: "Torznab", nexusphp: "NexusPHP", rss: "RSS" };
const wrapActions = { flexWrap: "wrap", gap: "0.5rem" } as const;
const buttonStyle = { whiteSpace: "nowrap" } as const;

function safeError(status: number) {
  switch (status) {
    case 400: return "配置无效，请检查名称、完整 URL、协议与分类。";
    case 401: return "会话已失效，请重新登录当前账户后查询原命令回执。";
    case 403: return "当前账户无权执行此操作，请使用有权限的原账户。";
    case 404: return "站点不存在，请重新加载列表。";
    case 409: return "配置版本或命令键冲突。请重新加载最新配置，再人工核对修改。";
    case 503: return "服务暂时不可用，请稍后查询原命令回执。";
    default: return "请求结果未确认，请查询原命令回执。";
  }
}
function timestamp(value?: string) {
  if (!value) return "暂无";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "暂无" : date.toLocaleString();
}
function probeResultText(entry: SiteProbeEntry) {
  switch (entry.state) {
    case "queued": return "等待检测。";
    case "running": return "正在测试连接…";
    case "online": return `探针完成：连通正常${entry.latencyMs === null ? "（耗时未知）" : `（${entry.latencyMs} ms）`}`;
    case "offline": return "本轮探针未确认连通。";
    case "unknown": return "本轮结果未确认，请核对最新健康观测。";
    case "stale": return "配置已变化或无法读取，本轮结果不适用于当前配置。";
    case "cancelled": return "尚未发出，已停止后续检测。";
    case "rejected": return safeError(entry.status ?? 503);
  }
}


// Reuse the application's reset stream (including reconnect and periodic reset hints).
// Mount only for administrators; useEffectEvent keeps the refresh callback current.
function SiteLiveRefresh({ invalidate }: { invalidate: () => void }) {
  const { version } = useActivityInvalidation();
  const onReset = useEffectEvent(invalidate);
  useEffect(() => { onReset(); }, [version]);
  return null;
}

export function ManagedSitesView() {
  const { user } = useAuth();
  if (!user) return <div className="sites-container"><p role="status">请登录后查看站点。</p></div>;
  return <ManagedSitesPanel key={`${user.id}:${user.role}`} userID={user.id} admin={user.role === "admin"} />;
}

function ManagedSitesPanel({ userID, admin }: { userID: string; admin: boolean }) {
  const [indexers, setIndexers] = useState<Site[]>([]);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [templates, setTemplates] = useState<IndexerTemplate[]>([]);
  const [selectedTemplateId, setSelectedTemplateId] = useState<string>("");
  const [editing, setEditing] = useState<{ id: string; version: number } | null>(null);
  const [conflict, setConflict] = useState(false);
  const [rssID, setRSSID] = useState<string | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const pendingRef = useRef<Pending | null>(null);
  const [recovering, setRecovering] = useState(true);
  const [storageError, setStorageError] = useState("");
  const [busy, setBusy] = useState(false);
  const writing = useRef(false);
  const [probeState, setProbeState] = useState<SiteProbeBatchSnapshot>({running: false, stopping: false, entries: []});
  const testing = probeState.running;
  const testingRef = useRef(false);
  const probeBatch = useRef<ReturnType<typeof createSiteProbeBatch> | null>(null);
  const active = useRef(false);
  const readers = useRef(new Set<AbortController>());
  const listReader = useRef<AbortController | null>(null);
  const listSequence = useRef(0);
  const sitesRef = useRef<Site[]>([]);
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const storageKey = sitePendingStorageKey(userID);

  function applyTemplate(templateId: string) {
    setSelectedTemplateId(templateId);
    const tmpl = templates.find(t => t.id === templateId);
    if (!tmpl) return;
    setDraft(previous => applyTemplateToDraft(previous, tmpl, !!editing));
    setMessage(`已载入「${tmpl.name}」模板，分类代码已自动填充。`);
    setError("");
  }
  useEffect(() => {
    if (!admin) return;
    const controller = new AbortController();
    api.GET("/api/v1/indexer-templates", { signal: controller.signal })
      .then(({ data }) => {
        if (!active.current || controller.signal.aborted) return;
        if (data?.templates) setTemplates(data.templates);
      })
      .catch(() => {});
    return () => controller.abort();
  }, [admin]);
  function changePending(value: Pending | null) { pendingRef.current = value; setPending(value); }
  function resetDraft() { setEditing(null); setDraft(emptyDraft()); setConflict(false); setSelectedTemplateId(""); }
  const loadIndexers = useCallback(async () => {
    const sequence = ++listSequence.current;
    listReader.current?.abort();
    const controller = new AbortController();
    listReader.current = controller;
    setLoading(true);
    setListError("");
    try {
      const { data, response } = await api.GET("/api/v1/indexers", { signal: controller.signal, cache: "no-store" });
      if (!active.current || controller.signal.aborted || sequence !== listSequence.current) return;
      if (!response.ok || !data) {
        sitesRef.current = []; setIndexers([]);
        setListError(safeError(response.status));
        return;
      }
      sitesRef.current = data.items; setIndexers(data.items);
      return true;
    } catch {
      if (!active.current || controller.signal.aborted || sequence !== listSequence.current) return;
      sitesRef.current = []; setIndexers([]);
      setListError("站点列表读取失败，请重试刷新。当前健康观测暂不可用。");
    } finally {
      if (active.current && sequence === listSequence.current) setLoading(false);
    }
  }, []);
  const invalidate = useCallback(() => {
    if (refreshTimer.current !== null) return;
    refreshTimer.current = setTimeout(() => { refreshTimer.current = null; if (active.current) void loadIndexers(); }, 250);
  }, [loadIndexers]);

  // Failed cleanup keeps a terminal fence: it can only repair storage, never resend.
  function clearStoredPending(): boolean {
    try { sessionStorage.removeItem(storageKey); setStorageError(""); return true; }
    catch { setStorageError("回执已确认，但恢复记录清理失败。请重试清理后再提交新命令。"); return false; }
  }
  function settle(current: Pending, phase: "confirmed" | "rejected") {
    changePending({ ...current, phase, command: undefined });
    if (phase === "confirmed") {
      resetDraft(); setMessage(current.record.action === "delete" ? "站点已删除。" : "站点配置已保存。"); setError("");
    }
    if (clearStoredPending()) changePending(null);
    if (refreshTimer.current !== null) { clearTimeout(refreshTimer.current); refreshTimer.current = null; }
    void loadIndexers();
  }
  async function lookupReceipt() {
    const current = pendingRef.current;
    if (!admin || !current || writing.current) return;
    if (current.phase === "confirmed" || current.phase === "rejected") {
      if (clearStoredPending()) changePending(null);
      return;
    }
    writing.current = true; setBusy(true); setError("");
    const controller = new AbortController(); readers.current.add(controller);
    try {
      const { data, response } = await api.GET("/api/v1/indexer-commands/{key}", {
        params: { path: { key: current.record.key }, query: { action: current.record.action } },
        cache: "no-store", signal: controller.signal,
      });
      if (!active.current || controller.signal.aborted) return;
      if (response.ok && data && data.action === current.record.action && data.deleted === (current.record.action === "delete") && (!current.record.targetId || data.indexer.id === current.record.targetId)) {
        settle(current, "confirmed");
      } else if (response.status === 404) {
        changePending({ ...current, phase: "absent" });
        setMessage("服务器明确返回原命令回执不存在。可使用原键重试原请求；刷新前的输入已清除时，请重新输入完全相同的原意图，或取消恢复。");
      } else {
        setError(`${safeError(response.status)} 原命令仍待确认，暂不能创建新命令。`);
      }
    } catch {
      if (active.current && !controller.signal.aborted) setError("回执查询连接失败，原命令仍待确认。请重试查询。");
    } finally {
      readers.current.delete(controller); writing.current = false;
      if (active.current) setBusy(false);
    }
  }
  function restore() {
    if (!admin) { setRecovering(false); return; }
    try {
      const text = sessionStorage.getItem(storageKey);
      if (text !== null) {
        const record = parseSitePending(text, userID);
        if (!record) { setStorageError("恢复记录损坏或与当前账户不符。请保留当前页面并联系管理员核对；新命令已暂停。"); return; }
        changePending({ record, phase: "unknown" });
        void lookupReceipt();
      }
    } catch { setStorageError("无法读取命令恢复记录。请恢复浏览器会话存储后重新读取，暂不能提交新命令。"); }
    finally { setRecovering(false); }
  }
  const restoreOnMount = useEffectEvent(restore);
  useEffect(() => {
    active.current = true;
    probeBatch.current = createSiteProbeBatch({
      getSites: () => sitesRef.current.map(site => ({id: site.id, name: site.name, configVersion: site.config_version})),
      canRun: () => active.current && admin && !writing.current && !pendingRef.current,
      probe: async (site, signal) => {
        const {data, response} = await api.POST("/api/v1/indexers/{id}/test", {params: {path: {id: site.id}}, signal});
        return {status: response.status, alive: data?.alive, latencyMs: data?.latency_ms};
      },
      refresh: async () => { if (!await loadIndexers()) throw new Error("Site list refresh not confirmed"); },
      now: Date.now,
      onChange: state => { if (active.current) { testingRef.current = state.running; setProbeState(state); } },
    });
    restoreOnMount(); void loadIndexers();
    return () => {
      active.current = false; ++listSequence.current;
      probeBatch.current?.dispose(); probeBatch.current = null;
      listReader.current?.abort();
      for (const reader of readers.current) reader.abort();
      readers.current.clear();
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current);
      refreshTimer.current = null;
    };
  }, [loadIndexers]);

  function startEdit(site: Site) {
    if (!admin || testingRef.current || pendingRef.current || writing.current) return;
    setEditing({ id: site.id, version: site.config_version });
    setDraft({ ...emptyDraft(), name: site.name, protocol: site.protocol, categories: [...site.categories].join(", ") });
    setConflict(false); setError(""); setMessage("");
    formRef.current?.scrollIntoView({ block: "start" });
  }
  async function sendCommand(command: Command, original?: Pending) {
    if (!admin || testingRef.current || writing.current || recovering || storageError || conflict || (pendingRef.current && !original)) return;
    if (original && original !== pendingRef.current) return;
    if (original && (original.phase === "confirmed" || original.phase === "rejected")) return;
    const record: SitePendingRecord = original?.record ?? {
      key: crypto.randomUUID(), action: command.action, userID,
      ...(command.action === "update" ? { targetId: command.id, expectedVersion: command.body.expected_version } : command.action === "delete" ? { targetId: command.id, expectedVersion: command.version } : {}),
    };
    const current: Pending = { record, command, phase: "unknown" };
    // Persist safe recovery metadata before the first possible transport; secrets stay in memory.
    try { sessionStorage.setItem(storageKey, serializeSitePending(record)); }
    catch { setStorageError("无法保存安全恢复记录，本次请求尚未发送。请恢复会话存储后重新读取。"); return; }
    changePending(current); writing.current = true; setBusy(true); setError(""); setMessage("");
    const controller = new AbortController(); readers.current.add(controller);
    const header = { "Idempotency-Key": record.key };
    try {
      const result = command.action === "create"
        ? await api.POST("/api/v1/indexers", { params: { header }, body: command.body, signal: controller.signal })
        : command.action === "update"
          ? await api.PUT("/api/v1/indexers/{id}", { params: { path: { id: command.id }, header }, body: command.body, signal: controller.signal })
          : await api.DELETE("/api/v1/indexers/{id}", { params: { path: { id: command.id }, header, query: { expected_version: command.version } }, signal: controller.signal });
      if (!active.current || controller.signal.aborted) return;
      const status = result.response.status;
      if ((command.action === "create" && status === 201 && result.data) || (command.action === "update" && status === 200 && result.data && result.data.id === command.id) || (command.action === "delete" && status === 204)) {
        settle(current, "confirmed");
      } else if (!original && [400, 403, 409].includes(status)) {
        settle(current, "rejected"); setError(safeError(status));
        if (status === 409) setConflict(true);
      } else {
        setError(`${safeError(status)} 原请求和命令键已保留，请查询回执或手动重试原请求。`);
      }
    } catch {
      if (active.current && !controller.signal.aborted) setError("连接中断，保存结果未确认。原请求已保留；请查询回执或手动重试原请求。");
    } finally {
      readers.current.delete(controller); writing.current = false;
      if (active.current) setBusy(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const original = pendingRef.current;
    if (original && (original.phase !== "absent" || original.command)) return;
    const parts = draft.categories.trim() ? draft.categories.trim().split(/[,，\s]+/) : [];
    const categories = parts.map(Number);
    if (parts.some(part => !/^\d+$/.test(part)) || categories.length > 100 || categories.some(n => !Number.isSafeInteger(n) || n < 1 || n > 2147483647) || new Set(categories).size !== categories.length) {
      setError("分类须为不重复的正整数，最大 2147483647，最多 100 项；留空表示空数组。"); return;
    }
    const replacingURL = !editing || !draft.keepURL;
    const url = draft.url.trim();
    if (replacingURL) {
      try {
        const parsed = new URL(url);
        if (!["http:", "https:"].includes(parsed.protocol) || parsed.hash || url.length > 8192) throw new Error();
      } catch { setError("请输入完整 HTTP(S) URL；不可包含片段。"); return; }
    }
    const common = { name: draft.name.trim(), protocol: draft.protocol, categories };
    if (!common.name) { setError("请输入站点名称。"); return; }
    // Preserve credential bytes exactly; deliberate clear is separate from an empty edit field.
    const credential = draft.clearCredential ? { api_key: "" } : draft.credential !== "" ? { api_key: draft.credential } : {};
    const command: Command = editing
      ? { action: "update", id: editing.id, body: { ...common, expected_version: editing.version, ...(replacingURL ? { url } : {}), ...credential } }
      : { action: "create", body: { ...common, url, ...credential } };
    if (original && (original.record.action !== command.action || original.record.targetId !== (command.action === "update" ? command.id : undefined))) {
      setError("恢复输入必须对应原命令类型与站点。"); return;
    }
    void sendCommand(command, original ?? undefined);
  }
  function reenterOriginal() {
    const current = pendingRef.current;
    if (!current || current.phase !== "absent" || current.command || busy) return;
    setError("");
    if (current.record.action === "delete") {
      if (current.record.targetId && current.record.expectedVersion && window.confirm("使用原命令键及原配置版本重新提交删除？")) {
        void sendCommand({ action: "delete", id: current.record.targetId, version: current.record.expectedVersion }, current);
      }
      return;
    }
    setEditing(current.record.action === "update" && current.record.targetId && current.record.expectedVersion ? { id: current.record.targetId, version: current.record.expectedVersion } : null);
    setDraft(emptyDraft()); setConflict(false);
    setMessage("请重新输入与原提交完全相同的名称、协议、分类和替换内容。原先保留的地址或凭据继续选择保留；将复用原命令键。");
    formRef.current?.scrollIntoView({ block: "start" });
  }
  function cancelRecovery() {
    if (pendingRef.current?.phase !== "absent" || busy) return;
    if (!clearStoredPending()) return;
    changePending(null); resetDraft(); setMessage("已取消回执不存在的原命令，可重新填写。"); setError("");
  }
  async function reloadConfiguration() {
    if (busy || pendingRef.current) return;
    const target = editing?.id;
    await loadIndexers();
    if (!active.current) return;
    const site = sitesRef.current.find(item => item.id === target);
    resetDraft();
    if (site) startEdit(site);
    setError(""); setMessage("已重新加载配置，请核对最新版本并重新输入修改内容。");
  }
  async function probe(ids: string[]) {
    if (!admin || loading || testingRef.current || writing.current || pendingRef.current || recovering || storageError || conflict) return;
    await probeBatch.current?.start(ids);
  }

  const probeResults = new Map(probeState.entries.map(entry => [entry.target.id, entry]));
  const completedProbes = probeState.entries.filter(entry => entry.state !== "queued" && entry.state !== "running").length;
  const queuedProbes = probeState.entries.filter(entry => entry.state === "queued").length;
  function probeResult(site: Site) {
    const entry = probeResults.get(site.id);
    if (!entry) return null;
    const current = entry.target.configVersion === site.config_version;
    return <p className="site-probe-result" data-state={current ? entry.state : "stale"} role="status">{current ? probeResultText(entry) : "配置已变化，本轮结果不适用于当前版本。"}</p>;
  }

  const reentry = pending?.phase === "absent" && !pending.command && pending.record.action !== "delete";
  const mutationBlocked = !admin || loading || busy || testing || recovering || !!storageError || !!pending || conflict;
  const formBlocked = !admin || loading || busy || testing || recovering || !!storageError || (!!pending && !reentry) || conflict;
  const selectedRSS = indexers.find(site => site.id === rssID);
  const templateGroups = groupTemplatesByProtocol(templates);
  return <div className="sites-container" style={{ minWidth: 0 }}>
    {admin && <SiteLiveRefresh invalidate={invalidate} />}
    <div className="page-header" style={{ flexWrap: "wrap", gap: "1rem" }}>
      <div style={{ minWidth: 0 }}><h2>站点与索引器管理 (Indexers)</h2><p className="subtitle">管理 NexusPHP、公开 RSS 与 Torznab 站点，查看持久健康观测。</p></div>
      {admin && <div className="header-actions"><button type="button" className="btn-primary" style={buttonStyle} disabled={mutationBlocked || !indexers.length} onClick={() => void probe(indexers.map(site => site.id))}>{testing ? <Loader2 size={16} className="spinner-icon" /> : <Zap size={16} />}<span>{testing ? "正在测试连接…" : "测试全部站点"}</span></button></div>}
    </div>
    {admin && probeState.entries.length > 0 && <section className="site-probe-summary" aria-label="本轮站点检测" aria-busy={testing}>
      <div className="site-probe-summary-heading"><h3>{testing ? probeState.stopping ? "等待当前检测结束" : "正在检测站点" : "本轮检测已结束"}</h3><span role="status">已结束 {completedProbes}/{probeState.entries.length} 项</span></div>
      <progress aria-label="本轮检测处理进度" value={completedProbes} max={probeState.entries.length} />
      {testing && <div className="site-probe-stop"><button type="button" className="btn-secondary" disabled={!queuedProbes || probeState.stopping} onClick={() => probeBatch.current?.stopQueued()}>停止后续检测</button><p>尚未发出的站点将停止检测；当前请求结束后，本轮才会完成。</p></div>}
    </section>}
    {!admin && <p className="config-description" role="status">当前为成员账户。站点管理、连通性测试与 RSS 配置仅限管理员；列表按当前账户权限读取。</p>}
    {message && <p className="config-description" role="status">{message}</p>}
    {error && <div className="config-error" role="alert">{error}</div>}
    {storageError && <div className="config-error" role="alert"><p>{storageError}</p>{!pending && <button type="button" className="btn-secondary" disabled={busy} onClick={() => { setStorageError(""); restore(); }}>重新读取恢复记录</button>}</div>}
    {pending && <section className="card" aria-label="待确认站点命令" aria-busy={busy}>
      <h3>{pending.phase === "confirmed" || pending.phase === "rejected" ? "清理命令恢复记录" : "站点命令待确认"}</h3>
      <p className="config-description">操作：{pending.record.action === "create" ? "添加站点" : pending.record.action === "update" ? "修改站点" : "删除站点"}{pending.record.expectedVersion ? ` · 原配置版本 ${pending.record.expectedVersion}` : ""}。刷新后通过安全回执查询恢复；完整地址与凭据仅在本页内存中保留。</p>
      <div className="config-actions" style={wrapActions}>
        <button type="button" className="btn-secondary" disabled={busy} onClick={() => void lookupReceipt()}>{pending.phase === "confirmed" || pending.phase === "rejected" ? "重试清理恢复记录" : "查询原命令回执"}</button>
        {pending.command && pending.phase !== "confirmed" && pending.phase !== "rejected" && <button type="button" className="btn-primary" disabled={busy || !!storageError} onClick={() => { if (pending.command) void sendCommand(pending.command, pending); }}>重试原请求（相同命令键）</button>}
        {pending.phase === "absent" && !pending.command && <button type="button" className="btn-secondary" disabled={busy} onClick={reenterOriginal}>{pending.record.action === "delete" ? "用原键重新确认删除" : "重新输入原提交内容"}</button>}
        {pending.phase === "absent" && <button type="button" className="btn-secondary" disabled={busy} onClick={cancelRecovery}>取消恢复（回执不存在）</button>}
      </div>
    </section>}
    <section className="card" aria-busy={loading}>
      <div className="card-header" style={{ flexWrap: "wrap", gap: "0.5rem" }}><h3>已配置做种与检索站点（共 {indexers.length} 个）</h3><button type="button" className="btn-sm btn-secondary" style={buttonStyle} disabled={loading} onClick={() => void loadIndexers()}><RefreshCw size={13} /><span>刷新列表</span></button></div>
      {listError ? <div role="alert" className="config-error">{listError}</div> : loading && !indexers.length ? <div className="loading-state" role="status"><Loader2 size={24} className="spinner-icon" /><p>加载站点列表中…</p></div> : !indexers.length ? <div className="empty-state"><p>{admin ? "暂无配置站点，请在下方添加。" : "当前账户没有可查看的站点。"}</p></div> : <div className="indexers-table">{indexers.map(site => <div key={site.id} className="indexer-row" style={{ flexWrap: "wrap", gap: "0.75rem", minWidth: 0 }}>
        <div className="idx-info" style={{ minWidth: 0, overflowWrap: "anywhere", flex: "1 1 16rem" }}>
          <div className="idx-name-box" style={wrapActions}><strong>{site.name}</strong><span className="protocol-pill">{protocolNames[site.protocol]}</span><span className={`badge ${site.health_checked_at ? site.health : "unknown"}`}>{!site.health_checked_at ? "尚未确认" : site.health === "online" ? "最近观测在线" : site.health === "offline" ? "最近观测离线" : "健康状态待确认"}</span></div>
          <div className="idx-url">{site.url}（公开来源；完整地址已隐藏）</div>
          <p className="config-description">配置版本 {site.config_version} · 创建于 {timestamp(site.created_at)}{site.legacy_v1 ? " · 历史 V1 配置，请核对协议" : ""}</p>
          <p className="config-description">最近观测：{timestamp(site.health_observed_at ?? site.health_checked_at)} · {site.health_reason === "ok" ? "探针成功" : site.health_reason === "rate_limited" ? "端点限流，预算内等待" : site.health_reason === "unavailable" ? "探针失败，按退避重试" : "等待检测"}</p>
          {site.health_next_check_at && <p className="config-description">下次检测：{timestamp(site.health_next_check_at)}（受共享预算约束）</p>}
          {probeResult(site)}
        </div>
        {admin && <div className="idx-actions" style={wrapActions}>
          <button type="button" className="btn-sm btn-secondary" style={buttonStyle} disabled={mutationBlocked} aria-expanded={rssID === site.id} onClick={() => setRSSID(site.id)}>RSS 配置</button>
          <button type="button" className="btn-sm btn-secondary" style={buttonStyle} disabled={mutationBlocked} onClick={() => startEdit(site)}>编辑</button>
          <button type="button" className="btn-sm btn-primary" style={buttonStyle} disabled={mutationBlocked} onClick={() => void probe([site.id])}>测试连接</button>
          <button type="button" className="btn-sm btn-danger" style={buttonStyle} disabled={mutationBlocked} aria-label={`删除站点 ${site.name}`} onClick={() => { if (window.confirm(`确定删除站点「${site.name}」（配置版本 ${site.config_version}）？`)) void sendCommand({ action: "delete", id: site.id, version: site.config_version }); }}><Trash2 size={13} /><span>删除</span></button>
        </div>}
        <SiteAccountStatsPanel sourceID={site.id} sourceVersion={site.config_version} />
      </div>)}</div>}
    </section>
    {admin && selectedRSS && !pending && <RSSConfigEditor key={selectedRSS.id} id={selectedRSS.id} name={selectedRSS.name} onClose={() => setRSSID(null)} />}
    {admin && <section className="card form-card">
      <h3><Plus size={18} /><span>{editing ? "编辑做种与检索站点" : "添加做种与检索站点"}</span></h3>
      {editing && <p className="config-description">当前编辑配置版本：{editing.version}。保留现有地址和凭据时，服务器逐字节保留原内容。</p>}
      <form className="form" ref={formRef} onSubmit={submit} autoComplete="off">
        <fieldset className="config-fieldset" disabled={formBlocked}><legend>站点配置</legend>
          {templates.length > 0 && (
            <div className="form-group">
              <label htmlFor="managed-site-template">内置站点模板（选择后自动填充推荐配置）</label>
              <select
                id="managed-site-template"
                value={selectedTemplateId}
                onChange={event => applyTemplate(event.target.value)}
              >
                <option value="">-- 选择预设模板 (共 {templates.length} 个内置站点) --</option>
                <optgroup label="聚合器与通用 (Torznab)">
                  {templateGroups.torznab.map(t => (
                    <option key={t.id} value={t.id}>{t.name} ({t.domain})</option>
                  ))}
                </optgroup>
                <optgroup label="国内主流 PT 站 (NexusPHP)">
                  {templateGroups.nexusphp.map(t => (
                    <option key={t.id} value={t.id}>{t.name} ({t.domain})</option>
                  ))}
                </optgroup>
                <optgroup label="公开与订阅 (RSS)">
                  {templateGroups.rss.map(t => (
                    <option key={t.id} value={t.id}>{t.name} ({t.domain})</option>
                  ))}
                </optgroup>
              </select>
              {(() => {
                const current = templates.find(t => t.id === selectedTemplateId);
                if (!current) return null;
                return (
                  <div className="template-help-box" style={{ marginTop: "0.5rem", padding: "0.5rem 0.75rem", background: "var(--bg-secondary, rgba(255,255,255,0.04))", borderRadius: "4px", fontSize: "0.85rem" }}>
                    <p style={{ margin: "0 0 0.25rem 0" }}><strong>{current.name}</strong> · {current.description}</p>
                    <p style={{ margin: "0 0 0.25rem 0", color: "var(--text-muted, #888)" }}>认证提示：{current.credential_help}</p>
                    <div style={{ display: "flex", flexWrap: "wrap", gap: "0.35rem", alignItems: "center" }}>
                      <span style={{ color: "var(--text-muted, #888)" }}>推荐分类说明：</span>
                      {current.category_mappings.map(m => (
                        <span key={m.id} className="badge" style={{ fontSize: "0.75rem", padding: "0.15rem 0.35rem" }}>{m.id}: {m.name}</span>
                      ))}
                    </div>
                  </div>
                );
              })()}
            </div>
          )}
          <div className="form-group"><label htmlFor="managed-site-name">站点名称 *</label><input id="managed-site-name" required value={draft.name} onChange={event => setDraft(previous => ({ ...previous, name: event.target.value }))} /></div>
          <div className="form-group"><label htmlFor="managed-site-protocol">协议 *</label><select id="managed-site-protocol" value={draft.protocol} onChange={event => { const protocol = event.target.value; if (protocol === "torznab" || protocol === "nexusphp" || protocol === "rss") setDraft(previous => ({ ...previous, protocol })); }}><option value="torznab">Torznab（Prowlarr / Jackett）</option><option value="nexusphp">NexusPHP（Cookie）</option><option value="rss">RSS（公开源可留空凭据）</option></select></div>
          {editing && <label className="config-check"><input type="checkbox" checked={draft.keepURL} onChange={event => setDraft(previous => ({ ...previous, keepURL: event.target.checked, url: "" }))} />保留现有地址</label>}
          <div className="form-group"><label htmlFor="managed-site-url">{editing ? "替换为完整 URL" : "完整 URL *"}</label><input id="managed-site-url" type="password" autoComplete="new-password" autoCapitalize="none" spellCheck={false} disabled={!!editing && draft.keepURL} required={!editing || !draft.keepURL} maxLength={8192} value={draft.url} onChange={event => setDraft(previous => ({ ...previous, url: event.target.value }))} aria-describedby="managed-site-url-help" /><p id="managed-site-url-help" className="field-hint">请输入完整执行地址（含原有路径及必要参数）。列表中的公开来源不可用于补全原地址。</p></div>
          <div className="form-group"><label htmlFor="managed-site-credential">{draft.protocol === "nexusphp" ? "登录 Cookie" : "API Key"}{editing ? "（留空保留已有凭据）" : "（可留空）"}</label><input id="managed-site-credential" type="password" autoComplete="new-password" spellCheck={false} disabled={draft.clearCredential} value={draft.credential} onChange={event => setDraft(previous => ({ ...previous, credential: event.target.value }))} /><p className="field-hint">凭据加密保存且不回显。公开 RSS 无需填写凭据。</p></div>
          {editing && <label className="config-check"><input type="checkbox" checked={draft.clearCredential} onChange={event => setDraft(previous => ({ ...previous, clearCredential: event.target.checked, credential: "" }))} />清除独立 API Key / Cookie（地址中的认证信息仍保留）</label>}
          <div className="form-group"><label htmlFor="managed-site-categories">分类编号</label><input id="managed-site-categories" inputMode="text" placeholder="例如：2000, 5000" value={draft.categories} onChange={event => setDraft(previous => ({ ...previous, categories: event.target.value }))} aria-describedby="managed-site-categories-help" /><p id="managed-site-categories-help" className="field-hint">使用逗号或空格分隔。每次保存会替换整个分类数组；留空将清空分类。编辑时已复制当前持久分类，请核对希望保存的完整集合。</p></div>
        </fieldset>
        <div className="form-actions" style={{ display: "flex", ...wrapActions }}><button type="submit" className="btn-primary" style={buttonStyle} disabled={formBlocked}>{busy ? "保存中…" : reentry ? "用原键提交重新输入的原内容" : editing ? "保存站点修改" : "保存并启用站点"}</button>{editing && <button type="button" className="btn-secondary" style={buttonStyle} disabled={busy || !!pending} onClick={resetDraft}>取消编辑</button>}{(editing || conflict) && <button type="button" className="btn-secondary" style={buttonStyle} disabled={busy || !!pending || loading} onClick={() => void reloadConfiguration()}>重新加载配置（放弃草稿）</button>}</div>
      </form>
    </section>}
  </div>;
}

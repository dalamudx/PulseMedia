import { useEffect, useId, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { TVEpisodeUpgradePanel } from "./TVEpisodeUpgradePanel";
import { TVEpisodeDiscoveryPanel, type TVEpisodeDiscoverySelection } from "./TVEpisodeDiscoveryPanel";
import { OperationDownloadControl } from "./OperationDownloadControl";
import { readJournal, completedStages, pendingEntry, latestResult } from "./tvEpisodeUpgradeJournal";
type SelectionSummary = { rootName: string; relPath: string; unitKey: string };
import { useInteractiveReleaseSearch } from "./useInteractiveReleaseSearch";
import { parseLocators, sameSubscription, saveLocator, selectionKey, singleMappedUnit, type Locator, type Mapping, type MappingOptions, type TVUpgradeSubscription } from "./tvEpisodeUpgradeSelection";
import "./tv-episode-upgrade-workspace.css";

export interface TVEpisodeUpgradeWorkspaceProps {
  subscription: TVUpgradeSubscription;
  wantedId?: string;
  onUpdated?: () => void;
}
export function TVEpisodeUpgradeWorkspace(props: TVEpisodeUpgradeWorkspaceProps) {
  const { user, checking, statusError } = useAuth();
  if (!user || checking || statusError || user.role !== "admin" || props.subscription.media_type !== "tv") return null;
  const s = props.subscription;
  return <Workspace key={`${user.id}:${s.id}:${s.media_id}:${s.root_id}:${s.intent_version}`} {...props} actor={user.id} />;
}
function Workspace({ subscription, actor, onUpdated }: TVEpisodeUpgradeWorkspaceProps & { actor: string }) {
  const uid = useId();
  const reader = useRef<AbortController | null>(null);
  const [current, setCurrent] = useState<TVUpgradeSubscription | null>(null);
  const [files, setFiles] = useState<MappingOptions["files"]>([]);

  const [filesCursor, setFilesCursor] = useState("");
  const [locators, setLocators] = useState<Locator[]>([]);
  const [handoff, setHandoff] = useState<Locator | null>(null);
  const [handoffSummary, setHandoffSummary] = useState<SelectionSummary | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [storageBlocked, setStorageBlocked] = useState(false);
  const key = selectionKey(actor, subscription.id);
  useEffect(() => {
    function load() {
      try { setLocators(parseLocators(sessionStorage.getItem(key))); setStorageBlocked(false); }
      catch { setStorageBlocked(true); }
    }
    load();
    const changed = (event: StorageEvent) => { if (event.key === null || event.key === key) load(); };
    window.addEventListener("storage", changed);
    return () => { reader.current?.abort(); window.removeEventListener("storage", changed); };
  }, [key]);
  async function read(recovery?: Locator) {
    if (reader.current) return;
    const c = new AbortController(); reader.current = c;
    setBusy(true); setError(""); setNotice(""); setCurrent(null); setFiles([]); setFilesCursor(""); setHandoff(null);
    setHandoffSummary(null);
    const timeout = window.setTimeout(() => c.abort(), 15000);
    try {
      const [s, f] = await Promise.all([
        api.GET("/api/v1/subscriptions/{id}", { params: { path: { id: subscription.id } }, signal: c.signal, cache: "no-store" }),
        recovery ? Promise.resolve(null) : api.GET("/api/v1/library/mapping-options", { params: { query: { root_id: subscription.root_id } }, signal: c.signal, cache: "no-store" }),
      ]);
      if (c.signal.aborted) return;
      if (s.error || !s.data || !sameSubscription(subscription, s.data)) {
        onUpdated?.(); throw Error("Subscription changed");
      }
      if (recovery) {
        // Keep the original scope even if its candidate disappeared or its old file retired.
        setHandoff(recovery); setNotice("已打开原选择。请显式读取原命令回执；服务器会重新核验当前权限与原范围。");
      } else {
        if (!f?.data || f.error) throw Error("Files unavailable");
        setCurrent(s.data); setFiles(f.data.files.filter(file => file.root_id === s.data.root_id));
        setFilesCursor(f.data.files_has_more ? f.data.files_next_cursor : "");
      }
    } catch { if (!c.signal.aborted) setError("当前订阅或文件读取失败，或意图已变化。请刷新订阅后重新读取。"); }
    finally {
      window.clearTimeout(timeout);
      if (reader.current === c) { reader.current = null; setBusy(false); if (c.signal.aborted) setError("读取已取消或超时，请重新读取。"); }
    }
  }
  async function readMoreFiles() {
    if (!filesCursor || reader.current || !current) return;
    const cursor = filesCursor, c = new AbortController(); reader.current = c;
    setBusy(true); setError("");
    const timeout = window.setTimeout(() => c.abort(), 15000);
    try {
      const result = await api.GET("/api/v1/library/mapping-options", { params: { query: { root_id: current.root_id, cursor } }, signal: c.signal, cache: "no-store" });
      if (c.signal.aborted) return;
      if (result.error || !result.data || (result.data.files_has_more && (!result.data.files_next_cursor || result.data.files_next_cursor === cursor))) throw Error("Files unavailable");
      const page = result.data;
      setFiles(previous => [...new Map([...previous, ...page.files.filter(file => file.root_id === current.root_id)].map(file => [file.id, file])).values()]);
      setFilesCursor(page.files_has_more ? page.files_next_cursor : "");
    } catch { if (!c.signal.aborted) setError("后续文件读取失败，已有选择保留。可重试读取下一页。"); }
    finally {
      window.clearTimeout(timeout);
      if (reader.current === c) { reader.current = null; setBusy(false); if (c.signal.aborted) setError("后续文件读取已取消或超时，可重试。"); }
    }
  }
  function accept(locator: Locator, summary: SelectionSummary) {
    try { setLocators(saveLocator(sessionStorage, key, locator)); setHandoff(locator); setHandoffSummary(summary); setCurrent(null); }
    catch { setStorageBlocked(true); setError("无法保存原选择定位记录，已暂停交接。请恢复会话存储后重新打开工作区。"); }
  }
  return <section className="tv-upgrade-workspace" aria-labelledby={`${uid}-title`} aria-busy={busy}>
    <h3 id={`${uid}-title`}>选择 TV 单集升级资源</h3>
    <p>先选择已入库的单集文件，再检索候选并读取比较。下载、整理与旧版本退休分别确认。</p>
    {error && <p role="alert">{error}</p>}
    {storageBlocked && <p role="alert">会话选择记录不可读或已满，已暂停新选择。原阶段记录仍保留，请恢复会话存储后重新打开。</p>}
    {notice && <p role="status">{notice}</p>}
    <button type="button" className="btn-secondary" disabled={busy || storageBlocked} onClick={() => void read()}>读取当前文件与订阅</button>
    {busy && <p role="status">正在核验当前订阅…</p>}
    {locators.length > 0 && <details><summary>恢复原升级选择（{locators.length}）</summary>
      <p>这里只保存原选择编号。旧文件退休或候选不在当前结果中时，仍可打开原范围读取回执；不会自动搜索或提交阶段命令。</p>
      <ol>{locators.map((locator, index) => <li key={`${locator.unitId}:${locator.currentFileId}:${locator.candidateId}`}>
        <button type="button" className="btn-secondary" disabled={busy} onClick={() => void read(locator)}>打开原选择 {index + 1}</button>
      </li>)}</ol>
    </details>}
    {current && filesCursor && !storageBlocked && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void readMoreFiles()}>读取下一页文件</button>}
    {current && !storageBlocked && <SelectionForm key={`${current.id}:${current.intent_version}`} subscription={current} files={files} onAccept={accept} onInvalidated={() => { setCurrent(null); setFiles([]); onUpdated?.(); }} />}
    {handoff && <div aria-label="单集升级交接">
      {handoffSummary && <p>{handoffSummary.rootName} / {handoffSummary.relPath} · {handoffSummary.unitKey}</p>}
      <UpgradeHandoff subscriptionId={subscription.id} locator={handoff} actor={actor} onUpdated={onUpdated} />
    </div>}
  </section>;
}
function UpgradeHandoff({ subscriptionId, locator, actor, onUpdated }: { subscriptionId: string; locator: Locator; actor: string; onUpdated?: () => void }) {
  const [, refresh] = useState(0);
  const scope = { subscription_id: subscriptionId, unit_id: locator.unitId, current_file_id: locator.currentFileId, candidate_id: locator.candidateId };
  let operationId: string | undefined;
  let contextReady = false;
  try {
    const journal = readJournal(localStorage, actor, scope);
    if (journal && completedStages(journal).includes("send")) {
      operationId = journal.entries[0]?.result?.operation_id;
      contextReady = !pendingEntry(journal) && latestResult(journal)?.status === "tracking";
    }
  } catch { /* The existing upgrade panel reports invalid journals. */ }
  return <>
    <TVEpisodeUpgradePanel subscriptionId={subscriptionId} unitId={locator.unitId} currentFileId={locator.currentFileId} candidateId={locator.candidateId} onUpdated={() => { refresh(n => n + 1); onUpdated?.(); }} />
    {operationId && <OperationDownloadControl operationId={operationId} contextReady={contextReady} />}
  </>;
}
function SelectionForm({ subscription, files, onAccept, onInvalidated }: {
  subscription: TVUpgradeSubscription; files: MappingOptions["files"];
  onAccept: (locator: Locator, summary: SelectionSummary) => void; onInvalidated: () => void;
}) {
  const uid = useId(), reader = useRef<AbortController | null>(null);
  const [fileId, setFileId] = useState(""), [candidateId, setCandidateId] = useState("");
  const [query, setQuery] = useState(subscription.title ?? "");
  const [mapping, setMapping] = useState<Mapping | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  // The installed Hook has no enabled/intentVersion options. Mount only after a verified read.
  const search = useInteractiveReleaseSearch({ scope: `tv-episode-upgrade:${subscription.id}:${subscription.intent_version}`, subscriptionId: subscription.id, mediaId: subscription.media_id });
  useEffect(() => () => { reader.current?.abort(); }, []);
  const file = files.find(f => f.id === fileId);
  const unit = file && mapping ? singleMappedUnit(subscription, file, mapping) : null;
  const candidate = search.canAcquire ? search.results.find(c => c.id === candidateId) : undefined;
  const candidateRef = useRef(candidate), canAcquireRef = useRef(search.canAcquire);
  candidateRef.current = candidate; canAcquireRef.current = search.canAcquire;
  function cancelRead() { reader.current?.abort(); reader.current = null; setBusy(false); }
  async function verify(handoff = false, discovered?: TVEpisodeDiscoverySelection) {
    const selectedCandidate = discovered?.candidate ?? candidate;
    if (!file || reader.current || (handoff && (!unit || !selectedCandidate || (!discovered && !search.canAcquire)))) return;
    if (discovered && (discovered.currentFileId !== file.id || discovered.unitId !== unit?.id)) { setError("发现结果与当前文件或标准集不一致，请刷新后重新选择。"); return; }
    const c = new AbortController(); reader.current = c; setBusy(true); setError("");
    const timer = window.setTimeout(() => c.abort(), 15000);
    try {
      const [s, m] = await Promise.all([
        api.GET("/api/v1/subscriptions/{id}", { params: { path: { id: subscription.id } }, signal: c.signal, cache: "no-store" }),
        api.GET("/api/v1/library/files/{id}/mapping", { params: { path: { id: file.id }, query: { media_id: subscription.media_id } }, signal: c.signal, cache: "no-store" }),
      ]);
      if (c.signal.aborted) return;
      if (!s.data || s.error || !sameSubscription(subscription, s.data)) { onInvalidated(); return; }
      const actualFile = file;
      if (m.error || !m.data) throw Error("Mapping changed");
      const actualUnit = singleMappedUnit(s.data, actualFile, m.data);
      if (!actualUnit || (handoff && actualUnit.id !== unit?.id)) throw Error("Not a unique episode");
      if (handoff && !discovered && (!canAcquireRef.current || candidateRef.current?.id !== selectedCandidate?.id)) throw Error("Candidate availability changed");
      if (discovered && (actualFile.id !== discovered.currentFileId || actualUnit.id !== discovered.unitId)) throw Error("Discovery scope changed");
      setMapping(m.data);
      if (handoff && selectedCandidate) onAccept({ unitId: actualUnit.id, currentFileId: actualFile.id, candidateId: selectedCandidate.id, mediaId: s.data.media_id, rootId: s.data.root_id }, { rootName: actualFile.root_name, relPath: actualFile.rel_path, unitKey: actualUnit.unit_key });
    } catch { if (!c.signal.aborted) { setMapping(null); setError("无法确认唯一有效单集映射。多集、共享文件、退休关系或范围变化均不能用于此次升级，请重新读取。"); } }
    finally { window.clearTimeout(timer); if (reader.current === c) { reader.current = null; setBusy(false); if (c.signal.aborted) { setMapping(null); setError("映射读取超时，请重试。"); } } }
  }
  return <div className="tv-upgrade-selection">
    <p>当前媒体：{subscription.title ?? "未命名剧集"} · 目标根：{subscription.root_name ?? files[0]?.root_name ?? "未命名根"}{subscription.root_path ? ` · ${subscription.root_path}` : ""}</p>
    {error && <p role="alert">{error}</p>}
    <label htmlFor={`${uid}-file`}>当前文件</label>
    <select id={`${uid}-file`} value={fileId} onChange={e => { cancelRead(); setFileId(e.target.value); setMapping(null); setCandidateId(""); setError(""); }}>
      <option value="">选择文件路径</option>{files.map(f => <option key={f.id} value={f.id}>{f.root_name} · {f.rel_path}</option>)}
    </select>
    {files.length === 0 && <p role="status">当前目标根没有可列出的文件。可刷新文件选项或恢复原选择。</p>}
    <button type="button" className="btn-secondary" disabled={!file || busy} onClick={() => void verify()}>核验标准集映射</button>
    {file && <p>文件：{file.root_name} · {file.rel_path}</p>}
    {unit && <p role="status">已核验标准集：{unit.unit_key}（唯一有效关联）</p>}
    {busy && <p role="status">正在核验文件与标准集…</p>}
    {unit && file && <TVEpisodeDiscoveryPanel subscriptionId={subscription.id} unitId={unit.id} intentVersion={subscription.intent_version} policyRevision={subscription.quality_policy_revision} onSelect={selection => { void verify(true, selection); }} />}
    <label htmlFor={`${uid}-query`}>单集资源检索词</label>
    <input id={`${uid}-query`} value={query} maxLength={512} onChange={e => setQuery(e.target.value)} />
    <button type="button" className="btn-secondary" disabled={!unit || busy || search.busy || !query.trim()} onClick={() => { setCandidateId(""); void search.start(query); }}>检索站点</button>
    <p role="status">{search.message}</p>
    {search.sourceSummary && <p>{search.sourceSummary}</p>}{search.policySummary && <p>{search.policySummary}</p>}
    {search.recovery && <div className="tv-upgrade-actions">
      <button type="button" className="btn-secondary" disabled={busy || search.busy} onClick={() => { setCandidateId(""); search.refresh(); }}>读取原搜索观察</button>
      <button type="button" className="btn-secondary" disabled={busy || search.busy} onClick={() => { setCandidateId(""); void search.cancel(); }}>取消原搜索需求</button>
    </div>}
    <label htmlFor={`${uid}-candidate`}>已保存候选资源</label>
    <select id={`${uid}-candidate`} value={candidate?.id ?? ""} disabled={busy || !unit || !search.canAcquire} onChange={e => setCandidateId(e.target.value)}>
      <option value="">选择候选资源</option>{search.results.map(c => <option key={c.id} value={c.id}>{c.title} · {c.indexer_name}</option>)}
    </select>
    {search.canAcquire && search.results.length === 0 && <p>当前搜索没有可选候选；可调整检索词后显式搜索。</p>}
    {candidate && <p>候选：{candidate.title} · {candidate.indexer_name}</p>}
    <button type="button" className="btn-primary" disabled={busy || !unit || !candidate || !search.canAcquire} onClick={() => void verify(true)}>核验选择并进入升级比较</button>
  </div>;
}

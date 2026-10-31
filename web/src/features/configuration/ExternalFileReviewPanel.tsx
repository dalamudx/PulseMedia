import { useEffect, useRef, useState } from "react";
import { useAuth } from "@/features/auth/AuthContext";
import type { paths } from "@/api/client";
import { UntrackedCompanionsPanel } from "@/features/media-scrape/UntrackedCompanionsPanel";

type Scope = NonNullable<paths["/api/v1/library/untracked-scopes/{scope_id}"]["get"]["responses"]["200"]["content"]["application/json"]["config"]>;
type Discoveries = paths["/api/v1/library/untracked-scopes/{scope_id}/discoveries"]["get"]["responses"]["200"]["content"]["application/json"];
type Review = paths["/api/v1/library/untracked-scopes/{scope_id}/discoveries/{file_id}/reviews/{review_id}"]["get"]["responses"]["200"]["content"]["application/json"];
type ReviewReceipt = paths["/api/v1/library/untracked-scopes/{scope_id}/discoveries/{file_id}/reviews"]["post"]["responses"]["202"]["content"]["application/json"];
type Confirmation = paths["/api/v1/library/untracked-scopes/{scope_id}/discoveries/{file_id}/confirmations"]["post"]["responses"]["200"]["content"]["application/json"];
type Organization = paths["/api/v1/library/untracked-scopes/{scope_id}/discoveries/{file_id}/organizations"]["post"]["responses"]["202"]["content"]["application/json"];
type OrganizationView = paths["/api/v1/library/untracked-scopes/{scope_id}/discoveries/{file_id}/organizations/{operation_id}"]["get"]["responses"]["200"]["content"]["application/json"];
type EpisodeOption = { canonical_unit_id: string; media_metadata_version: number; unit_metadata_version: number; season: number; episode: number; tmdb_episode_id: number; unit_key: string; title: string };
type ScopePage = { items: { id: string; version: number; enabled: boolean; local_root: string; target_root_path: string; target_media_type: string }[]; next_cursor: string; has_more: boolean };
class ReviewHTTPError extends Error { constructor(message: string, readonly status: number) { super(message); } }
type Pending = { kind: "review" | "confirmation" | "organization"; key: string; scope: string; file: string; body: Record<string, unknown>; attempted?: boolean };
type OrganizationLocator = { scope: string; file: string; operation: string };
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
function restore(storage: string): Pending | null {
  try {
    const raw = sessionStorage.getItem(storage);
    if (!raw || raw.length > 16000) return null;
    const p = JSON.parse(raw) as Pending;
    return p && ["review", "confirmation", "organization"].includes(p.kind) && uuid.test(p.scope) && uuid.test(p.file) && /^[A-Za-z0-9:_.-]{1,160}$/.test(p.key) && p.body && typeof p.body === "object" && !Array.isArray(p.body) ? { ...p, attempted: true } : null;
  } catch { return null; }
}

function restoreOrganization(storage: string): OrganizationLocator | null {
  try {
    const raw = sessionStorage.getItem(storage);
    if (!raw || raw.length > 512) return null;
    const p = JSON.parse(raw) as OrganizationLocator;
    return p && uuid.test(p.scope) && uuid.test(p.file) && uuid.test(p.operation) ? { scope: p.scope, file: p.file, operation: p.operation } : null;
  } catch { return null; }
}

export function ExternalFileReviewPanel() {
  const { user } = useAuth();
  if (user?.role !== "admin") return null;
  return <ExternalFileReviewEditor key={`${user.id}:${user.role}`} account={user.id} />;
}

function ExternalFileReviewEditor({ account }: { account: string }) {
  const storage = `pulse.external-file-review.v1:${account}`;
  const [initial] = useState(() => restore(storage));
  const organizationStorage = `pulse.external-file-organization.v1:${account}`;
  const [lastOrganization, setLastOrganization] = useState(() => restoreOrganization(organizationStorage));
  const [scopeID, setScopeID] = useState(initial?.scope ?? "");
  const [scope, setScope] = useState<Scope | null>(null);
  const [scopes, setScopes] = useState<ScopePage | null>(null);
  const [files, setFiles] = useState<Discoveries | null>(null);
  const [fileID, setFileID] = useState(initial?.file ?? "");
  const [tmdb, setTMDB] = useState("");
  const [season, setSeason] = useState("1");
  const [explicit, setExplicit] = useState(false);
  const [options, setOptions] = useState<EpisodeOption[] | null>(null);
  const [unitID, setUnitID] = useState("");
  const [request, setRequest] = useState<ReviewReceipt | null>(null);
  const [review, setReview] = useState<Review | null>(null);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const [organization, setOrganization] = useState<Pick<Organization, "operation"> | null>(null);
  const [progress, setProgress] = useState<OrganizationView | null>(null);
  const [target, setTarget] = useState("");
  const [action, setAction] = useState("hardlink");
  const [ack, setAck] = useState(false);
  const [pending, setPending] = useState<Pending | null>(initial);
  const [canRetryOriginal, setCanRetryOriginal] = useState<Pending | null>(null);
  const pendingCurrent = useRef(pending);
  pendingCurrent.current = pending;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState(initial ? "存在结果待确认的命令，请先读取原回执。" : "");
  const owner = useRef<AbortController | null>(null);
  const writing = useRef(false);
  useEffect(() => () => { owner.current?.abort(); }, []);
  const base = `/api/v1/library/untracked-scopes/${scopeID}/discoveries/${fileID}`;
  const selected = files?.items.find(f => f.id === fileID);
  const option = options?.find(o => o.canonical_unit_id === unitID);
  const id = Number(tmdb || 0), seasonNumber = Number(season);
  const ready = review?.state === "ready" && !review.stale && review.evidence?.complete && review.evidence.candidate;
  function keep(p: Pending | null) {
    pendingCurrent.current = p; setPending(p); setCanRetryOriginal(null);
    try { if (p) sessionStorage.setItem(storage, JSON.stringify(p)); else sessionStorage.removeItem(storage); } catch { /* In-memory recovery remains available when storage is disabled. */ }
  }
  function resetPreview() { setCanRetryOriginal(null); setRequest(null); setReview(null); setConfirmation(null); setOrganization(null); setProgress(null); setAck(false); }
  function persistCommand(p: Pending): Pending {
    const saved = { ...p, attempted: true };
    pendingCurrent.current = p; setPending(p);
    try {
      const serialized = JSON.stringify(saved);
      sessionStorage.setItem(storage, serialized);
      if (sessionStorage.getItem(storage) !== serialized) throw new Error("journal mismatch");
    } catch {
      setNotice(p.attempted === false ? "原命令保留在当前页面，尚未提交。恢复会话存储后可提交同一命令。" : "已停止再次提交，请保留原命令并读取原回执。");
      throw new Error("无法保存原命令，已停止提交。请允许浏览器会话存储后重试。");
    }
    pendingCurrent.current = saved; setPending(saved);
    return saved;
  }
  async function loadScopes(cursor = "") {
    await run(async signal => {
      const page = await fetchJSON<ScopePage>(`/api/v1/library/untracked-scopes${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`, signal);
      setScopes(page);
    });
  }
  async function fetchJSON<T>(url: string, signal: AbortSignal, body?: Record<string, unknown>, key?: string): Promise<T> {
    const headers: Record<string, string> = {};
    if (body) {
      headers["Content-Type"] = "application/json";
      headers["Idempotency-Key"] = key!;
      try {
        const token = document.cookie.split("; ").find(cookie => cookie.startsWith("csrf_token="))?.slice("csrf_token=".length);
        if (token) headers["X-CSRF-Token"] = decodeURIComponent(token);
      } catch { /* Invalid Cookie cannot authorize a write. */ }
    }
    const response = await fetch(url, { method: body ? "POST" : "GET", credentials: "same-origin", cache: "no-store", signal,
      headers, body: body ? JSON.stringify(body) : undefined });
    signal.throwIfAborted();
    if (!response.ok) throw new ReviewHTTPError(response.status === 409 ? "文件、范围或元数据已变化，请重新读取后预览。" : response.status === 403 || response.status === 401 ? "当前账号无权处理该文件，请检查登录和管理员权限。" : response.status === 404 ? "尚未找到该对象或回执，请保留原命令并再次读取。" : "请求未完成，请稍后读取原状态。", response.status);
    const value = await response.json() as T;
    signal.throwIfAborted();
    return value;
  }
  async function run(work: (signal: AbortSignal) => Promise<void>) {
    if (writing.current) return;
    writing.current = true; setBusy(true); setError(""); setCanRetryOriginal(null);
    const controller = new AbortController(); owner.current?.abort(); owner.current = controller;
    try { await work(controller.signal); } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : "请求未完成，请重新读取。"); }
    finally { writing.current = false; if (!controller.signal.aborted) setBusy(false); }
  }
  async function loadFiles(cursor = "") {
    await run(async signal => {
      if (!uuid.test(scopeID)) throw new Error("请先选择一个已配置目录范围。");
      const result = await fetchJSON<{ config: Scope | null }>(`/api/v1/library/untracked-scopes/${scopeID}`, signal);
      if (!result.config?.enabled) throw new Error("该目录范围尚未启用，请先完成目录配置。");
      const page = await fetchJSON<Discoveries>(`/api/v1/library/untracked-scopes/${scopeID}/discoveries?limit=64${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`, signal);
      setScope(result.config); setFiles(page);
      if (!pending) { setFileID(""); resetPreview(); setOptions(null); setUnitID(""); }
    });
  }
  async function loadOptions() {
    await run(async signal => {
      setOptions(null); setUnitID(""); resetPreview();
      if (!Number.isSafeInteger(id) || id < 1 || !Number.isInteger(seasonNumber) || seasonNumber < 1 || seasonNumber > 99) throw new Error("请输入有效 TMDB 剧集 ID 和规范季号。");
      const result = await fetchJSON<{ items: EpisodeOption[] }>(`${base}/episode-options?tmdb_id=${id}&season=${seasonNumber}`, signal);
      setOptions(result.items);
    });
  }
  async function accept(p: Pending, value: ReviewReceipt | Confirmation | Organization) {
    if (p.kind === "review") { setRequest(value as ReviewReceipt); setReview(null); }
    if (p.kind === "confirmation") setConfirmation(value as Confirmation);
    if (p.kind === "organization") {
      const accepted = value as Organization;
      const locator = { scope: p.scope, file: p.file, operation: accepted.operation.id };
      setOrganization(accepted); setProgress(null); setLastOrganization(locator);
      try { sessionStorage.setItem(organizationStorage, JSON.stringify(locator)); } catch { /* Current-page result remains available. */ }
    }
    keep(null); setNotice(p.kind === "review" ? "识别请求已保存，请读取识别预览。" : p.kind === "confirmation" ? "已确认映射和目标，尚未提交整理。" : "整理计划已排队，请读取执行状态。");
  }
  async function submit(original: Pending, signal: AbortSignal) {
    const p = persistCommand(original);
    setNotice("正在提交；若响应中断，请读取原回执。");
    const endpoint = p.kind === "review" ? "reviews" : p.kind === "confirmation" ? "confirmations" : "organizations";
    try {
      const value = await fetchJSON<ReviewReceipt | Confirmation | Organization>(`/api/v1/library/untracked-scopes/${p.scope}/discoveries/${p.file}/${endpoint}`, signal, p.body, p.key);
      await accept(p, value);
    } catch (e) {
      if (!signal.aborted && e instanceof ReviewHTTPError && [400, 401, 403, 404, 409].includes(e.status)) {
        keep(null); resetPreview(); setOptions(null); setUnitID("");
        setNotice("提交被拒绝，请重新读取文件和元数据后预览。");
      }
      throw e;
    }
  }
  async function send(kind: Pending["kind"], body: Record<string, unknown>) {
    const p: Pending = { kind, body, scope: scopeID, file: fileID, key: crypto.randomUUID(), attempted: false };
    await run(signal => submit(p, signal));
  }
  async function recover() {
    if (!pending) return;
    const p = pending;
    await run(async signal => {
      const endpoint = p.kind === "review" ? "review-commands" : p.kind === "confirmation" ? "confirmation-commands" : "organization-commands";
      try {
        const value = await fetchJSON<ReviewReceipt | Confirmation | Organization>(`/api/v1/library/untracked-scopes/${p.scope}/discoveries/${p.file}/${endpoint}/${p.key}`, signal);
        if (pendingCurrent.current === p) await accept(p, value);
      } catch (e) {
        if (!signal.aborted && pendingCurrent.current === p && e instanceof ReviewHTTPError && e.status === 404) setCanRetryOriginal(p);
        throw e;
      }
    });
  }
  async function readLastOrganization() {
    if (pending || !lastOrganization) return;
    const locator = lastOrganization;
    await run(async signal => {
      resetPreview();
      const value = await fetchJSON<OrganizationView>(`/api/v1/library/untracked-scopes/${locator.scope}/discoveries/${locator.file}/organizations/${locator.operation}`, signal);
      setScopeID(locator.scope); setFileID(locator.file);
      setScope(null); setFiles(null); setOptions(null); setUnitID("");
      setOrganization({ operation: value.operation }); setProgress(value);
    });
  }
  function requestReview() {
    if (!scope || !selected) return;
    const proof = explicit && option ? { canonical_unit_id: option.canonical_unit_id, media_metadata_version: option.media_metadata_version, unit_metadata_version: option.unit_metadata_version, season: option.season, episode: option.episode, tmdb_episode_id: option.tmdb_episode_id } : {};
    void send("review", { scope_version: scope.version, file_revision: selected.revision, media_type: scope.target_media_type, tmdb_id: id, ...proof });
  }
  return <section className="card config-panel" aria-label="外部文件识别与整理">
    <div className="config-heading"><h2>外部文件识别与整理</h2></div>
    <p className="config-description">选择已配置目录中的观测文件，核对识别预览后确认整理。特殊文件名可明确指定已有的规范单集。</p>
    {error && <p role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}{busy && <p role="status">正在处理…</p>}
    {pending && <div className="config-editor">{pending.attempted === false ? <><p>原命令尚未提交，保留了文件、选择和请求键。</p><button type="button" className="btn-secondary" disabled={busy} onClick={() => void run(signal => submit(pending, signal))}>保存并提交原命令</button></> : <><p>上次提交的结果尚未确认。读取原回执会保留原文件、选择和请求键。</p><button type="button" className="btn-secondary" disabled={busy} onClick={() => void recover()}>读取原回执</button>{canRetryOriginal === pending && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void run(signal => submit(pending, signal))}>使用原键提交</button>}</>}</div>}
    {!pending && lastOrganization && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void readLastOrganization()}>读取上次整理结果</button>}
    <fieldset className="config-editor" disabled={busy || pending !== null}>
      <legend>目录与文件</legend>
      <button type="button" className="btn-secondary" onClick={() => void loadScopes()}>读取已配置目录</button>
      {scopes && <><label>已配置目录<select aria-label="已配置目录" value={scopeID} onChange={e => { setScopeID(e.target.value); setScope(null); setFiles(null); setFileID(""); setTMDB(""); setExplicit(false); setOptions(null); setUnitID(""); resetPreview(); }}><option value="">选择来源与目标目录</option>{scopes.items.map(s => <option key={s.id} value={s.id} disabled={!s.enabled}>{s.local_root} → {s.target_root_path}{s.enabled ? "" : "（已停用）"}</option>)}</select></label>{scopes.items.length === 0 && <p role="status">当前账号尚未保存目录范围，请先完成目录范围配置。</p>}{scopes.has_more && <button type="button" className="btn-secondary" onClick={() => void loadScopes(scopes.next_cursor)}>下一页目录</button>}</>}
      <button type="button" className="btn-secondary" disabled={!scopeID} onClick={() => void loadFiles()}>读取目录文件</button>
      {scope && <p>{scope.local_root} → {scope.target_root_path}</p>}
      {files && <><label>观测文件<select value={fileID} onChange={e => { setFileID(e.target.value); resetPreview(); setOptions(null); setUnitID(""); }}><option value="">选择一个文件</option>{files.items.map(f => <option key={f.id} value={f.id}>{f.relative_path}</option>)}</select></label>{files.items.length === 0 && <p>当前没有观测文件，请等待已配置目录的扫描完成。</p>}<p>目录观测不表示文件已写完；提交前会再次检查文件。</p>{files.has_more && <button type="button" className="btn-secondary" onClick={() => void loadFiles(files.next_cursor)}>下一页文件</button>}</>}
    </fieldset>
    {fileID && scope && <fieldset className="config-editor" disabled={busy || pending !== null || confirmation !== null || organization !== null}>
      <legend>选择媒体身份</legend>
      <label>TMDB {scope.target_media_type === "tv" ? "剧集" : "电影"} ID<input inputMode="numeric" value={tmdb} onChange={e => { setTMDB(e.target.value); resetPreview(); setOptions(null); setUnitID(""); }} /></label>
      <p>留空时按文件名自动建议。明确填写 ID 后仍会校验媒体详情。</p>
      {scope.target_media_type === "tv" && <><label><input type="checkbox" checked={explicit} onChange={e => { setExplicit(e.target.checked); resetPreview(); setOptions(null); setUnitID(""); }} />明确选择一个规范单集</label>{explicit && <><label>规范季号<input inputMode="numeric" value={season} onChange={e => { setSeason(e.target.value); setOptions(null); setUnitID(""); resetPreview(); }} /></label><button type="button" className="btn-secondary" onClick={() => void loadOptions()}>读取规范剧集</button>{options && <label>规范单集<select aria-label="规范单集" value={unitID} onChange={e => { setUnitID(e.target.value); resetPreview(); }}><option value="">选择已验证的单集</option>{options.map(o => <option key={o.canonical_unit_id} value={o.canonical_unit_id}>{o.title} · {o.unit_key}</option>)}</select></label>}{options?.length === 0 && <p role="status">没有该季的可信剧集元数据。请先刷新对应电视剧的元数据，再重新读取。</p>}</>}</>}
      <button type="button" className="btn-primary" disabled={!selected || !Number.isSafeInteger(id) || id < 0 || (explicit && !option)} onClick={requestReview}>生成识别预览</button>
    </fieldset>}
    {request && <button type="button" className="btn-secondary" disabled={busy || pending !== null} onClick={() => void run(async signal => { const value = await fetchJSON<Review>(`${base}/reviews/${request.request.id}`, signal); setReview(value); setAck(false); })}>读取识别预览</button>}
    {review && <div className="config-editor"><h3>识别预览</h3>{review.stale ? <p role="status">文件、目录范围或元数据已变化，请重新生成预览。</p> : ready ? <><p>{ready.title} · {ready.namespace} {ready.tmdb_id}</p><ul>{ready.units.map(u => <li key={u.unit_key}>{u.unit_key}{u.tmdb_episode_id ? ` · TMDB 单集 ${u.tmdb_episode_id}` : ""}</li>)}</ul></> : <p role="status">{["queued", "running"].includes(review.state) ? "识别仍在处理，请稍后再次读取。" : "当前证据不足，请核对 TMDB 身份与规范剧集后重新预览。"}</p>}</div>}
    {ready && !review?.stale && !organization && <fieldset className="config-editor" disabled={busy || pending !== null || confirmation !== null}>
      <legend>确认目标</legend>
      <label>目标相对路径<input value={target} onChange={e => { setTarget(e.target.value); setAck(false); }} placeholder="Series (2026)/Season 01/Series.S01E07.mkv" /></label>
      <label>整理方式<select value={action} onChange={e => { setAction(e.target.value); setAck(false); }}><option value="hardlink">硬链接</option><option value="copy">复制</option><option value="reflink">写时复制</option><option value="move">移动</option></select></label>
      <label><input type="checkbox" checked={ack} onChange={e => setAck(e.target.checked)} />我已核对该文件对应的媒体、规范集号与目标路径{action === "move" ? "，理解成功移动后将移除源文件" : ""}</label>
      <button type="button" className="btn-primary" disabled={!ack || !target} onClick={() => void send("confirmation", { review_id: review!.request.id, material_hash: review!.material_hash, scope_version: review!.request.input.scope_version, file_revision: review!.request.input.file_revision, candidate_key: ready.key, action, target_relative_path: target })}>确认识别与目标</button>
    </fieldset>}
    {confirmation && !organization && <div className="config-editor"><p>已确认：{confirmation.material.input.target_relative_path}</p><button type="button" className="btn-primary" disabled={busy || pending !== null} onClick={() => void send("organization", { confirmation_id: confirmation.material.id })}>提交整理计划</button></div>}
    {organization && <div className="config-editor"><button type="button" className="btn-secondary" disabled={busy || pending !== null} onClick={() => void run(async signal => setProgress(await fetchJSON<OrganizationView>(`${base}/organizations/${organization.operation.id}`, signal)))}>读取整理状态</button><p role="status">{progress?.state === "completed" ? "整理完成，已记录文件入库。" : progress?.review_required ? "整理需要人工复核，请查看活动记录。" : progress?.state === "failed" ? "整理失败，请查看活动记录。" : "整理已排队或正在执行，请稍后读取状态。"}</p></div>}
    {organization && progress?.state === "completed" && <UntrackedCompanionsPanel operationId={organization.operation.id} />}
  </section>;
}

import { useEffect, useRef, useState } from "react";
import { useAuth } from "@/features/auth/AuthContext";
import type { paths } from "@/api/client";
import "./external-batch.css";

type Review = paths["/api/v1/library/untracked-scopes/{scope_id}/discoveries/{file_id}/reviews/{review_id}"]["get"]["responses"]["200"]["content"]["application/json"];
type FileRow = { id: string; relative_path: string; revision: number };
type Option = { canonical_unit_id: string; media_metadata_version: number; unit_metadata_version: number; season: number; episode: number; tmdb_episode_id: number; unit_key: string; title: string };
type Confirmation = { review_id: string; material_hash: string; scope_version: number; file_revision: number; candidate_key: string; action: string; target_relative_path: string };
type Selection = { file_id: string; confirmation: Confirmation };
type Batch = { id: string; command_key: string; scope_id: string; rows: { selection: Selection; file: FileRow; review: Review }[] };
type View = { batch: Batch; status: { confirmation: object | null; organization: object | null; progress: { state: string } | null }[] };
type Draft = { file: FileRow; option: string; target: string; action: string; reviewID?: string; review?: Review };
type Pending = { kind: "review" | "batch" | "confirm" | "organize"; key: string; file?: string; row?: number; batch?: string };
type Saved = { scope: string; scopeVersion: number; tmdb: string; season: string; drafts: Draft[]; options: Option[]; batch?: string; batchKey?: string; pending?: Pending };
type Scope = { id: string; enabled: boolean; target_media_type: string; local_root: string; target_root_path: string };
const empty: Saved = { scope: "", scopeVersion: 0, tmdb: "", season: "1", drafts: [], options: [] };
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
function restore(key: string): Saved {
  try { const raw = sessionStorage.getItem(key); if (!raw || raw.length > 524288) return empty; const s = JSON.parse(raw) as Saved;
    if (!s || !uuid.test(s.scope) || !Array.isArray(s.drafts) || s.drafts.length > 20 || !Array.isArray(s.options) || s.options.length > 10000 || s.drafts.some(d => !uuid.test(d.file?.id))) return empty;
    if (s.batch && !uuid.test(s.batch) || s.pending && !["review", "batch", "confirm", "organize"].includes(s.pending.kind)) return empty;
    return s;
  } catch { return empty; }
}
function actionText(action: string): string {
  switch (action) {
    case "hardlink": return "硬链接";
    case "copy": return "复制";
    case "reflink": return "写时复制";
    case "move": return "移动（成功后移除源）";
    default: return "方式待核对";
  }
}
function progressText(state: string | undefined): string | undefined {
  switch (state) {
    case undefined: return undefined;
    case "pending": case "queued": return "等待整理";
    case "running": return "正在整理";
    case "completed": return "整理已完成";
    case "failed": case "dead": case "review_required": return "整理未完成，需人工复核";
    default: return "整理状态待核对";
  }
}

class BatchHTTPError extends Error { constructor(readonly status: number) { super(status === 409 ? "文件、目录或元数据已变化，请重新预览该行。" : status === 401 || status === 403 ? "当前账号无权读取或处理该批次。" : status === 404 ? "尚未找到原回执，请保留原命令并再次读取。" : "请求未完成，请读取原回执。"); } }
export function ExternalBatchImportPanel() {
  const { user } = useAuth();
  return user?.role === "admin" ? <ExternalBatchEditor key={`${user.id}:${user.role}`} account={user.id} /> : null;
}
function ExternalBatchEditor({ account }: { account: string }) {
  const storage = `pulse.external-batch.v1:${account}`;
  const [saved, setSaved] = useState<Saved>(() => restore(storage));
  const current = useRef(saved); current.current = saved;
  const [scopes, setScopes] = useState<Scope[]>([]), [files, setFiles] = useState<FileRow[]>([]);
  const [fileCursor, setFileCursor] = useState(""), [scopeCursor, setScopeCursor] = useState("");
  const [view, setView] = useState<View | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [checked, setChecked] = useState<Record<number, boolean>>({});
  const owner = useRef<AbortController | null>(null), running = useRef(false);
  useEffect(() => () => owner.current?.abort(), []);
  function keep(value: Saved) {
    const journal = { ...value, drafts: value.drafts.map(({ review: _review, ...d }) => d), options: value.options.filter(o => value.drafts.some(d => d.option === o.canonical_unit_id)) };
    const raw = JSON.stringify(journal); if (raw.length > 524288) throw new Error("恢复记录过大，请减少本批文件数量。尚未提交新命令。"); sessionStorage.setItem(storage, raw);
    if (sessionStorage.getItem(storage) !== raw) throw new Error("无法保存恢复记录，已停止提交。");
    current.current = value; setSaved(value);
  }
  function patch(value: Partial<Saved>) { keep({ ...current.current, ...value }); }
  function draft(index: number, value: Partial<Draft>) { patch({ drafts: current.current.drafts.map((d, i) => i === index ? { ...d, ...value } : d) }); }
  async function request<T>(url: string, signal: AbortSignal, body?: object, key?: string): Promise<T> {
    const headers: Record<string, string> = {};
    if (body) { headers["Content-Type"] = "application/json"; if (key) headers["Idempotency-Key"] = key;
      const csrf = document.cookie.split("; ").find(c => c.startsWith("csrf_token="))?.slice(11); if (csrf) headers["X-CSRF-Token"] = decodeURIComponent(csrf); }
    const response = await fetch(url, { method: body ? "POST" : "GET", body: body ? JSON.stringify(body) : undefined, headers, credentials: "same-origin", cache: "no-store", signal });
    signal.throwIfAborted(); if (!response.ok) throw new BatchHTTPError(response.status); const value = await response.json() as T; signal.throwIfAborted(); return value;
  }
  async function run(work: (signal: AbortSignal) => Promise<void>) {
    if (running.current) return; running.current = true; setBusy(true); setError("");
    const c = new AbortController(); owner.current?.abort(); owner.current = c;
    try { await work(c.signal); } catch (e) { if (!c.signal.aborted) setError(e instanceof Error ? e.message : "请求未完成。"); }
    finally { running.current = false; if (!c.signal.aborted) setBusy(false); }
  }
  const base = `/api/v1/library/untracked-scopes/${saved.scope}`;
  async function readBatch(signal: AbortSignal) {
    const s = current.current;
    const url = s.batch ? `${base}/external-batches/${s.batch}` : `${base}/external-batch-commands/${encodeURIComponent(s.batchKey ?? s.pending?.key ?? "")}`;
    const value = await request<View>(url, signal); setView(value);
    const p = s.pending;
    const resolved = !p || p.kind === "batch" || p.kind === "confirm" && value.status[p.row!]?.confirmation || p.kind === "organize" && value.status[p.row!]?.organization;
    keep({ ...s, batch: value.batch.id, batchKey: value.batch.command_key, pending: resolved ? undefined : p });
  }
  async function recover(signal: AbortSignal) {
    const s = current.current, p = s.pending;
    if (p?.kind === "review") {
      const receipt = await request<{ request: { id: string } }>(`${base}/discoveries/${p.file}/review-commands/${encodeURIComponent(p.key)}`, signal);
      patch({ drafts: s.drafts.map(d => d.file.id === p.file ? { ...d, reviewID: receipt.request.id } : d), pending: undefined });
    } else await readBatch(signal);
  }
  async function sendReview(index: number, signal: AbortSignal) {
    const s = current.current, d = s.drafts[index], o = s.options.find(x => x.canonical_unit_id === d.option);
    if (!o) throw new Error("请逐行明确选择规范单集。");
    const p: Pending = { kind: "review", key: crypto.randomUUID(), file: d.file.id, row: index }; patch({ pending: p });
    try {
      const { unit_key: _unit, title: _title, ...canonical } = o;
      const receipt = await request<{ request: { id: string } }>(`${base}/discoveries/${d.file.id}/reviews`, signal, { scope_version: s.scopeVersion, file_revision: d.file.revision, media_type: "tv", tmdb_id: Number(s.tmdb), ...canonical }, p.key);
      draft(index, { reviewID: receipt.request.id }); patch({ pending: undefined });
    } catch (e) { if (e instanceof BatchHTTPError && [400, 401, 403, 404, 409].includes(e.status)) patch({ pending: undefined }); throw e; }
  }
  const duplicate = saved.drafts.some((d, i) => saved.drafts.some((other, j) => i !== j && (d.option && other.option === d.option || d.target && other.target === d.target)));
  const sameDirectory = new Set(saved.drafts.map(d => d.file.relative_path.split("/").slice(0, -1).join("/"))).size <= 1;
  const ready = saved.drafts.length > 0 && saved.drafts.every(d => d.target && d.review?.state === "ready" && !d.review.stale && d.review.evidence?.candidate) && !duplicate && sameDirectory;
  async function createBatch(signal: AbortSignal) {
    const s = current.current, key = crypto.randomUUID();
    const rows: Selection[] = s.drafts.map(d => ({ file_id: d.file.id, confirmation: { review_id: d.review!.request.id, material_hash: d.review!.material_hash, scope_version: d.review!.request.input.scope_version, file_revision: d.review!.request.input.file_revision, candidate_key: d.review!.evidence!.candidate!.key, target_relative_path: d.target, action: d.action } }));
    patch({ batchKey: key, pending: { kind: "batch", key } });
    try { const value = await request<View>(`${base}/external-batches`, signal, { rows }, key); setView(value); patch({ batch: value.batch.id, pending: undefined }); }
    catch (e) { if (e instanceof BatchHTTPError && [400, 401, 403, 404, 409].includes(e.status)) patch({ pending: undefined }); throw e; }
  }
  async function rowCommand(index: number, kind: "confirm" | "organize", signal: AbortSignal) {
    const s = current.current; if (!s.batch || !checked[index]) return;
    patch({ pending: { kind, key: "", row: index, batch: s.batch } });
    try { await request(`${base}/external-batches/${s.batch}/rows/${index}/${kind}`, signal, { confirmed: true }); patch({ pending: undefined }); await readBatch(signal); }
    catch (e) { if (e instanceof BatchHTTPError && [400, 401, 403, 404, 409].includes(e.status)) patch({ pending: undefined }); throw e; }
  }
  return <section className="card config-panel external-batch" aria-label="批量外部文件导入">
    <div className="config-heading"><h2>批量外部文件导入</h2></div>
    <p className="config-description">同一目录、作品和季最多选择 20 个文件，逐行明确规范集号与目标。保存预览后，每行单独确认并提交整理。</p>
    {error && <p role="alert">{error}</p>}
    {saved.pending && <div className="config-editor"><p role="status">上次提交结果待确认。读取原回执不会再次提交。</p><button className="btn-secondary" disabled={busy} onClick={() => void run(recover)}>读取批次原回执</button></div>}
    {!saved.pending && (saved.batch || saved.batchKey) && <button className="btn-secondary" disabled={busy} onClick={() => void run(readBatch)}>读取批次状态</button>}
    {!saved.batch && <fieldset className="config-editor" disabled={busy || !!saved.pending}>
      <legend>目录与规范剧集</legend>
      <button className="btn-secondary" onClick={() => void run(async signal => { const page = await request<{ items: Scope[]; next_cursor: string }>(`/api/v1/library/untracked-scopes${scopeCursor ? `?cursor=${encodeURIComponent(scopeCursor)}` : ""}`, signal); setScopes(prev => [...prev, ...page.items.filter(s => s.target_media_type === "tv")]); setScopeCursor(page.next_cursor); })}>{scopeCursor ? "下一页批量目录" : "读取批量目录"}</button>
      <label>批量目录<select aria-label="批量目录" value={saved.scope} onChange={e => { keep({ ...empty, scope: e.target.value }); setFiles([]); setView(null); setFileCursor(""); }}><option value="">选择目录</option>{scopes.map(s => <option key={s.id} value={s.id} disabled={!s.enabled}>{s.local_root} → {s.target_root_path}</option>)}</select></label>
      <button className="btn-secondary" disabled={!saved.scope} onClick={() => void run(async signal => { const scope = await request<{ config: { version: number } }>(base, signal); const page = await request<{ items: FileRow[]; next_cursor: string }>(`${base}/discoveries?limit=64${fileCursor ? `&cursor=${encodeURIComponent(fileCursor)}` : ""}`, signal); patch({ scopeVersion: scope.config.version }); setFiles(prev => fileCursor ? [...prev, ...page.items] : page.items); setFileCursor(page.next_cursor); })}>{fileCursor ? "下一页批量文件" : "读取批量文件"}</button>
      {files.map(f => <label key={f.id} className="external-batch-file"><input type="checkbox" checked={saved.drafts.some(d => d.file.id === f.id)} disabled={saved.drafts.length >= 20 && !saved.drafts.some(d => d.file.id === f.id)} onChange={e => patch({ drafts: e.target.checked ? [...saved.drafts, { file: f, option: "", target: "", action: "hardlink" }] : saved.drafts.filter(d => d.file.id !== f.id), options: [] })} />{f.relative_path}</label>)}
      <p>已选 {saved.drafts.length}/20；目录观测不证明文件已写完。</p>
      <label>批量 TMDB 剧集 ID<input inputMode="numeric" value={saved.tmdb} onChange={e => patch({ tmdb: e.target.value, options: [], drafts: saved.drafts.map(d => ({ ...d, option: "", review: undefined, reviewID: undefined })) })} /></label>
      <label>批量规范季号<input inputMode="numeric" value={saved.season} onChange={e => patch({ season: e.target.value, options: [], drafts: saved.drafts.map(d => ({ ...d, option: "", review: undefined, reviewID: undefined })) })} /></label>
      <button className="btn-secondary" disabled={!saved.drafts.length || !Number.isSafeInteger(Number(saved.tmdb)) || Number(saved.tmdb) < 1 || !Number.isInteger(Number(saved.season)) || Number(saved.season) < 1 || Number(saved.season) > 99} onClick={() => void run(async signal => { const result = await request<{ items: Option[] }>(`${base}/discoveries/${saved.drafts[0].file.id}/episode-options?tmdb_id=${saved.tmdb}&season=${saved.season}`, signal); patch({ options: result.items }); })}>读取批量规范剧集</button>
    </fieldset>}
    {duplicate && <p role="alert">规范单集或目标重复，请逐行修改；不会按数量推断集号。</p>}
    {!sameDirectory && <p role="alert">所选文件必须位于同一目录。</p>}
    {saved.drafts.length > 0 && !saved.batch && <div className="external-batch-scroll"><table><caption>逐行核对文件、规范集号和目标</caption><thead><tr><th>文件</th><th>规范单集</th><th>目标与方式</th><th>预览</th></tr></thead><tbody>{saved.drafts.map((d, i) => <tr key={d.file.id}>
      <td data-label="文件">{d.file.relative_path}</td><td data-label="规范单集"><select aria-label={`第${i + 1}行规范单集`} value={d.option} disabled={busy || !!saved.pending || !!d.reviewID} onChange={e => draft(i, { option: e.target.value })}><option value="">明确选择</option>{saved.options.map(o => <option key={o.canonical_unit_id} value={o.canonical_unit_id}>{o.title} · {o.unit_key} · TMDB {o.tmdb_episode_id}</option>)}</select></td>
      <td data-label="目标与方式"><label>目标相对路径<input aria-label={`第${i + 1}行目标`} value={d.target} disabled={busy || !!saved.pending} onChange={e => draft(i, { target: e.target.value })} /></label><select aria-label={`第${i + 1}行方式`} value={d.action} disabled={busy || !!saved.pending} onChange={e => draft(i, { action: e.target.value })}><option value="hardlink">硬链接</option><option value="copy">复制</option><option value="reflink">写时复制</option><option value="move">移动（成功后移除源）</option></select></td>
      <td data-label="预览">{!d.reviewID ? <button className="btn-secondary" disabled={busy || !!saved.pending || !d.option || duplicate || !sameDirectory} onClick={() => void run(signal => sendReview(i, signal))}>预览第{i + 1}行</button> : <><button className="btn-secondary" disabled={busy || !!saved.pending} onClick={() => void run(async signal => draft(i, { review: await request<Review>(`${base}/discoveries/${d.file.id}/reviews/${d.reviewID}`, signal) }))}>读取第{i + 1}行预览</button><p>{d.review?.stale ? "该行已过期，请重新选择文件与规范集。" : d.review?.state === "ready" ? "证据已就绪" : d.review?.state === "review_required" ? "存在歧义，需要人工复核" : "等待读取预览"}</p></>}</td>
    </tr>)}</tbody></table></div>}
    {!saved.batch && <button className="btn-primary" disabled={busy || !!saved.pending || !ready} onClick={() => void run(createBatch)}>保存批量预览</button>}
    {view && <div className="external-batch-scroll"><table><caption>原批次逐行结果</caption><thead><tr><th>文件与规范集</th><th>冻结目标</th><th>确认与结果</th></tr></thead><tbody>{view.batch.rows.map((r, i) => <tr key={r.selection.file_id}><td data-label="文件与规范集">{r.file.relative_path}<br />{r.review.evidence?.candidate?.units.map(u => u.unit_key).join(", ")}</td><td data-label="冻结目标">{r.selection.confirmation.target_relative_path}<br />{actionText(r.selection.confirmation.action)}</td><td data-label="确认与结果"><label><input type="checkbox" checked={!!checked[i]} onChange={e => setChecked(prev => ({ ...prev, [i]: e.target.checked }))} />已核对第{i + 1}行{r.selection.confirmation.action === "move" ? "并同意成功后移除源文件" : ""}</label><button className="btn-secondary" disabled={busy || !!saved.pending || !checked[i] || !!view.status[i]?.confirmation} onClick={() => void run(signal => rowCommand(i, "confirm", signal))}>确认第{i + 1}行</button><button className="btn-primary" disabled={busy || !!saved.pending || !checked[i] || !view.status[i]?.confirmation || !!view.status[i]?.organization} onClick={() => void run(signal => rowCommand(i, "organize", signal))}>整理第{i + 1}行</button><p role="status">{progressText(view.status[i]?.progress?.state) ?? (view.status[i]?.organization ? "已排队" : view.status[i]?.confirmation ? "已确认，尚未整理" : "待逐行确认")}</p></td></tr>)}</tbody></table></div>}
    {!saved.pending && !busy && (saved.drafts.length > 0 || saved.batch) && <button className="btn-secondary" onClick={() => { keep(empty); setView(null); setFiles([]); setChecked({}); }}>新建批次</button>}
  </section>;
}

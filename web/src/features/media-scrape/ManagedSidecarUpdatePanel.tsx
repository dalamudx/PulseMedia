import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import "./media-scrape.css";

type Artifact = { name: string; state: "ready" | "unchanged" | "held" | "updated" | "review_required"; old_hash?: string | null; new_hash?: string | null };
type Result = { id: string; file_id: string; state: "preview" | "held" | "completed" | "review_required"; expires_at: string; artifacts: Artifact[]; command_key?: string };
type Journal = { version: 1; userId: string; fileId: string; unitId: string; key: string; previewId: string; resolved: boolean };
type Options = { params: { path: { id: string; key?: string }; header?: { "Idempotency-Key": string } }; body?: { unit_id: string } | { preview_id: string; confirmed: true }; signal: AbortSignal; cache: "no-store" };
type Reply = { data?: unknown; error?: unknown };
// Keep the shared client's auth/CSRF middleware while these new paths await generation.
const client = api as unknown as { POST(path: "/api/v1/library/files/{id}/sidecar-update-previews" | "/api/v1/library/files/{id}/sidecar-update-commands", options: Options): Promise<Reply>; GET(path: "/api/v1/library/files/{id}/sidecar-update-commands/{key}", options: Options): Promise<Reply> };
const uuid = (value: unknown): value is string => typeof value === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value);
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const hash = (value: unknown) => value == null || (typeof value === "string" && /^[a-f0-9]{64}$/i.test(value));
function valid(value: unknown, fileId: string, commandKey?: string): value is Result {
  return object(value) && uuid(value.id) && value.file_id === fileId && typeof value.expires_at === "string" && Number.isFinite(Date.parse(value.expires_at)) &&
    (commandKey ? ["completed", "review_required"].includes(String(value.state)) && (value.command_key === undefined || value.command_key === commandKey) : ["preview", "held"].includes(String(value.state))) &&
    Array.isArray(value.artifacts) && value.artifacts.length > 0 && value.artifacts.length <= 3 && new Set(value.artifacts.map(a => object(a) ? a.name : null)).size === value.artifacts.length &&
    value.artifacts.every(a => object(a) && ["movie.nfo", "tvshow.nfo", "poster.jpg"].includes(String(a.name)) && (commandKey ? ["ready", "unchanged", "held", "updated", "review_required"] : ["ready", "unchanged", "held"]).includes(String(a.state)) && hash(a.old_hash) && hash(a.new_hash));
}
const labels = { ready: "可更新", unchanged: "内容未变化", held: "保留原文件", updated: "已更新", review_required: "需要人工核对" };

export function ManagedSidecarUpdatePanel({ fileId, unitId }: { fileId: string; unitId: string }) {
  const { user } = useAuth();
  if (!user || user.role !== "admin" || !uuid(fileId) || !uuid(unitId)) return null;
  return <Update key={`${user.id}:${fileId}:${unitId}`} userId={user.id} fileId={fileId} unitId={unitId} />;
}
function Update({ userId, fileId, unitId }: { userId: string; fileId: string; unitId: string }) {
  const storageKey = `pulse:managed-sidecar:v1:${userId}:${fileId}:${unitId}`;
  const [view, setView] = useState<Result | null>(null), [pending, setPending] = useState<Journal | null>(null);
  const [busy, setBusy] = useState(false), [ack, setAck] = useState(false), [blocked, setBlocked] = useState(false), [message, setMessage] = useState("");
  const owner = useRef<AbortController | null>(null), writing = useRef(false);
  function restore(): Journal | null {
    const raw = localStorage.getItem(storageKey);
    if (raw === null) return null;
    if (raw.length > 1024) throw Error();
    const saved: unknown = JSON.parse(raw);
    if (!object(saved) || saved.version !== 1 || saved.userId !== userId || saved.fileId !== fileId || saved.unitId !== unitId || !uuid(saved.key) || !uuid(saved.previewId) || typeof saved.resolved !== "boolean") throw Error();
    return saved as Journal;
  }
  function save(journal: Journal) {
    const raw = JSON.stringify(journal);
    localStorage.setItem(storageKey, raw);
    if (localStorage.getItem(storageKey) !== raw) throw Error();
  }
  async function run(action: "preview" | "confirm" | "read", signal: AbortSignal) {
    if (writing.current || signal.aborted) return;
    writing.current = true; setBusy(true); setAck(false); setMessage("");
    try {
      if (!navigator.locks) throw Error();
      await navigator.locks.request(storageKey, { signal }, async () => {
        if (signal.aborted) return;
        const saved = restore();
        if (action === "read") {
          if (!saved) return;
          setPending(saved); setView(null);
          const reply = await client.GET("/api/v1/library/files/{id}/sidecar-update-commands/{key}", { params: { path: { id: fileId, key: saved.key } }, signal, cache: "no-store" });
          if (signal.aborted) return;
          if (reply.error || !valid(reply.data, fileId, saved.key)) throw Error();
          setView(reply.data); const resolved = { ...saved, resolved: true }; setPending(resolved); save(resolved); setBlocked(false);
          return;
        }
        if (saved && !saved.resolved) { setPending(saved); setView(null); setBlocked(true); throw Error(); }
        if (action === "preview") {
          setView(null);
          const reply = await client.POST("/api/v1/library/files/{id}/sidecar-update-previews", { params: { path: { id: fileId } }, body: { unit_id: unitId }, signal, cache: "no-store" });
          if (signal.aborted) return;
          if (reply.error || !valid(reply.data, fileId)) throw Error();
          setView(reply.data); setBlocked(false); return;
        }
        if (blocked || !ack || !view || view.state !== "preview" || Date.parse(view.expires_at) <= Date.now() || !view.artifacts.some(a => a.state === "ready") || (saved && saved.previewId === view.id)) throw Error();
        const journal: Journal = { version: 1, userId, fileId, unitId, key: crypto.randomUUID(), previewId: view.id, resolved: false };
        save(journal); setPending(journal); setBlocked(true); setView(null);
        const reply = await client.POST("/api/v1/library/files/{id}/sidecar-update-commands", { params: { path: { id: fileId }, header: { "Idempotency-Key": journal.key } }, body: { preview_id: journal.previewId, confirmed: true }, signal, cache: "no-store" });
        if (signal.aborted) return;
        if (reply.error || !valid(reply.data, fileId, journal.key)) throw Error();
        setView(reply.data); const resolved = { ...journal, resolved: true }; setPending(resolved); save(resolved); setBlocked(false);
      });
    } catch {
      if (!signal.aborted) { setBlocked(true); setMessage(action === "preview" ? "预览未完成。请重新读取预览，未确认更新。" : "更新结果尚未确认或恢复记录不可用。请只读取原键结果，不会自动重新提交。若仍无法读取，请人工核对。"); }
    } finally { if (owner.current?.signal === signal) { writing.current = false; if (!signal.aborted) setBusy(false); } }
  }
  useEffect(() => {
    const controller = new AbortController(); owner.current = controller;
    try { const saved = restore(); if (saved) { setPending(saved); setBlocked(!saved.resolved); void run("read", controller.signal); } }
    catch { setBlocked(true); setMessage("恢复记录不可用，已停止更新。请人工核对原命令。"); }
    return () => { controller.abort(); writing.current = false; };
    // This boundary remounts for every account, file, and unit change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const unresolved = !!pending && !pending.resolved;
  const canConfirm = !busy && !blocked && ack && view?.state === "preview" && Date.parse(view.expires_at) > Date.now() && view.artifacts.some(a => a.state === "ready");
  const heading = `managed-sidecar-${fileId}-${unitId}`;
  return <section className="card media-scrape" aria-labelledby={heading} aria-busy={busy}>
    <div className="card-header media-scrape-heading"><h3 id={heading}>受管 NFO 与海报更新</h3></div>
    <p className="card-desc">先预览，再明确确认。仅更新可证明由应用生成且旧内容未改动的 NFO 与海报；人工内容、旧版文件和冲突保留。</p>
    {message ? <p role="alert">{message}</p> : null}
    {view ? <div aria-live="polite"><p>{view.state === "completed" ? "更新已完成" : view.state === "review_required" ? "需要人工核对；保留已有文件" : view.state === "held" ? "本次更新已保留，请人工核对" : "请核对以下预览"}</p>
      <ul>{view.artifacts.map(a => <li key={a.name}><strong>{a.name}</strong> · {labels[a.state]}<div>旧摘要：{a.old_hash ?? "无"}</div><div>新摘要：{a.new_hash ?? "无"}</div></li>)}</ul>
      {view.state === "preview" ? <p>预览有效期至：<time dateTime={view.expires_at}>{view.expires_at}</time></p> : null}
    </div> : null}
    {unresolved ? <p role="status">原命令结果待核对，仅可读取结果。</p> : null}
    <div className="media-scrape-actions">
      <button type="button" style={{ minHeight: 44 }} className="btn-secondary" disabled={busy || unresolved} onClick={() => owner.current && void run("preview", owner.current.signal)}>预览 NFO 与海报更新</button>
      {pending ? <button type="button" style={{ minHeight: 44 }} className="btn-secondary" disabled={busy} onClick={() => owner.current && void run("read", owner.current.signal)}>读取原键结果</button> : null}
    </div>
    {view?.state === "preview" ? <><label className="media-scrape-confirm"><input type="checkbox" checked={ack} disabled={busy || blocked} onChange={e => setAck(e.target.checked)} />我已核对预览，确认更新可更新的 NFO 与海报</label><button type="button" style={{ minHeight: 44 }} className="btn-primary" disabled={!canConfirm} onClick={() => owner.current && void run("confirm", owner.current.signal)}>确认更新 NFO 与海报</button></> : null}
  </section>;
}

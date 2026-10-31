import { useEffect, useRef, useState } from "react";
import { useAuth } from "@/features/auth/AuthContext";
import "./untracked-companions.css";

type Candidate = { key: string; source_relative_path: string; target_relative_path: string };
type Attachment = { id: string; candidate: Candidate; action: "copy" | "hardlink"; state: "pending" | "running" | "unknown" | "completed" | "review_required" };
type View = { video_state: "completed"; items: Attachment[] };
const outcomes: Record<Attachment["state"], string> = {
  pending: "已选择，尚未整理", running: "执行尚未结束，请读取结果", unknown: "结果未知，仅核对已有文件",
  completed: "字幕已整理，源文件保留", review_required: "需要人工核对",
};

// The parent mounts this only for an existing organization operation. The server
// independently requires completed video import for every companion request.
export function UntrackedCompanionsPanel({ operationId }: { operationId: string }) {
  const { user } = useAuth();
  if (!user || user.role !== "admin") return null;
  return <Companions key={`${user.id}:${operationId}`} operationId={operationId} />;
}

function Companions({ operationId }: { operationId: string }) {
  const [view, setView] = useState<View | null>(null);
  const [candidates, setCandidates] = useState<Candidate[] | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [action, setAction] = useState<"copy" | "hardlink">("copy");
  const [busy, setBusy] = useState(false), [readable, setReadable] = useState(false);
  const [message, setMessage] = useState("");
  const alive = useRef(false), writing = useRef(false), controller = useRef<AbortController | null>(null);
  const base = `/api/v1/library/untracked-organizations/${encodeURIComponent(operationId)}/companions`;

  async function request<T>(suffix: string, method = "GET", body?: unknown, key?: string): Promise<T> {
    const headers: Record<string, string> = { ...(body ? { "Content-Type": "application/json" } : {}), ...(key ? { "Idempotency-Key": key } : {}) };
    if (method !== "GET") {
      try {
        const csrf = document.cookie.split("; ").find(cookie => cookie.startsWith("csrf_token="))?.slice("csrf_token=".length);
        if (csrf) headers["X-CSRF-Token"] = decodeURIComponent(csrf);
      } catch { /* A missing or malformed Cookie cannot authorize a write. */ }
    }
    const response = await fetch(base + suffix, {
      method, credentials: "same-origin", cache: "no-store", signal: controller.current?.signal,
      headers,
      ...(body ? { body: JSON.stringify(body) } : {}),
    });
    if (!response.ok) throw new Error("companion request failed");
    return response.json() as Promise<T>;
  }
  async function read() {
    if (writing.current) return;
    writing.current = true; setBusy(true); setReadable(false);
    try {
      const current = await request<View>("");
      if (alive.current) { setView(current); setReadable(true); setMessage(""); }
    } catch {
      if (alive.current) { setView(null); setCandidates(null); setSelected([]); setMessage("状态读取失败。请重新读取，确认视频和字幕各自的结果。"); }
    } finally { writing.current = false; if (alive.current) setBusy(false); }
  }
  useEffect(() => {
    alive.current = true; controller.current = new AbortController(); void read();
    return () => { alive.current = false; controller.current?.abort(); };
    // Account and operation changes remount this boundary.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function discover() {
    if (writing.current) return;
    writing.current = true; setBusy(true); setCandidates(null); setSelected([]); setMessage("");
    try {
      const found = await request<{ items: Candidate[] }>("/discover", "POST");
      if (alive.current) setCandidates(found.items);
    } catch { if (alive.current) { setReadable(false); setMessage("未能完成目录检查；未选择或整理任何字幕，请先读取状态。"); } }
    finally { writing.current = false; if (alive.current) setBusy(false); }
  }
  async function select() {
    if (writing.current || selected.length === 0) return;
    writing.current = true; setBusy(true); setReadable(false); setMessage("");
    const body = { candidate_keys: [...selected], action };
    try {
      const current = await request<View>("", "POST", body, crypto.randomUUID());
      if (alive.current) { setView(current); setCandidates(null); setSelected([]); setReadable(true); }
    } catch { if (alive.current) { setCandidates(null); setSelected([]); setMessage("选择结果尚未确认。请读取已保存的字幕，不会自动重新提交。"); } }
    finally { writing.current = false; if (alive.current) setBusy(false); }
  }
  async function execute(item: Attachment) {
    if (writing.current) return;
    writing.current = true; setBusy(true); setReadable(false); setMessage("");
    try {
      const result = await request<Attachment>(`/${encodeURIComponent(item.id)}/execute`, "POST");
      if (alive.current) { setView(current => current ? { ...current, items: current.items.map(v => v.id === result.id ? result : v) } : current); setReadable(true); }
    } catch { if (alive.current) setMessage("此次字幕结果尚未确认；视频入库结果不变。请先读取状态，再显式核对已有文件。"); }
    finally { writing.current = false; if (alive.current) setBusy(false); }
  }
  const chosen = new Set(view?.items.map(item => item.candidate.key));
  return <section className="card untracked-companions" aria-labelledby={`companions-${operationId}`} aria-busy={busy}>
    <div className="card-header"><h3 id={`companions-${operationId}`}>随片字幕</h3>
      <button className="btn-secondary" type="button" disabled={busy} onClick={() => void read()}>读取字幕结果</button>
    </div>
    <p role="status">{view ? "视频已入库；字幕按下列结果独立记录。" : "视频与字幕结果尚未读取。"}</p>
    <p className="card-desc">检查同目录唯一同名的 SRT / ASS，可保留语言后缀。仅整理明确选中的字幕，保留源文件和已有目标文件。</p>
    {message ? <p role="status">{message}</p> : null}
    <button className="btn-secondary" type="button" disabled={busy || !readable} onClick={() => void discover()}>检查同目录字幕</button>
    {candidates ? <fieldset disabled={busy || !readable}>
      <legend>选择要随片整理的字幕</legend>
      {candidates.length === 0 ? <p>没有唯一匹配的 SRT / ASS；这不表示视频缺失。</p> : candidates.map(item => <label className="companion-choice" key={item.key}>
        <input type="checkbox" checked={selected.includes(item.key)} disabled={chosen.has(item.key)} onChange={event => setSelected(current => event.target.checked ? [...current, item.key] : current.filter(key => key !== item.key))} />
        <span>{item.source_relative_path}<small>目标：{item.target_relative_path}{chosen.has(item.key) ? "（已选择）" : ""}</small></span>
      </label>)}
      <label className="companion-action">整理方式<select value={action} onChange={event => setAction(event.target.value as "copy" | "hardlink")}><option value="copy">复制，保留源字幕</option><option value="hardlink">硬链接，保留源字幕</option></select></label>
      <button className="btn-primary" type="button" disabled={selected.length === 0} onClick={() => void select()}>确认所选字幕及目标</button>
    </fieldset> : null}
    {view?.items.length ? <ul className="companion-results">{view.items.map(item => <li key={item.id}>
      <span>{item.candidate.target_relative_path}<small>{outcomes[item.state]} · {item.action === "copy" ? "复制" : "硬链接"}</small></span>
      {item.state === "pending" || item.state === "unknown" ? <button className="btn-secondary" type="button" disabled={busy || !readable} onClick={() => void execute(item)}>{item.state === "pending" ? "整理此字幕" : "核对已有结果"}</button> : null}
    </li>)}</ul> : null}
  </section>;
}

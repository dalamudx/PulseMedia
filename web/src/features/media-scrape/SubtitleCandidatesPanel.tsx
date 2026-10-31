import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { useAuth } from "@/features/auth/AuthContext";

import "@/features/configuration/mp-business-panels.css";

type State = components["schemas"]["SubtitleCandidateState"];
function subtitleTimestamp(ms: number) {
  const hours = Math.floor(ms / 3_600_000), minutes = Math.floor(ms / 60_000) % 60;
  const seconds = Math.floor(ms / 1_000) % 60, millis = ms % 1_000;
  return `${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}.${String(millis).padStart(3, "0")}`;
}
const outcomes: Record<State["outcome"], string> = {
  not_queried: "尚未查询字幕", querying: "正在查询；请读取结果", candidates: "请选择要安装的字幕",
  not_found: "Shooter 未找到中文 SRT，可稍后重新查询", already_present: "已存在字幕，保留原文件",
  too_small: "视频过小，无法按 Shooter 协议查询", provider_error: "Shooter 查询失败，可重新查询",
  confirming: "安装尚未结束，请读取结果", confirmed: "字幕已安装", review_required: "结果需要人工核对，请先检查已有字幕",
};
export function SubtitleCandidatesPanel({ fileId, mappingRevision }: { fileId: string; mappingRevision?: number }) {
  const { user } = useAuth();
  if (!user || user.role !== "admin") return null;
  return <Candidates key={`${user.id}:${fileId}:${mappingRevision ?? "current"}`} fileId={fileId} frozenRevision={mappingRevision} />;
}
function Candidates({ fileId, frozenRevision }: { fileId: string; frozenRevision?: number }) {
  const [state, setState] = useState<State | null>(null), [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false), [message, setMessage] = useState("");
  const [readable, setReadable] = useState(false);
  const alive = useRef(false), writing = useRef(false), generation = useRef(0), originalKey = useRef<string | null>(null);
  const mappingRevision = frozenRevision ?? state?.mapping_revision ?? 0;
  const load = useCallback(async () => {
    const epoch = ++generation.current;
    setReadable(false);
    try {
      const { data, error } = await api.GET("/api/v1/library/files/{file_id}/subtitle-candidates", { params: { path: { file_id: fileId } }, cache: "no-store" });
      if (!alive.current || epoch !== generation.current) return;
      if (!data || error) throw new Error();
      setState(data); setSelected(""); setReadable(true); setMessage("");
      if (data.outcome !== "querying") originalKey.current = null;
    } catch { if (alive.current && epoch === generation.current) setMessage("字幕状态读取失败，请重试读取。"); }
  }, [fileId]);
  useEffect(() => { alive.current = true; void load(); return () => { alive.current = false; generation.current++; }; }, [load]);
  async function query() {
    if (writing.current || !readable) return;
    writing.current = true; setBusy(true); setMessage("");
    try {
      const key = originalKey.current ?? crypto.randomUUID(); originalKey.current = key;
      const { data, error } = await api.POST("/api/v1/library/files/{file_id}/subtitle-candidates/queries", {
        params: { path: { file_id: fileId }, header: { "Idempotency-Key": key } }, body: { expected_mapping_revision: mappingRevision },
      });
      if (!alive.current) return;
      if (!data || error) throw new Error();
      setState(data); setSelected("");
      if (data.outcome !== "querying") originalKey.current = null;
    } catch { if (alive.current) { setReadable(false); setMessage("查询结果尚未确认，请先读取结果。"); } }
    finally { writing.current = false; if (alive.current) setBusy(false); }
  }
  async function confirm() {
    if (writing.current || !readable || !selected || state?.outcome !== "candidates") return;
    writing.current = true; setBusy(true); setMessage("");
    try {
      const { data, error, response } = await api.POST("/api/v1/library/files/{file_id}/subtitle-candidates/{candidate_id}/confirm", {
        params: { path: { file_id: fileId, candidate_id: selected } }, body: { expected_mapping_revision: mappingRevision },
      });
      if (!alive.current) return;
      if (!data || error) {
        setReadable(false); setMessage(response.status === 409 ? "文件版本已变化或已有文件受到保护，请重新读取并核对。" : "安装结果尚未确认，请读取结果。不会自动重试。"); return;
      }
      setState(data); setSelected("");
    } catch { if (alive.current) { setReadable(false); setMessage("安装结果尚未确认，请读取结果。不会自动重试。"); } }
    finally { writing.current = false; if (alive.current) setBusy(false); }
  }
  const stale = !!state && state.mapping_revision !== mappingRevision;
  const canQuery = readable && !busy && state?.outcome !== "confirming" && state?.outcome !== "querying";
  return <section className="card subtitle-candidates-panel" aria-labelledby={`subtitle-${fileId}`} aria-busy={busy}>
    <div className="card-header config-heading"><h3 id={`subtitle-${fileId}`}>中文字幕</h3>
      <button type="button" className="btn-secondary" disabled={busy} onClick={() => void load()}>读取结果</button>
    </div>
    <p role="status">{state ? outcomes[state.outcome] : "字幕状态未知"}</p>
    {message ? <p role="status">{message}</p> : null}
    {stale ? <p role="alert">这些候选属于旧文件版本，请重新查询。</p> : null}
    <p className="card-desc">当前提供 Shooter 中文 SRT。条数和时间范围仅描述字幕时间轴，不代表视频时长或翻译质量；汉字仅是语言线索。安装会保留已有字幕。</p>
    <button type="button" className="btn-secondary" disabled={!canQuery} onClick={() => void query()}>{busy ? "处理中…" : state?.outcome === "not_queried" ? "查询字幕候选" : "重新查询字幕候选"}</button>
    {state?.outcome === "candidates" && !stale ? <fieldset disabled={busy || !readable}>
      <legend>选择字幕</legend>
      {state.candidates.map((candidate, index) => <label key={candidate.id} className="subtitle-candidate-option">
        <input type="radio" name={`subtitle-choice-${fileId}`} value={candidate.id} checked={selected === candidate.id} onChange={() => setSelected(candidate.id)} />
        <span>
          <span className="subtitle-candidate-title">Shooter 候选 {index + 1} · 中文线索 · SRT</span>
          <span className="subtitle-candidate-summary">{candidate.summary
            ? <>{candidate.summary.cue_count} 条字幕 · 字幕时间范围 <span className="subtitle-timestamp">{subtitleTimestamp(candidate.summary.start_ms)}</span> – <span className="subtitle-timestamp">{subtitleTimestamp(candidate.summary.end_ms)}</span></>
            : "字幕条数与时间范围不可用"}</span>
        </span>
      </label>)}
      <button type="button" className="btn-primary" disabled={!selected} onClick={() => void confirm()}>确认安装所选字幕</button>
    </fieldset> : null}
  </section>;
}

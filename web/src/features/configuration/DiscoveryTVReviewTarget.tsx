import { useEffect, useId, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { useDiscoveryReviewHandoff } from "./DiscoveryReviewHandoff";
import { currentDiscoveryTVTarget, isReviewTMDBID, isReviewUUID, matchesDiscoveryTV, type DiscoveryReviewSubscription } from "./discoveryReviewHandoff";

export type DiscoveryTVReviewTargetProps = {
  tmdbId: number;
  candidateId: string;
  searchId: string;
  disabled?: boolean;
  onPending?: (pending: boolean) => void;
  onNavigate?: () => void;
};

export function DiscoveryTVReviewTarget(props: DiscoveryTVReviewTargetProps) {
  const { user } = useAuth();
  const signature = `${user?.id}:${user?.role}:${props.tmdbId}:${props.candidateId}:${props.searchId}:${!!props.disabled}`;
  const scope = useRef({ signature });
  if (scope.current.signature !== signature) scope.current = { signature };
  const token = scope.current;
  if (!user || user.role !== "admin") return null;
  return <TVReviewPicker key={signature} {...props} userId={user.id} isCurrent={() => scope.current === token} />;
}

function TVReviewPicker({ tmdbId, candidateId, searchId, disabled, onPending, onNavigate, userId, isCurrent }: DiscoveryTVReviewTargetProps & { userId: string; isCurrent: () => boolean }) {
  const navigate = useNavigate(), handoff = useDiscoveryReviewHandoff(), labelId = useId();
  const [open, setOpen] = useState(false), [busy, setBusy] = useState(false);
  const [rows, setRows] = useState<DiscoveryReviewSubscription[]>([]), [selected, setSelected] = useState("");
  const [message, setMessage] = useState("");
  const owner = useRef<AbortController | null>(null), mounted = useRef(true);
  const opened = useRef(false);
  const callbacks = useRef({ onPending, onNavigate });
  callbacks.current = { onPending, onNavigate };
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false; opened.current = false;
      if (owner.current) { owner.current.abort(); owner.current = null; callbacks.current.onPending?.(false); }
    };
  }, []);
  const valid = !disabled && isReviewUUID(userId) && isReviewTMDBID(tmdbId) && isReviewUUID(candidateId) && isReviewUUID(searchId);
  function close() {
    opened.current = false;
    owner.current?.abort(); owner.current = null;
    setBusy(false); setOpen(false); setRows([]); setSelected(""); setMessage("");
    callbacks.current.onPending?.(false);
  }
  async function run(action: (controller: AbortController, current: () => boolean) => Promise<void>) {
    if (!valid || !isCurrent() || owner.current || !mounted.current || !opened.current) return;
    const controller = new AbortController(); owner.current = controller;
    const current = () => mounted.current && opened.current && isCurrent() && owner.current === controller && !controller.signal.aborted;
    setBusy(true); setMessage(""); callbacks.current.onPending?.(true);
    try { await action(controller, current); }
    catch { if (current()) setMessage("订阅条件读取失败，请重试。未进入审核。"); }
    finally {
      if (owner.current === controller) {
        owner.current = null;
        if (mounted.current && isCurrent()) setBusy(false);
        callbacks.current.onPending?.(false);
      }
    }
  }
  async function load() {
    if (!valid || !isCurrent() || owner.current || !mounted.current) return;
    opened.current = true;
    setOpen(true); setRows([]); setSelected("");
    await run(async (controller, current) => {
      const { data, error } = await api.GET("/api/v1/subscriptions", { signal: controller.signal, cache: "no-store" });
      if (!current()) return;
      if (error || !data || !Array.isArray(data.items)) { setMessage("订阅列表读取失败，请重试。"); return; }
      const matches = data.items.filter(row => matchesDiscoveryTV(row, userId, tmdbId));
      setRows(matches);
      if (matches.length === 1) setSelected(matches[0].id);
      if (!matches.length) setMessage("没有当前账户下与本剧 TMDB 身份一致的订阅。请先订阅本剧，再重新选择资源进入审核。");
    });
  }
  async function proceed() {
    const chosen = rows.find(row => row.id === selected);
    if (!chosen || !open) return;
    await run(async (controller, current) => {
      const options = { params: { path: { id: chosen.id } }, signal: controller.signal, cache: "no-store" as const };
      const [detail, status] = await Promise.all([
        api.GET("/api/v1/subscriptions/{id}", options),
        api.GET("/api/v1/subscriptions/{id}/status", options),
      ]);
      if (!current()) return;
      if (detail.error || status.error || !detail.data || !status.data || !currentDiscoveryTVTarget(chosen, detail.data, status.data, userId, tmdbId)) {
        setSelected(""); setMessage("订阅绑定、状态或版本已变化，无法进入审核。请重新读取订阅列表。"); return;
      }
      // Status holds remain the original panel's responsibility; this is never execution permission.
      if (!handoff.offer({ userId, subscriptionId: chosen.id, candidateId, tmdbId, searchId, createdAt: Date.now() })) {
        setMessage("候选引用已失效，请返回当前搜索重新选择。"); return;
      }
      navigate(`/subscriptions?subscription=${chosen.id}`);
      callbacks.current.onNavigate?.();
    });
  }
  return <div className="config-section">
    {!open ? <button type="button" className="btn-secondary" disabled={!valid} onClick={() => void load()}>前往订阅审核此资源</button> : <>
      <p className="config-description">选择本剧的订阅目标。这里只进入原审核流程，仍需核对作品、覆盖与当前获取条件；刷新页面后需重新选择资源。</p>
      {rows.length > 0 ? <label htmlFor={labelId}>订阅目标根
        <select id={labelId} aria-label="订阅目标根" className="config-input" disabled={busy || !valid} value={selected} onChange={event => setSelected(event.target.value)}>
          <option value="">请选择目标根</option>
          {rows.map(row => <option key={row.id} value={row.id}>{row.root_name || row.root_id} · {row.id}</option>)}
        </select>
      </label> : null}
      {message ? <p role="status" className="config-description">{message}</p> : null}
      <div className="config-actions">
        <button type="button" className="btn-secondary" disabled={busy || !valid} onClick={() => void load()}>重新读取订阅</button>
        <button type="button" className="btn-primary" disabled={busy || !valid || !selected} onClick={() => void proceed()}>{busy ? "正在读取订阅条件…" : "进入原订阅审核"}</button>
        <button type="button" className="btn-secondary" onClick={close}>取消选择</button>
      </div>
    </>}
  </div>;
}

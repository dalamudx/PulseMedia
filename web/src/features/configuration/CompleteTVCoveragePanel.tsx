import { useEffect, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { safeReason, statusReason } from "@/features/configuration/statusReasons";
import { journalKey, readJournal, saveEntry, sendEntry, validCoverage, type Entry, type Journal } from "./completeTVJournal";
import { reviewAlternative, type ReviewAlternative } from "./tvSelectionPreview";

type Preview = paths["/api/v1/subscriptions/{id}/tv-coverage-candidate-reviews/preview"]["post"]["responses"]["200"]["content"]["application/json"];
type Status = paths["/api/v1/subscriptions/{id}/status"]["get"]["responses"]["200"]["content"]["application/json"];
const coverageReason = (value: string): string => {
  const reasons: Record<string,string> = {complete_normal_season_required:"需普通季的完整明确覆盖证据，当前资源不满足",source_or_mapping_changed:"来源或编号映射已变化，请重新读取",invalid_title_identity:"资源标题含不安全或冲突的作品身份",identity_conflict:"资源作品身份与订阅冲突",uncertain_pack_or_special_coverage:"整包、OVA 或特别集标签不能证明完整覆盖，需人工复核",proof_set_changed:"完整季元数据集合已变化",invalid_proof:"季集证据不完整或重复",whole_selected_proof_required:"资源完整覆盖与当前订阅目标不一致，不能扩大范围"};
  return value.startsWith("whole_explicit_coverage_required:") ? "缺少逐集明确的完整覆盖，不能补齐区间或采用部分结果" : safeReason(value, reasons);
};
type Detail = paths["/api/v1/subscriptions/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
type Evidence = paths["/api/v1/subscriptions/{id}/episode-mapping/explain-release"]["post"]["responses"]["200"]["content"]["application/json"];
type Props = { subscriptionId: string; signature: string; candidateId: string; candidateTitle?: string; disabled?: boolean; onReviewed?: (row: ReviewAlternative) => void; onReadStart?: () => void };

export function CompleteTVCoveragePanel(props: Props) {
  const { user } = useAuth();
  if (user?.role !== "admin") return null;
  // Account/object changes remount state synchronously; late requests keep only their captured storage namespace.
  return <CoverageWorkflow key={`${user.id}:${props.subscriptionId}`} {...props} userId={user.id} />;
}
function CoverageWorkflow({ subscriptionId, signature, candidateId: selectedCandidate, candidateTitle, disabled, userId, onReviewed, onReadStart }: Props & { userId: string }) {
  const key = journalKey(userId, subscriptionId);
  const [journal, setJournal] = useState<Journal>({ version: 2, entries: [] });
  const [recoveryCandidate, setRecoveryCandidate] = useState("");
  const [storageReady, setStorageReady] = useState(false);
  const [observedSignature, setObservedSignature] = useState<string | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [status, setStatus] = useState<Status | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [evidence, setEvidence] = useState<Evidence | null>(null);
  const [statusSignature, setStatusSignature] = useState<string | null>(null);
  const [reviewAck, setReviewAck] = useState(false), [acquireAck, setAcquireAck] = useState(false);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const owner = useRef<AbortController | null>(null), alert = useRef<HTMLParagraphElement>(null);
  const mounted = useRef(false), epoch = useRef(0), liveSignature = useRef(signature);
  liveSignature.current = signature;
  const confirmedInMemory = useRef<Entry | null>(null);
  useEffect(() => {
    mounted.current = true;
    const sync = () => {
      try { setJournal(readJournal(localStorage, key)); setStorageReady(true); }
      catch { setStorageReady(false); setError("无法读取完整覆盖恢复记录，暂不能提交。请恢复浏览器存储后重新读取，勿清除未确认命令。"); }
    };
    sync(); window.addEventListener("storage", sync);
    return () => { mounted.current = false; owner.current?.abort(); window.removeEventListener("storage", sync); };
  }, [key]);
  useEffect(() => { epoch.current++; setPreview(null); setStatus(null); setReviewAck(false); setAcquireAck(false); setEvidence(null); setRecoveryCandidate(""); }, [signature, selectedCandidate]);
  useEffect(() => { void refresh(); }, []);
  useEffect(() => { if (error) alert.current?.focus(); }, [error]);
  const pending = journal.entries.find(e => e.phase === "pending");
  const candidateId = recoveryCandidate || selectedCandidate || pending?.candidateId || journal.entries.at(-1)?.candidateId || "";
  const authorized = !!status && statusSignature === signature;
  const review = [...journal.entries].reverse().find(e => e.kind === "review" && e.phase === "confirmed" && e.candidateId === candidateId && e.signature === signature);
  const reviewed = review?.kind === "review" ? review.receipt : undefined;
  const acquired = journal.entries.some(e => e.kind === "acquire" && e.body.review_id === reviewed?.id && e.phase !== "rejected");
  const current = observedSignature === signature && preview?.candidate_id === candidateId && preview.subscription_id === subscriptionId;
  const canReview = current && preview.can_review && preview.execution_allowed === false && validCoverage(preview.rows.map(r => ({ unitId: r.unit_id, unitKey: r.unit_key })));
  const needsReview = !reviewed || !!preview && (preview.input_stamp !== reviewed.input_stamp || preview.material_stamp !== reviewed.material_stamp);
  const canAcquire = authorized && canReview && !!reviewed && preview.input_stamp === reviewed.input_stamp && preview.material_stamp === reviewed.material_stamp && JSON.stringify(preview.rows.map(r => ({ unitId: r.unit_id, unitKey: r.unit_key }))) === JSON.stringify(review?.coverage) && !acquired;
  const locked = !!disabled || busy || !storageReady;
  useEffect(() => {
    if (!busy && authorized && canReview && reviewed && !needsReview && preview &&
        JSON.stringify(preview.rows.map(r => ({ unitId: r.unit_id, unitKey: r.unit_key }))) === JSON.stringify(review?.coverage)) {
      const row = reviewAlternative({ userId, subscriptionId, signature }, "pack", reviewed, preview.rows.map(r => r.unit_key), candidateId === selectedCandidate ? candidateTitle ?? "已审阅完整覆盖资源" : "已保存完整覆盖资源");
      if (row) onReviewed?.(row);
    }
  }, [busy, authorized, canReview, reviewed, needsReview, preview, review, userId, subscriptionId, signature, candidateId, selectedCandidate, candidateTitle, onReviewed]);

  async function run(action: (c: AbortController, generation: number) => Promise<void>) {
    if (owner.current || disabled) return;
    const c = new AbortController(), generation = epoch.current;
    owner.current = c; onReadStart?.(); setBusy(true); setError("");
    try { await action(c, generation); }
    catch {
      if (mounted.current) { setPreview(null); setStatus(null); setReviewAck(false); setAcquireAck(false);
        try { setJournal(readJournal(localStorage, key)); } catch { setStorageReady(false); }
        setError(confirmedInMemory.current ? "命令已确认，恢复记录或状态读取失败。只需重新读取，勿再次提交。" : "请求或恢复存储失败，未沿用旧允许状态。未确认命令只能使用原命令重试；会话失效请登录原账号。"); }
    } finally { if (owner.current === c) owner.current = null; if (mounted.current) setBusy(false); }
  }
  async function readStatus(c: AbortController, generation: number) {
    setStatus(null);
    const [r, d] = await Promise.all([
      api.GET("/api/v1/subscriptions/{id}/status", { signal: c.signal, cache: "no-store", params: { path: { id: subscriptionId } } }),
      api.GET("/api/v1/subscriptions/{id}", { signal: c.signal, cache: "no-store", params: { path: { id: subscriptionId } } }),
    ]);
    if (!mounted.current || c.signal.aborted || generation !== epoch.current) return;
    if (r.error || !r.data || r.data.id !== subscriptionId || d.error || !d.data || d.data.id !== subscriptionId || r.data.intent_version !== d.data.intent_version || r.data.policy_revision !== d.data.quality_policy_revision) throw new Error("status unavailable");
    if (liveSignature.current === signature) { setStatus(r.data); setDetail(d.data); setStatusSignature(signature); }
  }
  async function refresh() {
    await run(async (c, g) => {
      if (!navigator.locks) throw new Error("browser lock unsupported");
      await navigator.locks.request(key, { mode: "exclusive", signal: c.signal }, async () => {
        if (confirmedInMemory.current) saveEntry(localStorage, key, confirmedInMemory.current);
        setJournal(readJournal(localStorage, key)); setStorageReady(true);
      });
      await readStatus(c, g); // Always GET. Never replay a confirmed POST to repair a failed read.
    });
  }
  async function observe() {
    if (!candidateId || pending) return;
    setPreview(null); setReviewAck(false); setAcquireAck(false);
    await run(async (c, g) => {
      const r = await api.POST("/api/v1/subscriptions/{id}/tv-coverage-candidate-reviews/preview", {
        signal: c.signal, cache: "no-store", params: { path: { id: subscriptionId } }, body: { candidate_id: candidateId },
      });
      if (!mounted.current || c.signal.aborted || g !== epoch.current) return;
      if (r.error || !r.data || r.data.subscription_id !== subscriptionId || r.data.candidate_id !== candidateId) throw new Error("preview unavailable");
      if (liveSignature.current !== signature) return;
      setObservedSignature(signature); setPreview(r.data);
      const x = await api.POST("/api/v1/subscriptions/{id}/episode-mapping/explain-release", { signal: c.signal, cache: "no-store", params: { path: { id: subscriptionId } }, body: { candidate_id: candidateId } });
      if (mounted.current && !c.signal.aborted && g === epoch.current && !x.error && x.data?.candidate_id === candidateId && x.data.subscription_id === subscriptionId) setEvidence(x.data);
      await readStatus(c, g);
    });
  }
  async function commit(kind: "review" | "acquire") {
    if (locked || confirmedInMemory.current?.phase === "confirmed" && pending?.key === confirmedInMemory.current.key) return;
    if (!pending && (kind === "review" ? !canReview || !reviewAck : !canAcquire || !acquireAck)) return;
    const entry: Entry = pending ?? (kind === "review" ? {
      kind, materialStamp: preview!.material_stamp, userId, subscriptionId, coverage: preview!.rows.map(r => ({ unitId: r.unit_id, unitKey: r.unit_key })), key: crypto.randomUUID(), signature, candidateId, phase: "pending",
      body: { candidate_id: candidateId, expected_input_stamp: preview!.input_stamp, confirmed: true, reason: "已逐项核对完整普通季覆盖，确认只保存审阅" },
    } : {
      kind, materialStamp: reviewed!.material_stamp, userId, subscriptionId, coverage: review!.coverage, key: crypto.randomUUID(), signature, candidateId, phase: "pending",
      body: { review_id: reviewed!.id, expected_input_stamp: reviewed!.input_stamp, expected_material_stamp: reviewed!.material_stamp, confirmed: true },
    });
    confirmedInMemory.current = null;
    await run(async (c, g) => {
      // Web Locks serialize tabs before re-reading storage and freezing any new command.
      if (!navigator.locks) { setError("此浏览器无法安全锁定恢复记录。请使用支持 Web Locks 的安全连接后重试，尚未提交。"); return; }
      await navigator.locks.request(key, { signal: c.signal }, async () => {
        if (c.signal.aborted || !mounted.current || liveSignature.current !== signature && !pending) return;
        const next = await sendEntry(localStorage, key, entry, async original => {
          if (mounted.current) setJournal(readJournal(localStorage, key));
          const params = { path: { id: subscriptionId }, header: { "Idempotency-Key": original.key } };
          if (original.kind === "review") {
            const r = await api.POST("/api/v1/subscriptions/{id}/tv-coverage-candidate-reviews", { signal: c.signal, params, body: original.body });
            return { status: r.response.status, data: r.data };
          }
          const r = await api.POST("/api/v1/subscriptions/{id}/complete-tv-acquisitions", { signal: c.signal, params, body: original.body });
          return { status: r.response.status, data: r.data };
        }, confirmed => { confirmedInMemory.current = confirmed; });
        if (!mounted.current) return;
        setJournal(next); setPreview(null); setReviewAck(false); setAcquireAck(false);
        if (next.entries.find(e => e.key === entry.key)?.phase === "rejected") {
          setError("权限、来源或版本已变化，本命令未获接受。请重新读取完整覆盖并再次审核，不会覆盖新意图。"); return;
        }
        await readStatus(c, g);
      });
    });
  }
  const records = authorized ? journal.entries.filter(e => e.phase === "confirmed") : [];
  return <section aria-label="完整季覆盖审阅与获取" className="complete-tv-workflow">
    <h4>完整季覆盖审阅与获取</h4>
    <p className="config-description">沿用上方所选站点资源。先逐集审核完整普通季，再单独确认获取；不扩大订阅范围、不解除解析复核或编号映射限制。</p>
    {authorized && journal.entries.some(e => e.kind === "review" && e.phase === "confirmed") && <label>恢复已保存的完整覆盖审阅<select aria-label="恢复已保存的完整覆盖审阅" value={recoveryCandidate} disabled={locked || !!pending} onChange={e => { setRecoveryCandidate(e.target.value); epoch.current++; setPreview(null); setEvidence(null); setReviewAck(false); setAcquireAck(false); }}><option value="">沿用上方资源或最近记录</option>{[...journal.entries].reverse().filter((e,i,all) => e.kind === "review" && e.phase === "confirmed" && all.findIndex(x => x.candidateId === e.candidateId && x.kind === "review" && x.phase === "confirmed") === i).map(e => <option key={e.key} value={e.candidateId}>{e.coverage.map(r => r.unitKey).join("、")}</option>)}</select></label>}
    {error && <p ref={alert} role="alert" tabIndex={-1} className="config-error">{error}</p>}
    {busy && <p role="status">正在核验完整覆盖与持久回执…</p>}
    {pending && <p>原命令全部目标：{pending.coverage.map(r => r.unitKey).join("、")}。恢复使用原范围，不采用上方新选择。</p>}
    {pending && <p role="status">有未确认的{pending.kind === "review" ? "覆盖审阅" : "获取"}命令，已保留原资源、版本和请求标识。切换资源或重新登录后仍只恢复该命令；读取现状不会确认或丢弃它。</p>}
    {!candidateId && !pending && <p>请先在上方检索并选择真实站点资源。</p>}
    <div className="config-actions">
      <button type="button" className="btn-secondary" disabled={locked || !!pending || !candidateId} onClick={() => void observe()}>读取完整季覆盖</button>
      <button type="button" className="btn-secondary" disabled={busy || disabled} onClick={() => void refresh()}>重新读取完整覆盖任务状态</button>
    </div>
    {authorized && detail && <p className="config-description">目标媒体库：{detail.root_path || detail.root_name || "当前绑定媒体库"}。品质策略版本：{detail.quality_policy_revision}。</p>}
    {current && preview && <>
      <p>品质核验：{evidence?.quality_reason === "quality_allowed" ? "当前规则通过" : evidence?.quality_reason === "quality_rejected" ? "当前规则拒绝" : "品质事实未知或尚未核验"}。</p>
      <p role="status">{preview.can_review ? `已明确对应 ${preview.rows.length} 集，请逐项核对。` : `尚不能确认完整覆盖：${coverageReason(preview.hold_reason)}`}</p>
      <div className="mapping-table-scroll" tabIndex={0} aria-label="完整季明确覆盖集合"><table>
        <caption>当前资源明确对应的全部季集（不补齐区间）</caption>
        <thead><tr><th scope="col">标准季集</th><th scope="col">来源编号</th></tr></thead>
        <tbody>{preview.rows.map(r => <tr key={r.unit_id}><td>{r.unit_key}</td><td>{r.source_number}</td></tr>)}</tbody>
      </table></div>
      {needsReview && <label className="config-check"><input type="checkbox" checked={reviewAck} disabled={locked || !!pending || !canReview} onChange={e => setReviewAck(e.target.checked)} />已逐集核对完整普通季，仅保存覆盖审阅，不创建下载任务</label>}
    </>}
    {needsReview && <button type="button" className="btn-primary" disabled={locked || !!pending || !canReview || !reviewAck} onClick={() => void commit("review")}>确认保存完整覆盖审阅</button>}
    {authorized && reviewed && <>
      <p role="status">{acquired ? "此审阅已有获取记录，请查看历史与当前状态。" : "覆盖审阅已保存，尚未授权获取。请重新读取当前覆盖，核验后另行确认。"}</p>
      <p>原审阅全部目标：{review!.coverage.map(r => r.unitKey).join("、")}。</p>
      <label className="config-check"><input type="checkbox" checked={acquireAck} disabled={locked || !!pending || !canAcquire} onChange={e => setAcquireAck(e.target.checked)} />确认获取以上全部目标并创建下载任务；实际发送仍需完整文件覆盖校验，下载完成不代表入库</label>
      <button type="button" className="btn-primary" disabled={locked || !!pending || !canAcquire || !acquireAck} onClick={() => void commit("acquire")}>确认获取完整季覆盖</button>
    </>}
    {pending && <button type="button" className="btn-primary" disabled={locked || confirmedInMemory.current?.key === pending.key} onClick={() => void commit(pending.kind)}>恢复同一{pending.kind === "review" ? "覆盖审阅" : "完整覆盖获取"}命令</button>}
    {records.length > 0 && <details><summary>已确认的覆盖审阅与获取记录（{records.length}）</summary><ul>{records.map(e => <li key={e.key}>{e.coverage.map(r => r.unitKey).join("、")} · {e.kind === "review" ? "覆盖审阅已保存，未授权获取" : e.receipt?.status === "reserved_not_executable" ? "历史回执：仅已占位，未进入可执行队列；不会重新提交升级回执" : "获取已排队，待受控执行器核验并提交下载器"}</li>)}</ul></details>}
    {authorized && status && <div role="status"><p>数据库观测：{new Date(status.observed_at).toLocaleString()}。已满足 {status.items.filter(i => i.is_satisfied).length} / {status.items.filter(i => !i.is_skipped).length} 个当前目标。</p>
      {status.blockers.length > 0 && <ul>{status.blockers.map(r => <li key={r}>{statusReason(r)}</li>)}</ul>}
      <ul>{status.items.map(i => <li key={i.id}>{i.unit_key} · {i.current_fact_stage === "downloaded" ? "下载已完成，尚不代表入库" : i.current_fact_stage === "submitted" ? "已提交下载器，尚不代表入库" : i.status === "satisfied" ? "当前拥有条件满足" : i.status === "in_flight" ? "获取进行中" : i.status === "missing" ? "待补缺" : i.status === "skipped" ? "不在当前目标" : i.status === "unknown" ? "当前状态待确认" : "等待条件"}{i.reservation_protected ? " · 占位受保护" : ""}{i.wait_reason ? ` · ${statusReason(i.wait_reason)}` : ""}</li>)}</ul>
    </div>}
  </section>;
}



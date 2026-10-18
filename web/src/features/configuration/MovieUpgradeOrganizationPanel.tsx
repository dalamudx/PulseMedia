import { useEffect, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import type { Bodies, Journal, Kind } from "./movieUpgradeJournal";

type State = paths["/api/v1/subscriptions/{id}/upgrade-organizations/{operation_id}"]["get"]["responses"]["200"]["content"]["application/json"];
type Review = paths["/api/v1/subscriptions/{id}/upgrade-organization-previews"]["post"]["responses"]["200"]["content"]["application/json"];
type RetirementReview = paths["/api/v1/subscriptions/{id}/upgrade-retirement-previews"]["post"]["responses"]["200"]["content"]["application/json"];
const messages: Record<State["status"], string> = {
  tracking: "正在等待下载完成与文件清单核验。",
  organization_review_required: "下载完成，等待复核整理材料。",
  organization_authorized_not_executable: "整理已授权，下一步冻结硬链接计划。",
  organization_prepared_not_executable: "硬链接计划已冻结，等待确认整理入库。",
  queued: "整理已排队。", running: "正在执行整理。", failed: "整理暂未完成，请检查任务状态。",
  dead: "整理需要人工复核，请检查任务状态。", manual_review: "整理需要人工复核，请检查文件、媒体库和任务状态。",
  linked_pending_ingestion: "文件已链接，正在核验入库。", imported_pending_retirement: "新文件已入库，旧版本等待受控退休。",
  retirement_queued: "旧版本回收已排队。", retirement_running: "正在核验并回收旧版本。",
  retirement_manual_review: "旧版本回收需要人工复核，原保护继续保留。", retired: "旧版本已移入回收区，本次升级完成。",
};
const active = new Set<State["status"]>(["tracking", "queued", "running", "linked_pending_ingestion", "retirement_queued", "retirement_running"]);
type Props = {subscriptionId: string; operationId: string; journal: Journal; disabled: boolean; verified: Set<string>; ack: Kind|null; setAck: (kind: Kind|null)=>void; fresh: <K extends Kind>(kind: K, body: Bodies[K])=>void; revision: string};
export function MovieUpgradeOrganizationPanel({subscriptionId, operationId, journal, disabled, verified, ack, setAck, fresh, revision}: Props) {
  const [state, setState] = useState<State|null>(null), [review, setReview] = useState<Review|null>(null);
  const [retirementReview, setRetirementReview] = useState<RetirementReview|null>(null);
  const [loading, setLoading] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState(""), [refresh, setRefresh] = useState(0);
  const request = useRef<AbortController|null>(null), alert = useRef<HTMLParagraphElement>(null);
  const currentState = useRef<State|null>(null), currentReview = useRef<Review|null>(null);
  const currentRetirement = useRef<RetirementReview|null>(null);
  const authorization = journal.organizationAuthorization, preparation = journal.organizationPreparation;
  const changeKey = [authorization, preparation, journal.organizationJob, journal.organizationRetirement].map(e=>`${e?.key??""}:${e?.phase??""}`).join("/");
  useEffect(()=>{
    const c = new AbortController(); request.current?.abort(); request.current = c;
    let timer: ReturnType<typeof setTimeout>|undefined;
    function discardReview() {currentReview.current = null; setReview(null); currentRetirement.current = null; setRetirementReview(null); setAck(null);}
    async function read() {
      if (c.signal.aborted) return;
      setLoading(true);
      try {
        const r = await api.GET("/api/v1/subscriptions/{id}/upgrade-organizations/{operation_id}", {signal: c.signal, cache: "no-store", params: {path: {id: subscriptionId, operation_id: operationId}}});
        if (c.signal.aborted) return;
        if (r.error || !r.data || r.data.operation_id !== operationId || r.data.execution_allowed !== false || !Object.hasOwn(messages, r.data.status)) throw Error("state");
        const previous = currentState.current;
        if (previous && (previous.status !== r.data.status || previous.authorization_id !== r.data.authorization_id || previous.preparation_id !== r.data.preparation_id)) discardReview();
        if (r.data.status === "organization_review_required" && currentReview.current) {
          const material = await api.POST("/api/v1/subscriptions/{id}/upgrade-organization-previews", {signal: c.signal, cache: "no-store", params: {path: {id: subscriptionId}}, body: {operation_id: operationId}});
          if (c.signal.aborted) return;
          if (material.error || !material.data || material.data.input_stamp !== currentReview.current?.input_stamp || material.data.operation_id !== operationId || material.data.subscription_id !== subscriptionId || material.data.execution_allowed !== false) {
            discardReview(); setNotice("材料状态已变化，请重新读取整理复核。");
          }
        }
        if (r.data.status === "imported_pending_retirement" && currentRetirement.current) {
          const material = await api.POST("/api/v1/subscriptions/{id}/upgrade-retirement-previews", {signal: c.signal, cache: "no-store", params: {path: {id: subscriptionId}}, body: {operation_id: operationId}});
          if (c.signal.aborted) return;
          if (material.error || !material.data || material.data.operation_id !== operationId || material.data.input_stamp !== currentRetirement.current?.input_stamp || material.data.execution_allowed !== false) {
            discardReview(); setNotice("文件或保护状态已变化，请重新读取旧版回收材料。");
          }
        }
        currentState.current = r.data; setState(r.data); setError("");
        if (active.has(r.data.status)) timer = setTimeout(()=>void read(), 5000);
      } catch {if (!c.signal.aborted) {currentState.current = null; setState(null); discardReview(); setError("整理状态读取失败，请刷新后重试。");}}
      finally {if (!c.signal.aborted) setLoading(false);}
    }
    void read();
    return ()=>{c.abort(); request.current?.abort(); clearTimeout(timer); request.current = null;};
  }, [subscriptionId, operationId, revision, changeKey, refresh, setAck]);
  useEffect(()=>{if (error) alert.current?.focus();}, [error]);
  async function readReview() {
    if (disabled || loading) return;
    const c = new AbortController(); request.current?.abort(); request.current = c;
    currentReview.current = null; setLoading(true); setReview(null); setAck(null); setError(""); setNotice("");
    try {
      const r = await api.POST("/api/v1/subscriptions/{id}/upgrade-organization-previews", {signal: c.signal, cache: "no-store", params: {path: {id: subscriptionId}}, body: {operation_id: operationId}});
      if (c.signal.aborted) return;
      if (r.error || !r.data || r.data.operation_id !== operationId || r.data.subscription_id !== subscriptionId || r.data.execution_allowed !== false) throw Error("review");
      currentReview.current = r.data; setReview(r.data);
    } catch {if (!c.signal.aborted) setError("整理材料已变化或暂不可用，请刷新状态后重新复核。");}
    finally {if (!c.signal.aborted) setLoading(false);}
  }
  async function readRetirementReview() {
    if (disabled || loading) return;
    const c = new AbortController(); request.current?.abort(); request.current = c;
    currentRetirement.current = null; setRetirementReview(null); setAck(null); setLoading(true); setError(""); setNotice("");
    try {
      const r = await api.POST("/api/v1/subscriptions/{id}/upgrade-retirement-previews", {signal:c.signal,cache:"no-store",params:{path:{id:subscriptionId}},body:{operation_id:operationId}});
      if(c.signal.aborted)return;
      if(r.error || !r.data || r.data.operation_id !== operationId || r.data.execution_allowed !== false || r.data.old_file_id === r.data.new_file_id)throw Error("retirement");
      currentRetirement.current = r.data; setRetirementReview(r.data);
    } catch {if(!c.signal.aborted)setError("旧版回收材料已变化或暂不可用，请刷新状态后重新复核。");}
    finally {if(!c.signal.aborted)setLoading(false);}
  }
  const locked = disabled || loading;
  const canPrepare = state?.status === "organization_authorized_not_executable" && authorization?.phase === "closed" && verified.has(authorization.key) && !preparation;
  const canQueue = state?.status === "organization_prepared_not_executable" && preparation?.phase === "closed" && verified.has(preparation.key) && !journal.organizationJob;
  return <section aria-label="升级整理入库">
    <h4>整理入库</h4>
    <p role="status">{state ? messages[state.status] : loading ? "正在读取整理状态…" : "整理状态尚未读取。"}</p>
    {error && <p className="config-error" role="alert" tabIndex={-1} ref={alert}>{error}</p>}
    {notice && <p role="status">{notice}</p>}
    <button type="button" className="btn-secondary" disabled={locked} onClick={()=>{currentReview.current = null; setReview(null); setAck(null); setRefresh(v=>v+1);}}>刷新整理状态</button>
    {state?.status === "organization_review_required" && !authorization && <>
      <button type="button" className="btn-secondary" disabled={locked} onClick={()=>void readReview()}>读取整理复核</button>
      {review && <div aria-label="整理复核材料">
        <p style={{overflowWrap:"anywhere"}}>{review.source_relative_path} · {review.source_size.toLocaleString()} 字节</p>
        <p>当前品质分数 {review.current_score} → 新文件 {review.new_score}</p>
        <label><input type="checkbox" checked={ack === "organizationAuthorization"} disabled={locked} onChange={e=>setAck(e.target.checked ? "organizationAuthorization" : null)}/>我确认使用此完成材料授权整理</label>
        <button type="button" className="btn-secondary" disabled={locked || ack !== "organizationAuthorization"} onClick={()=>fresh("organizationAuthorization", {operation_id: operationId, observation_id: review.observation_id, input_stamp: review.input_stamp, confirmed: true})}>确认整理授权</button>
      </div>}
    </>}
    {canPrepare && <div>
      <label><input type="checkbox" checked={ack === "organizationPreparation"} disabled={locked} onChange={e=>setAck(e.target.checked ? "organizationPreparation" : null)}/>我确认冻结此次硬链接整理计划</label>
      <button type="button" className="btn-secondary" disabled={locked || ack !== "organizationPreparation"} onClick={()=>fresh("organizationPreparation", {operation_id: operationId, authorization_id: authorization.receipt!.authorization_id, confirmed: true})}>冻结硬链接计划</button>
    </div>}
    {canQueue && <div>
      <p>确认后将创建硬链接并核验入库；旧文件继续保留。</p>
      <label><input type="checkbox" checked={ack === "organizationJob"} disabled={locked} onChange={e=>setAck(e.target.checked ? "organizationJob" : null)}/>我确认执行此次整理并核验入库</label>
      <button type="button" className="btn-primary" disabled={locked || ack !== "organizationJob"} onClick={()=>fresh("organizationJob", {operation_id: operationId, preparation_id: preparation.receipt!.preparation_id, confirmed: true})}>确认整理入库</button>
    </div>}
    {state?.status === "imported_pending_retirement" && !journal.organizationRetirement && <div>
      <button type="button" className="btn-secondary" disabled={locked} onClick={()=>void readRetirementReview()}>读取旧版回收材料</button>
      {retirementReview && <div aria-label="旧版回收材料">
        <p style={{overflowWrap:"anywhere"}}>旧版本：{retirementReview.old_relative_path}</p>
        <p style={{overflowWrap:"anywhere"}}>已入库：{retirementReview.new_relative_path}</p>
        <p>品质分数 {retirementReview.current_score} → {retirementReview.new_score}。旧文件将在同一媒体库的回收区至少保留 {retirementReview.retention_days} 天，保种源文件继续保留。</p>
        <label><input type="checkbox" checked={ack === "organizationRetirement"} disabled={locked} onChange={e=>setAck(e.target.checked ? "organizationRetirement" : null)}/>我确认将旧版本移入回收区</label>
        <button type="button" className="btn-primary" disabled={locked || ack !== "organizationRetirement"} onClick={()=>fresh("organizationRetirement", {operation_id:operationId,input_stamp:retirementReview.input_stamp,confirmed:true})}>回收旧版本</button>
      </div>}
    </div>}
    {state?.status === "retired" && state.retained_until && <p>旧文件至少保留至 {new Date(state.retained_until).toLocaleString()}，到期后可人工清理。</p>}
    {((state?.status === "organization_authorized_not_executable" && !authorization) || (state?.status === "organization_prepared_not_executable" && !preparation)) && <p>此升级已有整理记录，请在原确认浏览器恢复记录。</p>}
  </section>;
}

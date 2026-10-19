import {useCallback,useEffect,useRef,useState} from "react";
import {useInteractiveReleaseSearch} from "./useInteractiveReleaseSearch";
import {useAuth} from "@/features/auth/AuthContext";
import {TVSelectionPreviewPanel} from "./TVSelectionPreviewPanel";
import {collectReview,reviewAlternative,type ReviewAlternative} from "./tvSelectionPreview";
import {api,type paths} from "@/api/client";
import {safeReason} from "./statusReasons";
import {ReviewedEpisodeAcquisitionPanel,type EpisodeReviewReceipt,restoreEpisodeReviewReceipt} from "./ReviewedEpisodeAcquisitionPanel";
import {confirmedEpisodeReview,repairEpisodeReview,sendEpisodeReview,type PendingReview} from "./singleEpisodeReviewRecovery";
import {CompleteTVCoveragePanel} from "./CompleteTVCoveragePanel";
import {useDiscoveryReviewHandoff, type DiscoveryReviewCandidateRef} from "./DiscoveryReviewHandoff";
import {currentDiscoveryReview} from "./discoveryReviewHandoff";
import {journalKey,readJournal} from "./completeTVJournal";

type Evidence=paths["/api/v1/subscriptions/{id}/episode-mapping/explain-release"]["post"]["responses"]["200"]["content"]["application/json"];
type Pending=PendingReview;
const storage=(id:string)=>`pulse:episode-review:${id}`;
const reason=(v:string)=>safeReason(v,{source_not_bound:"资源来源未绑定本订阅",source_changed:"来源配置或健康状态已变化",source_observation_expired:"来源观测已过期",standard_coverage_only:"标准单集对应已确认，仍不是执行许可",standard_multi_coverage_only:"明确多集编号完整对应，只读解释；多单元获取未开放",multi_unit_review_required:"涉及多个单元，不能保存单集审核或创建单集任务",standard_coverage_unresolved:"标准覆盖不完整或不明确，未返回部分单元",duplicate_episode_coverage:"编号集合重复，完整覆盖未确认",mapped_coverage_only:"编号对应已确认，仍不是执行许可",exact_title_year_review:"标题与作品年份一致，需人工审核",media_identity_unproven:"作品身份证据不足，请核对资源",media_title_unproven:"标题不能可靠对应作品",media_year_conflict:"资源年份与作品矛盾，不能审核通过",media_identity_conflict:"资源外部身份与作品矛盾",external_identity_unverified:"资源外部身份尚未核实",quality_unknown:"品质事实未知",quality_allowed:"当前品质规则通过",quality_rejected:"当前品质规则拒绝",policy_unavailable:"品质策略不可读取",target_missing:"当前目标未满足",coverage_unresolved:"覆盖尚未确定",target_not_in_scope:"单集不在当前目标范围"});
function restore(id:string):Pending|null{try{const raw=sessionStorage.getItem(storage(id));if(!raw)return null;const value=JSON.parse(raw) as Pending;if(typeof value.key!=="string"||!value.key||!value.body||typeof value.body.candidate_id!=="string"||!/^[-0-9a-f]{36}$/i.test(value.body.candidate_id)||!/^[0-9a-f]{64}$/.test(value.body.expected_input_stamp)||value.body.confirmed!==true||value.body.reason!=="已人工核对当前资源的作品与单集身份")return null;return confirmedEpisodeReview(id,value)?null:value}catch{return null}}
type Props={subscriptionId:string;signature:string;disabled?:boolean;discoveryCandidate?:DiscoveryReviewCandidateRef};
export function EpisodeCandidateReviewPanel(props:Props){
 const {user}=useAuth();
 if(!user)return null;
 return <EpisodeReviewWorkflow key={`${user.id}:${props.subscriptionId}`} {...props} userId={user.id}/>;
}
function EpisodeReviewWorkflow({subscriptionId,signature,disabled,userId,discoveryCandidate}:Props&{userId:string}){
 const [alternatives,setAlternatives]=useState<ReviewAlternative[]>([]),[comparisonEpoch,setComparisonEpoch]=useState(0);
 const remember=useCallback((row:ReviewAlternative)=>setAlternatives(current=>collectReview(current,row)),[]);
 const invalidateComparison=useCallback(()=>setComparisonEpoch(current=>current+1),[]);
 useEffect(()=>{setAlternatives([])},[signature]);
 const searcher=useInteractiveReleaseSearch({scope:`episode:${subscriptionId}`,subscriptionId});
 const items=searcher.results,searchUnsafe=!!searcher.recovery&&!searcher.canAcquire;
 useEffect(()=>{if(searcher.phase==="stale"||searcher.phase==="auth"){setEvidence(null);setAck(false)}},[searcher.phase]);
 const [query,setQuery]=useState(""),[selected,setSelected]=useState(()=>(confirmedEpisodeReview(subscriptionId)?.receipt??restoreEpisodeReviewReceipt(subscriptionId))?.candidate_id??""),[evidence,setEvidence]=useState<Evidence|null>(null),[ack,setAck]=useState(false),[pending,setPending]=useState<Pending|null>(()=>restore(subscriptionId)),[notice,setNotice]=useState(""),[error,setError]=useState(""),[busy,setBusy]=useState(false);
 const [reviewed,setReviewed]=useState<EpisodeReviewReceipt|null>(()=>confirmedEpisodeReview(subscriptionId)?.receipt??restoreEpisodeReviewReceipt(subscriptionId)),[acquirePending,setAcquirePending]=useState(false);
 const [repairPending,setRepairPending]=useState(()=>confirmedEpisodeReview(subscriptionId)?.persisted===false);
 const blocked=pending!==null||repairPending;
 const owner=useRef<AbortController|null>(null),epoch=useRef(0),alert=useRef<HTMLParagraphElement>(null),previous=useRef(signature);
 useEffect(()=>()=>owner.current?.abort(),[]);useEffect(()=>{if(error)alert.current?.focus()},[error]);useEffect(()=>{if(previous.current!==signature){previous.current=signature;epoch.current++;setEvidence(null);setAck(false);setNotice("订阅或元数据已变化，旧解释已清除；未确认审核保留原命令。")}},[signature]);
 const locked=busy||disabled||acquirePending;const chosen=items?.find(i=>i.id===selected);const canReview=!searchUnsafe&&!!evidence?.source_current&&evidence.coverage_known&&evidence.rows.length===1&&evidence.identity_decision!=="reject"&&!!evidence.candidate_input_stamp&&evidence.candidate_id===selected;
 const handoff=useDiscoveryReviewHandoff(), {user}=useAuth();
 const appliedDiscovery=useRef<string|null>(null);
 useEffect(()=>{
  const ref=currentDiscoveryReview(discoveryCandidate??null,userId,user?.role);
  if(!ref||ref.subscriptionId!==subscriptionId||disabled||busy)return;
  const token=`${ref.searchId}:${ref.candidateId}`;
  if(appliedDiscovery.current===token)return;
  appliedDiscovery.current=token;
  let savedPending=false;
  try{savedPending=!!sessionStorage.getItem(storage(subscriptionId))||!!sessionStorage.getItem(`pulse:episode-acquisition:${subscriptionId}`)||readJournal(localStorage,journalKey(userId,subscriptionId)).entries.some(entry=>entry.phase==="pending")}catch{savedPending=true}
  if(pending||repairPending||acquirePending||savedPending||["creating","recovering","unknown","missing","blocked"].includes(searcher.phase)){
   setNotice("已有未确认搜索、审核或获取记录，已保留原命令。请先恢复原结果，再重新选择发现页候选。");
  }else{
   setSelected(ref.candidateId);setEvidence(null);setReviewed(null);setAck(false);invalidateComparison();
   setNotice(`已选择发现页候选 ${ref.candidateId}。请先读取当前证据，再分别确认审核与获取。`);
  }
  handoff.clear();
 },[discoveryCandidate,disabled,busy,userId,user?.role,subscriptionId,pending,repairPending,acquirePending,searcher.phase,invalidateComparison]);
 async function run(action:(c:AbortController,g:number)=>Promise<void>){if(owner.current||disabled||acquirePending)return;const c=new AbortController(),g=epoch.current;owner.current=c;invalidateComparison();setBusy(true);setError("");try{await action(c,g)}catch{if(!c.signal.aborted){setEvidence(null);setAck(false);setError("请求连接失败，旧解释已清除；未确认审核仍保留原命令。")}}finally{if(owner.current===c)owner.current=null;if(!c.signal.aborted)setBusy(false)}}
 async function search(){if(blocked||!query.trim()||searcher.busy)return;setEvidence(null);setSelected("");setAck(false);invalidateComparison();await searcher.start(query);}
 async function explain(){const id=pending?.body.candidate_id??selected;if(!id)return;setEvidence(null);setAck(false);await run(async(c,g)=>{const r=await api.POST("/api/v1/subscriptions/{id}/episode-mapping/explain-release",{signal:c.signal,cache:"no-store",params:{path:{id:subscriptionId}},body:{candidate_id:id}});if(c.signal.aborted||g!==epoch.current)return;if(r.error||!r.data){setError("候选证据读取失败，未沿用旧身份或编号结果。");return}setEvidence(r.data);if(pending)setNotice("读取观测不能确认审核命令；重试使用原命令 ID。")})}
 async function review(){
  if(repairPending){
   try{const known=repairEpisodeReview(subscriptionId,sessionStorage);if(known?.persisted){setRepairPending(false);setError("");setNotice("已修复审核回执存储；未重新提交审核。请读取当前候选或获取条件。")}else setError("审核已确认，回执存储仍不可用；恢复只修复存储，不会再次提交。")}catch{setError("审核已确认，回执存储仍不可用；恢复只修复存储，不会再次提交。")}
   return;
  }
  if(!pending&&(!canReview||!ack))return;
  await run(async(c,g)=>{
   const wasPending=pending!==null;
   const command:Pending=pending??{key:crypto.randomUUID(),body:{candidate_id:selected,expected_input_stamp:evidence!.candidate_input_stamp!,confirmed:true,reason:"已人工核对当前资源的作品与单集身份"}};
   setPending(command);
   const result=await sendEpisodeReview(subscriptionId,command,wasPending,sessionStorage,async()=>{
    const r=await api.POST("/api/v1/subscriptions/{id}/episode-candidate-reviews",{signal:c.signal,cache:"no-store",params:{path:{id:subscriptionId},header:{"Idempotency-Key":command.key}},body:command.body});
    return {status:r.response.status,data:r.error?undefined:r.data};
   });
   // sendEpisodeReview captures valid success before observing cancellation.
   if(c.signal.aborted)return;
   setEvidence(null);setAck(false);
   if(result.kind==="confirmed"){
    const {receipt,persisted}=result.confirmation;
    // Only a fresh successful command in this account mount may feed comparison.
    // Legacy cached/unknown receipts lack actor binding and remain recovery-only.
    if(!wasPending&&g===epoch.current&&evidence?.subscription_id===subscriptionId&&evidence.candidate_id===receipt.candidate_id&&evidence.candidate_input_stamp===receipt.input_stamp&&evidence.rows.length===1&&evidence.rows[0].unit_id===receipt.unit_id){
     const row=reviewAlternative({userId,subscriptionId,signature},"episode",receipt,evidence.rows.map(r=>r.unit_key),chosen?.title??"已审核单集资源");
     if(row)remember(row);
    }
    setPending(null);setSelected(command.body.candidate_id);setReviewed(receipt);setRepairPending(!persisted);
    setNotice(`审核记录 ${receipt.id} 已保存，尚未创建下载或整理任务。标准单集获取需另行确认。`);
    if(!persisted)setError("审核已确认，浏览器回执存储失败；请恢复回执存储，不会再次提交审核。保持当前页面，修复存储后再刷新。");
   }else if(result.kind==="not_sent"){
    setPending(null);setError("无法保存审核恢复命令，未提交。请检查浏览器存储权限。");
   }else if(result.kind==="rejected"){
    setPending(null);setError("本次新审核明确未获接受或版本已冲突，请重新读取当前证据后确认。");
   }else setError("原审核结果仍未确认，保留原候选、命令和 key；权限恢复后重试同一命令，不要重新审核或换资源。");
  });
 }
 if(searcher.phase==="auth")return <section className="config-editor"><p role="alert">{searcher.message}</p></section>;
 return <section className="config-editor" aria-label="真实资源单集审核"><h4>真实资源单集审核</h4><p className="config-description">检索会访问站点；读取候选证据不会再次检索。来源、作品身份、编号和品质分别校验；保存审核不创建任务。</p>{error&&<p ref={alert} tabIndex={-1} role="alert" className="config-error">{error}</p>}{notice&&<p role="status" className="config-description">{notice}</p>}{pending&&<p role="status">有未确认审核，保留候选 {pending.body.candidate_id} 和原命令 ID。关闭后仍可恢复。</p>}<label>资源检索关键词<input aria-label="单集资源关键词" value={query} maxLength={160} disabled={locked||blocked} onChange={e=>{setQuery(e.target.value);setSelected("");setEvidence(null);setAck(false)}}/></label><button type="button" className="btn-secondary" disabled={locked||blocked||searcher.busy||!query.trim()} onClick={()=>void search()}>检索真实站点资源</button><p role="status">{searcher.message}</p><p className="config-description">{searcher.sourceSummary}</p><p className="config-description">{searcher.policySummary}</p>{searcher.recovery&&<div className="config-actions"><button type="button" className="btn-secondary" disabled={locked||blocked||searcher.busy} onClick={searcher.refresh}>刷新搜索观察</button><button type="button" className="btn-secondary" disabled={locked||blocked||searcher.busy} onClick={()=>void searcher.cancel()}>取消原搜索需求</button>{searcher.canRetryOriginal&&<button type="button" disabled={locked||blocked||searcher.busy} onClick={()=>void searcher.retryOriginal()}>明确重试原键命令</button>}</div>}{items.length>0&&<label>站点资源<select aria-label="待审核站点资源" value={selected} disabled={locked||blocked} onChange={e=>{setSelected(e.target.value);setEvidence(null);setAck(false);setNotice("")}}><option value="">请选择资源</option>{items.map(i=><option key={i.id} value={i.id}>{i.title} · {i.indexer_name}</option>)}</select></label>}{chosen&&<p className="config-description">当前资源：{chosen.title} · {chosen.indexer_name} · {chosen.size_bytes} 字节</p>}<div className="config-actions"><button type="button" className="btn-secondary" disabled={locked||searchUnsafe||(!selected&&!pending)} onClick={()=>void explain()}>读取当前候选证据</button></div>{evidence&&<div className="mapping-preview" role="status"><p>来源与覆盖：{reason(evidence.reason)}</p><p>作品身份：{reason(evidence.identity_reason??"media_identity_unproven")}</p><p>品质：{reason(evidence.quality_reason??"quality_unknown")}</p><p>当前目标：{reason(evidence.target_reason??"coverage_unresolved")}</p>{evidence.rows.map(r=><p key={r.unit_id}>{r.unit_key} · 单元版本 {r.unit_version}</p>)}<p className="config-description">映射 {evidence.mapping_revision} / 意图 {evidence.intent_version} · {new Date(evidence.observed_at).toLocaleString()}。这些结果不是执行许可。</p><label className="config-check"><input type="checkbox" checked={ack} disabled={locked||blocked||!canReview} onChange={e=>setAck(e.target.checked)}/>已核对当前资源确属此作品及单集，确认只保存审核，不创建任务</label></div>}<div className="config-actions"><button type="button" className="btn-primary" disabled={locked||(!repairPending&&!pending&&(!canReview||!ack))} onClick={()=>void review()}>{repairPending?"恢复审核回执存储":pending?"重试同一审核命令":"保存当前单集审核"}</button></div><fieldset className="config-fieldset" disabled={searchUnsafe}><CompleteTVCoveragePanel subscriptionId={subscriptionId} signature={signature} candidateId={selected} candidateTitle={chosen?.title} onReviewed={remember} onReadStart={invalidateComparison} disabled={disabled||busy}/><TVSelectionPreviewPanel key={JSON.stringify([userId,subscriptionId,signature,selected,comparisonEpoch])} userId={userId} subscriptionId={subscriptionId} signature={signature} alternatives={alternatives} disabled={disabled||busy||acquirePending||blocked}/><ReviewedEpisodeAcquisitionPanel subscriptionId={subscriptionId} signature={signature} review={reviewed} disabled={disabled||busy||blocked} onPending={setAcquirePending}/></fieldset></section>
}



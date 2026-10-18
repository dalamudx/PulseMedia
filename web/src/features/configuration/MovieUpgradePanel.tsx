import { useEffect, useId, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { useActivityInvalidation } from "./useActivityInvalidation";
import { journalKey, readJournal, persistJournal, runEntry, kinds as stages, type Bodies, type Entry, type Journal, type Kind, type Mode } from "./movieUpgradeJournal";
import { MovieUpgradeOrganizationPanel } from "./MovieUpgradeOrganizationPanel";

type Subscription = paths["/api/v1/subscriptions/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
type Files = paths["/api/v1/library/mapping-options"]["get"]["responses"]["200"]["content"]["application/json"];
type Preview = paths["/api/v1/subscriptions/{id}/upgrade-previews"]["post"]["responses"]["200"]["content"]["application/json"];
type Release = paths["/api/v1/releases/search"]["get"]["responses"]["200"]["content"]["application/json"]["results"][number];
type Candidate = Pick<Release, "id" | "title"> & { indexer_name?: string };
const stageLabels: Record<Kind, string> = {mode:"模式设置", reservation:"冻结预留", preparation:"取源准备", send:"发送确认", organizationAuthorization:"整理授权", organizationPreparation:"整理计划", organizationJob:"整理确认", organizationRetirement:"旧版本回收"};
const stageOutcomes: Record<Exclude<Kind,"mode">, string> = {reservation:"完成，尚不可发送", preparation:"完成，尚未发送", send:"已排队，尚不能确认下载结果", organizationAuthorization:"完成，尚未执行整理", organizationPreparation:"已冻结，尚未执行整理", organizationJob:"已排队，等待核验入库", organizationRetirement:"已排队，等待验证旧版回收"};
const modeLabels: Record<Mode,string> = {disabled:"关闭",stop_at_cutoff:"达到阈值后停止",replace_if_better:"持续替换为更优版本"};
const reasons: Record<string, string> = {
  current_meets_cutoff:"现有文件已达到当前策略的停止升级阈值。", not_better:"候选没有严格优于现有文件。",
  quality_unknown:"文件品质证据不足，需人工复核。", projection_input_changed:"当前事实已改变，请刷新状态。",
  root_unavailable:"媒体库当前不可用。", candidate_not_allowed:"候选不符合当前允许品质规则。",
};
const knownReason = (reason:string) => {
 if(/^current_score_\d+_already_reached_cutoff_\d+$/.test(reason))return reasons.current_meets_cutoff;
 if(/^new_score_\d+_not_better_than_current_\d+$/.test(reason))return reasons.not_better;
 return Object.hasOwn(reasons,reason)?reasons[reason]:"当前证据不能支持升级，请检查订阅、源和文件状态。";
};

export function MovieUpgradePanel(props:{subscription:Subscription; wantedId:string; onUpdated:()=>void}) {
  const {user}=useAuth();
  if (!user || user.role!=="admin" || props.subscription.media_type!=="movie") return null;
  return <UpgradePanel key={`${user.id}:${props.subscription.id}`} {...props} userId={user.id}/>;
}
function UpgradePanel({subscription,wantedId,onUpdated,userId}:{subscription:Subscription;wantedId:string;onUpdated:()=>void;userId:string}) {
  const uid=useId(), alert=useRef<HTMLParagraphElement>(null), memory=useRef<Journal>({version:1,userId,subscriptionId:subscription.id});
  const write=useRef<AbortController|null>(null), reader=useRef<AbortController|null>(null), repairRef=useRef(false);
  const syncPending=useRef(false);
  const [open,setOpen]=useState(false),[journal,setJournal]=useState<Journal|null>(null),[storageBlocked,setStorageBlocked]=useState(false),[repair,setRepair]=useState(false);
  const [busy,setBusy]=useState(false),[loading,setLoading]=useState(false),[error,setError]=useState(""),[notice,setNotice]=useState("");
  const [current,setCurrent]=useState<Subscription|null>(null),[files,setFiles]=useState<Files["files"]>([]),[candidates,setCandidates]=useState<Candidate[]>([]);
  const [fileId,setFileId]=useState(""),[candidateId,setCandidateId]=useState(""),[query,setQuery]=useState(subscription.title??""),[preview,setPreview]=useState<Preview|null>(null);
  const [mode,setMode]=useState<Mode|"">(""),[ack,setAck]=useState<Kind|null>(null),[verified,setVerified]=useState<Set<string>>(()=>new Set());
  const activity=useActivityInvalidation();
  function adopt(value:Journal){memory.current=value;setJournal({...value});}
  function loadSaved(){
    if(repairRef.current)return;
    try {adopt(readJournal(localStorage,userId,subscription.id));setStorageBlocked(false);}
    catch {setStorageBlocked(true);setError("本地确认记录无法读取。为保留可能已提交的原命令，已暂停新确认；请先恢复浏览器存储。");}
  }
  useEffect(()=>{loadSaved();const changed=(event:StorageEvent)=>{if(event.key===null||event.key===journalKey(userId,subscription.id)){if(repairRef.current)return;setVerified(new Set());setPreview(null);setAck(null);if(write.current)syncPending.current=true;else loadSaved();}};
    window.addEventListener("storage",changed);return()=>{window.removeEventListener("storage",changed);reader.current?.abort();write.current?.abort();};
  },[userId,subscription.id]);
  useEffect(()=>{reader.current?.abort();reader.current=null;setLoading(false);setPreview(null);setAck(value=>value?.startsWith("organization")?value:null);setCurrent(null);},[activity.version]);
  useEffect(()=>{reader.current?.abort();reader.current=null;setLoading(false);setPreview(null);setAck(null);setCurrent(null);},[subscription.intent_version,subscription.quality_policy_revision]);
  useEffect(()=>{if(error||storageBlocked)alert.current?.focus();},[error,storageBlocked]);
  const pending=stages.map(k=>journal?.[k]).find(e=>e?.phase==="pending");
  const reservation=journal?.reservation, preparation=journal?.preparation, send=journal?.send;
  const locked=busy||repair||storageBlocked||!journal;
  const enabled=journal?.mode?.phase==="closed" && (journal.mode.receipt?.mode==="stop_at_cutoff"||journal.mode.receipt?.mode==="replace_if_better") && verified.has(journal.mode.key) && current?.intent_version===journal.mode.receipt.intent_version;
  async function readResources(){
    if(reader.current||busy)return;const c=new AbortController();reader.current=c;setLoading(true);setError("");setPreview(null);setAck(null);setCurrent(null);
    try{
      const [s,f,r]=await Promise.all([
        api.GET("/api/v1/subscriptions/{id}",{signal:c.signal,cache:"no-store",params:{path:{id:subscription.id}}}),
        api.GET("/api/v1/library/mapping-options",{signal:c.signal,cache:"no-store"}),
        wantedId ? api.GET("/api/v1/subscriptions/{id}/wanted/{wanted_id}/candidates",{signal:c.signal,cache:"no-store",params:{path:{id:subscription.id,wanted_id:wantedId}}}).then(r=>({items:r.data?.items??[],available:!r.error||r.response.status===404})) : Promise.resolve({items:[],available:true}),
      ]);
      if(c.signal.aborted)return;
      if(!s.data||s.error||!f.data||f.error||!r.available)throw Error("read");
      setCurrent(s.data);setFiles(f.data.files.filter(file=>file.root_id===s.data.root_id));setCandidates(old=>[...new Map([...old,...r.items].map(candidate=>[candidate.id,candidate])).values()]);
      if(f.data.files.length>=f.data.limit)setNotice("文件选项已达本页上限；未列出的文件不代表不存在。");
    }catch{if(!c.signal.aborted)setError("升级资源读取失败，请重新读取。未沿用旧的比较结论。");}
    finally{if(reader.current===c)reader.current=null;if(!c.signal.aborted)setLoading(false);}
  }
  async function search(){
    if(reader.current||locked||reservation||!query.trim())return;const c=new AbortController();reader.current=c;setLoading(true);setCandidates([]);setCandidateId("");setPreview(null);setAck(null);setError("");
    try{const r=await api.GET("/api/v1/releases/search",{signal:c.signal,cache:"no-store",params:{query:{query:query.trim(),media_type:"movie"}}});if(c.signal.aborted)return;
      if(r.error||!r.data)throw Error("search");setCandidates(r.data.results);setNotice(r.data.partial?"部分站点检索未完成，仅展示已返回的资源。":"站点检索已返回。是否允许升级仍需读取比较。");
    }catch{if(!c.signal.aborted)setError("站点检索失败，不能当作没有资源。请重试。");}finally{if(reader.current===c)reader.current=null;if(!c.signal.aborted)setLoading(false);}
  }
  async function compare(){
    if(reader.current||locked||reservation||!fileId||!candidateId)return;const c=new AbortController();reader.current=c;setLoading(true);setPreview(null);setAck(null);setError("");
    try{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-previews",{signal:c.signal,cache:"no-store",params:{path:{id:subscription.id}},body:{current_file_id:fileId,candidate_id:candidateId}});if(c.signal.aborted)return;
      if(r.error||!r.data||r.data.current_file_id!==fileId||r.data.candidate_id!==candidateId||r.data.version.actor_id!==userId||r.data.version.subscription_id!==subscription.id)throw Error("preview");setPreview(r.data);
    }catch{if(!c.signal.aborted)setError("比较读取失败，请刷新订阅后重新比较。旧结论已清除。");}finally{if(reader.current===c)reader.current=null;if(!c.signal.aborted)setLoading(false);}
  }
  async function transport(entry:Entry,c:AbortController){
    const options={signal:c.signal,cache:"no-store" as RequestCache,params:{path:{id:subscription.id},header:{"Idempotency-Key":entry.key}}};
    switch(entry.kind){
      case "mode":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-modes",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
      case "reservation":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-reservations",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
      case "preparation":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-preparations",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
      case "send":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-sends",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
      case "organizationAuthorization":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-organization-authorizations",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
      case "organizationPreparation":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-organization-preparations",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
      case "organizationJob":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-organization-jobs",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
      case "organizationRetirement":{const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-retirements",{...options,body:entry.body});return {status:r.response.status,data:r.data};}
    }
  }
  async function execute(entry:Entry,replayClosed=false){
    if(write.current||locked||loading)return;const c=new AbortController();write.current=c;setBusy(true);setError("");setNotice("");setAck(null);let confirmed=false;
    try{await runEntry({storage:localStorage,locks:navigator.locks,journal:memory.current,userId,subscriptionId:subscription.id,entry,replaceMode:entry.kind==="mode",replayClosed,transport:e=>transport(e,c),onJournal:value=>{confirmed=true;if(!c.signal.aborted){adopt(value);setVerified(old=>new Set(old).add(entry.key));}}});
      if(!c.signal.aborted){adopt(memory.current);setNotice(replayClosed?"已核实原回执；它只说明原命令的提交结果。":"已收到服务器回执。");onUpdated();}
    }catch{
      if(!c.signal.aborted){adopt(memory.current);if(confirmed){repairRef.current=true;setRepair(true);setError("服务器已确认，但本地回执保存失败。请仅修复记录，避免重新提交。");}
        else setError("原命令结果尚未确认或当前条件已不允许。原键与原参数已保留，请恢复同一命令；刷新或撤权不会证明它未提交。");}
    }finally{if(write.current===c){write.current=null;if(syncPending.current){syncPending.current=false;if(!c.signal.aborted)loadSaved();}}if(!c.signal.aborted)setBusy(false);}
  }
  function fresh<K extends Kind>(kind:K,body:Bodies[K]){if(ack!==kind||pending||locked)return;void execute({kind,key:crypto.randomUUID(),body,phase:"pending"} as Entry);}
  async function repairRecord(){if(write.current||!repair)return;setBusy(true);try{adopt(await persistJournal(localStorage,navigator.locks,memory.current,userId,subscription.id));repairRef.current=false;setRepair(false);setError("");setNotice("本地回执已修复，未重新提交。");}catch{setError("本地回执仍无法保存，请恢复浏览器存储后重试修复。");}finally{setBusy(false);}}
  function recover(entry:Entry){void execute({kind:entry.kind,key:entry.key,body:entry.body,phase:"pending"} as Entry,entry.phase==="closed");}
  const selectedFile=files.find(f=>f.id===fileId),selectedCandidate=candidates.find(c=>c.id===candidateId);
  const canReserve=enabled&&preview?.current_file_id===fileId&&preview.candidate_id===candidateId&&preview.comparable&&preview.improvement&&!!preview.expected_client_digest&&preview.version.intent_version===current?.intent_version;
  return <section className="config-editor" aria-label="电影品质升级">
    <div className="config-heading"><h3>电影品质升级</h3><button type="button" className="btn-secondary" aria-expanded={open} onClick={()=>{setOpen(!open);if(!open)void readResources();}}>{open?"收起升级":"管理品质升级"}</button></div>
    {open&&<>
      <p className="config-description">比较候选品质后，预留、取源准备和发送分别确认；原文件在受控回收完成前保留。</p>
      <button type="button" className="btn-secondary" disabled={busy||loading} onClick={()=>void readResources()}>刷新升级资源</button>
      {loading&&<p role="status">正在读取升级资源…</p>}
      {!current&&!loading&&<p role="status">状态已变化或连接已恢复，请刷新升级资源后重新确认。</p>}
      {(storageBlocked||error)&&<p role="alert" tabIndex={-1} ref={alert} className="config-error">{storageBlocked?"本地确认记录无法读取。为保留可能已提交的原命令，已暂停新确认；请先恢复浏览器存储。":error}</p>}
      {notice&&<p role="status">{notice}</p>}
      {repair&&<button type="button" className="btn-secondary" disabled={busy} onClick={()=>void repairRecord()}>仅修复本地回执</button>}
      {pending&&<div><p>有一项{stageLabels[pending.kind]}结果待确认。请恢复原命令，继续使用原键与原参数。</p><button type="button" className="btn-secondary" disabled={locked||loading} onClick={()=>recover(pending)}>恢复原{stageLabels[pending.kind]}</button></div>}
      {journal&&stages.map(kind=>{const e=journal[kind];return e?.phase==="closed"?<div key={e.key}>
        <p>{verified.has(e.key)?"已核实":"浏览器已保存，待核实"}：{stageLabels[kind]}{kind==="mode"?`（${modeLabels[journal.mode?.receipt?.mode??"disabled"]}）`:stageOutcomes[kind]}。</p>
        {!verified.has(e.key)&&<button type="button" className="btn-secondary" disabled={locked||loading||!!pending} onClick={()=>recover(e)}>核实原{stageLabels[kind]}回执</button>}
      </div>:null;})}
      {!reservation&&<>
        <div className="config-field"><label htmlFor={`${uid}-mode`}>升级模式</label><select id={`${uid}-mode`} value={mode} disabled={locked||!!pending} onChange={e=>{setMode(e.target.value as Mode|"");setAck(null);}}><option value="">选择要应用的模式</option><option value="disabled">关闭升级</option><option value="stop_at_cutoff">达到停止升级阈值后停止</option><option value="replace_if_better">持续替换为更优版本</option></select></div>
        {mode==="replace_if_better"&&<p className="config-description">达到停止升级阈值后仍可继续升级。仅接受严格更优的版本，同级优化保持关闭。</p>}
        {mode&&<><label><input type="checkbox" checked={ack==="mode"} disabled={locked||!!pending} onChange={e=>setAck(e.target.checked?"mode":null)}/>我确认设置此升级模式</label><button type="button" className="btn-secondary" disabled={locked||loading||!!pending||ack!=="mode"||!current} onClick={()=>current&&fresh("mode",{expected_intent:current.intent_version,mode,confirmed:true})}>确认升级模式</button></>}
        <div className="config-field"><label htmlFor={`${uid}-file`}>当前媒体库文件</label><select id={`${uid}-file`} value={fileId} disabled={locked||!!pending} onChange={e=>{reader.current?.abort();reader.current=null;setLoading(false);setFileId(e.target.value);setPreview(null);setAck(null);}}><option value="">选择当前文件</option>{files.map(f=><option key={f.id} value={f.id}>{f.root_name} · {f.rel_path}</option>)}</select></div>
        <div className="config-field"><label htmlFor={`${uid}-query`}>电影资源检索词</label><input id={`${uid}-query`} value={query} disabled={locked||!!pending} onChange={e=>setQuery(e.target.value)}/><button type="button" className="btn-secondary" disabled={locked||loading||!!pending||!query.trim()} onClick={()=>void search()}>检索站点</button></div>
        <div className="config-field"><label htmlFor={`${uid}-candidate`}>候选资源</label><select id={`${uid}-candidate`} value={candidateId} disabled={locked||!!pending} onChange={e=>{reader.current?.abort();reader.current=null;setLoading(false);setCandidateId(e.target.value);setPreview(null);setAck(null);}}><option value="">选择候选资源</option>{candidates.map(c=><option key={c.id} value={c.id}>{c.title}{c.indexer_name?` · ${c.indexer_name}`:""}</option>)}</select></div>
        <button type="button" className="btn-secondary" disabled={locked||loading||!!pending||!fileId||!candidateId} onClick={()=>void compare()}>读取升级比较</button>
        {preview&&<div aria-label="升级比较"><p>{preview.comparable&&preview.improvement?"候选品质严格优于当前文件。":knownReason(preview.reason)}</p><p>当前文件分数 {preview.current_score??"未知"} · 候选分数 {preview.candidate_score??"未知"}。比较结果不代表已授权下载。</p><p>{selectedFile?.rel_path} → {selectedCandidate?.title}</p>
          {!enabled&&<p>请先确认启用一种升级模式，再刷新资源与比较。</p>}
          {canReserve&&<><label><input type="checkbox" checked={ack==="reservation"} disabled={locked||!!pending} onChange={e=>setAck(e.target.checked?"reservation":null)}/>我确认冻结此次文件和候选，保留原文件保护</label><button type="button" className="btn-secondary" disabled={locked||loading||!!pending||ack!=="reservation"} onClick={()=>fresh("reservation",{current_file_id:fileId,candidate_id:candidateId,expected:preview.version,expected_client_digest:preview.expected_client_digest!,confirmed:true})}>确认冻结预留</button></>}
        </div>}
      </>}
      {reservation?.phase==="closed"&&!preparation&&<div><p>下一步读取并核验原始种子，准备完成仍不会发送。</p>{!verified.has(reservation.key)?<p>请先核实原冻结预留回执。</p>:<><label><input type="checkbox" checked={ack==="preparation"} disabled={locked||!!pending} onChange={e=>setAck(e.target.checked?"preparation":null)}/>我确认取源并准备此次升级</label><button type="button" className="btn-secondary" disabled={locked||loading||!!pending||ack!=="preparation"} onClick={()=>fresh("preparation",{operation_id:reservation.receipt!.operation_id,confirmed:true})}>确认取源准备</button></>}</div>}
      {preparation?.phase==="closed"&&!send&&<div><p>下一步将向订阅当前绑定的下载器提交一次。结果不明时保留保护并转人工观察。</p>{!verified.has(preparation.key)?<p>请先核实原取源准备回执。</p>:<><label><input type="checkbox" checked={ack==="send"} disabled={locked||!!pending} onChange={e=>setAck(e.target.checked?"send":null)}/>我确认向下载器发送此次升级</label><button type="button" className="btn-primary" disabled={locked||loading||!!pending||ack!=="send"} onClick={()=>fresh("send",{operation_id:preparation.receipt!.operation_id,task_id:preparation.receipt!.task_id,confirmed:true})}>确认发送升级</button></>}</div>}
      {send?.phase==="closed"&&journal&&<MovieUpgradeOrganizationPanel subscriptionId={subscription.id} operationId={send.receipt!.operation_id} journal={journal} disabled={locked||loading||!!pending} verified={verified} ack={ack} setAck={setAck} fresh={fresh} revision={String(activity.version)}/>}
    </>}
  </section>;
}

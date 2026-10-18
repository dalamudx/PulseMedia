import {useEffect,useRef,useState} from 'react';
import {api} from '@/api/client';
import {useAuth} from '@/features/auth/AuthContext';
import {readJournal,saveJournal,validReceipt,validPreviewPage,withOrganizationLock,type OrganizationJournal,type OrganizationPreview} from './completeTVOrganizationJournal';
import './complete-tv-organization.css';

export function CompleteTVOrganizationPanel({operationId,contextReady,refreshVersion}:{operationId:string;contextReady:boolean;refreshVersion:number}){
 const {user}=useAuth();
 if(!user||user.role!=='admin')return null;
 return <OrganizationPanel key={`${user.id}:${user.role}:${operationId}`} account={user.id} operationId={operationId} contextReady={contextReady} refreshVersion={refreshVersion}/>;
}
function OrganizationPanel({account,operationId,contextReady,refreshVersion}:{account:string;operationId:string;contextReady:boolean;refreshVersion:number}){
 const [preview,setPreview]=useState<OrganizationPreview|null>(null),[journal,setJournal]=useState<OrganizationJournal|null>(null),[ack,setAck]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(''),[storageError,setStorageError]=useState(false),[readVersion,setReadVersion]=useState(0);
 const owner=useRef<AbortController|null>(null),epoch=useRef(0),confirmed=useRef<OrganizationJournal|null>(null),submitting=useRef(false);
 const refreshPending=useRef(false),permissionEpoch=useRef(0),currentContext=useRef(contextReady);currentContext.current=contextReady;
 const lockAvailable=typeof navigator!=='undefined'&&Boolean(navigator.locks?.request);
 function begin(){owner.current?.abort();const ctl=new AbortController();owner.current=ctl;const generation=++epoch.current;return {ctl,current:()=>!ctl.signal.aborted&&epoch.current===generation};}
 async function read(){
  permissionEpoch.current++;
  if(submitting.current){refreshPending.current=true;setPreview(null);setAck(false);return;}
  const {ctl,current}=begin();setBusy(true);setPreview(null);setAck(false);setError('');
  try{
   await withOrganizationLock(account,operationId,ctl.signal,async()=>{
    if(!current())return;
    // A valid receipt closes the command in memory BEFORE storage repair can fail.
    if(confirmed.current){saveJournal(confirmed.current);if(current())setStorageError(false);}
    const saved=readJournal(account,operationId);if(current()){setJournal(saved);setStorageError(false);if(saved?.receipt)confirmed.current=saved;}
   });
   if(!current())return;
   if(!contextReady){setError('操作当前状态不可用，新确认已停用；原待确认命令仍可用原键恢复。');return;}
   const {data,error:failure}=await api.GET('/api/v1/operations/{id}/complete-tv-organizations',{signal:ctl.signal,cache:'no-store',params:{path:{id:operationId}}});
   if(!current())return;
   if(failure||!validPreviewPage(data,operationId)){setError('整理材料读取失败，请重新读取。新确认已停用，原待确认命令仍可用原键恢复。');return;}
   const item=data.items[0]??null;
   if(item&&item.operation_id!==operationId){setError('整理材料不匹配，请重新读取。');return;}
   setPreview(item);
  }catch{if(current()){setStorageError(true);setError('读取或本地记录修复失败。请恢复存储与跨标签锁后重新读取；不会创建新键。');}}
  finally{if(current()){setBusy(false);owner.current=null;}}
 }
 useEffect(()=>()=>{owner.current?.abort();owner.current=null;epoch.current++;},[account,operationId]);
 // A stream reset invalidates new permission, but an in-flight original command
 // keeps its receipt owner. Read fresh state once that bounded request finishes.
 useEffect(()=>{void read();},[account,operationId,refreshVersion,contextReady,readVersion]);
 async function submit(){
  if(submitting.current)return;
  submitting.current=true;const permission=permissionEpoch.current;const {ctl,current}=begin();setBusy(true);setError('');setAck(false);
  try{await withOrganizationLock(account,operationId,ctl.signal,async()=>{
   if(!current())return;
   if(confirmed.current){saveJournal(confirmed.current);if(current()){setJournal(confirmed.current);setStorageError(false);}return;}
   let saved=readJournal(account,operationId);
   if(saved?.receipt){confirmed.current=saved;if(current())setJournal(saved);return;}
   if(!saved){
    if(!currentContext.current||permission!==permissionEpoch.current||!preview?.can_confirm||!ack||storageError)throw new Error('context required');
    saved={version:1,account,operation:operationId,command:{task_id:preview.task_id,operation_id:operationId,tracking_job_id:preview.tracking_job_id,key:crypto.randomUUID()}};
    saveJournal(saved); // Durable original refs AND key before the first POST.
   }
   if(!current())return;
   setJournal(saved);setStorageError(false);
   const command=saved.command;
   const {data,error:failure}=await api.POST('/api/v1/download-tasks/{id}/complete-tv-organizations',{signal:ctl.signal,params:{path:{id:command.task_id},header:{'Idempotency-Key':command.key}},body:{operation_id:command.operation_id,tracking_job_id:command.tracking_job_id}});
   if(!current())return;
   if(failure||!validReceipt(data,command.task_id)){setPreview(null);setError('整理确认结果尚未确认。即使收到权限或冲突响应，也只允许恢复原命令，不能换键。');return;}
   const closed={...saved,receipt:data};confirmed.current=closed;setJournal(closed);setPreview(null);
   try{saveJournal(closed);setStorageError(false);}catch{setStorageError(true);setError('已确认排队，但本地回执保存失败。重新读取只修复记录，不再发送确认。');}
  });}catch{if(current()){setPreview(null);setStorageError(true);setError('连接或本地记录不可用。请重新读取恢复原记录；禁止创建替代命令。');}}
  finally{submitting.current=false;if(current()){setBusy(false);owner.current=null;if(refreshPending.current){refreshPending.current=false;setReadVersion(v=>v+1);}}}
 }
 const receipt=journal?.receipt??confirmed.current?.receipt;
 const pending=Boolean(journal&&!receipt);
 return <section className="config-editor complete-tv-organization" aria-label="完整季整理确认"><div className="config-heading"><h3>完整季整理</h3><button type="button" className="btn-secondary" disabled={busy||!lockAvailable} onClick={()=>void read()}>重新读取整理材料</button></div>
 {!lockAvailable&&<p role="alert">当前浏览器不支持跨标签锁，整理确认已停用。</p>}
 {busy&&<p role="status">正在读取或确认整理…</p>}
 {error&&<p role="alert" className="config-error">{error}</p>}
 {receipt?<><p role="status">原确认回执：已排队（queued）。这不是当前入库状态，请以操作详情和整理计划为准。</p><p>计划 {receipt.plan_id}</p>{storageError&&<p>本地回执待修复。重新读取不会重发已确认命令。</p>}</>:null}
 {pending?<><p role="status">有结果未确认的整理命令。保留原任务与原键，可在重新登录或读取失败后恢复。</p><details><summary>查看原命令引用</summary><p>原下载任务 {journal?.command.task_id}</p><p>原完成跟踪 {journal?.command.tracking_job_id}</p></details><button type="button" className="btn-primary" disabled={busy||!lockAvailable} onClick={()=>void submit()}>用原键恢复整理确认</button></>:null}
 {preview?<><p>第 {preview.season} 季 · {preview.videos.length} 个视频 · {preview.attachment_count} 个附件留在源目录</p><p>当前整理状态：{preview.organization_state}。材料来自持久记录，确认时仍须重新检查源文件与目标根。</p><details open><summary>查看精确季集与源 / 目标相对路径</summary><ul>{preview.videos.map(v=><li key={v.unit_id}><strong>{v.unit_key}</strong> · {v.size_bytes} 字节<dl><dt>源视频</dt><dd>{v.source_relative}</dd><dt>目标视频</dt><dd>{v.target_relative}</dd></dl></li>)}</ul></details>
 {!journal&&preview.can_confirm?<><label className="config-check"><input type="checkbox" checked={ack} disabled={busy||!contextReady} onChange={e=>setAck(e.target.checked)}/>我已核对全部季集和目标，确认执行硬链接整理；附件留在源目录。</label><button type="button" className="btn-primary" disabled={busy||!ack||!contextReady||storageError||!lockAvailable} onClick={()=>void submit()}>确认完整季整理</button></>:null}</>:!busy&&!error&&!receipt?<p>当前没有可确认的完整普通季整理材料。下载完成本身不代表已入库。</p>:null}
 </section>;
}

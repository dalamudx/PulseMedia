import {useEffect,useId,useRef,useState} from 'react';
import {api} from '@/api/client';
import type {paths} from '@/api/client';
import {useAuth} from '@/features/auth/AuthContext';
import {journalName,readJournal,saveJournal,validReceipt,withReuseLock} from './libraryReuseJournal';
import type {ReuseCommand,ReuseJournal} from './libraryReuseJournal';
import './libraryReuse.css';
type Subscription=paths['/api/v1/subscriptions']['get']['responses']['200']['content']['application/json']['items'][number];
type Preview=paths['/api/v1/library/reuse-previews']['post']['responses']['200']['content']['application/json'][number];
type Status=paths['/api/v1/library/reuses']['get']['responses']['200']['content']['application/json'][number];
const reasons:Record<string,string>={already_owned:'目标库已拥有',ownership_unknown:'目标拥有状态待确认',root_unavailable:'目标库离线或尚未完成扫描',target_unavailable:'目标库离线或尚未完成扫描',no_available_qualified_source:'没有当前可用且符合策略的源文件',source_unavailable:'源库暂不可用',complete_file_coverage_required:'需确认整个文件的全部覆盖',quality_rejected:'不符合当前品质策略',candidate_limit_exceeded:'候选未读全，请缩小范围后重试',reserved:'目标已有任务占位'};
const reviewReasons:Record<string,string>={root_unavailable:'源库或目标库暂不可用，恢复后会继续检查',no_space:'目标空间不足，释放空间后会继续检查',authorization_changed:'原确认账号的权限已变化',inputs_changed:'策略、意图或文件映射已变化，请核对当前材料',action_unsupported:'文件系统不支持所选动作',file_evidence_changed:'文件身份或目标内容与冻结材料不符，请检查目标文件'};
const states:Record<string,string>={queued:'已排队',waiting:'等待目录恢复',running:'正在整理',completed:'已完整入库',review_required:'需人工复核，目标保护保留'};
function label(key:string){return key==='movie'?'单电影':key}
function coverage(keys:string[]){return [...keys].sort((a,b)=>a.localeCompare(b,undefined,{numeric:true})).map(label).join('、')}
function qualityLabel(value:Record<string,unknown>){return ['resolution','source','codec','video_codec','audio','audio_codec','release_group'].map(key=>value[key]).filter(v=>typeof v==='string'&&v).join(' · ')}
export function LibraryReusePanel({subscription:sub,onUpdated}:{subscription:Subscription;onUpdated:()=>void}){
 const {user}=useAuth(),account=user?.id??'',heading=useId();
 const available=(sub.wanted_items??[]).filter(w=>!w.is_skipped&&!w.retired);
 const [selected,setSelected]=useState<string[]>(()=>available.filter(w=>!w.is_satisfied).slice(0,20).map(w=>w.unit_id));
 const [previews,setPreviews]=useState<Preview[]>([]),[choice,setChoice]=useState<{preview:Preview;file:string}|null>(null);
 const [target,setTarget]=useState(''),[action,setAction]=useState<ReuseCommand['action']>('hardlink'),[ack,setAck]=useState(false);
 const [journal,setJournal]=useState<ReuseJournal|null>(null),[blocked,setBlocked]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(''),[statuses,setStatuses]=useState<Status[]>([]);
 const owner=useRef<AbortController|null>(null),alert=useRef<HTMLParagraphElement>(null);
 const pending=journal!==null&&!journal.receipt&&!journal.rejected,source=choice?.preview.candidates.find(c=>c.file_id===choice.file);
 const reset=()=>{setPreviews([]);setChoice(null);setAck(false)};
 useEffect(()=>{
  const restore=()=>{try{setJournal(readJournal(account,sub.id));setBlocked(false)}catch{setBlocked(true);setError('无法读取本地命令记录。请恢复浏览器存储后重新打开，避免重复提交。')}};
  restore();const changed=(event:StorageEvent)=>{if(event.key===journalName(account,sub.id)){owner.current?.abort();owner.current=null;setBusy(false);reset();restore()}};
  window.addEventListener('storage',changed);return()=>{window.removeEventListener('storage',changed);owner.current?.abort();owner.current=null};
 },[account,sub.id]);
 useEffect(()=>{if(error)alert.current?.focus()},[error]);
 async function preview(){
  if(owner.current||pending||blocked||!selected.length)return;
  const c=new AbortController();owner.current=c;setBusy(true);setError('');reset();
  try{const r=await api.POST('/api/v1/library/reuse-previews',{signal:c.signal,cache:'no-store',body:{targets:selected.map(unit_id=>({unit_id,root_id:sub.root_id,subscription_id:sub.id,policy_id:sub.quality_policy_id,policy_revision:sub.quality_policy_revision}))}});
   if(c.signal.aborted)return;if(r.error||!Array.isArray(r.data)||r.data.length!==selected.length){setError('预览读取失败，请重新读取；旧结果已清除。');return}setPreviews(r.data);
  }catch{if(!c.signal.aborted)setError('预览连接中断，请重试。')}finally{if(owner.current===c)owner.current=null;if(!c.signal.aborted)setBusy(false)}
 }
 async function refresh(){
  if(owner.current)return;const c=new AbortController();owner.current=c;setBusy(true);setError('');setStatuses([]);
  try{const r=await api.GET('/api/v1/library/reuses',{signal:c.signal,cache:'no-store'});if(c.signal.aborted)return;if(r.error||!Array.isArray(r.data)){setError('复用状态读取失败，请重试。');return}setStatuses(r.data.filter(row=>row.root_id===sub.root_id&&row.unit_ids.some(id=>available.some(w=>w.unit_id===id))));onUpdated()}
  catch{if(!c.signal.aborted)setError('复用状态连接中断，请重试。')}finally{if(owner.current===c)owner.current=null;if(!c.signal.aborted)setBusy(false)}
 }
 async function confirm(){
  if(owner.current||blocked||!pending&&(!source||!choice||!ack||!target))return;
  if(!pending&&(!target||target.length>1024||/[\\\u0000-\u001f\u007f]/.test(target)||target.split('/').some(part=>!part||part==='.'||part==='..'))){setError('请输入目标库内的相对文件路径，不可包含空目录、点目录或反斜线。');return}
  const c=new AbortController();owner.current=c;setBusy(true);setError('');
  try{await withReuseLock(account,sub.id,c.signal,async()=>{
   const saved=readJournal(account,sub.id);
   let command=saved;
   if(saved?.receipt&&journal?.key!==saved.key){setJournal(saved);reset();return}
   if(!command||command.receipt||command.rejected){if(!choice||!source||!ack)return;command={version:1,account,subscription:sub.id,key:crypto.randomUUID(),body:{target:choice.preview.target,source_file_id:source.file_id,confirmed_unit_ids:source.unit_ids,target_rel:target,action,input_stamp:choice.preview.input_stamp,confirmed:true}};saveJournal(command)}
   setJournal(command);reset();
   const r=await api.POST('/api/v1/library/reuses',{signal:c.signal,cache:'no-store',params:{header:{'Idempotency-Key':command.key}},body:command.body});if(c.signal.aborted)return;
   if(r.error||!validReceipt(r.data)){if(r.response.status===400||r.response.status===409){const rejected:ReuseJournal={...command,rejected:r.response.status};saveJournal(rejected);setJournal(rejected);setError('原命令未获接受，请重新预览后确认。')}else setError('回执尚未确认。恢复命令会沿用原动作、完整覆盖和请求键。');return}
   const completed={...command,receipt:r.data};saveJournal(completed);setJournal(completed);onUpdated();
  })}catch{if(!c.signal.aborted){try{setJournal(readJournal(account,sub.id))}catch{setBlocked(true)}setError('命令记录或回执尚未确认。恢复浏览器存储和连接后，沿用原命令重试。')}}finally{if(owner.current===c)owner.current=null;if(!c.signal.aborted)setBusy(false)}
 }
 if(user?.role!=='admin')return null;
 return <details className="config-editor library-reuse" aria-labelledby={heading}>
  <summary id={heading}>从其他媒体库复用</summary>
  <p className="config-description">目标库：{sub.root_name||'当前订阅媒体库'}。选择目标后查看已有文件，可保留源文件并整理到这里。</p>
  {error&&<p className="config-error" role="alert" tabIndex={-1} ref={alert}>{error}</p>}
  <fieldset disabled={busy||pending||blocked} className="reuse-targets"><legend>本批目标（最多 20 个）</legend>
   {available.map(w=><label className="config-check" key={w.unit_id}><input type="checkbox" checked={selected.includes(w.unit_id)} disabled={!selected.includes(w.unit_id)&&selected.length>=20} onChange={e=>{setSelected(ids=>e.target.checked?[...ids,w.unit_id]:ids.filter(id=>id!==w.unit_id));reset()}}/>{label(w.unit_key||'未命名单元')}</label>)}
   {!available.length&&<p>当前没有可选择的目标。</p>}
  </fieldset>
  <div className="config-actions"><button type="button" className="btn-secondary" disabled={busy||pending||blocked||!selected.length} onClick={()=>void preview()}>预览所选目标</button><button type="button" className="btn-secondary" disabled={busy} onClick={()=>void refresh()}>刷新复用状态</button></div>
  {previews.map(p=><section key={p.target.unit_id} className="reuse-result"><h4>{label(p.unit_key)}</h4>
   <p role="status">{p.already_owned?'目标库已拥有':p.ownership_unknown?'目标拥有状态待确认':!p.complete?'候选超过上限，无法确认':p.can_confirm?'可选择源文件':reasons[p.wait_reason]||'当前没有可用源文件'}</p>
   <ul className="reuse-sources">{p.candidates.map(candidate=><li key={candidate.file_id}>
    <label><input type="radio" name={heading} checked={choice?.file===candidate.file_id&&choice.preview.target.unit_id===p.target.unit_id} disabled={busy||pending||!p.can_confirm||!candidate.qualified||!candidate.source_available} onChange={()=>{setChoice({preview:p,file:candidate.file_id});setTarget(candidate.rel_path);setAck(false)}}/><strong>{candidate.root_name}</strong> · {candidate.source_available?'在线':'暂不可用'}</label>
    <p className="reuse-path">{candidate.rel_path}</p><p>完整覆盖：{coverage(candidate.unit_keys)}</p>
    <p className="config-description">{qualityLabel(candidate.quality)}{candidate.wait_reason&&` · ${reasons[candidate.wait_reason]||'材料待核验'}`}</p>
   </li>)}</ul>
  </section>)}
  {source&&choice&&<div className="reuse-confirm">
   <h4>确认整理</h4><p>完整覆盖 {source.unit_ids.length} 个目标：{coverage(source.unit_keys)}</p>
   <label className="config-field">目标相对路径<input value={target} disabled={busy||pending} maxLength={1024} onChange={e=>{setTarget(e.target.value);setAck(false)}}/></label>
   <label className="config-field">文件动作<select aria-label="文件动作" value={action} disabled={busy||pending} onChange={e=>{setAction(e.target.value as ReuseCommand['action']);setAck(false)}}><option value="hardlink">硬链接（同一文件系统）</option><option value="copy">复制</option><option value="reflink">克隆（reflink）</option></select></label>
   <label className="config-check"><input type="checkbox" checked={ack} disabled={busy||pending} onChange={e=>setAck(e.target.checked)}/>确认以上全部覆盖和目标路径，保留源文件</label>
  </div>}
  {pending&&<p role="status">有待确认命令：{journal.body.target_rel} · 完整覆盖 {journal.body.confirmed_unit_ids.length} 个目标。</p>}
  {(source||pending)&&<button type="button" className="btn-primary" disabled={busy||blocked||!pending&&(!ack||!target)} onClick={()=>void confirm()}>{busy?'正在处理…':pending?'恢复同一复用命令':'确认复用并排队'}</button>}
  {journal?.receipt&&<p role="status">复用已排队。刷新复用状态查看整理结果。</p>}
  {statuses.length>0&&<ul className="reuse-statuses">{statuses.map(row=><li key={row.plan_id}><strong>{states[row.state]}</strong>{row.reason&&<span>{reviewReasons[row.reason]||'请核对当前文件材料'}</span>}<small className="reuse-path">需求 {row.plan_id}</small><span className="reuse-path">{row.target_rel}</span><span>完整覆盖 {row.unit_ids.length} 个目标</span></li>)}</ul>}
 </details>;
}

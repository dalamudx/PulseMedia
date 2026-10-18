import {useEffect,useId,useRef,useState} from 'react';
import {api} from '@/api/client';
import type {paths} from '@/api/client';
import {useAuth} from '@/features/auth/AuthContext';
type Preview=paths['/api/v1/transfer-plans/{id}/revision-preview']['post']['responses']['200']['content']['application/json'];
type Body=paths['/api/v1/transfer-plans/{id}/revision']['post']['requestBody']['content']['application/json'];
type Receipt=paths['/api/v1/transfer-plans/{id}/revision']['post']['responses']['202']['content']['application/json'];
type Action=NonNullable<Body['action']>;
const actions:Record<Action,{label:string;description:string}>={
 hardlink:{label:'硬链接',description:'保留源文件；源与目标须位于同一文件系统。'},
 copy:{label:'复制',description:'完整校验后发布目标，保留源文件。'},
 reflink:{label:'克隆（reflink）',description:'保留源文件；须通过当前目录的克隆能力探测。'},
 move:{label:'移动',description:'完整校验目标后移除源路径，已有保种硬链接保留。'},
};
function isAction(value:unknown):value is Action {return typeof value==='string'&&Object.hasOwn(actions,value)}
type Pending={key:string;planId:string;body:Body};
const reason='修订整理路径与文件动作，保留原计划历史';
const legacyReasons=['修订相对目标路径，保留原计划历史','修订单电影相对目标路径，保留原计划历史'];
function restore(key:string):Pending|null {
 try {
  const v=JSON.parse(sessionStorage.getItem(key)??'null') as Pending;
  if(!v||!/^[-0-9a-f]{36}$/i.test(v.planId)||!/^[-0-9a-f]{36}$/i.test(v.key)||v.body?.confirmed!==true||
   !/^[0-9a-f]{64}$/.test(v.body.expected_input_stamp)||
   (v.body.reason!==reason&&!legacyReasons.includes(v.body.reason))||
   (v.body.action!==undefined&&!isAction(v.body.action))||
   typeof v.body.target_rel!=='string'||v.body.target_rel.length>1024||v.body.target_rel.startsWith('/')||/[\\\x00\r\n:?#]/.test(v.body.target_rel))return null;
  return v;
 }catch{return null}
}
const reasons:Record<string,string>={
 target_episode_not_canonical:'新目标必须保留原季集键，不能改为其他集或多集。',
 own_requirement_not_pending:'本人在原目标上的需求已撤回，不能启动新修订。',
 missing_target_revision:'原目标不存在，源身份一致，可以确认新目标。',
 target_not_absent:'原目标或新目标已存在，不能覆盖；请核对原计划恢复。',
 organization_execution_pending:'原整理任务尚未结束，请稍后重新预览。',
 plan_execution_pending:'原计划仍可能执行，暂不能修订。',
 file_observation_expired:'文件观察已过期，请重新预览。',
 source_identity_changed:'源身份已变化，需要人工复核。',
 root_identity_changed:'目录身份已变化，需要人工复核。',
 unsupported_action_for_media:'复制、移动和克隆仅支持单电影；单集请使用硬链接。',
 verified_action_required:'原计划已有完整内容证据，请选择复制、克隆或移动并重新预览。',
 file_action_unavailable:'当前文件服务不支持此动作，请重新选择。',
 reflink_unavailable:'当前目录不支持克隆，可明确选择复制并重新预览。',
};
export function TransferRevisionPanel({planId,operationId}:{planId:string;operationId:string}) {
 const {user}=useAuth(); const storage=`pulse:transfer-revision:${user?.id??''}:${operationId}`;
 const [target,setTarget]=useState(''),[preview,setPreview]=useState<Preview|null>(null),[pending,setPending]=useState<Pending|null>(null),[receipt,setReceipt]=useState<Receipt|null>(null),[ack,setAck]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState('');
 const [action,setAction]=useState<Action>('hardlink');
 const actionHint=useId();
 const owner=useRef<AbortController|null>(null),alert=useRef<HTMLParagraphElement>(null);
 useEffect(()=>{const saved=restore(storage);setPending(saved);setTarget(saved?.body.target_rel??'');setAction(saved?.body.action??'hardlink');setPreview(null);setReceipt(null);setAck(false);setError('');setBusy(false);return()=>{owner.current?.abort();owner.current=null}},[storage]);
 useEffect(()=>{if(error)alert.current?.focus()},[error]);
 async function read(){if(owner.current||pending)return;const c=new AbortController();owner.current=c;setBusy(true);setPreview(null);setAck(false);setReceipt(null);setError('');try{const r=await api.POST('/api/v1/transfer-plans/{id}/revision-preview',{signal:c.signal,cache:'no-store',params:{path:{id:planId}},body:{target_rel:target,action}});if(c.signal.aborted)return;if(r.error||!r.data){setError('修订预览未获允许，请核对路径、权限和原计划状态。');return}setPreview(r.data)}catch{if(!c.signal.aborted)setError('修订预览失败，旧许可已清除，请重试。')}finally{if(owner.current===c)owner.current=null;if(!c.signal.aborted)setBusy(false)}}
 async function commit(){if(owner.current||!pending&&(!ack||!preview?.revisable))return;const command=pending??{key:crypto.randomUUID(),planId,body:{target_rel:preview!.target_rel,action:preview!.action??action,expected_input_stamp:preview!.input_stamp,confirmed:true as const,reason}};try{sessionStorage.setItem(storage,JSON.stringify(command))}catch{setError('无法保存未确认命令，尚未提交。');return}setPending(command);setPreview(null);setAck(false);setError('');const c=new AbortController();owner.current=c;setBusy(true);try{const r=await api.POST('/api/v1/transfer-plans/{id}/revision',{signal:c.signal,cache:'no-store',params:{path:{id:command.planId},header:{'Idempotency-Key':command.key}},body:command.body});if(c.signal.aborted)return;if(r.error||!r.data){if([400,403,409].includes(r.response.status)){sessionStorage.removeItem(storage);setPending(null);setError('修订未获接受或原条件已变化，请重新预览。')}else setError('修订回执未确认，请恢复同一命令，暂勿修改目标。');return}sessionStorage.removeItem(storage);setPending(null);setReceipt(r.data)}catch{if(!c.signal.aborted)setError('修订回执未确认，请恢复同一命令，暂勿修改目标。')}finally{if(owner.current===c)owner.current=null;if(!c.signal.aborted)setBusy(false)}}
 if(user?.role!=='admin')return null;
 return <section className="config-editor" aria-label="修订整理目标">
  <h4>修订整理目标</h4>
  <p className="config-description">选择新目标和文件动作。原执行须已结束且原目标不存在；单集须使用硬链接并保留原季集键。</p>
  {error&&<p className="config-error" role="alert" tabIndex={-1} ref={alert}>{error}</p>}
  <label className="config-field">目标相对路径
   <input value={target} maxLength={1024} placeholder="Movie (2020)/Movie revised.mkv" disabled={busy||pending!==null} onChange={e=>{setTarget(e.target.value);setPreview(null);setAck(false);setReceipt(null)}}/>
  </label>
  <label className="config-field">文件动作
   <select value={action} aria-describedby={actionHint} disabled={busy||pending!==null} onChange={e=>{if(isAction(e.target.value)){setAction(e.target.value);setPreview(null);setAck(false);setReceipt(null)}}}>
    {Object.entries(actions).map(([value,item])=><option key={value} value={value}>{item.label}</option>)}
   </select>
  </label>
  <p className="config-description" id={actionHint}>{actions[action].description}</p>
  <button type="button" className="btn-secondary" disabled={busy||pending!==null||!target} onClick={()=>void read()}>预览修订目标</button>
  {preview&&<p role="status">{reasons[preview.reason]??'当前原计划、来源或覆盖范围不支持修订。'}</p>}
  {preview?.revisable&&<div style={{overflowWrap:'anywhere'}}>
   <p>原目标：{preview.original_target_rel}</p><p>新目标：{preview.target_rel}</p>
   {preview.media_type==='tv'&&<p>原单集：{preview.unit_key}</p>}
   <p>源文件：{preview.source_size.toLocaleString()} B · {actions[preview.action??action].label}</p>
  </div>}
  {pending&&<p role="status">有未确认修订命令，恢复时沿用原动作、目标和请求键。</p>}
  <label className="config-check"><input type="checkbox" checked={ack} disabled={busy||pending!==null||!preview?.revisable} onChange={e=>setAck(e.target.checked)}/>确认使用所选动作和新目标，保留原计划历史</label>
  <button type="button" className="btn-primary" disabled={busy||!pending&&(!ack||!preview?.revisable)} onClick={()=>void commit()}>{pending?'恢复同一修订命令':'确认修订并排队'}</button>
  {receipt&&<p role="status">新计划 {receipt.plan_id} 已排队，原计划 {receipt.previous_plan_id} 保留。请刷新操作状态查看执行结果。</p>}
 </section>;
}

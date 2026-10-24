import {useCallback,useEffect,useRef,useState} from 'react';
import {automationInputHash,automationStorageKey,parseAutomationCommand,serializeAutomationCommand,type AutomationCommand} from './automationSettingsCommand';

export interface ConfigurationResult<C> {status:number;config:C|null;key?:string}
export function configurationResult<C>(r:{response:Response;data?:{config:C|null;command_key?:string}}):ConfigurationResult<C>{
 if(r.response.ok && (!r.data || !('config' in r.data))) throw Error('Invalid saved configuration');
 return {status:r.response.status,config:r.data?.config??null,key:r.data?.command_key};
}
export function csrfHeaders(){return {'X-CSRF-Token':decodeURIComponent(document.cookie.split('; ').find(c=>c.startsWith('csrf_token='))?.slice(11)??'')};}
interface Adapter<C,I> {
 read:(signal:AbortSignal)=>Promise<ConfigurationResult<C>>;
 save:(input:I,key:string,signal:AbortSignal)=>Promise<ConfigurationResult<C>>;
 receipt:(key:string,signal:AbortSignal)=>Promise<ConfigurationResult<C>>;
}
export function useAutomationSettings<C extends {version:number}, I extends {expected_version:number}>(owner:string,scope:string,adapter:Adapter<C,I>,options?:{isCurrent?:()=>boolean;savedMessage?:string;requireReceiptBeforeRetry?:boolean}){
 const storageKey=automationStorageKey(owner,scope);
 const [config,setConfig]=useState<C|null>(null),[ready,setReady]=useState(false),[busy,setBusy]=useState(false),[message,setMessage]=useState('');
 const [pending,setPending]=useState<AutomationCommand|null>(null),[phase,setPhase]=useState<'unknown'|'absent'|'confirmed'>('unknown'),[storageError,setStorageError]=useState(false),[conflict,setConflict]=useState(false);
 const current=useRef<AutomationCommand|null>(null),original=useRef<I|null>(null),gate=useRef(false),alive=useRef(false),epoch=useRef(0),controller=useRef<AbortController|null>(null);
 const begin=()=>{controller.current?.abort();const ctl=new AbortController();controller.current=ctl;return {ctl,epoch:++epoch.current};};
 const active=(r:{ctl:AbortController;epoch:number})=>(options?.isCurrent?.()??true)&&alive.current&&!r.ctl.signal.aborted&&r.epoch===epoch.current;
 const remember=(p:AutomationCommand|null)=>{current.current=p;setPending(p);};
 const hydrate=useCallback(()=>{
  try{const raw=localStorage.getItem(storageKey);const p=raw===null?null:parseAutomationCommand(raw,owner,scope);if(raw!==null&&!p)throw Error();current.current=p;setPending(p);setStorageError(false);setPhase('unknown');setConflict(p?.rejected === true);if(p)setMessage('上次保存结果待确认，请查询原请求回执。');return true;}
  catch{setStorageError(true);setMessage('恢复记录不可读。请修复浏览器存储后重新读取；尚未发出保存请求。');return false;}
 },[storageKey,owner,scope]);
 const refresh=useCallback(async()=>{
  if(gate.current||!(options?.isCurrent?.()??true))return false;gate.current=true;setBusy(true);setReady(false);setConfig(null);
  const ctl=new AbortController();controller.current?.abort();controller.current=ctl;const ticket=++epoch.current;
  try{const r=await adapter.read(ctl.signal);if(!alive.current||ctl.signal.aborted||ticket!==epoch.current||!(options?.isCurrent?.()??true))return false;
   if(r.status!==200)throw Error();
   if(r.config&&(!Number.isSafeInteger(r.config.version)||r.config.version<1))throw Error();
   setConfig(r.config);setReady(true);setMessage(previous=>previous==="配置读取失败，当前状态未知。请重新读取后再保存。"?"":previous);return true;
  }catch{if(alive.current&&!ctl.signal.aborted&&ticket===epoch.current&&(options?.isCurrent?.()??true))setMessage('配置读取失败，当前状态未知。请重新读取后再保存。');return false;}
  finally{if(alive.current&&ticket===epoch.current&&(options?.isCurrent?.()??true)){gate.current=false;setBusy(false);}}
 },[adapter]);
 useEffect(()=>{
  alive.current=true;hydrate();void refresh();
  const changed=(event:StorageEvent)=>{if(event.key===storageKey||event.key===null){setStorageError(true);setMessage('恢复记录在其他标签页变化，请重新读取恢复记录后核对原回执。');}};
  window.addEventListener('storage',changed);
  return()=>{alive.current=false;gate.current=false;++epoch.current;controller.current?.abort();original.current=null;window.removeEventListener('storage',changed);};
 },[hydrate,refresh,storageKey]);
 const clear=(p:AutomationCommand)=>{
  const raw=localStorage.getItem(storageKey);
  if(raw!==null&&raw!==serializeAutomationCommand(p))throw Error('Recovery record changed');
  localStorage.removeItem(storageKey);remember(null);original.current=null;setPhase('unknown');setConflict(false);setStorageError(false);
 };
 const recover=async()=>{
  const p=current.current;if(!p||gate.current)return;gate.current=true;setBusy(true);const r=begin();let reload=false;
  try{const result=await adapter.receipt(p.key,r.ctl.signal);if(!active(r))return;
   if(result.status===404){setPhase('absent');setMessage('原请求暂无回执。可重新填写完全相同的内容，使用原键重试；不会自动保存。');return;}
   if(result.status!==200||result.key!==p.key||!result.config||result.config.version!==p.version+1)throw Error();
   setPhase('confirmed');clear(p);setMessage('原保存回执已恢复。配置已保存，执行结果请另行查看。');reload=true;
  }catch{if(active(r)){setMessage('原回执读取或恢复记录清理失败，原请求已保留。请再次查询。');}}
  finally{if(active(r)){gate.current=false;setBusy(false);}}
  if(reload)await refresh();
 };
 const save=async(input:I,sourceVersion?:number)=>{
  if(gate.current||!ready||storageError||!(options?.isCurrent?.()??true))return;if(current.current&&options?.requireReceiptBeforeRetry&&phase!=="absent"){setMessage("请先查询原保存回执，确认尚无回执后再明确重试。");return;}
  gate.current=true;setBusy(true);const r=begin();let reload=false;
  try{
   const hash=await automationInputHash(input);if(!active(r))return;
   let p=current.current;
   if(p){if(hash!==p.hash||input.expected_version!==p.version){setMessage('内容与原请求不一致，不能使用原键提交。请恢复原回执或重新填写原内容。');return;}}
   else{p={key:crypto.randomUUID(),owner,scope,version:input.expected_version,hash,...(sourceVersion===undefined?{}:{sourceVersion})};localStorage.setItem(storageKey,serializeAutomationCommand(p));remember(p);}
   if(p.rejected){p={...p,rejected:undefined};localStorage.setItem(storageKey,serializeAutomationCommand(p));remember(p);}
   original.current=input;setPhase('unknown');setConflict(false);
   const result=await adapter.save(input,p.key,r.ctl.signal);if(!active(r))return;
   if([400,403,404,409,412].includes(result.status)){const rejected={...p,rejected:true as const};localStorage.setItem(storageKey,serializeAutomationCommand(rejected));remember(rejected);setConflict(true);setReady(false);setMessage('保存被拒绝或配置版本变化。原请求已保留，请查询回执并重新读取当前配置。');return;}
   if(result.status!==200||result.key!==p.key||!result.config||result.config.version!==p.version+1){throw Error();}
   else{clear(p);setMessage(options?.savedMessage??'配置已保存。保存不代表同步、下载或签到已经成功。');reload=true;}
  }catch{if(active(r))setMessage(current.current?'保存结果尚未确认，原请求已保留。请查询原回执，不会自动重试。':'无法持久保存恢复记录，尚未提交请求。');}
  finally{if(active(r)){gate.current=false;setBusy(false);}}
  if(reload)await refresh();
 };
 const resolveConflict=async()=>{
  const p=current.current;if(!p||!conflict||phase!=='absent'||!ready||gate.current)return;
  gate.current=true;setBusy(true);const r=begin();
  try{const receipt=await adapter.receipt(p.key,r.ctl.signal);if(!active(r))return;if(receipt.status!==404)throw Error();const now=await adapter.read(r.ctl.signal);if(!active(r))return;if(now.status!==200)throw Error();clear(p);setConfig(now.config);setReady(true);setMessage('已读取当前配置并结束版本冲突。未再次提交保存请求。');}
  catch{if(active(r)){setReady(false);setMessage('冲突核对失败，原请求保留。请重新读取并查询回执。');}}
  finally{if(active(r)){gate.current=false;setBusy(false);}}
 };
 return {config,ready,busy,message,pending,phase,storageError,conflict,refresh,recover,save,resolveConflict,repairStorage:hydrate,retry:()=>original.current?save(original.current,current.current?.sourceVersion):undefined,canRetry:original.current!==null,expectedVersion:pending?.version??config?.version??0,setMessage};
}

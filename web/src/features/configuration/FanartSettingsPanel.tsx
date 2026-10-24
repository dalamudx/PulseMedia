import {useEffect,useId,useMemo,useState} from 'react';
import {api} from '@/api/client';
import type {components} from '@/api/schema';
import {useAuth} from '@/features/auth/AuthContext';
import {useAutomationSettings,csrfHeaders} from './useAutomationSettings';
import {AutomationSettingsFeedback} from './AutomationSettingsFeedback';
import './automation-settings.css';
type Setting=components['schemas']['SettingItem'];
type Input=components['schemas']['UpdateSettingRequest'];
export function FanartSettingsPanel(){
 const {user,checking}=useAuth();
 if(checking||user?.role!=='admin')return null;
 return <section aria-label="Fanart艺术画报设置"><FanartSetting key={`${user.id}:${user.role}:key`} owner={user.id} setting="fanart.api_key"/><FanartSetting key={`${user.id}:${user.role}:artwork`} owner={user.id} setting="fanart.artwork"/></section>;
}
export function FanartSetting({owner,setting}:{owner:string;setting:'fanart.api_key'|'fanart.artwork'}){
 const id=useId(),secret=setting==='fanart.api_key';
 const adapter=useMemo(()=>({
  read:async(signal:AbortSignal)=>{const r=await api.GET('/api/v1/settings',{signal,cache:'no-store'});if(r.response.ok&&!r.data)throw Error();const config=r.data?.items.find(x=>x.key===setting)??null;return {status:r.response.status,config};},
  save:async(body:Input,key:string,signal:AbortSignal)=>{const r=await api.PUT('/api/v1/settings/{key}',{params:{path:{key:setting}},body,signal,headers:{...csrfHeaders(),'Idempotency-Key':key}});if(r.data?.key!==setting&&r.response.ok)throw Error();return {status:r.response.status,config:r.data??null,key};},
  receipt:async(key:string,signal:AbortSignal)=>{const r=await api.GET('/api/v1/settings/{key}/commands/{request_key}',{params:{path:{key:setting,request_key:key}},signal,cache:'no-store'});if(r.data?.key!==setting&&r.response.ok)throw Error();return {status:r.response.status,config:r.data??null,key};},
 }),[setting]);
 const model=useAutomationSettings<Setting,Input>(owner,setting,adapter,{requireReceiptBeforeRetry:true,savedMessage:secret?'Fanart密钥已加密保存。保存不会抓取图片。':'画报设置已保存，后续侧写任务按此设置执行；现有图片不会覆盖。'});
 const [key,setKey]=useState(''),[enabled,setEnabled]=useState(false);
 useEffect(()=>{setKey('');setEnabled(model.ready&&model.config?.value.enabled===true);},[model.ready,model.config]);
 const locked=model.busy||!model.ready||model.storageError||!!model.pending&&model.phase!=='absent';
 return <section className="card automation-settings" aria-label={secret?'Fanart密钥配置':'Fanart画报开关'} aria-busy={model.busy}>
 <div className="card-header"><h3>{secret?'Fanart访问密钥':'高清艺术画报'}</h3><button type="button" className="btn-secondary" disabled={model.busy} onClick={()=>void model.refresh()}>重新读取{secret?'密钥状态':'画报设置'}</button></div>
 <p className="card-desc">{secret?'密钥仅在当前输入期间保留，服务器加密保存，不会回显。':'启用ClearLogo、ClearArt和横幅；DiscArt仅支持电影。写入保留透明度，不覆盖已有图片。'}</p>
 <p role="status">{model.ready?model.config?`版本 ${model.config.version} · ${secret?(model.config.value.configured===true?'已配置密钥':'未配置密钥'):(model.config.value.enabled===true?'已启用':'已停用')}`:'尚未配置':'配置状态未知，不能保存'}</p>
 <AutomationSettingsFeedback model={model}/>
 <form autoComplete="off" onSubmit={e=>{e.preventDefault();void model.save({expected_version:model.expectedVersion,value:secret?{api_key:key.trim()}:{enabled}});}}><fieldset disabled={locked}><legend>{secret?'更换密钥':'自动侧写'}</legend>
 {secret?<div className="form-group"><label htmlFor={id}>Fanart API Key</label><input id={id} type="password" autoComplete="new-password" required value={key} onChange={e=>setKey(e.target.value)}/></div>:<label className="automation-check"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>为新侧写任务启用艺术画报</label>}
 <p className="config-description">保存不会立即下载、覆盖或删除图片，也不能证明外部服务已连接。</p><button type="submit" className="btn-primary">{secret?'保存Fanart密钥':'保存画报开关'}</button>
 </fieldset></form></section>;
}

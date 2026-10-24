import {useEffect,useId,useMemo,useState} from 'react';
import {api,type paths} from '@/api/client';
import {useAuth} from '@/features/auth/AuthContext';
import {configurationResult,csrfHeaders,useAutomationSettings} from './useAutomationSettings';
import {AutomationSettingsFeedback} from './AutomationSettingsFeedback';
import './automation-settings.css';
type Config=NonNullable<paths['/api/v1/indexers/{indexer_id}/checkin/config']['get']['responses']['200']['content']['application/json']['config']>;
type Input=paths['/api/v1/indexers/{indexer_id}/checkin/config']['put']['requestBody']['content']['application/json'];
type History=paths['/api/v1/indexers/{indexer_id}/checkin/history']['get']['responses']['200']['content']['application/json'];
export function SiteCheckinSettingsPanel({sourceID,sourceVersion}:{sourceID:string;sourceVersion:number}){
 const {user,checking}=useAuth();const [open,setOpen]=useState(false);const id=useId();
 useEffect(()=>{setOpen(false);},[user?.id,user?.role,checking,sourceID,sourceVersion]);
 if(checking||!user||user.role!=='admin')return null;
 return <section className="site-checkin-settings"><button type="button" className="btn-secondary" aria-expanded={open} aria-controls={`${id}-body`} onClick={()=>setOpen(x=>!x)}>定时签到设置与历史</button>{open&&<div id={`${id}-body`}><CheckinForm key={`${user.id}:${user.role}:${sourceID}:${sourceVersion}`} owner={user.id} sourceID={sourceID} sourceVersion={sourceVersion}/></div>}</section>;
}
function CheckinForm({owner,sourceID,sourceVersion}:{owner:string;sourceID:string;sourceVersion:number}){
 const id=useId();
 const adapter=useMemo(()=>({
  read:async(signal:AbortSignal)=>configurationResult<Config>(await api.GET('/api/v1/indexers/{indexer_id}/checkin/config',{params:{path:{indexer_id:sourceID}},signal,cache:'no-store'})),
  save:async(body:Input,key:string,signal:AbortSignal)=>configurationResult<Config>(await api.PUT('/api/v1/indexers/{indexer_id}/checkin/config',{params:{path:{indexer_id:sourceID},header:{'Idempotency-Key':key}},body,signal,headers:csrfHeaders()})),
  receipt:async(key:string,signal:AbortSignal)=>configurationResult<Config>(await api.GET('/api/v1/indexers/{indexer_id}/checkin/config/commands/{command_key}',{params:{path:{indexer_id:sourceID,command_key:key}},signal,cache:'no-store'})),
 }),[sourceID]);
 const model=useAutomationSettings(owner,`checkin:${sourceID}`,adapter);
 const [uid,setUID]=useState(''),[timezone,setTimezone]=useState(''),[daily,setDaily]=useState(''),[enabled,setEnabled]=useState(false);
 useEffect(()=>{if(!model.ready)return;const c=model.config;setUID(c?.expected_account_id??'');setTimezone(c?.timezone??'');setDaily(c?.daily_at??'');setEnabled(!!c?.enabled&&c.source_config_version===sourceVersion);},[model.ready,model.config,sourceVersion]);
 const locked=model.busy||!model.ready||model.storageError||!!model.pending&&model.phase!=='absent';
 const submit=()=>{const body:Input={expected_account_id:uid.trim(),profile:'nexusphp_1103_attendance_v1',timezone:timezone.trim(),daily_at:daily,enabled,expected_version:model.expectedVersion,expected_source_version:model.pending?.sourceVersion??sourceVersion};void model.save(body,body.expected_source_version);};
 return <div role="region" className="automation-settings" aria-label="站点定时签到配置" aria-busy={model.busy}>
  <div className="card-header"><h3>定时签到</h3><button type="button" className="btn-secondary" disabled={model.busy} onClick={()=>void model.refresh()}>重新读取签到配置</button></div>
  <p className="config-description">仅支持NexusPHP 1.10.3原生签到。请核对站点账号和站点使用的时区；不绕过验证码，不保证站点保号政策。</p>
  <p role="status">{model.ready?model.config?`已保存版本 ${model.config.version} · ${model.config.enabled?'已启用':'已停用'} · 来源版本 ${model.config.source_config_version}`:'尚未保存签到设置':'签到配置状态未知，不能保存'}</p>
  <p>当前站点配置版本：{sourceVersion}</p>
  {model.config&&model.config.source_config_version!==sourceVersion&&<p role="status">站点配置已变化。请核对当前账号与时区，再明确启用。</p>}
  <AutomationSettingsFeedback model={model}/>
  <form onSubmit={e=>{e.preventDefault();submit();}}><fieldset disabled={!!locked}><legend>账号与每日计划</legend><div className="form-grid">
   <div className="form-group"><label htmlFor={`${id}-uid`}>站点账号UID</label><input id={`${id}-uid`} required inputMode="numeric" pattern="[1-9][0-9]{0,19}" maxLength={20} autoComplete="off" value={uid} onChange={e=>setUID(e.target.value)}/></div>
   <div className="form-group"><label htmlFor={`${id}-timezone`}>站点时区</label><input id={`${id}-timezone`} required maxLength={96} placeholder="例如 Asia/Shanghai" autoComplete="off" value={timezone} onChange={e=>setTimezone(e.target.value)}/></div>
   <div className="form-group"><label htmlFor={`${id}-time`}>每日签到时间（站点时区）</label><input id={`${id}-time`} type="time" required step={60} value={daily} onChange={e=>setDaily(e.target.value)}/></div>
   <label className="automation-check"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>启用每日签到</label>
  </div><p className="config-description">保存不会立即访问站点或签到。结果不明时保留原许可，仅观察恢复；修改设置不能获得当天第二次发送机会。</p><button className="btn-primary" type="submit">{model.pending?'使用原键保存相同内容':'保存签到设置'}</button></fieldset></form>
  <CheckinHistory sourceID={sourceID}/>
 </div>;
}
function CheckinHistory({sourceID}:{sourceID:string}){
 const [page,setPage]=useState<History|null>(null),[cursor,setCursor]=useState<string|undefined>(),[tick,setTick]=useState(0),[loading,setLoading]=useState(true),[error,setError]=useState('');
 useEffect(()=>{const ctl=new AbortController();setPage(null);setLoading(true);setError('');void api.GET('/api/v1/indexers/{indexer_id}/checkin/history',{params:{path:{indexer_id:sourceID},query:{limit:10,...(cursor?{cursor}:{})}},signal:ctl.signal,cache:'no-store'}).then(r=>{if(ctl.signal.aborted)return;if(!r.response.ok||!r.data)throw Error();setPage(r.data);}).catch(()=>{if(!ctl.signal.aborted)setError('签到历史读取失败，旧结果已清除。');}).finally(()=>{if(!ctl.signal.aborted)setLoading(false);});return()=>ctl.abort();},[sourceID,cursor,tick]);
 const status={open:'等待处理',prepared:'发送结果待确认',attended_observed:'已观察到当天签到',review_required:'需要人工复核'};
 const failures={auth_required:'需要重新登录站点',blocked:'站点验证拦截',rate_limited:'站点限流',bad_response:'响应无法核验',unavailable:'站点暂不可用'};
 return <section aria-label="持久签到历史"><div className="card-header"><h4>签到历史</h4><button type="button" className="btn-secondary" disabled={loading} onClick={()=>{setCursor(undefined);setTick(x=>x+1);}}>刷新签到历史</button></div><p className="config-description">仅读取已保存的执行记录，不会访问站点。日期与时区沿用当次记录。</p>{loading&&<p role="status">正在读取历史…</p>}{error&&<p role="alert">{error}</p>}{page&&page.rows.length===0&&<p>暂无已记录的签到，不代表当天未签到。</p>}{page&&page.rows.length>0&&<div className="automation-history"><table><thead><tr><th>站点日期</th><th>时区</th><th>结果</th><th>来源版本</th></tr></thead><tbody>{page.rows.map(item=><tr key={item.id}><td>{item.calendar_day}</td><td>{item.timezone}</td><td>{status[item.state]}{item.safe_failure_code?`（${failures[item.safe_failure_code]}）`:''}</td><td>{item.source_version}</td></tr>)}</tbody></table></div>}{page?.next_cursor&&<button type="button" className="btn-secondary" disabled={loading} onClick={()=>setCursor(page.next_cursor??undefined)}>下一页签到历史</button>}</section>;
}

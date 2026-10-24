import {useEffect,useId,useMemo,useState} from 'react';
import {api,type paths} from '@/api/client';
import {useAuth} from '@/features/auth/AuthContext';
import {configurationResult,csrfHeaders,useAutomationSettings} from './useAutomationSettings';
import {AutomationSettingsFeedback} from './AutomationSettingsFeedback';
import './automation-settings.css';
type Config=NonNullable<paths['/api/v1/sync/douban/config']['get']['responses']['200']['content']['application/json']['config']>;
type Input=paths['/api/v1/sync/douban/config']['put']['requestBody']['content']['application/json'];
type Root=paths['/api/v1/library-roots']['get']['responses']['200']['content']['application/json']['items'][number];
type Site=paths['/api/v1/indexers']['get']['responses']['200']['content']['application/json']['items'][number];
type Client=paths['/api/v1/download-clients']['get']['responses']['200']['content']['application/json']['items'][number];
export function DoubanSyncSettingsPanel(){
 const {user,checking}=useAuth();
 if(checking||!user)return null;
 return <DoubanSettings key={`${user.id}:${user.role}`} owner={user.id}/>;
}
function DoubanSettings({owner}:{owner:string}){
 const id=useId();
 const adapter=useMemo(()=>({
  read:async(signal:AbortSignal)=>configurationResult<Config>(await api.GET('/api/v1/sync/douban/config',{signal,cache:'no-store'})),
  save:async(body:Input,key:string,signal:AbortSignal)=>configurationResult<Config>(await api.PUT('/api/v1/sync/douban/config',{params:{header:{'Idempotency-Key':key}},body,signal,headers:csrfHeaders()})),
  receipt:async(key:string,signal:AbortSignal)=>configurationResult<Config>(await api.GET('/api/v1/sync/douban/config/commands/{command_key}',{params:{path:{command_key:key}},signal,cache:'no-store'})),
 }),[]);
 const model=useAutomationSettings(owner,'douban',adapter);
 const [uid,setUID]=useState(''),[enabled,setEnabled]=useState(false),[movie,setMovie]=useState(''),[tv,setTV]=useState(''),[sites,setSites]=useState<string[]>([]),[client,setClient]=useState('');
 const [options,setOptions]=useState<{roots:Root[];sites:Site[];clients:Client[]}|null>(null),[optionsError,setOptionsError]=useState(''),[optionsTick,setOptionsTick]=useState(0);
 useEffect(()=>{
  const ctl=new AbortController();setOptions(null);setOptionsError('');
  void Promise.all([api.GET('/api/v1/library-roots',{signal:ctl.signal,cache:'no-store'}),api.GET('/api/v1/indexers',{signal:ctl.signal,cache:'no-store'}),api.GET('/api/v1/download-clients',{signal:ctl.signal,cache:'no-store'})]).then(([r,s,c])=>{
   if(ctl.signal.aborted)return;if(!r.response.ok||!r.data||!s.response.ok||!s.data||!c.response.ok||!c.data)throw Error();
   setOptions({roots:r.data.items,sites:s.data.items,clients:c.data.items});
  }).catch(()=>{if(!ctl.signal.aborted)setOptionsError('目标根、站点或下载器读取失败。请重新读取选项后再保存。');});
  return()=>ctl.abort();
 },[optionsTick]);
 useEffect(()=>{if(!model.ready)return;const c=model.config;setUID(c?.douban_uid??'');setEnabled(c?.enabled??false);setMovie(c?.movie_root_id??'');setTV(c?.tv_root_id??'');setSites(c?.bindings?.indexer_ids??[]);setClient(c?.bindings?.downloader_id??'');},[model.ready,model.config]);
 const locked=model.busy||!model.ready||!options||model.storageError||!!model.pending&&model.phase!=='absent';
 const rootOptions=(type:'movie'|'tv',selected:string)=>{
  const roots=options?.roots.filter(r=>r.media_type===type)??[];
  return <><option value="">不同步此类型</option>{selected&&!roots.some(r=>r.id===selected)&&<option value={selected}>已保存的目标（当前不可读取）</option>}{roots.map(r=><option key={r.id} value={r.id}>{r.name}{r.health!=='online'?'（离线）':!r.baseline_completed?'（基线待完成）':''}</option>)}</>;
 };
 const submit=()=>{
  if(enabled&&(!movie&&!tv)){model.setMessage('启用同步前，请至少选择一个电影或剧集目标根。');return;}
  if(sites.length&&!client){model.setMessage('选择站点后，请明确选择下载器。');return;}
  const body:Input={douban_uid:uid.trim(),enabled,expected_version:model.expectedVersion,...(movie?{movie_root_id:movie}:{}),...(tv?{tv_root_id:tv}:{}),bindings:{indexer_ids:[...sites].sort(),...(sites.length&&client?{downloader_id:client}:{})}};
  void model.save(body);
 };
 return <section className="card automation-settings" aria-label="豆瓣想看同步配置" aria-busy={model.busy}>
  <div className="card-header"><h3>豆瓣想看同步</h3><button type="button" className="btn-secondary" disabled={model.busy} onClick={()=>void model.refresh()}>重新读取配置</button></div>
  <p className="card-desc">读取公开的电影与电视剧想看列表。保存的是同步设置；后台核验作品身份后才会创建订阅并按所选站点、下载器补缺。</p>
  <p role="status">{model.ready?model.config?`已保存版本 ${model.config.version} · ${model.config.enabled?'已启用':'已停用'}`:'尚未保存同步设置':'配置状态未知，不能保存'}</p>
  <AutomationSettingsFeedback model={model}/>
  {optionsError&&<p role="alert">{optionsError}</p>}
  <button type="button" className="btn-secondary" disabled={model.busy} onClick={()=>setOptionsTick(x=>x+1)}>重新读取目标与端点</button>
  <form onSubmit={e=>{e.preventDefault();submit();}}><fieldset disabled={!!locked}><legend>同步范围与下载选择</legend>
   <div className="form-grid"><div className="form-group"><label htmlFor={`${id}-uid`}>豆瓣用户ID或主页唯一域名</label><input id={`${id}-uid`} required maxLength={64} pattern="[A-Za-z0-9][A-Za-z0-9_.-]*" autoComplete="off" value={uid} onChange={e=>setUID(e.target.value)}/></div>
   <label className="automation-check"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>启用想看同步</label>
   <div className="form-group"><label htmlFor={`${id}-movie`}>电影目标根</label><select id={`${id}-movie`} value={movie} onChange={e=>setMovie(e.target.value)}>{rootOptions('movie',movie)}</select></div>
   <div className="form-group"><label htmlFor={`${id}-tv`}>剧集目标根</label><select id={`${id}-tv`} value={tv} onChange={e=>setTV(e.target.value)}>{rootOptions('tv',tv)}</select></div></div>
   <fieldset><legend>用于自动补缺的站点</legend><div className="automation-sites">{options?.sites.length===0&&<p>尚无可选站点，可先保存同步范围。</p>}{options?.sites.map(s=><label className="automation-check" key={s.id}><input type="checkbox" checked={sites.includes(s.id)} onChange={e=>setSites(old=>e.target.checked?[...old,s.id]:old.filter(x=>x!==s.id))}/>{s.name}</label>)}{sites.filter(x=>!options?.sites.some(s=>s.id===x)).map(x=><label className="automation-check" key={x}><input type="checkbox" checked onChange={()=>setSites(old=>old.filter(v=>v!==x))}/>已保存但当前不可选的站点</label>)}</div></fieldset>
   <div className="form-group"><label htmlFor={`${id}-client`}>下载器</label><select id={`${id}-client`} value={client} required={sites.length>0} onChange={e=>setClient(e.target.value)}><option value="">不绑定下载器</option>{client&&!options?.clients.some(c=>c.id===client)&&<option value={client}>已保存的下载器（当前不可读取）</option>}{options?.clients.map(c=><option value={c.id} key={c.id}>{c.name}</option>)}</select></div>
   <p className="config-description">未选择站点时只同步订阅范围，不会自动选取下载端点。已有订阅的暂停状态和绑定不会被覆盖。</p>
   <button type="submit" className="btn-primary">{model.pending?'使用原键保存相同内容':'保存同步设置'}</button>
  </fieldset></form>
 </section>;
}

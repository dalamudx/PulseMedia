import {useEffect,useState} from 'react';
import {api,type paths} from '@/api/client';
import {useAuth} from '@/features/auth/AuthContext';
import {SiteCheckinSettingsPanel} from './SiteCheckinSettingsPanel';
import './automation-settings.css';
type Site=paths['/api/v1/indexers']['get']['responses']['200']['content']['application/json']['items'][number];
export function CheckinSettingsPanel(){
 const {user,checking}=useAuth();
 if(checking||!user||user.role!=='admin')return null;
 return <CheckinSources key={`${user.id}:${user.role}`}/>;
}
function CheckinSources(){
 const [sites,setSites]=useState<Site[]|null>(null),[error,setError]=useState(''),[tick,setTick]=useState(0);
 useEffect(()=>{const ctl=new AbortController();setSites(null);setError('');void api.GET('/api/v1/indexers',{signal:ctl.signal,cache:'no-store'}).then(r=>{if(ctl.signal.aborted)return;if(!r.response.ok||!r.data)throw Error();setSites(r.data.items);}).catch(()=>{if(!ctl.signal.aborted)setError('站点配置读取失败，请重新读取。旧站点设置已清除。');});return()=>ctl.abort();},[tick]);
 return <section className="card automation-settings" aria-label="站点定时签到设置"><div className="card-header"><h3>站点定时签到</h3><button type="button" className="btn-secondary" disabled={sites===null&&!error} onClick={()=>setTick(x=>x+1)}>重新读取签到站点</button></div><p className="card-desc">逐站点配置签到账号、站点时区和每日时间。仅支持NexusPHP 1.10.3原生签到；保存不会立即访问站点或签到。</p>{error&&<p role="alert">{error}</p>}{sites===null&&!error&&<p role="status">正在读取站点配置…</p>}{sites?.length===0&&<p>尚无可配置的站点，请先在站点页添加来源。</p>}{sites?.map(s=><div className="automation-source" key={s.id}><h4>{s.name} · 来源版本 {s.config_version}</h4><SiteCheckinSettingsPanel sourceID={s.id} sourceVersion={s.config_version}/></div>)}</section>;
}

import {useEffect,useId,useRef,useState} from 'react';
import {api} from '@/api/client';
import type {paths} from '@/api/client';
import type {components} from '@/api/schema';
import {useAuth} from '@/features/auth/AuthContext';
import './automation-settings.css';
import './advanced-settings.css';
type Page=components['schemas']['AuditTimelinePage'];
type Query=NonNullable<paths['/api/v1/system/audit']['get']['parameters']['query']>;
type Filters={actor_type:string;actor_id:string;action:string;target_type:string;target_id:string;from:string;until:string};
const empty:Filters={actor_type:'',actor_id:'',action:'',target_type:'',target_id:'',from:'',until:''};
export function AuditTimelinePanel(){const {user,checking}=useAuth();if(checking||user?.role!=='admin')return null;return <AuditTimeline key={`${user.id}:${user.role}`}/>;}
export function AuditTimeline(){
 const id=useId(),[draft,setDraft]=useState<Filters>(empty),[filters,setFilters]=useState<Query>({limit:50}),[page,setPage]=useState<Page|null>(null),[busy,setBusy]=useState(false),[error,setError]=useState('');
 const live=useRef(true),owner=useRef<AbortController|null>(null),serial=useRef(0);
 useEffect(()=>{live.current=true;return()=>{live.current=false;serial.current++;owner.current?.abort();};},[]);
 const read=async(query:Query)=>{owner.current?.abort();const ctl=new AbortController();owner.current=ctl;const ticket=++serial.current;setPage(null);setBusy(true);setError('');try{const r=await api.GET('/api/v1/system/audit',{params:{query},signal:ctl.signal,cache:'no-store'});if(ctl.signal.aborted||!live.current||ticket!==serial.current)return;if(!r.response.ok||!r.data)throw Error();setPage(r.data);}catch{if(!ctl.signal.aborted&&live.current&&ticket===serial.current)setError('审计读取失败，旧结果已清空。请重新读取；未执行任何业务操作。');}finally{if(live.current&&ticket===serial.current)setBusy(false);}};
 useEffect(()=>{void read(filters);},[filters]);
 const apply=()=>{const q:Query={limit:50};for(const key of ['actor_id','action','target_type','target_id'] as const){if(draft[key].trim())q[key]=draft[key].trim();}if(draft.actor_type)q.actor_type=draft.actor_type as Query['actor_type'];for(const k of ['from','until'] as const){if(draft[k]){const t=new Date(draft[k]);if(!Number.isFinite(t.getTime())){setError('请输入有效的起止时间。');return;}q[k]=t.toISOString();}}if(q.from&&q.until&&q.from>=q.until){setError('结束时间须晚于开始时间。');return;}setFilters(q);};
 return <section className="card automation-settings" aria-label="系统审计看板" aria-busy={busy}><div className="card-header"><h3>系统操作审计</h3><button type="button" className="btn-secondary" disabled={busy} onClick={()=>void read(filters)}>重新读取审计</button></div><p className="card-desc">查看已提交操作的执行人、动作和目标摘要。筛选精确匹配，时间使用本地输入；不显示历史敏感内容。列表读取不会新增审计。</p>
 <form onSubmit={e=>{e.preventDefault();apply();}}><fieldset disabled={busy}><legend>筛选审计记录</legend><div className="form-grid"><div className="form-group"><label htmlFor={`${id}-actor`}>执行人类型</label><select id={`${id}-actor`} value={draft.actor_type} onChange={e=>setDraft(x=>({...x,actor_type:e.target.value}))}><option value="">全部类型</option><option value="user">用户</option><option value="service">服务</option><option value="system">系统</option></select></div>{([['actor_id','执行人ID'],['action','动作'],['target_type','目标类型'],['target_id','目标ID'],['from','开始时间（含）'],['until','结束时间（不含）']] as const).map(([key,label])=><div className="form-group" key={key}><label htmlFor={`${id}-${key}`}>{label}</label><input id={`${id}-${key}`} type={key==='from'||key==='until'?'datetime-local':'text'} maxLength={256} value={draft[key]} onChange={e=>setDraft(x=>({...x,[key]:e.target.value}))}/></div>)}</div><button type="submit" className="btn-primary">应用筛选</button></fieldset></form>
 {error&&<p role="alert">{error}</p>}{busy&&<p role="status">正在读取审计记录…</p>}{page&&<><p role="status">本页{page.items.length}条 · 按记录时间倒序，不代表完整历史总数</p>{page.items.length===0?<p>没有符合条件的审计记录。</p>:<ol className="audit-entries">{page.items.map(row=><li key={row.id}><strong>{row.action}</strong><time dateTime={row.created_at}>{new Date(row.created_at).toLocaleString()}</time><dl><dt>执行人</dt><dd>{row.actor_type} · {row.actor_id}</dd><dt>目标</dt><dd>{row.target_type} · {row.target_id}</dd></dl></li>)}</ol>}<button type="button" className="btn-secondary" disabled={busy||!page.next_cursor} onClick={()=>{if(page.next_cursor)void read({...filters,cursor:page.next_cursor});}}>下一页审计</button></>}
 </section>;
}

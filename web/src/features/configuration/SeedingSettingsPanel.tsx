import {useEffect,useId,useMemo,useState} from "react";
import {api,type paths} from "@/api/client";
import type {components} from "@/api/schema";
import {useAuth} from "@/features/auth/AuthContext";
import {AutomationSettingsFeedback} from "./AutomationSettingsFeedback";
import {configurationResult,csrfHeaders,useAutomationSettings} from "./useAutomationSettings";
import {seedingRuleInput} from "./SeedingRuleInput";
import "./automation-settings.css";
type Client=paths["/api/v1/download-clients"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type Config=components["schemas"]["SeedingConfig"];
type Input=components["schemas"]["SeedingConfigInput"];
export function SeedingSettingsPanel(){const {user,checking}=useAuth();if(checking||user?.role!=="admin")return null;return <Downloaders key={`${user.id}:${user.role}`} owner={user.id}/>;}
function Downloaders({owner}:{owner:string}){
 const id=useId(),[clients,setClients]=useState<Client[]|null>(null),[selected,setSelected]=useState(""),[error,setError]=useState(false),[revision,setRevision]=useState(0);
 useEffect(()=>{const ctrl=new AbortController();setClients(null);setError(false);void api.GET("/api/v1/download-clients",{signal:ctrl.signal,cache:"no-store"}).then(({data,error})=>{if(ctrl.signal.aborted)return;if(error||!data){setError(true);return;}setClients(data.items);}).catch(()=>{if(!ctrl.signal.aborted)setError(true)});return()=>ctrl.abort();},[revision]);
 const client=clients?.find(row=>row.id===selected);
 return <section className="card automation-settings" aria-label="做种策略配置"><div className="card-header"><h3>做种策略</h3><button type="button" className="btn-secondary" onClick={()=>setRevision(n=>n+1)}>重新读取下载器</button></div><p className="card-desc">只移除已证明完成入库、无待处理需求的下载任务，始终保留下载文件与媒体库。历史缺证、自动剧集、整包和升级任务继续保留。</p>
 {error?<p role="alert">下载器读取失败，当前版本未知，不能保存策略。</p>:clients===null?<p role="status">正在读取下载器…</p>:<div className="form-group"><label htmlFor={id}>策略使用的下载器</label><select id={id} value={selected} onChange={e=>setSelected(e.target.value)}><option value="">请选择下载器</option>{clients.map(row=><option key={row.id} value={row.id}>{row.name} · {row.client_type}</option>)}</select></div>}
 {client&&<ClientSeeding key={`${owner}:${client.id}:${client.updated_at}`} owner={owner} client={client}/>}
 </section>;
}
function ClientSeeding({owner,client}:{owner:string;client:Client}){
 const id=useId();
 const adapter=useMemo(()=>({
 read:async(signal:AbortSignal)=>configurationResult<Config>(await api.GET("/api/v1/download-clients/{id}/seeding/config",{params:{path:{id:client.id}},signal,cache:"no-store"})),
 save:async(body:Input,key:string,signal:AbortSignal)=>configurationResult<Config>(await api.PUT("/api/v1/download-clients/{id}/seeding/config",{params:{path:{id:client.id},header:{"Idempotency-Key":key}},body,signal,headers:csrfHeaders()})),
 receipt:async(key:string,signal:AbortSignal)=>configurationResult<Config>(await api.GET("/api/v1/download-clients/{id}/seeding/config/commands/{command_key}",{params:{path:{id:client.id,command_key:key}},signal,cache:"no-store"})),
 }),[client.id]);
 const model=useAutomationSettings(owner,`seeding:${client.id}`,adapter,{requireReceiptBeforeRetry:true,savedMessage:"做种策略已保存。保存不执行移除，后台仍须验证入库证明、原生事实和执行许可。"});
 const [enabled,setEnabled]=useState(false),[interval,setInterval]=useState("300"),[mode,setMode]=useState("all"),[seconds,setSeconds]=useState(""),[ratio,setRatio]=useState(""),[bytes,setBytes]=useState("");
 useEffect(()=>{if(!model.ready)return;const c=model.config;setEnabled(c?.enabled??false);setInterval(String(c?.interval_seconds??300));setMode(c?.rule.mode??"all");setSeconds(c?.rule.min_seeding_seconds===undefined?"":String(c.rule.min_seeding_seconds));setRatio(c?.rule.min_share_ratio===undefined?"":String(c.rule.min_share_ratio));setBytes(c?.rule.low_free_bytes===undefined?"":String(c.rule.low_free_bytes));},[model.ready,model.config]);
 const locked=model.busy||!model.ready||model.storageError||!!model.pending&&model.phase!=="absent";
 const submit=()=>{const rule=seedingRuleInput(mode,seconds,ratio,bytes);if(!rule){model.setMessage("请至少填写一项有效正阈值；做种时长不超过315360000秒、分享率不超过1000000，空间使用精确整数字节。");return;}if(!/^[0-9]+$/.test(interval)||Number(interval)<60||Number(interval)>86400){model.setMessage("检查间隔须为60至86400秒的整数。");return;}void model.save({expected_version:model.expectedVersion,expected_client_updated_at:client.updated_at,enabled,interval_seconds:Number(interval),rule});};
 return <div aria-label={`下载器 ${client.name} 的做种规则`} role="region" aria-busy={model.busy}>
 <p role="status">{model.ready?model.config?`策略版本 ${model.config.version} · ${model.config.enabled?"已启用":"已停用"}`:"该下载器尚未保存做种规则":"策略状态未知，不能保存"}</p><p className="config-description">原生做种秒数与分享率不能用添加时间或累计字节替代；空间必须匹配任务下载路径。未知事实不会被当成零。</p>
 <button type="button" className="btn-secondary" disabled={model.busy} onClick={()=>void model.refresh()}>重新读取做种规则</button><AutomationSettingsFeedback model={model}/>
 <form onSubmit={e=>{e.preventDefault();submit();}}><fieldset disabled={!!locked}><legend>保留文件的移除条件</legend>
 <label className="automation-check"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>启用此下载器的自动移除策略</label>
 <div className="form-grid"><div className="form-group"><label htmlFor={`${id}-mode`}>阈值匹配方式</label><select id={`${id}-mode`} value={mode} onChange={e=>setMode(e.target.value)}><option value="all">满足所有已填条件</option><option value="any">满足任一已填条件</option></select></div><div className="form-group"><label htmlFor={`${id}-interval`}>检查间隔（秒）</label><input id={`${id}-interval`} type="number" required min={60} max={86400} step={1} value={interval} onChange={e=>setInterval(e.target.value)}/></div><div className="form-group"><label htmlFor={`${id}-seconds`}>最短原生做种时长（秒，可选）</label><input id={`${id}-seconds`} type="number" min={1} max={315360000} step={1} value={seconds} onChange={e=>setSeconds(e.target.value)}/></div><div className="form-group"><label htmlFor={`${id}-ratio`}>最低原生分享率（可选）</label><input id={`${id}-ratio`} type="number" min={0.000001} max={1000000} step="any" value={ratio} onChange={e=>setRatio(e.target.value)}/></div><div className="form-group"><label htmlFor={`${id}-bytes`}>低剩余空间阈值（字节，可选）</label><input id={`${id}-bytes`} type="number" min={1} max={Number.MAX_SAFE_INTEGER} step={1} value={bytes} onChange={e=>setBytes(e.target.value)}/></div></div>
 <p className="config-description">文件处理固定为保留全部文件；后台无法确认执行结果时仅观察或转人工，不自动重发移除。</p><button type="submit" className="btn-primary">{model.pending?"使用原键保存相同做种规则":"保存做种规则"}</button>
 </fieldset></form></div>;
}

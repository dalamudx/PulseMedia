import {useEffect,useId,useMemo,useState} from "react";
import {api} from "@/api/client";
import type {components} from "@/api/schema";
import {useAuth} from "@/features/auth/AuthContext";
import {AutomationSettingsFeedback} from "./AutomationSettingsFeedback";
import {configurationResult,csrfHeaders,useAutomationSettings} from "./useAutomationSettings";
import "./automation-settings.css";
type Config=components["schemas"]["CookieCloudConfig"];
type Input=components["schemas"]["CookieCloudConfigInput"];
export function CookieCloudSettingsPanel(){
 const {user,checking}=useAuth();if(checking||user?.role!=="admin")return null;
 return <CookieCloudSettings key={`${user.id}:${user.role}`} owner={user.id}/>;
}
function CookieCloudSettings({owner}:{owner:string}){
 const id=useId();
 const adapter=useMemo(()=>({
 read:async(signal:AbortSignal)=>configurationResult<Config>(await api.GET("/api/v1/integrations/cookiecloud/config",{signal,cache:"no-store"})),
 save:async(body:Input,key:string,signal:AbortSignal)=>configurationResult<Config>(await api.PUT("/api/v1/integrations/cookiecloud/config",{params:{header:{"Idempotency-Key":key}},body,signal,headers:csrfHeaders()})),
 receipt:async(key:string,signal:AbortSignal)=>configurationResult<Config>(await api.GET("/api/v1/integrations/cookiecloud/config/commands/{command_key}",{params:{path:{command_key:key}},signal,cache:"no-store"})),
 }),[]);
 const model=useAutomationSettings(owner,"cookiecloud",adapter,{requireReceiptBeforeRetry:true,savedMessage:"CookieCloud配置已保存。后台按计划读取并在本地解密，尚不能确认Cookie已同步。"});
 const [enabled,setEnabled]=useState(false),[interval,setInterval]=useState("300"),[replace,setReplace]=useState(true);
 const [url,setURL]=useState(""),[userKey,setUserKey]=useState(""),[secret,setSecret]=useState("");
 useEffect(()=>{if(!model.ready)return;setEnabled(model.config?.enabled??false);setInterval(String(model.config?.interval_seconds??300));setReplace(!model.config?.has_connection);setURL("");setUserKey("");setSecret("");},[model.ready,model.config]);
 const locked=model.busy||!model.ready||model.storageError||!!model.pending&&model.phase!=="absent";
 const submit=()=>{
  if(!/^[0-9]+$/.test(interval)||Number(interval)<60||Number(interval)>86400){model.setMessage("同步间隔须为60至86400秒的整数。");return;}
  if(replace&&(!url.trim()||!userKey.trim()||!secret)){model.setMessage("请完整填写服务地址、用户键和解密密码。");return;}
  const input:Input={expected_version:model.expectedVersion,enabled,interval_seconds:Number(interval),...(replace?{connection:{server_url:url.trim(),user_key:userKey.trim(),password:secret}}:{})};
  void model.save(input);
 };
 return <section className="card automation-settings" aria-label="CookieCloud同步配置" aria-busy={model.busy}>
 <div className="card-header"><h3>CookieCloud同步</h3><button type="button" className="btn-secondary" disabled={model.busy} onClick={()=>void model.refresh()}>重新读取CookieCloud配置</button></div>
 <p className="card-desc">从你的CookieCloud服务读取加密备份并在本地解密，仅更新已配置且域名匹配的站点Cookie。保存不会立即访问服务，也不代表同步已经成功。</p>
 <p role="status">{model.ready?model.config?`已保存版本 ${model.config.version} · ${model.config.enabled?"已启用":"已停用"} · ${model.config.server_origin}`:"尚未保存CookieCloud配置":"配置状态未知，不能保存"}</p>
 <AutomationSettingsFeedback model={model}/>
 <form onSubmit={e=>{e.preventDefault();submit();}} autoComplete="off"><fieldset disabled={!!locked}><legend>连接与后台同步</legend>
 <label className="automation-check"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>启用CookieCloud自动同步</label>
 <div className="form-group"><label htmlFor={`${id}-interval`}>同步间隔（秒）</label><input id={`${id}-interval`} type="number" min={60} max={86400} step={1} required value={interval} onChange={e=>setInterval(e.target.value)}/></div>
 {model.config?.has_connection&&<label className="automation-check"><input type="checkbox" checked={replace} onChange={e=>{setReplace(e.target.checked);setURL("");setUserKey("");setSecret("");}}/>更换已加密的连接凭据</label>}
 {replace?<div className="form-grid"><div className="form-group"><label htmlFor={`${id}-url`}>CookieCloud服务地址</label><input id={`${id}-url`} type="url" required value={url} onChange={e=>setURL(e.target.value)} autoComplete="off"/></div><div className="form-group"><label htmlFor={`${id}-key`}>CookieCloud用户键</label><input id={`${id}-key`} type="password" required value={userKey} onChange={e=>setUserKey(e.target.value)} autoComplete="new-password"/></div><div className="form-group"><label htmlFor={`${id}-secret`}>CookieCloud解密密码</label><input id={`${id}-secret`} type="password" required value={secret} onChange={e=>setSecret(e.target.value)} autoComplete="new-password"/></div></div>:<p className="config-description">保留服务器已保存的加密连接，可在服务离线时停用。已保存凭据不回显，浏览器恢复记录不保存连接明文。</p>}
 <button type="submit" className="btn-primary">{model.pending?"使用原键保存相同CookieCloud配置":"保存CookieCloud同步配置"}</button>
 </fieldset></form></section>;
}

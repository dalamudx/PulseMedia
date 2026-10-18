import { useEffect, useId, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import "./configuration.css";
type Info=paths["/api/v1/indexers/{id}/rss"]["get"]["responses"]["200"]["content"]["application/json"];
interface Props{id:string;name:string;onClose:()=>void}
function timestamp(value?:string){if(!value)return "暂无";const date=new Date(value);return Number.isNaN(date.valueOf())?"暂无":date.toLocaleString()}
export function RSSConfigEditor({id,name,onClose}:Props){
 const prefix=useId();const [info,setInfo]=useState<Info|null>(null);const [enabled,setEnabled]=useState(false);const [kind,setKind]=useState<"rss"|"torznab">("torznab");const [interval,setInterval]=useState("300");const [url,setURL]=useState("");const [busy,setBusy]=useState(false);const [loading,setLoading]=useState(true);const [revision,setRevision]=useState(0);const [error,setError]=useState("");const [conflict,setConflict]=useState(false);const [notice,setNotice]=useState("");
 const key=useRef("");const submitting=useRef(false);const errorRef=useRef<HTMLDivElement>(null);
 useEffect(()=>{const controller=new AbortController();setLoading(true);setError("");setInfo(null);setURL("");setConflict(false);setNotice("");key.current="";
  api.GET("/api/v1/indexers/{id}/rss",{params:{path:{id}},signal:controller.signal}).then(({data,error,response})=>{if(controller.signal.aborted)return;if(error||!data){setError(response.status===403?"RSS 配置仅限管理员访问。":"RSS 配置读取失败，请重新加载。");return};setInfo(data);setEnabled(data.enabled);setKind(data.kind);setInterval(String(data.interval_seconds))}).catch(()=>{if(!controller.signal.aborted)setError("连接失败，请重新加载 RSS 配置。")}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});
  return ()=>controller.abort();
 },[id,revision]);
 function change(){key.current="";setNotice("")}
 function fail(text:string,isConflict=false){setError(text);setConflict(isConflict);requestAnimationFrame(()=>errorRef.current?.focus())}
 async function save(event:React.FormEvent){event.preventDefault();if(!info||submitting.current||conflict)return;
  const seconds=Number(interval);if(!Number.isInteger(seconds)||seconds<30||seconds>86400){fail("轮询间隔须为 30–86400 的整数秒。");return}
  const feedURL=url.trim();if(enabled&&kind==="rss"&&!feedURL){fail("启用普通 RSS 时每次保存都须输入完整 Feed URL。服务器不会回显旧地址。");return}
  if(feedURL){try{const parsed=new URL(feedURL);if(!["http:","https:"].includes(parsed.protocol)||parsed.username||parsed.password||parsed.hash||feedURL.length>8192)throw Error()}catch{fail("Feed URL 须为有效的 HTTP(S) 地址，不可含用户信息或片段。");return}}
  submitting.current=true;setBusy(true);setError("");setNotice("");key.current ||= crypto.randomUUID();
  try{const {data,error,response}=await api.PUT("/api/v1/indexers/{id}/rss",{params:{path:{id},header:{"Idempotency-Key":key.current}},body:{enabled,kind,interval_seconds:seconds,expected_version:info.config_version,...(feedURL?{feed_url:feedURL}:{})}});
   if(error||!data){fail(response.status===409?"站点配置已改变。重新加载最新版本后再保存（将放弃当前草稿）。":response.status===403?"RSS 配置仅限管理员修改。":response.status===400?"配置无效，请检查轮询间隔和完整 Feed URL。":"保存未确认，请重试相同请求，或重新加载确认数据库状态。",response.status===409);return}
   setInfo(data);setURL("");key.current="";setNotice("RSS 配置已保存。只有完整轮询回执才会显示候选数；启用不代表已有可下载资源。");
  }catch{fail("连接中断，结果未确认。重试将复用同一命令；请勿另行重复创建请求。")}finally{submitting.current=false;setBusy(false)}
 }
 return <section className="card rss-config-panel" aria-label={`${name} RSS 配置`}>
  <div className="card-header"><h3>{name} · RSS 轮询</h3><button type="button" className="btn-sm btn-secondary" disabled={busy} onClick={onClose}>关闭配置</button></div>
  <div className="config-editor" aria-busy={loading||busy}>
   {error&&<div ref={errorRef} tabIndex={-1} role="alert" className="config-error">{error}</div>}
   {loading?<p role="status" className="config-description">正在加载 RSS 配置…</p>:info&&<form onSubmit={save}>
    <fieldset className="config-fieldset" disabled={busy||conflict}><legend>轮询设置</legend>
     <label className="config-check"><input type="checkbox" checked={enabled} onChange={event=>{change();setEnabled(event.target.checked)}}/>启用持久 RSS 轮询</label>
     <div className="form-grid"><div className="form-group"><label htmlFor={`${prefix}-kind`}>来源类型</label><select id={`${prefix}-kind`} value={kind} onChange={event=>{change();setKind(event.target.value as "rss"|"torznab")}}><option value="torznab">Torznab（使用索引器地址/API Key）</option><option value="rss">普通 RSS（完整 Feed URL）</option></select></div>
      <div className="form-group"><label htmlFor={`${prefix}-interval`}>轮询间隔（秒）</label><input id={`${prefix}-interval`} type="number" min={30} max={86400} step={1} required value={interval} onChange={event=>{change();setInterval(event.target.value)}}/></div></div>
     <div className="form-group"><label htmlFor={`${prefix}-url`}>{kind==="rss"?"完整 Feed URL":"Torznab 地址覆盖（可选）"}</label><input id={`${prefix}-url`} type="password" autoComplete="new-password" autoCapitalize="none" spellCheck={false} maxLength={8192} value={url} onChange={event=>{change();setURL(event.target.value)}} aria-describedby={`${prefix}-url-help`}/><p id={`${prefix}-url-help`} className="config-description">地址可能含 passkey，服务器加密保存且不会回显。普通 RSS 每次启用保存须重新输入；空地址保存会清除旧地址覆盖。</p></div>
    </fieldset>
    <p className="config-description">轮询与定向搜索共用端点预算，遵守限流退避。这里只保存来源设置；自动获取还需在订阅详情中绑定此索引器。</p>
    <div className="config-actions"><button type="button" className="btn-secondary" disabled={busy} onClick={()=>setRevision(n=>n+1)}>重新加载（放弃草稿）</button><button className="btn-primary" disabled={busy||conflict}>{busy?"保存中…":"保存 RSS 配置"}</button></div>
   </form>}
   {!loading&&!info&&<button type="button" className="btn-secondary" onClick={()=>setRevision(n=>n+1)}>重新加载 RSS 配置</button>}
   {notice&&<p role="status" className="config-description">{notice}</p>}
   {info&&<dl className="config-stats"><dt>数据库配置版本</dt><dd>{info.config_version}</dd><dt>RSS 状态</dt><dd>{info.enabled?"已启用（仍受在线状态/预算约束）":"未启用"}</dd><dt>计划轮询时间</dt><dd>{info.enabled?timestamp(info.next_poll_at):"—"}</dd><dt>最近完整回执</dt><dd>{timestamp(info.last_success_at)}</dd><dt>最近回执候选数</dt><dd>{info.last_candidate_count??"暂无完整回执"}</dd></dl>}
  </div>
 </section>
}

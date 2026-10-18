import { useEffect, useId, useState } from "react";
import { api, type paths } from "@/api/client";
import "./configuration.css";
type Indexer = paths["/api/v1/indexers"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type Downloader = paths["/api/v1/download-clients"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
interface Props { indexerIds: string[]; downloaderId: string; disabled?: boolean; onIndexers: (ids: string[]) => void; onDownloader: (id: string) => void; onReady?: (ready: boolean) => void }
export function EndpointBindingFields({indexerIds, downloaderId, disabled, onIndexers, onDownloader, onReady}: Props) {
 const id=useId(); const [indexers,setIndexers]=useState<Indexer[]>([]);const [downloaders,setDownloaders]=useState<Downloader[]>([]);
 const [loading,setLoading]=useState(true);const [error,setError]=useState("");const [revision,setRevision]=useState(0);
 useEffect(()=>{const controller=new AbortController();setLoading(true);setError("");onReady?.(false);
  Promise.all([api.GET("/api/v1/indexers",{signal:controller.signal}),api.GET("/api/v1/download-clients",{signal:controller.signal})]).then(([idx,dl])=>{
   if(controller.signal.aborted)return;
   if(idx.error||dl.error||!idx.data||!dl.data){setError("无法加载索引器或下载器，请重试。已有选择不会被清除。");return}
   setIndexers(idx.data.items);setDownloaders(dl.data.items);onReady?.(true);
  }).catch(()=>{if(!controller.signal.aborted)setError("连接失败，请重试加载配置。")}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});
  return ()=>controller.abort();
 },[revision,onReady]);
 const missing=indexerIds.filter(value=>!indexers.some(item=>item.id===value));
 return <div className="endpoint-binding-fields" aria-busy={loading}>
  {loading&&<p role="status" className="config-description">正在加载索引器与下载器…</p>}
  {error&&<div className="config-error" role="alert">{error} <button type="button" className="btn-sm btn-secondary" onClick={()=>setRevision(n=>n+1)}>重新加载选项</button></div>}
  <fieldset disabled={disabled||loading||!!error} className="config-fieldset"><legend>检索索引器</legend>
   <p id={`${id}-help`} className="config-description">仅使用所选端点。全部取消将关闭自动搜索；RSS 需在站点页另外启用。剧集和模糊匹配仍需人工处理。</p>
   {!loading&&!error&&indexers.length===0&&<p className="config-description">尚未配置索引器。管理员可在「站点」页添加。</p>}
   <div className="config-options" aria-describedby={`${id}-help`}>
    {indexers.map(item=><label key={item.id} className="config-check"><input type="checkbox" checked={indexerIds.includes(item.id)} onChange={event=>onIndexers(event.target.checked?[...indexerIds,item.id]:indexerIds.filter(value=>value!==item.id))}/><span>{item.name} · {item.health==="online"?"在线":"离线"}</span></label>)}
    {!loading&&!error&&missing.map(value=><label key={value} className="config-check"><input type="checkbox" checked onChange={()=>onIndexers(indexerIds.filter(item=>item!==value))}/><span>不可用的绑定 {value}（取消可移除）</span></label>)}
   </div>
   <div className="form-group"><label htmlFor={`${id}-downloader`}>下载器</label><select id={`${id}-downloader`} value={downloaderId} onChange={event=>onDownloader(event.target.value)}>
    <option value="">自动选择已配置下载器</option>
    {downloaderId&&!downloaders.some(item=>item.id===downloaderId)&&<option value={downloaderId}>不可用的下载器 {downloaderId}</option>}
    {downloaders.map(item=><option key={item.id} value={item.id}>{item.name} · {item.client_type}</option>)}
   </select></div>
  </fieldset>
 </div>;
}

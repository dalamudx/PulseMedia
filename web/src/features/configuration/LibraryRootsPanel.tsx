import {useEffect,useRef,useState} from "react";
import {api,type paths} from "@/api/client";
type Root=paths["/api/v1/library-roots"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
export function LibraryRootsPanel(){
 const [roots,setRoots]=useState<Root[]>([]),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const owner=useRef<AbortController|null>(null);
 async function load(){owner.current?.abort();const ctl=new AbortController();owner.current=ctl;setBusy(true);setRoots([]);setError("");try{const result=await api.GET("/api/v1/library-roots",{signal:ctl.signal,cache:"no-store"});if(ctl.signal.aborted)return;if(result.error||!result.data){setError("媒体根状态读取失败，旧状态已清除。请重新读取。");return}setRoots(result.data.items)}catch{if(!ctl.signal.aborted)setError("媒体根状态读取失败，旧状态已清除。请重新读取。")}finally{if(owner.current===ctl)owner.current=null;if(!ctl.signal.aborted)setBusy(false)}}
 useEffect(()=>{void load();return()=>owner.current?.abort()},[]);
 return <section className="config-editor" aria-label="媒体根与扫描基线"><h3>媒体根与扫描基线</h3><p className="config-description">根目录由部署配置和启动登记管理。在线且完成扫描基线后，获取仍须通过当前作品、来源与目标核验。</p><button type="button" className="btn-secondary" disabled={busy} onClick={()=>void load()}>重新读取媒体根</button>{busy&&<p role="status">正在读取媒体根…</p>}{error&&<p role="alert">{error}</p>}{!busy&&!error&&roots.length===0&&<p role="status">尚无已登记的媒体根。</p>}{roots.map(root=><article className="mapping-preview" key={root.id}><h4>{root.name} · {root.media_type==="movie"?"电影":"剧集"}</h4><p>根健康：{root.health==="online"?"在线":root.health==="offline"?"离线":"待确认"}</p><p>扫描基线：{root.baseline_completed?"已完成":"等待完整扫描"}</p><p className="config-description">{root.health!=="online"?"此根当前不可获取；恢复挂载和标记后等待健康探测。":!root.baseline_completed?"此根尚不能获取；扫描消费者完成完整基线后再重新读取。":"基线已建立；下载完成后仍需整理和入库，不能据此视为已拥有。"}</p></article>)}</section>
}

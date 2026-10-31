import {useEffect, useRef, useState} from "react";
import {api} from "@/api/client";
type Target={scope:string;version?:number;stamp?:string;rootID?:string;rootName?:string;rules:string[];expiresAt:number;message:string};
const empty=(scope:string):Target=>({scope,rules:[],expiresAt:0,message:""});
export function useClassificationTarget({enabled,userID,mediaType,tmdbID}:{enabled:boolean;userID:string;mediaType:"movie"|"tv";tmdbID:number}){
 const [revision,setRevision]=useState(0);const scope=JSON.stringify([userID,mediaType,tmdbID,enabled,revision]);const [target,setTarget]=useState<Target>(()=>empty(scope)),[busy,setBusy]=useState(false);const generation=useRef(0);
 useEffect(()=>{const ctl=new AbortController();const current=++generation.current;let expiry:ReturnType<typeof setTimeout>|undefined;setTarget(empty(scope));setBusy(enabled);
 if(enabled&&userID){void(async()=>{try{
 const [policy,roots]=await Promise.all([api.GET("/api/v1/classification",{signal:ctl.signal,cache:"no-store"}),api.GET("/api/v1/library-roots",{signal:ctl.signal,cache:"no-store"})]);if(ctl.signal.aborted||current!==generation.current)return;
 if(policy.error||!policy.data||roots.error||!roots.data)throw Error();if(policy.data.version<1){setTarget({...empty(scope),message:"尚无已发布分类，请先在设置中发布。"});return;}
 const version=policy.data.version;const preview=await api.POST("/api/v1/classification/previews",{signal:ctl.signal,body:{version,media_type:mediaType,tmdb_id:tmdbID}});if(ctl.signal.aborted||current!==generation.current)return;
 if(preview.error||!preview.data)throw Error();const observed=Date.parse(preview.data.observed_at);if(!Number.isFinite(observed)||observed>Date.now()+5000||Date.now()-observed>30000){setTarget({...empty(scope),message:"分类预览时间无法确认，请重新预览。"});return;}const result=preview.data.result;if(result.status!=="matched"||!result.root_id){const messages:Record<string,string>={insufficient_evidence:"缺少可信元数据，无法自动选择目标根。请手动选择目标根。",conflict:"分类命中多个目标根，请先调整分类规则。",no_match:"未命中已发布规则，请手动选择目标根。",target_unavailable:"分类目标根不可用，请核对媒体库。"};setTarget({...empty(scope),message:messages[result.status]??"分类结果尚未确认，请重新预览。"});return;}
 if(!preview.data.preview_stamp||!/^[0-9a-f]{64}$/.test(preview.data.preview_stamp))throw Error();
 const latest=await api.GET("/api/v1/classification",{signal:ctl.signal,cache:"no-store"});if(ctl.signal.aborted||current!==generation.current)return;if(latest.error||!latest.data||latest.data.version!==version||preview.data.version!==version){setTarget({...empty(scope),message:"已发布分类已变化，请重新预览。"});return;}
 const root=roots.data.items.find(r=>r.id===result.root_id&&r.media_type===mediaType&&r.health==="online");if(!root){setTarget({...empty(scope),message:"分类目标根不可用，请重新预览。"});return;}
 setTarget({scope,version,stamp:preview.data.preview_stamp,rootID:result.root_id,rootName:root.name,rules:result.matches.map(m=>m.rule_id),expiresAt:Math.min(Date.now()+30000,observed+30000),message:"分类预览已确认，仅用于这次新订阅。"});expiry=setTimeout(()=>setTarget(old=>old.scope===scope?{...empty(scope),message:"分类预览已过期，请重新预览。"}:old),Math.max(0,Math.min(30000,observed+30000-Date.now())));
 }catch{if(!ctl.signal.aborted&&current===generation.current)setTarget({...empty(scope),message:"分类状态读取失败，请重新预览。"});}finally{if(!ctl.signal.aborted&&current===generation.current)setBusy(false);}})();}else setBusy(false);
 return()=>{ctl.abort();generation.current++;if(expiry)clearTimeout(expiry);};},[enabled,scope,revision,userID,mediaType,tmdbID]);
 const current=target.scope===scope?target:empty(scope);const ready=enabled&&!busy&&current.version!==undefined&&current.expiresAt>Date.now();
 return {...current,busy,ready,refresh:()=>setRevision(v=>v+1),getSelection:()=>ready&&current.expiresAt>Date.now()?{version:current.version!,stamp:current.stamp!,rootID:current.rootID!}:undefined};
}

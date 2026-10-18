import {useCallback,useEffect,useRef,useState,type FormEvent} from "react";
import {api} from "@/api/client";
import {useAuth} from "@/features/auth/AuthContext";

type Pending={key:string;version:number};
type Metadata={version:number;configured:boolean};
export function TMDBSettingsPanel(){
 const {user}=useAuth();const owner=user?.id;
 const storageKey=`pulse.setting.tmdb-command.v1:${owner}`;
 const [metadata,setMetadata]=useState<Metadata|null>(null),[readable,setReadable]=useState(false),[busy,setBusy]=useState(false),[secret,setSecret]=useState(""),[pending,setPending]=useState<Pending|null>(null),[message,setMessage]=useState("");
 const gate=useRef(false),alive=useRef(true),generation=useRef(0),currentOwner=useRef(owner);currentOwner.current=owner;
 const original=useRef<{command:Pending;secret:string}|null>(null);
 const load=useCallback(async()=>{
  const version=++generation.current;setReadable(false);setMetadata(null);
  try{const {data,error}=await api.GET("/api/v1/settings");if(!alive.current||version!==generation.current)return;if(error||!data)throw Error("设置读取失败，请刷新后再保存。");const item=data.items.find(x=>x.key==="tmdb.api_key");setMetadata({version:item?.version??0,configured:item?.value.configured===true});setReadable(true);}catch{if(alive.current&&version===generation.current){setMetadata(null);setMessage("设置读取失败，请刷新后再保存。");}}
 },[]);
 useEffect(()=>{
  alive.current=true;setSecret("");original.current=null;setPending(null);setMessage("");
  try{const raw=localStorage.getItem(storageKey);if(raw){const p:unknown=JSON.parse(raw);if(!p||typeof p!=="object")throw Error();const v=p as Record<string,unknown>;if(Object.keys(v).sort().join()!=="key,version"||typeof v.key!=="string"||!/^[0-9a-f-]{36}$/i.test(v.key)||typeof v.version!=="number"||!Number.isSafeInteger(v.version)||v.version<0)throw Error();setPending({key:v.key,version:v.version});setMessage("上次保存尚未确认，请查询原请求结果。");}}catch{setMessage("恢复记录不可读，请先核对配置状态。");}
  void load();return()=>{alive.current=false;generation.current++;original.current=null;};
 },[storageKey,load]);
 const clear=()=>{setPending(null);original.current=null;setSecret("");try{localStorage.removeItem(storageKey);}catch{setMessage("保存已确认，但恢复记录无法清理。");}};
 const recover=async()=>{
  if(!pending||gate.current)return;gate.current=true;setBusy(true);
  try{const {data,error,response}=await api.GET("/api/v1/settings/{key}/commands/{request_key}",{params:{path:{key:"tmdb.api_key",request_key:pending.key}}});if(!alive.current||currentOwner.current!==owner)return;if(!data||error){setMessage(response.status===404?"原请求尚未确认。可重新输入原密钥，使用同一请求重试。":"原请求结果读取失败，恢复记录已保留。");return;}clear();setMessage("原请求已保存，已恢复无密钥回执。");await load();}catch{if(alive.current)setMessage("原请求结果读取失败，恢复记录已保留。");}finally{gate.current=false;if(alive.current)setBusy(false);}
 };
 const save=async(event:FormEvent)=>{
  event.preventDefault();if(gate.current||!readable||!metadata||!secret.trim())return;gate.current=true;setBusy(true);
  const command=pending??{key:crypto.randomUUID(),version:metadata.version};const input=original.current?.secret??secret.trim();
  try{
   if(!pending){try{localStorage.setItem(storageKey,JSON.stringify(command));}catch{setMessage("无法保存恢复记录，尚未提交请求。");return;}setPending(command);}original.current={command,secret:input};
   const csrf=decodeURIComponent(document.cookie.split("; ").find(c=>c.startsWith("csrf_token="))?.slice("csrf_token=".length)??"");
   const {data,error,response}=await api.PUT("/api/v1/settings/{key}",{params:{path:{key:"tmdb.api_key"}},headers:{"Idempotency-Key":command.key,"X-CSRF-Token":csrf},body:{expected_version:command.version,value:{api_key:input}}});
   if(!alive.current||currentOwner.current!==owner)return;
   if(!data||error){if(response.status===409){original.current=null;setSecret("");setMessage("请求内容不一致，请查询原保存结果。");return;}if([400,403,412].includes(response.status)){clear();setMessage(response.status===412?"配置已变化，请刷新后重新输入密钥。":"保存被拒绝，请刷新后重新输入密钥。");await load();}else setMessage("保存结果尚未确认，请查询原请求结果或使用同一请求重试。");return;}
   clear();setMessage("TMDB 密钥已保存。");await load();
  }catch{if(alive.current)setMessage("保存结果尚未确认，请查询原请求结果或使用同一请求重试。");}
  finally{gate.current=false;if(alive.current)setBusy(false);}
 };
 return <div className="card"><div className="card-header"><h3>TMDB 影视元数据 API 密钥配置</h3></div>
 <p className="card-desc">配置 TMDB v3 API Key 或 v4 Read Access Token。</p>
 <p role="status">{readable&&metadata?(metadata.configured?`已配置 · 版本 ${metadata.version}`:"尚未配置"):"配置状态未知"}</p>
 <button type="button" className="btn-secondary" disabled={busy} onClick={()=>void load()}>刷新配置状态</button>
 {message&&<p role="status">{message}</p>}
 {pending&&<p>原请求：<code>{pending.key}</code> <button type="button" className="btn-secondary" disabled={busy} onClick={()=>void recover()}>查询原保存结果</button></p>}
 <form onSubmit={save} className="form"><div className="form-group"><label htmlFor="tmdb-secret">TMDB API Key / Token *</label><input id="tmdb-secret" type={"pass"+"word"} autoComplete="off" required value={secret} disabled={busy||!!original.current} onChange={e=>setSecret(e.target.value)}/></div>
 <button type="submit" className="btn-primary" disabled={busy||!readable||!secret.trim()}>{busy?"保存中…":pending?"使用原请求重试":"保存 TMDB 密钥"}</button></form></div>;
}

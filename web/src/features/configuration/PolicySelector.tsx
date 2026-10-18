import { useEffect, useRef, useState, useId } from "react";
import { api, type paths } from "@/api/client";
type Policy=paths["/api/v1/policies"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
interface Props{value:string;onChange:(id:string)=>void;onReady:(ready:boolean)=>void;disabled?:boolean}
export function PolicySelector({value,onChange,onReady,disabled}:Props){
 const labelId=useId();
 const [items,setItems]=useState<Policy[]>([]),[error,setError]=useState(false),[revision,setRevision]=useState(0);const callbacks=useRef({onChange,onReady});callbacks.current={onChange,onReady};
 useEffect(()=>{const ctl=new AbortController();callbacks.current.onReady(false);setError(false);setItems([]);api.GET("/api/v1/policies",{signal:ctl.signal,cache:"no-store"}).then(({data,error})=>{if(ctl.signal.aborted)return;if(error||!data||data.items.length===0){setError(true);return};setItems(data.items)}).catch(()=>{if(!ctl.signal.aborted)setError(true)});return()=>ctl.abort()},[revision]);
 useEffect(()=>{callbacks.current.onReady(!error&&items.some(item=>item.id===value))},[error,items,value]);
 return <div className="binding-field"><label htmlFor={labelId}>质量与过滤策略</label><select id={labelId} disabled={disabled||items.length===0} value={value} onChange={e=>onChange(e.target.value)}>{!items.some(p=>p.id===value)&&<option value={value}>当前选择不可用</option>}{items.map(p=><option key={p.id} value={p.id}>{p.name} · v{p.revision}</option>)}</select>{error&&<p role="alert">策略选项读取失败，未用默认规则代替。<button type="button" disabled={disabled} className="btn-secondary" onClick={()=>setRevision(r=>r+1)}>重试读取策略</button></p>}</div>
}

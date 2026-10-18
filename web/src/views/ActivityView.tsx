import React, { useEffect,useRef,useState } from "react";
import {useAuth} from "@/features/auth/AuthContext";
import {CompleteTVOrganizationPanel} from "@/features/configuration/CompleteTVOrganizationPanel";
import {TransferRecoveryPanel} from "@/features/configuration/TransferRecoveryPanel";
import {TransferRevisionPanel} from "@/features/configuration/TransferRevisionPanel";
import {useActivityInvalidation} from "@/features/configuration/useActivityInvalidation";
import {OperationInbox} from "@/features/configuration/OperationInbox";
import { api } from "@/api/client";
import type { paths } from "@/api/client";
import { useToast } from "@/features/toast/ToastContext";
import { SubmissionRetryPanel } from "@/features/configuration/SubmissionRetryPanel";
import { RequirementPanel } from "@/features/configuration/RequirementPanel";
type OperationDetails = paths["/api/v1/operations/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
export const ActivityView: React.FC = () => { const {user}=useAuth(); return user?<ActivityContent key={`${user.id}:${user.role}`}/>:null; };
const ActivityContent: React.FC = () => {
 const toast=useToast();const {version,connected}=useActivityInvalidation();const [operationId,setOperationId]=useState(""),[selectedOperationId,setSelectedOperationId]=useState(""),[operation,setOperation]=useState<OperationDetails|null>(null),[loading,setLoading]=useState(false),[operationError,setOperationError]=useState("");const owner=useRef<AbortController|null>(null),epoch=useRef(0);useEffect(()=>()=>owner.current?.abort(),[]);
 const query=async(id:string,background=false)=>{if(!id)return;setSelectedOperationId(id);owner.current?.abort();const controller=new AbortController(),generation=++epoch.current;owner.current=controller;setLoading(true);if(!background)setOperation(null);setOperationError("");try{const {data,error,response}=await api.GET("/api/v1/operations/{id}",{signal:controller.signal,cache:"no-store",params:{path:{id}}});if(controller.signal.aborted||generation!==epoch.current)return;if(error){setOperationError("当前状态读取失败，请重新查询后再处理。显示的详情来自上次读取。");if(response.status===403||response.status===404)setOperation(null);toast.error("无法查询操作",error.message)}else setOperation(data??null)}catch{if(!controller.signal.aborted&&generation===epoch.current){setOperationError("连接中断，详情尚未刷新。请重新查询。");toast.error("网络异常","请检查连接后重试查询。")}}finally{if(owner.current===controller)owner.current=null;if(generation===epoch.current)setLoading(false)}};
 useEffect(()=>{if(operationId&&!owner.current)void query(operationId.trim(),true);},[version]);
 const lookUp=(event:React.FormEvent)=>{event.preventDefault();void query(operationId.trim())};
 return <div className="activity-container"><div className="page-header"><div><h2>活动</h2><p className="subtitle">查看进行中的获取与待处理操作，进入详情后处理自己的需求、整理恢复或目标修订。</p></div></div><p role="status">{connected?"活动更新已连接，状态以重新查询为准。":"活动更新未连接，可刷新列表或查询操作。"}</p><OperationInbox refreshVersion={version} onSelect={id=>{setOperationId(id);setOperation(null);void query(id)}}/>
 <form onSubmit={lookUp} className="media-control-bar"><label htmlFor="operation-id">操作 ID</label><input id="operation-id" type="text" className="media-filter-input" value={operationId} onChange={event=>{owner.current?.abort();owner.current=null;epoch.current++;setLoading(false);setOperation(null);setSelectedOperationId("");setOperationId(event.target.value)}} placeholder="也可直接输入操作 ID" autoComplete="off"/><button type="submit" className="btn-primary" disabled={loading||!operationId.trim()}>{loading?"查询中…":"查询 / 刷新"}</button></form>
 {operation&&<section className="card op-card" aria-label="操作详情"><h3>操作 {operation.operation_id}</h3>{operationError&&<p role="alert">{operationError}</p>}<dl><dt>当前状态</dt><dd>{operation.status}</dd>{operation.download_status&&<><dt>下载状态</dt><dd>{operation.download_status}</dd></>}{typeof operation.progress==="number"&&<><dt>下载进度</dt><dd>{operation.progress===1?100:Math.min(99,Math.floor(operation.progress*100))}%</dd></>}{operation.plan_status&&<><dt>整理计划状态</dt><dd>{operation.plan_status}</dd></>}{operation.facts?.length?<><dt>已确认事实</dt><dd>{operation.facts.join("、")}</dd></>:null}</dl><fieldset disabled={loading||Boolean(operationError)} style={{border:0,padding:0,margin:0}}><RequirementPanel key={operation.operation_id} operationId={operation.operation_id}/><SubmissionRetryPanel key={`retry:${operation.operation_id}`} operationId={operation.operation_id}/>{operation.plan_id&&<><TransferRecoveryPanel key={operation.plan_id} planId={operation.plan_id}/><TransferRevisionPanel key={`revision:${operation.plan_id}`} planId={operation.plan_id} operationId={operation.operation_id}/></>}</fieldset></section>}
 {selectedOperationId&&<CompleteTVOrganizationPanel operationId={selectedOperationId} contextReady={Boolean(operation&&operation.operation_id===selectedOperationId&&!loading&&!operationError)} refreshVersion={version}/>}
 </div>
};

import { useEffect, useMemo, useState, useRef } from "react";
import { api, type paths } from "@/api/client";
type Subscription = paths["/api/v1/subscriptions"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type Report=paths["/api/v1/subscriptions/{id}/status"]["get"]["responses"]["200"]["content"]["application/json"];
export function useSubscriptionStatus(subscription:Subscription,onUpdated:(value:Subscription)=>void){
 const refreshOwner=useRef<AbortController|null>(null);
 const wantedStamp=JSON.stringify((subscription.wanted_items??[]).map(w=>[w.id,w.unit_id,w.metadata_version,w.retired,w.user_skipped,w.scope_excluded,w.is_skipped,w.is_satisfied,w.current_fact_stage,w.wait_reason]).sort((a,b)=>String(a[0]).localeCompare(String(b[0]))));
 const [observedWantedStamp,setObservedWantedStamp]=useState<string|null>(null);
 const [report,setReport]=useState<Report|null>(null);const [error,setError]=useState("");const [loading,setLoading]=useState(true);const [revision,setRevision]=useState(0);
 useEffect(()=>{const controller=new AbortController();setReport(null);setError("");setLoading(true);
  api.GET("/api/v1/subscriptions/{id}/status",{params:{path:{id:subscription.id}},signal:controller.signal,cache:"no-store"}).then(({data,error})=>{if(controller.signal.aborted)return;if(error||!data){setError("读取失败，未使用旧投影宣称入库。请刷新状态。");return}setObservedWantedStamp(wantedStamp);setReport(data)}).catch(()=>{if(!controller.signal.aborted)setError("连接失败，当前状态未确认。请刷新状态。")}).finally(()=>{if(!controller.signal.aborted)setLoading(false)});
  return()=>{controller.abort();refreshOwner.current?.abort();refreshOwner.current=null};
 },[subscription.id,subscription.intent_version,subscription.quality_policy_revision,wantedStamp,revision]);
 const current=observedWantedStamp===wantedStamp&&report?.intent_version===subscription.intent_version&&report.policy_revision===subscription.quality_policy_revision?report:null;
 const observed=useMemo(()=>{
  const base=new Map((subscription.wanted_items??[]).map(item=>[item.id,item]));
  const items=current?current.items.map(fact=>({...base.get(fact.id),...fact})): (subscription.wanted_items??[]).map(item=>({...item,status:"unknown" as const,is_satisfied:false,current_fact_stage:undefined,wait_reason:loading?"status_loading":"status_unavailable",reservation_protected:false}));
  return {...subscription,wanted_items:items,total_episodes:items.filter(item=>!item.is_skipped).length,satisfied_episodes:items.filter(item=>!item.is_skipped&&item.is_satisfied).length};
 },[subscription,current,loading]);
 async function refresh(){
  if(refreshOwner.current)return;const controller=new AbortController();refreshOwner.current=controller;
  setReport(null);setLoading(true);setError("");
  try {const {data,error}=await api.GET("/api/v1/subscriptions/{id}",{params:{path:{id:subscription.id}},signal:controller.signal,cache:"no-store"});if(controller.signal.aborted)return;if(error||!data){setError("订阅读取失败，当前状态未确认。请重试刷新。");setLoading(false);return}onUpdated(data);setRevision(value=>value+1)}catch{if(!controller.signal.aborted){setError("连接失败，当前状态未确认。请重试刷新。");setLoading(false)}}finally{if(refreshOwner.current===controller)refreshOwner.current=null}
 }
 function invalidate(){setReport(null);setLoading(true);setError("");setRevision(value=>value+1);}
 return {report:current,error:error||(!loading&&report&&!current?"订阅版本已改变，请刷新状态以读取最新订阅。":""),loading,subscription:observed,refresh,invalidate};
}

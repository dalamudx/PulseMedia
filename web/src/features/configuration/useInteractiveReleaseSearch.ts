import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { isUUID, parseRecovery, recoveryKey, searchCommand, searchRunning, searchStateLabel, snapshotMessage, type Recovery, type SearchInput, type SearchSnapshot } from "./interactiveReleaseSearchRecovery";

type Phase = "idle" | "creating" | "recovering" | "observing" | "ready" | "unknown" | "missing" | "auth" | "stale" | "blocked";
type Options = { scope: string; subscriptionId?: string; mediaId?: string };
export function useInteractiveReleaseSearch({scope,subscriptionId,mediaId}: Options) {
  const {user} = useAuth();
  const userId = user?.id ?? "";
  const identity = `${userId}:${scope}:${subscriptionId ?? ""}:${mediaId ?? ""}`;
  const live = useRef(identity); live.current = identity;
  const epoch = useRef(0), request = useRef<AbortController | null>(null), timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const record = useRef<Recovery | null>(null), input = useRef<SearchInput | null>(null), confirmed = useRef(false), writing = useRef(false);
  const mounted = useRef(false), authDenied = useRef(false);
  const [view,setView] = useState<{identity:string;phase:Phase;snapshot:SearchSnapshot|null;recovery:Recovery|null}>({identity,phase:"idle",snapshot:null,recovery:null});
  const current = view.identity === identity && !!userId;
  const phase: Phase = current ? view.phase : "idle";
  const snapshot = current ? view.snapshot : null;
  const recovery = current ? view.recovery : null;
  function stop() { ++epoch.current; request.current?.abort(); request.current=null; if(timer.current)clearTimeout(timer.current); timer.current=null; }
  function valid(id:string,g:number) { return mounted.current && live.current===id && epoch.current===g; }
  function update(next:Partial<typeof view>) { setView(v=>({...v,identity,...next})); }
  async function observe(searchId:string) {
    if (!userId || writing.current || authDenied.current) return;
    stop(); const g=epoch.current,id=identity,c=new AbortController(); request.current=c;
    try {
      const r=await api.GET("/api/v1/release-searches/{id}",{params:{path:{id:searchId}},signal:c.signal,cache:"no-store"});
      if(!valid(id,g))return;
      if(r.response.status===401||r.response.status===403){authDenied.current=true;update({phase:"auth",snapshot:null});return;}
      if(r.error||!r.data||r.data.search.id!==searchId)throw Error("snapshot unavailable");
      update({snapshot:r.data,phase:searchRunning(r.data.state)?"observing":"ready"});
      if(searchRunning(r.data.state))timer.current=setTimeout(()=>void observe(searchId),1000);
    } catch { if(valid(id,g))update({phase:"stale"}); }
    finally { if(request.current===c)request.current=null; }
  }
  async function command(r:Recovery,fresh=false,retryMissing=false) {
    if(!userId||r.userId!==userId||writing.current||authDenied.current)return false;
    let storage: Storage;
    try{storage=sessionStorage;}catch{update({phase:"blocked",snapshot:null});return false;}
    stop();const g=epoch.current,id=identity,c=new AbortController();request.current=c;writing.current=true;
    record.current=r;confirmed.current=false;update({phase:fresh&&r.action==="create"?"creating":"recovering",recovery:r});
    const body=input.current;
    const outcome=await searchCommand({record:r,storage,fresh,retryMissing,
      lookup:async()=>{const x=await api.GET("/api/v1/release-search-commands/{key}",{params:{path:{key:r.key},query:{action:r.action}},signal:c.signal,cache:"no-store"});return {status:x.response.status,data:x.data};},
      send:async()=>{
        if(r.action==="cancel"&&r.searchId){const x=await api.POST("/api/v1/release-searches/{id}/cancel",{params:{path:{id:r.searchId},header:{"Idempotency-Key":r.key}},signal:c.signal});return {status:x.response.status,data:x.data};}
        if(!body)throw Error("original input unavailable");
        const x=await api.POST("/api/v1/release-searches",{params:{header:{"Idempotency-Key":r.key}},body,signal:c.signal});return {status:x.response.status,data:x.data};
      }});
    // Receipt capture/persistence above precedes every owner/abort check.
    if(!valid(id,g))return false;
    writing.current=false;if(request.current===c)request.current=null;
    record.current=outcome.record;
    if(outcome.kind==="confirmed") {
      confirmed.current=true;update({recovery:outcome.record,phase:outcome.persisted?"ready":"blocked"});
      if(outcome.persisted)void observe(outcome.receipt.id);
      return true;
    }
    if(outcome.kind==="auth")authDenied.current=true;
    update({phase:outcome.kind==="not_sent"?"blocked":outcome.kind,...(outcome.kind==="auth"?{snapshot:null}:{})});
    return false;
  }
  async function recover(retryMissing=false) {
    if(writing.current||!userId||authDenied.current)return false;
    try {
      const r=record.current ?? parseRecovery(sessionStorage.getItem(recoveryKey(userId,scope)),userId,scope);
      if(!r){update({phase:"idle"});return false;}
      if(r.subscriptionId!==subscriptionId||r.mediaId!==mediaId)throw Error("scope mismatch");
      return await command(r,false,retryMissing && (r.action==="cancel"||!!input.current));
    }catch{update({phase:"blocked",snapshot:null});return false;}
  }
  async function cancel() {
    const r=record.current;
    if(!r||!userId||r.userId!==userId||writing.current)return false;
    if(r.action==="cancel")return recover();
    if(!confirmed.current||!r.searchId){await recover();return false;}
    return command({...r,action:"cancel",key:crypto.randomUUID()},true);
  }
  async function start(query:string) {
    if(!userId||!query.trim()||writing.current||phase==="blocked"||phase==="auth")return false;
    if(record.current){
      if(!confirmed.current){await recover();return false;}
      if(record.current.action!=="cancel" && !(await cancel()))return false;
    }
    stop();input.current={query:query.trim(),...(isUUID(subscriptionId)?{subscription_id:subscriptionId}:{}),...(isUUID(mediaId)?{media_id:mediaId}:{})};
    const r:Recovery={version:1,userId,scope,key:crypto.randomUUID(),action:"create",...(subscriptionId?{subscriptionId}:{}),...(mediaId?{mediaId}:{})};
    update({snapshot:null});
    return command(r,true);
  }
  function refresh() { if(confirmed.current&&record.current?.searchId)void observe(record.current.searchId);else void recover(); }
  function pause() {stop();writing.current=false;update({phase:snapshot?"stale":"unknown"});}
  useEffect(()=>{
    mounted.current=true;stop();writing.current=false;record.current=null;input.current=null;confirmed.current=false;authDenied.current=false;
    setView({identity,phase:"idle",snapshot:null,recovery:null});
    if(userId)void recover();
    return()=>{mounted.current=false;stop();writing.current=false;};
  },[identity]);
  const results=snapshot?.results ?? [];
  const busy=phase==="creating"||phase==="recovering";
  const canAcquire=!!snapshot&&recovery?.action==="create"&&["observing","ready"].includes(phase)&&snapshot.state!=="cancelled";
  return {start,cancel,refresh,pause,retryRecovery:()=>recover(),retryOriginal:()=>recover(true),results,snapshot,phase,recovery,
    busy,loading:busy||phase==="observing",canAcquire,message:snapshotMessage(snapshot,phase),
    canRetryOriginal:phase==="missing"&&(recovery?.action==="cancel"||!!input.current),
    sourceSummary:snapshot?.sources.map(s=>`${s.name}：${searchStateLabel(s.state)}${s.partial?"（部分资源）":""}`).join("；") ?? "",
    policySummary:snapshot?`按品质、做种数及资源编号排序 · 品质策略 ${snapshot.search.ranking_policy_id} 版本 ${snapshot.search.ranking_policy_revision} · 做种数为最近收到并保存的观察，可能已变化${snapshot.truncated?` · 显示 ${results.length} / ${snapshot.total_candidates} 项`:""}`:"",
  };
}

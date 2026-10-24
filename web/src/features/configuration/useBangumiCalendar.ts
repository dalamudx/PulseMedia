import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import type { MediaItem } from "@/components/cards/MediaCard";
import { bangumiYear, type BangumiCalendar, type BangumiItem } from "./bangumiCalendar";
import { discoveryMediaItem } from "./discoveryCatalog";

export function useBangumiCalendar(){
 const [calendar,setCalendar]=useState<BangumiCalendar|null>(null);
 const [loading,setLoading]=useState(true),[error,setError]=useState("");
 const [selected,setSelected]=useState<BangumiItem|null>(null);
 const [candidates,setCandidates]=useState<MediaItem[]>([]);
 const [matching,setMatching]=useState(false),[matchError,setMatchError]=useState("");
 const active=useRef(false),read=useRef<AbortController|null>(null),lookup=useRef<AbortController|null>(null);
 const clearMatch=()=>{lookup.current?.abort();lookup.current=null;setSelected(null);setCandidates([]);setMatching(false);setMatchError("");};
 const refresh=async()=>{
  if(!active.current)return;clearMatch();read.current?.abort();const ctrl=new AbortController();read.current=ctrl;
  const owns=()=>active.current&&read.current===ctrl&&!ctrl.signal.aborted;
  setCalendar(null);setLoading(true);setError("");
  try{const {data,error}=await api.GET("/api/v1/metadata/bangumi/calendar",{signal:ctrl.signal});
   if(!owns())return;
   if(error||!data||data.execution_allowed!==false||!Array.isArray(data.days)){setError("周历读取失败，请检查服务状态后重试。");return;}
   setCalendar(data);
  }catch{if(owns())setError("连接失败，未取得周历数据。");}
  finally{if(owns()){read.current=null;setLoading(false);}}
 };
 const match=async(item:BangumiItem)=>{
  if(!active.current||!calendar||loading||error)return;
  lookup.current?.abort();const ctrl=new AbortController();lookup.current=ctrl;const owns=()=>active.current&&lookup.current===ctrl&&!ctrl.signal.aborted;
  setSelected(item);setCandidates([]);setMatching(true);setMatchError("");
  const query=item.original_title.trim()||item.title;const year=bangumiYear(item);
  try{const {data,error}=await api.GET("/api/v1/search/movies",{params:{query:{query,type:"tv",...(year===undefined?{}:{year})}},signal:ctrl.signal});
   if(!owns())return;
   if(error||!data||!Array.isArray(data.results)){setMatchError("TMDB候选读取失败，未提交订阅。");return;}
   const seen=new Set<number>();setCandidates(data.results.filter(row=>{
    if(row.media_type!=="tv"||!Number.isSafeInteger(row.tmdb_id)||row.tmdb_id<=0||seen.has(row.tmdb_id))return false;seen.add(row.tmdb_id);return true;
   }).map(discoveryMediaItem));
  }catch{if(owns())setMatchError("连接失败，未取得TMDB候选，未提交订阅。");}
  finally{if(owns()){lookup.current=null;setMatching(false);}}
 };
 useEffect(()=>{active.current=true;void refresh();return()=>{active.current=false;read.current?.abort();lookup.current?.abort();};},[]);
 return {calendar,loading,error,refresh,selected,candidates,matching,matchError,match,clearMatch};
}

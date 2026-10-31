import { useEffect, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import { useActivityInvalidation } from "./useActivityInvalidation";

type Snapshot = paths["/api/v1/release-searches/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
export type MovieDiscoveryStatus = {
  subscription_id: string;
  search_id?: string;
  current_file_id?: string;
  next_discovery_at?: string;
  state: string;
  execution_allowed: boolean;
};

export function useMovieDiscovery(subscriptionId: string) {
  const activity = useActivityInvalidation();
  const [refresh, setRefresh] = useState(0);
  const [view, setView] = useState<{status: MovieDiscoveryStatus | null; snapshot: Snapshot | null; loading: boolean; error: string}>({status:null,snapshot:null,loading:true,error:""});
  const current = useRef(subscriptionId); current.current = subscriptionId;
  useEffect(() => {
    const c = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const started = Date.now();
    setView({status:null,snapshot:null,loading:true,error:""});
    async function read() {
      try {
        const r = await api.GET("/api/v1/subscriptions/{id}/movie-discovery", {params:{path:{id:subscriptionId}},signal:c.signal,cache:"no-store"});
        if(c.signal.aborted || current.current !== subscriptionId) return;
        if(r.error || !r.data || r.data.subscription_id !== subscriptionId || r.data.execution_allowed !== false) throw Error("discovery");
        const status: MovieDiscoveryStatus = r.data;
        let snapshot: Snapshot | null = null;
        if(status.search_id && status.current_file_id) {
          const s = await api.GET("/api/v1/release-searches/{id}", {params:{path:{id:status.search_id}},signal:c.signal,cache:"no-store"});
          if(c.signal.aborted || current.current !== subscriptionId) return;
          if(s.error || !s.data || s.data.search.id !== status.search_id || s.data.search.subscription_id !== subscriptionId) throw Error("snapshot");
          snapshot = s.data;
        }
        setView({status,snapshot,loading:false,error:""});
        if(status.state === "running" && Date.now()-started < 35_000) timer=setTimeout(()=>void read(),2000);
      } catch {
        if(!c.signal.aborted) setView({status:null,snapshot:null,loading:false,error:"候选发现状态暂不可用，或当前订阅条件已改变。请刷新后重试。"});
      }
    }
    void read();
    return () => {c.abort();if(timer)clearTimeout(timer);};
  },[subscriptionId,activity.version,refresh]);
  return {...view, refresh:()=>setRefresh(n=>n+1)};
}

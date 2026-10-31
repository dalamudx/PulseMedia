import { useEffect, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { useMovieDiscovery } from "./useMovieDiscovery";

type Snapshot = paths["/api/v1/release-searches/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
type Candidate = Snapshot["results"][number];
type Preview = paths["/api/v1/subscriptions/{id}/upgrade-previews"]["post"]["responses"]["200"]["content"]["application/json"];
export type MovieDiscoverySelection = {candidate: Candidate; currentFileId: string};
type Props = {subscriptionId:string; intentVersion:number; policyRevision:number; onSelect:(selection:MovieDiscoverySelection)=>void};
const labels: Record<string,string> = {not_scheduled:"等待首次检查",waiting:"等待下一次检查",running:"正在检索候选",succeeded:"本轮检索已完成",partial:"本轮仅有部分检索结果",failed:"本轮检索失败",expired:"本轮检索窗口已结束",stale:"订阅或文件已改变，等待下次检查",cancelled:"本轮检索已停止"};

export function MovieDiscoveryPanel(props: Props) {
  const {user} = useAuth();
  if(!user || user.role !== "admin") return null;
  return <Discovery key={`${user.id}:${props.subscriptionId}:${props.intentVersion}:${props.policyRevision}`} {...props} userId={user.id}/>;
}

function Discovery({subscriptionId,intentVersion,policyRevision,onSelect,userId}:Props & {userId:string}) {
  const {status,snapshot,loading,error,refresh} = useMovieDiscovery(subscriptionId);
  const [preview,setPreview] = useState<Preview|null>(null), [checking,setChecking]=useState(false), [comparisonError,setComparisonError]=useState("");
  const request=useRef<AbortController|null>(null);
  useEffect(()=>{request.current?.abort();setPreview(null);setChecking(false);setComparisonError("");},[snapshot,status]);
  useEffect(()=>()=>request.current?.abort(),[]);
  async function compare(candidate:Candidate) {
    if(!status?.current_file_id || checking) return;
    request.current?.abort();const c=new AbortController();request.current=c;
    const fileId=status.current_file_id;
    setChecking(true);setPreview(null);setComparisonError("");
    try {
      const r=await api.POST("/api/v1/subscriptions/{id}/upgrade-previews",{params:{path:{id:subscriptionId}},body:{current_file_id:fileId,candidate_id:candidate.id},signal:c.signal,cache:"no-store"});
      if(c.signal.aborted)return;
      if(r.error || !r.data || r.data.current_file_id !== fileId || r.data.candidate_id !== candidate.id || r.data.version.subscription_id !== subscriptionId || r.data.version.actor_id !== userId || r.data.version.intent_version !== intentVersion || r.data.version.policy_revision !== policyRevision || r.data.execution_allowed !== false) throw Error("preview");
      setPreview(r.data);
    } catch {if(!c.signal.aborted)setComparisonError("比较失败或当前证据已改变，请刷新后重新比较。");}
    finally {if(!c.signal.aborted)setChecking(false);}
  }
  const candidates=snapshot?.results.slice(0,25) ?? [];
  const qualified=preview?.comparable && preview.improvement;
  return <section className="config-editor" aria-label="持续电影候选发现">
    <div className="config-heading"><h4>持续候选发现</h4><button type="button" className="btn-secondary" style={{ minWidth: 44, minHeight: 44 }} onClick={refresh} disabled={loading || checking}>刷新发现结果</button></div>
    <p className="config-description">升级模式启用且现有电影符合条件时，每六小时检查一次。达到阈值后停止模式仅在当前品质低于阈值时检查；持续改善模式会继续检查。</p>
    <p role="status">{loading ? "正在读取发现状态…" : labels[status?.state ?? ""] ?? "发现状态未知"}{status?.next_discovery_at ? `；下次检查：${new Date(status.next_discovery_at).toLocaleString()}` : ""}</p>
    {error ? <p role="alert">{error}</p> : null}
    {snapshot ? <>
      <p className="config-description">以下为检索候选，尚未认定优于现有文件。选择比较后才会显示品质结论；预留、准备和发送仍需分别确认。</p>
      {snapshot.partial || snapshot.truncated ? <p>检索结果不完整，未列出不代表不存在。</p> : null}
      {candidates.length === 0 ? <p>本轮尚无已保存的候选。</p> : <ul>{candidates.map(candidate=><li key={candidate.id}>
        <span style={{overflowWrap:"anywhere"}}>{candidate.title}</span>{" "}
        <button type="button" className="btn-secondary" style={{ minWidth: 44, minHeight: 44 }} disabled={checking || !status?.current_file_id} onClick={()=>void compare(candidate)}>比较此候选品质</button>
        {preview?.candidate_id === candidate.id ? <div>
          <p>{qualified ? "当前证据表明此候选可改善现有文件品质。" : "当前证据未证明此候选可用于改善品质。"}</p>
          {qualified && status?.current_file_id ? <button type="button" className="btn-secondary" style={{ minWidth: 44, minHeight: 44 }} onClick={()=>onSelect({candidate,currentFileId:status.current_file_id!})}>选择并进入升级确认</button> : null}
        </div> : null}
      </li>)}</ul>}
      {(snapshot.total_candidates ?? 0)>25 ? <p>显示前25条检索候选；可在现有站点检索中进一步查找。</p> : null}
    </> : null}
    {checking ? <p role="status">正在比较当前文件与候选…</p> : null}
    {comparisonError ? <p role="alert">{comparisonError}</p> : null}
  </section>;
}

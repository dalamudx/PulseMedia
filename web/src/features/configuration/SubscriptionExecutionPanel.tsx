import {useEffect, useRef, useState} from "react";
import {api, type paths} from "@/api/client";
import {useAuth} from "@/features/auth/AuthContext";
type Report=paths["/api/v1/subscriptions/{id}/execution"]["get"]["responses"]["200"]["content"]["application/json"];
const labels:Record<string,string>={source_results:"等待来源返回",finalization:"等待轮次收尾",no_round:"尚无执行轮次",round_finished:"最近轮次已结束",prerequisites:"等待前置条件",subscription_inactive:"订阅未启用",wait_for_next_evaluation:"等待下一次自动评估",wait_for_persisted_results:"等待来源任务写回，可稍后刷新",wait_for_worker:"等待后台任务完成",check_wanted_status:"查看订阅缺集状态和阻塞原因",check_subscription_state:"核对订阅当前控制状态",subscription_blocked:"已在该订阅屏蔽",no_persisted_rejection:"未选中；未保存单独拒绝原因",awaiting_decision:"等待评估",selected:"已选中",pending:"待评估",rejected:"已拒绝",not_selected:"未选中",queued:"排队中",running:"执行中",closed:"已结束",completed:"已完成",succeeded:"成功",failed:"失败",deferred:"延后",timeout:"超时",returned:"已返回",partial:"部分结果",aborted:"已中止",expired:"已过期",no_qualified_candidate:"无合格候选",review_required:"需要复核"};
const label=(v:string)=>labels[v]??"状态待核对";
export function SubscriptionExecutionPanel({subscriptionID}:{subscriptionID:string}){
 const {user}=useAuth();const [report,setReport]=useState<Report|null>(null),[error,setError]=useState(""),[revision,setRevision]=useState(0),[busy,setBusy]=useState(false);const generation=useRef(0);
 useEffect(()=>{const ctl=new AbortController();const current=++generation.current;setReport(null);setError("");setBusy(true);
 void api.GET("/api/v1/subscriptions/{id}/execution",{params:{path:{id:subscriptionID}},signal:ctl.signal,cache:"no-store"}).then(({data,error})=>{if(ctl.signal.aborted||current!==generation.current)return;if(error||!data){setError("执行详情读取失败，请重新读取。");return;}setReport(data);}).catch(()=>{if(!ctl.signal.aborted)setError("执行详情读取失败，请重新读取。");}).finally(()=>{if(!ctl.signal.aborted)setBusy(false);});return()=>{ctl.abort();generation.current++;};
 },[subscriptionID,user?.id,user?.role,revision]);
 return <section className="card" aria-label="自动订阅执行详情"><div className="card-header"><h3>自动执行详情</h3><button type="button" className="btn-secondary" disabled={busy} onClick={()=>setRevision(v=>v+1)}>{busy?"读取中…":"刷新执行详情"}</button></div>
 {error?<p role="alert">{error}</p>:null}
 {report?<><p role="status">{label(report.waiting)}。{label(report.next_step)}</p><p className="card-desc">快照时间：<time dateTime={report.observed_at}>{new Date(report.observed_at).toLocaleString()}</time></p>
 {report.rounds.length===0?<p>还没有持久执行记录。</p>:report.rounds.map(round=><details key={round.id}><summary>{round.kind==="tv"?"剧集":"电影"}轮次 · {label(round.state)} · {new Date(round.created_at).toLocaleString()}</summary><p>意图版本 {round.intent_version} · 截止 {new Date(round.deadline).toLocaleString()}{round.reason?` · ${label(round.reason)}`:""}</p>
 {round.sources.length===0?<p>尚无来源任务。</p>:round.sources.map((source,index)=><div key={source.indexer_id}><h4>来源 {index+1} · {label(source.state)}</h4>{source.pages.length===0?<p>尚无查询页回执。</p>:<ul>{source.pages.map(page=><li key={page.query_id}>第 {page.page+1} 页 · {label(page.state)} · {page.results} 条 · {page.receipt_present?(page.offset_verified?"分页已验证":"分页完整性未知"):"尚无分页证明"}</li>)}</ul>}</div>)}
 <h4>候选结果</h4>{round.candidates.length===0?<p>尚无持久候选。</p>:<ul>{round.candidates.map((candidate,index)=><li key={candidate.id}>候选 {index+1} · {label(candidate.outcome)} · {label(candidate.reason)}</li>)}</ul>}{round.limited?<p>仅显示最近 100 个候选。</p>:null}</details>)}{report.limited?<p>仅显示最近 20 个轮次。</p>:null}</>:null}</section>;
}

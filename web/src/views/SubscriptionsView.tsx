import React, { useState, useEffect, useMemo, useRef } from "react";
import { useNavigate } from "react-router-dom";
import {
  Plus,
  Pause,
  Play,
  RefreshCw,
  Film,
  Tv,
  Search,
} from "lucide-react";
import { api } from "@/api/client";
import { useToast } from "@/features/toast/ToastContext";
import { SubscriptionDetailModal } from "@/components/dialog/SubscriptionDetailModal";
import type { paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";

type PendingStateControl = {id:string;state:"active"|"paused"|"archived";version:number;key:string};
function readStateControl(key:string|null):PendingStateControl|null {
  if (!key) return null;
  try { const p=JSON.parse(sessionStorage.getItem(key)||"null");return p&&typeof p.id==="string"&&typeof p.key==="string"&&Number.isSafeInteger(p.version)&&p.version>0&&["active","paused","archived"].includes(p.state)?p:null } catch { return null }
}


type SubDetail = paths["/api/v1/subscriptions"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];

export const SubscriptionsView: React.FC = () => {
  const router = useNavigate();
  const toast = useToast();

  const [subscriptions, setSubscriptions] = useState<SubDetail[]>([]);
  const [loading, setLoading] = useState(true);
  const [operatingId, setOperatingId] = useState<string | null>(null);
const [controlError,setControlError]=useState("");const [controlBlocked,setControlBlocked]=useState(false);
const {user}=useAuth();
const controlStorageKey=user?`pulsemedia.subscription-state.${user.id}`:null;
const [pendingControl,updatePendingControl]=useState<PendingStateControl|null>(()=>readStateControl(controlStorageKey));
const setPendingControl=(command:PendingStateControl|null)=>{updatePendingControl(command);if(controlStorageKey)try{if(command)sessionStorage.setItem(controlStorageKey,JSON.stringify(command));else sessionStorage.removeItem(controlStorageKey)}catch{}};
useEffect(()=>{updatePendingControl(readStateControl(controlStorageKey));setControlBlocked(false);setControlError("")},[controlStorageKey]);
const controlOwner=useRef<AbortController|null>(null);
useEffect(()=>()=>controlOwner.current?.abort(),[]);
  const [activeTab, setActiveTab] = useState<"all" | "movie" | "tv">("all");
  const [searchFilter, setSearchFilter] = useState("");

  // Selected subscription for detail modal
  const [selectedSub, setSelectedSub] = useState<SubDetail | null>(null);

  const movieCount = useMemo(
    () => subscriptions.filter((s) => s.media_type !== "tv" && (!s.wanted_items || s.wanted_items.length <= 1)).length,
    [subscriptions]
  );
  const tvCount = useMemo(
    () =>
      subscriptions.filter(
        (s) => s.media_type === "tv" || (s.media_type !== "movie" && s.wanted_items && s.wanted_items.length > 1)
      ).length,
    [subscriptions]
  );

  const filteredSubscriptions = useMemo(() => {
    let list = subscriptions;
    if (activeTab === "movie") {
      list = list.filter((s) => s.media_type !== "tv" && (!s.wanted_items || s.wanted_items.length <= 1));
    } else if (activeTab === "tv") {
      list = list.filter((s) => s.media_type === "tv" || (s.wanted_items && s.wanted_items.length > 1));
    }

    if (searchFilter.trim()) {
      const q = searchFilter.trim().toLowerCase();
      list = list.filter(
        (s) =>
          (s.title && s.title.toLowerCase().includes(q)) ||
          s.id.toLowerCase().includes(q) ||
          (s.root_path && s.root_path.toLowerCase().includes(q))
      );
    }
    return list;
  }, [activeTab, subscriptions, searchFilter]);


  const loadSubscriptions = async () => {
    setLoading(true);
    try {
      const { data, error } = await api.GET("/api/v1/subscriptions");
      if (error) {
        setSubscriptions([]);setSelectedSub(null);setControlError("订阅列表读取失败，未沿用旧计数与控制状态。请重试读取。");
        return false;
      }
      const items=data?.items||[];setSubscriptions(items);setSelectedSub(prev=>prev?items.find(item=>item.id===prev.id)?{...prev,...items.find(item=>item.id===prev.id)!}:null:null);return true;
    } catch {setSubscriptions([]);setSelectedSub(null);setControlError("订阅连接失败，未沿用旧状态。请重试读取。");return false;} finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadSubscriptions();
  }, []);

  const applyState = async (id:string,state:"active"|"paused"|"archived") => {
if(controlOwner.current||controlBlocked)return;const current=selectedSub?.id===id?selectedSub:subscriptions.find(s=>s.id===id);if(!current)return;
const command=pendingControl??{id,state,version:current.intent_version,key:crypto.randomUUID()};if(command.id!==id||command.state!==state)return;
const ctl=new AbortController();controlOwner.current=ctl;setPendingControl(command);setControlError("");setOperatingId(id);let confirmed=false;
try{const{data,error,response}=await api.PUT("/api/v1/subscriptions/{id}/control",{signal:ctl.signal,params:{path:{id},header:{"Idempotency-Key":command.key}},body:{action:"state",state:command.state,expected_version:command.version}});if(ctl.signal.aborted)return;
if(error||!data){if(response.status===409||response.status===403||response.status===404){setPendingControl(null);setControlBlocked(true);setControlError("订阅版本或对象权限已变化。请重新读取后确认，不会自动覆盖新意图。")}else setControlError("操作结果未确认。重试会使用同一命令；重新读取可检查现状。");return}
confirmed=true;setPendingControl(null);setControlBlocked(true);setSubscriptions(prev=>prev.map(s=>s.id===id?{...s,state:data.state,intent_version:data.intent_version}:s));if(selectedSub?.id===id)setSelectedSub(prev=>prev?{...prev,state:data.state,intent_version:data.intent_version}:null);
const latest=await api.GET("/api/v1/subscriptions/{id}",{signal:ctl.signal,cache:"no-store",params:{path:{id}}});if(ctl.signal.aborted)return;if(latest.error||!latest.data){setControlError("控制操作已确认，但最新状态读取失败。只需重新读取，不要重复写入。");return}
setSubscriptions(prev=>prev.map(s=>s.id===id?{...s,...latest.data}:s));if(selectedSub?.id===id)setSelectedSub(prev=>prev?{...prev,...latest.data}:null);setControlBlocked(false);
toast.success(state==="paused"?"订阅已暂停":state==="archived"?"订阅已归档":"订阅已恢复",state==="archived"?"历史目标与共享获取保留，已发送动作不会被撤回。":"当前意图已保存，已有共享下载与物理占位保持保护。");
}catch{if(!ctl.signal.aborted)setControlError(confirmed?"控制已确认，刷新连接中断。请重新读取。":"连接中断，操作未确认。重试会复用同一命令。")}finally{if(controlOwner.current===ctl)controlOwner.current=null;if(!ctl.signal.aborted)setOperatingId(null)}
};
const pauseSub=(id:string)=>applyState(id,"paused");
const resumeSub=(id:string)=>applyState(id,"active");
const deleteSub=(sub:SubDetail)=>{if(confirm(`归档《${sub.title||"该影片"}》？历史目标和已经发送的获取会保留。`))return applyState(sub.id,"archived")};

  const recheckSub = async (sub: SubDetail) => {
    toast.info("正在检索候选资源", `正在检查《${sub.title || "影视"}》的站点搜索结果；仅在可靠匹配与门禁核验通过后尝试创建任务。`);
    try {
      const { data, error } = await api.POST("/api/v1/subscriptions/{id}/search", {
        params: { path: { id: sub.id } },
      });
      if (error) {
        toast.error("检索失败", error.message || "请求异常");
        return;
      }
      if (data?.matched && data.operation_id) {
        toast.success("已创建下载任务", data.message || "请到活动页确认下载器是否已接收。");
        await loadSubscriptions();
      } else if (data?.matched) {
        toast.info("已找到候选，未创建任务", data.message || "请检查目标单元、媒体库和占位状态。");
      } else {
        toast.info("未找到已核验的匹配", data?.message || "当前没有可安全获取的资源；未提交下载。");
      }
    } catch {
      toast.error("网络异常", "重新检索失败");
    }
  };


  const controlFeedback=(controlError||pendingControl)?<section className="config-editor" aria-label="订阅控制恢复">{controlError&&<p role="alert">{controlError}</p>}{pendingControl&&!controlBlocked&&<button type="button" className="btn-secondary" disabled={operatingId!==null} onClick={()=>applyState(pendingControl.id,pendingControl.state)}>重试同一控制命令</button>}<button type="button" className="btn-secondary" disabled={operatingId!==null||loading} onClick={async()=>{if(await loadSubscriptions()){setPendingControl(null);setControlBlocked(false);setControlError("")}}}>重新读取订阅控制状态</button></section>:null;

  return (
    <div className="subscriptions-container">
      {/* Top Header Bar */}
      <div className="page-header">
        <div>
          <h2>影视订阅与追更管理</h2>
          <p className="subtitle">管理订阅意图与季集进度。自动补缺仅支持通过订阅 API 绑定索引器和下载器的单电影；剧集及模糊匹配仍需人工处理。</p>
        </div>
        <div className="header-actions">
          <button className="btn-primary" onClick={() => router("/discover")}>
            <Plus size={16} />
            <span>添加新订阅</span>
          </button>
        </div>
      </div>

      {!selectedSub&&controlFeedback}
      <p className="config-description">列表计数是可重建投影，可能尚未重算。打开详情可读取当前数据库事实；计数不授权下载或证明文件当前可用。</p>
      {/* Tabs & Search Controls */}
      <div className="media-control-bar">
        <div className="tabs-bar" style={{ marginBottom: 0, borderBottom: "none", paddingBottom: 0 }}>
          <button
            className={`tab-btn ${activeTab === "all" ? "active" : ""}`}
            onClick={() => setActiveTab("all")}
          >
            全部订阅 ({subscriptions.length})
          </button>
          <button
            className={`tab-btn ${activeTab === "movie" ? "active" : ""}`}
            onClick={() => setActiveTab("movie")}
          >
            <Film size={14} />
            <span>电影订阅 ({movieCount})</span>
          </button>
          <button
            className={`tab-btn ${activeTab === "tv" ? "active" : ""}`}
            onClick={() => setActiveTab("tv")}
          >
            <Tv size={14} />
            <span>电视剧追更 ({tvCount})</span>
          </button>
        </div>

        {(
          <div className="media-search-wrap">
            <Search size={14} className="search-icon-inside" />
            <input
              type="text"
              placeholder="搜索已订阅影片..."
              value={searchFilter}
              onChange={(e) => setSearchFilter(e.target.value)}
              className="media-filter-input"
            />
          </div>
        )}
      </div>

      {/* Main View Container */}
      {loading ? (
        <div className="loading-state">
          <div className="spinner"></div>
          <p>正在加载订阅列表...</p>
        </div>
      ) : filteredSubscriptions.length === 0 ? (
        /* Empty State */
        <div className="empty-state">
          <div className="empty-icon">📺</div>
          <h3>暂无对应类型的订阅记录</h3>
          <p>前往「发现」页面搜索电影或电视剧，创建订阅后可在此查看真实状态。</p>
          <button className="btn-primary" onClick={() => router("/discover")}>
            去发现影视
          </button>
        </div>
      ) : (
        /* Poster Cards Grid (探索同款海报网格！) */
        <div className="movie-grid">
          {filteredSubscriptions.map((sub) => {
            const isTV = sub.media_type === "tv" || (sub.wanted_items && sub.wanted_items.length > 1);
            const activeWantedItems = sub.wanted_items?.filter((w) => !w.is_skipped) || [];
            const hasSkipped = (sub.wanted_items?.length || 0) > activeWantedItems.length && activeWantedItems.length > 0;
            const totalEps = activeWantedItems.length;
            const satisfiedEps = activeWantedItems.filter((w) => w.is_satisfied).length;
            const pct = totalEps > 0 ? Math.round((satisfiedEps / totalEps) * 100) : 0;
            const poster = sub.poster_url || undefined;

            return (
              <div key={sub.id} className="media-card-wrap sub-poster-card" onClick={() => setSelectedSub(sub)}>
                <div className="poster-container">
                  {/* Real Poster Image with Graceful SVG Placeholder Fallback */}
                  {poster ? (
                    <img
                      src={poster}
                      alt={sub.title || "影视"}
                      className="poster-image"
                      loading="lazy"
                      onError={(e) => {
                        (e.currentTarget as HTMLImageElement).style.display = "none";
                      }}
                    />
                  ) : null}
                  <div className="poster-empty-box">
                    {isTV ? <Tv size={36} /> : <Film size={36} />}
                    <span className="empty-title">{sub.title || "影视作品"}</span>
                  </div>

                  {/* Top Badges */}
                  <span className={`badge-type ${isTV ? "tv" : "movie"}`}>
                    {isTV ? "电视剧" : "电影"}
                  </span>

                  <span className={`badge-sub-state ${sub.state}`}>
                    {sub.state === "active" ? "订阅有效" : "已暂停"}
                  </span>

                  {/* Hover Overlay with Quick Actions (MoviePilot Style) */}
                  <div className="hover-overlay">
                    <div className="hover-content">
                      <span className="hover-year">{sub.year ? `${sub.year} 年` : "年份未知"}</span>
                      <h4 className="hover-title">{sub.title || "影视作品"}</h4>

                      {/* Mini Progress */}
                      {isTV && totalEps > 0 && (
                        <div className="sub-hover-progress">
                          <div className="sub-hover-progress-text">
                            <span>{hasSkipped ? `投影：已选 ${totalEps} 集 · 满足 ${satisfiedEps}/${totalEps} 集` : `投影满足: ${satisfiedEps}/${totalEps} 集`}</span>
                            <span>{pct}%</span>
                          </div>
                          <div className="progress-bar" style={{ height: "4px" }}>
                            <div className="progress-fill movie-fill" style={{ width: `${pct}%` }}></div>
                          </div>
                        </div>
                      )}

                      <div className="hover-specs-brief">
                        <span>质量策略：{sub.quality_policy_id === "default" ? "默认" : sub.quality_policy_id}</span>
                        <span>📁 {sub.root_name || "目标媒体库未知"}</span>
                      </div>

                      {/* Action Buttons on Card */}
                      <div className="hover-actions" onClick={(e) => e.stopPropagation()}>
                        {sub.state === "active" ? (
                          <button
                            className="btn-action btn-pause"
                            disabled={operatingId === sub.id}
                            onClick={() => pauseSub(sub.id)}
                            title="暂停订阅"
                          >
                            <Pause size={12} />
                            <span>暂停</span>
                          </button>
                        ) : (
                          <button
                            className="btn-action btn-resume"
                            disabled={operatingId === sub.id}
                            onClick={() => resumeSub(sub.id)}
                            title="恢复订阅"
                          >
                            <Play size={12} />
                            <span>恢复</span>
                          </button>
                        )}
                        <button
                          className="btn-action btn-search"
                          onClick={() => recheckSub(sub)}
                          title="检查资源候选"
                        >
                          <RefreshCw size={12} />
                          <span>检索</span>
                        </button>
                      </div>
                    </div>
                  </div>
                </div>

                {/* Below Poster Info */}
                <div className="card-caption">
                  <button type="button" className="caption-title subscription-open" aria-label={`查看${sub.title || "影视作品"}订阅详情`} onClick={event => { event.stopPropagation(); setSelectedSub(sub); }}>
                    {sub.title || "影视作品"}
                  </button>
                  <div className="caption-sub-row">
                    <span>{sub.year ? `${sub.year} 年` : "年份未知"}</span>
                    {isTV && totalEps > 0 ? (
                      <span className="caption-progress-tag">
                        {hasSkipped ? `投影：已选 ${totalEps} 集 · 满足 ${satisfiedEps} 集 (${pct}%)` : `投影 ${satisfiedEps}/${totalEps} 集 (${pct}%)`}
                      </span>
                    ) : (
                      <span className="caption-progress-tag">单部全量</span>
                    )}
                  </div>
                  {/* Subtle Progress Bar */}
                  {isTV && totalEps > 0 && (
                    <div className="card-mini-bar">
                      <div className="card-mini-fill" style={{ width: `${pct}%` }}></div>
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {/* Subscription Detail & Episode Matrix Modal (按季分组折叠排列！) */}
      {/* High-End Modern Subscription Detail Modal */}
      {selectedSub && (
        <SubscriptionDetailModal
          subscription={selectedSub}
          posterUrl={selectedSub.poster_url || undefined}
          operating={operatingId === selectedSub.id}
          onClose={() => setSelectedSub(null)}
          controlFeedback={controlFeedback}
          onPause={pauseSub}
          onResume={resumeSub}
          onRecheck={recheckSub}
          onDelete={deleteSub}
          onUpdated={data => { setSelectedSub(current => current?.id === data.id ? { ...current, ...data } : current); setSubscriptions(items => items.map(item => item.id === data.id ? { ...item, ...data } : item)); }}
        />
      )}
    </div>
  );
};

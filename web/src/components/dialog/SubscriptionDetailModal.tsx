import React, { useState, useMemo, useRef, useId } from "react";
import { useDialogFocus } from "@/features/configuration/useDialogFocus";
import {
  X,
  Pause,
  Play,
  RefreshCw,
  Trash2,
  Check,
  AlertCircle,
  Clock,
  SkipForward,
  Folder,
  Sliders,
  Layers,
  Sparkles,
  Tv,
  Film,
} from "lucide-react";
import type { paths } from "@/api/client";
import { SubscriptionBindingEditor } from "@/features/configuration/SubscriptionBindingEditor";
import { useSubscriptionStatus } from "@/features/configuration/useSubscriptionStatus";
import { statusReason } from "@/features/configuration/statusReasons";
import { TVMetadataRefreshPanel } from "@/features/configuration/TVMetadataRefreshPanel";
import { EpisodeMappingPanel } from "@/features/configuration/EpisodeMappingPanel";
import { SubscriptionScopeEditor } from "@/features/configuration/SubscriptionScopeEditor";
import { CandidateReasonsPanel } from "@/features/configuration/CandidateReasonsPanel";
import { MovieUpgradePanel } from "@/features/configuration/MovieUpgradePanel";
import { LibraryReusePanel } from "@/features/configuration/LibraryReusePanel";

type SubDetail = paths["/api/v1/subscriptions"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type WantedItem = NonNullable<SubDetail["wanted_items"]>[number] & { reservation_protected?: boolean; status?: string };

interface SubscriptionDetailModalProps {
  subscription: SubDetail;
  posterUrl?: string;
  operating?: boolean;
  onClose: () => void;
  controlFeedback?: React.ReactNode;
  onPause: (id: string) => void;
  onResume: (id: string) => void;
  onRecheck: (sub: SubDetail) => void;
  onDelete: (sub: SubDetail) => void;
  onUpdated: (sub: SubDetail) => void;
}

function parseSeasonAndEpisode(unitKey?: string | null): { season: number; episode: number } {
  if (!unitKey) return { season: 1, episode: 1 };
  const match = unitKey.match(/^S(\d+)E(\d+)/i);
  if (match) {
    return { season: parseInt(match[1], 10), episode: parseInt(match[2], 10) };
  }
  return { season: 1, episode: 1 };
}

export const SubscriptionDetailModal: React.FC<SubscriptionDetailModalProps> = ({
  subscription,
  posterUrl,
  operating,
  onClose,
  controlFeedback,
  onPause,
  onResume,
  onRecheck,
  onDelete,
  onUpdated,
}) => {
  const dialog = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useDialogFocus(dialog, onClose);
  const status = useSubscriptionStatus(subscription,onUpdated);
  const observedSubscription = status.subscription;
  const isTV = subscription.media_type === "tv" || (subscription.wanted_items && subscription.wanted_items.length > 1);

  // Group all wanted items strictly by season
  const seasonsMap = useMemo(() => {
    if (!observedSubscription.wanted_items || observedSubscription.wanted_items.length === 0) {
      return new Map<number, WantedItem[]>();
    }
    const map = new Map<number, WantedItem[]>();
    for (const wi of observedSubscription.wanted_items) {
      const { season } = parseSeasonAndEpisode(wi.unit_key);
      if (!map.has(season)) map.set(season, []);
      map.get(season)!.push(wi);
    }
    // Sort episodes inside each season numerically
    for (const [_, eps] of map.entries()) {
      eps.sort((a, b) => {
        const epA = parseSeasonAndEpisode(a.unit_key).episode;
        const epB = parseSeasonAndEpisode(b.unit_key).episode;
        return epA - epB;
      });
    }
    return map;
  }, [observedSubscription]);

  const seasonNumbers = useMemo(() => {
    return Array.from(seasonsMap.keys()).sort((a, b) => a - b);
  }, [seasonsMap]);

  // Initial active season (first regular season > 0 or 1)
  const [activeSeason, setActiveSeason] = useState<number>(() => {
    const sNums = Array.from(seasonsMap.keys()).sort((a, b) => a - b);
    const firstRegular = sNums.find((n) => n > 0);
    return firstRegular !== undefined ? firstRegular : sNums[0] || 1;
  });

  // Filter option: show all or only missing
  const [onlyShowMissing, setOnlyShowMissing] = useState(false);

  // Stats for the whole subscription
  const activeWantedItems = useMemo(() => {
    return observedSubscription.wanted_items?.filter((w) => !w.is_skipped) || [];
  }, [observedSubscription]);

  const explanationWanted = subscription.wanted_items?.find(w=>!w.retired);
  const hasSkipped = (subscription.wanted_items?.length || 0) > activeWantedItems.length && activeWantedItems.length > 0;
  const totalTargetEps = activeWantedItems.length;
  const satisfiedEps = activeWantedItems.filter((w) => w.is_satisfied).length;

  const unknownCount = activeWantedItems.filter(item => item.status === "unknown" || item.wait_reason === "status_loading" || item.wait_reason === "status_unavailable").length;
  const overallPct = totalTargetEps > 0 ? Math.round((satisfiedEps / totalTargetEps) * 100) : 0;

  // Active season episodes
  const currentSeasonEps = useMemo(() => {
    const allInSeason = seasonsMap.get(activeSeason) || [];
    if (!onlyShowMissing) return allInSeason;
    return allInSeason.filter((e) => !e.is_satisfied && !e.is_skipped);
  }, [seasonsMap, activeSeason, onlyShowMissing]);

  // Active season stats
  const activeSeasonStats = useMemo(() => {
    const allInSeason = seasonsMap.get(activeSeason) || [];
    const activeEps = allInSeason.filter((e) => !e.is_skipped);
    const targetCount = activeEps.length;
    const satCount = activeEps.filter((e) => e.is_satisfied).length;
    const pendingCount = activeEps.filter((e) => !e.is_satisfied).length;
    const skippedCount = allInSeason.length - activeEps.length;
    const sPct = targetCount > 0 ? Math.round((satCount / targetCount) * 100) : 0;

    return {
      allCount: allInSeason.length,
      targetCount,
      satCount,
      pendingCount,
      skippedCount,
      sPct,
    };
  }, [seasonsMap, activeSeason]);

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div ref={dialog} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} className="modal-card sub-modern-modal" onClick={(e) => e.stopPropagation()}>
        {/* Visual Hero Header with Backdrop & Poster */}
        <div
          className="sub-hero-banner"
          style={{
            backgroundImage: posterUrl
              ? `linear-gradient(to right, rgba(9, 13, 22, 0.96) 0%, rgba(9, 13, 22, 0.88) 35%, rgba(9, 13, 22, 0.6) 100%), url(${posterUrl})`
              : "none",
          }}
        >
          <button className="close-btn detail-close" onClick={onClose} title="关闭">
            <X size={18} />
          </button>

          <div className="sub-hero-content">
            {/* Left Poster Thumbnail */}
            <div className="sub-hero-poster-box">
              {posterUrl ? (
                <img
                  src={posterUrl}
                  alt={subscription.title || "影视"}
                  className="sub-hero-poster-img"
                  onError={(e) => {
                    (e.currentTarget as HTMLImageElement).style.display = "none";
                  }}
                />
              ) : null}
              <div className="sub-poster-fallback">
                {isTV ? <Tv size={36} /> : <Film size={36} />}
                <span>{subscription.title || "影视作品"}</span>
              </div>
            </div>

            {/* Right Meta Info */}
            <div className="sub-hero-info">
              <div className="sub-badges-row">
                <span className={`badge ${isTV ? "tv" : "movie"}`}>
                  {isTV ? "电视剧 (TV Series)" : "电影 (Movie)"}
                </span>
                <span className={`badge ${subscription.state}`}>
                  {subscription.state === "active" ? "订阅有效" : subscription.state === "paused" ? "已暂停" : "已归档"}
                </span>
                {subscription.year && (
                  <span className="badge-info-pill">
                    <span>{subscription.year} 年</span>
                  </span>
                )}
                {isTV && seasonNumbers.length > 0 && (
                  <span className="badge-info-pill">
                    <Layers size={13} />
                    <span>全剧共 {seasonNumbers.filter((n) => n > 0).length} 季</span>
                  </span>
                )}
              </div>

              <h2 id={titleId} className="sub-hero-title">{subscription.title || "影视作品 (未命名)"}</h2>

              {/* Specs Pills */}
              <div className="sub-specs-pills">
                <span className="spec-pill">
                  <Sliders size={13} />
                  <span>策略: <strong>{subscription.quality_policy_id === "default" ? "默认画质策略" : subscription.quality_policy_id}</strong></span>
                </span>
                <span className="spec-pill">
                  <Folder size={13} />
                  <span>存储库: <strong>{subscription.root_name || subscription.root_path || "目标库未确认"}</strong></span>
                </span>
                <span className="spec-pill">
                  <Sparkles size={13} />
                  <span>范围: <strong>{subscription.scope_type === "all" ? (isTV ? "全剧" : "单电影") : "自选指定季集"}</strong></span>
                </span>
              </div>

              {/* Overall Progress Bar */}
              {isTV && totalTargetEps > 0 && (
                <div className="sub-overall-progress-card">
                  <div className="overall-progress-head">
                    <span className="overall-lbl">
                      {hasSkipped
                        ? `自选目标: 本次确认满足 ${satisfiedEps} / ${totalTargetEps} 集`
                        : `当前目标: 本次确认满足 ${satisfiedEps} / ${totalTargetEps} 集`}
                    </span>
                    <strong className="overall-pct">{status.report ? `${overallPct}%` : "—"}</strong>
                  </div>
                  {unknownCount > 0 && <p className="tile-hint">{unknownCount} 个目标尚未确认拥有，不表示文件已丢失。</p>}
                  <div className="overall-progress-bar">
                    <div
                      className="overall-progress-fill"
                      style={{ width: `${overallPct}%` }}
                    ></div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Modal Scrollable Body */}
        <div className="sub-modern-body">
          {controlFeedback}
          <section className="config-editor" aria-label="当前目标状态" aria-busy={status.loading}>
            <div className="config-heading"><h3>当前目标状态</h3><button type="button" className="btn-sm btn-secondary" disabled={status.loading} onClick={status.refresh}>刷新状态</button></div>
            {status.loading && <p role="status" className="config-description">正在读取当前数据库状态…</p>}
            {status.error && <p role="alert" className="config-error">{status.error}</p>}
            {status.report && <>
              <p className="config-description">观测时间：{new Date(status.report.observed_at).toLocaleString()} · 意图 {status.report.intent_version} / 策略 {status.report.policy_revision} / 根 {status.report.root_generation}。仅解释数据库记录，不授权新的下载。</p>
              {status.report.blockers.length > 0 && <ul className="config-description">{status.report.blockers.map(reason=><li key={reason}>{statusReason(reason)}</li>)}</ul>}
              {status.report.endpoint_retry_at && <p className="config-description">记录的端点最早预算时间：{new Date(status.report.endpoint_retry_at).toLocaleString()}（不是承诺执行时间）。</p>}
            </>}
          </section>
          <LibraryReusePanel key={`reuse-${subscription.id}-${subscription.intent_version}-${subscription.quality_policy_revision}`} subscription={observedSubscription} onUpdated={()=>void status.refresh()}/>
          {!isTV&&explanationWanted&&<CandidateReasonsPanel key={"candidates-"+subscription.id} subscriptionId={subscription.id} wantedId={explanationWanted.id} intentVersion={subscription.intent_version} onChanged={()=>void status.refresh()}/> }
          {!isTV&&<MovieUpgradePanel subscription={subscription} wantedId={explanationWanted?.id??""} onUpdated={()=>void status.refresh()}/>}
          {isTV&&<TVMetadataRefreshPanel subscription={subscription} onUpdated={onUpdated} disabled={operating}/>}
{isTV&&<EpisodeMappingPanel subscription={subscription} onUpdated={onUpdated} disabled={operating}/>}
          <SubscriptionScopeEditor subscriptionId={subscription.id} onUpdated={onUpdated} disabled={operating}/>
          <SubscriptionBindingEditor key={subscription.id} subscription={subscription} onUpdated={onUpdated} disabled={operating} />
          {/* Season Pill Tabs (按季平铺导航药丸) */}
          {isTV && seasonNumbers.length > 1 && (
            <div className="season-pills-bar">
              <span className="season-pills-label">季导航切换:</span>
              <div className="season-pills-list">
                {seasonNumbers.map((sNum) => {
                  const eps = seasonsMap.get(sNum)!;
                  const activeEps = eps.filter((e) => !e.is_skipped);
                  const targetCount = activeEps.length > 0 ? activeEps.length : eps.length;
                  const satCount = activeEps.filter((e) => e.is_satisfied).length;
                  const sPct = targetCount > 0 ? Math.round((satCount / targetCount) * 100) : 0;
                  const isSpecials = sNum === 0;
                  const isPartial = activeEps.length > 0 && activeEps.length < eps.length;

                  return (
                    <button
                      key={sNum}
                      type="button"
                      className={`season-pill-btn ${activeSeason === sNum ? "active" : ""}`}
                      onClick={() => setActiveSeason(sNum)}
                    >
                      <span className="pill-title">
                        {isSpecials ? "特别篇" : `第 ${sNum} 季`}
                      </span>
                      <span className="pill-stat">
                        {isPartial
                          ? `已选 ${targetCount} 集 · ${sPct}%`
                          : `${satCount}/${eps.length} 集 · ${sPct}%`}
                      </span>
                      {sPct === 100 && (
                        <span className="pill-done-dot">✓</span>
                      )}
                    </button>
                  );
                })}
              </div>
            </div>
          )}

          {/* Active Season Episode Tiles Section */}
          <div className="active-season-tiles-section">
            <div className="season-tiles-header">
              <div className="tiles-header-left">
                <strong className="tiles-season-title">
                  {isTV ? `${activeSeason === 0 ? "特别篇" : `第 ${activeSeason} 季`} · 剧集追踪状态` : "电影目标状态"}
                </strong>
                <span className="tiles-season-summary">
                  共 {activeSeasonStats.allCount} 集 · 已选 <strong>{activeSeasonStats.targetCount} 集</strong> · 已入库 <strong>{activeSeasonStats.satCount} 集</strong> · 待处理 <strong>{activeSeasonStats.pendingCount} 集</strong>
                  {activeSeasonStats.skippedCount > 0 && <span> · 已跳过 {activeSeasonStats.skippedCount} 集</span>}
                </span>
              </div>

              <div className="tiles-header-right">
                <label className="checkbox-filter-label">
                  <input
                    type="checkbox"
                    checked={onlyShowMissing}
                    onChange={(e) => setOnlyShowMissing(e.target.checked)}
                  />
                  <span>仅看未满足目标</span>
                </label>
              </div>
            </div>

            {/* Episode Tiles Grid */}
            {currentSeasonEps.length > 0 ? (
              <div className="episode-tiles-grid">
                {currentSeasonEps.map((wi) => {
                  const isSkipped = wi.is_skipped;
                  const isSatisfied = wi.is_satisfied;
                  const waitReason = wi.wait_reason;

                  let statusClass = "tile-missing";
                  let statusText = "未满足";
                  let StatusIcon = AlertCircle;

                  if (isSkipped) {
                    statusClass = "tile-skipped";
                    statusText = "已跳过";
                    StatusIcon = SkipForward;
                  } else if (isSatisfied) {
                    statusClass = "tile-satisfied";
                    statusText = wi.current_fact_stage ? `已入库 (${wi.current_fact_stage})` : "已入库";
                    StatusIcon = Check;
                  } else if (waitReason) {
                    statusClass = "tile-waiting";
                    statusText = statusReason(waitReason);
                    StatusIcon = Clock;
                  }

                  return (
                    <div key={wi.id} className={`episode-tile-card ${statusClass}`}>
                      <div className="tile-top-row">
                        <strong className="tile-ep-code">{wi.unit_key || wi.unit_id.slice(0, 8)}</strong>
                        <span className={`tile-status-pill ${statusClass}`}>
                          <StatusIcon size={12} />
                          <span>{statusText}</span>
                        </span>
                      </div>
                      {wi.reservation_protected && <p className="tile-hint">占位仍受保护，不会因观测到期自动释放。</p>}
                      <div className="tile-foot-row">
                        <span className="tile-hint">
                          {isSatisfied
                            ? "✓ 本地媒体库已就绪"
                            : isSkipped
                            ? "未纳入本轮订阅"
                            : waitReason ? statusReason(waitReason)
                            : subscription.state === "paused" ? "订阅已暂停"
                            : isTV ? "自动获取尚未启用"
                            : !subscription.indexer_ids?.length ? "未绑定索引器"
                            : "等待核验与调度"}
                        </span>
                      </div>
                    </div>
                  );
                })}
              </div>
            ) : (
              <div className="tiles-empty-box">
                <p>当前筛选下没有目标。不代表全部目标已入库。</p>
              </div>
            )}
          </div>
        </div>

        {/* Modal Elevated Footer Actions */}
        <div className="sub-modal-footer">
          <div className="footer-left">
            <button
              type="button"
              className="btn-danger-outline btn-sm"
              onClick={() => onDelete(subscription)}
              title="归档订阅，保留历史和已发送获取"
            >
              <Trash2 size={14} />
              <span>归档订阅</span>
            </button>
          </div>

          <div className="footer-right">
            {subscription.state === "active" ? (
              <button
                type="button"
                className="btn-secondary btn-sm"
                disabled={operating}
                onClick={() => onPause(subscription.id)}
              >
                <Pause size={14} />
                <span>暂停订阅</span>
              </button>
            ) : (
              <button
                type="button"
                className="btn-primary btn-sm"
                disabled={operating}
                onClick={() => onResume(subscription.id)}
              >
                <Play size={14} />
                <span>恢复订阅</span>
              </button>
            )}

            <button
              type="button"
              className="btn-primary btn-sm"
              onClick={() => onRecheck(subscription)}
              title="检索绑定端点并尝试安全获取"
              disabled={operating || subscription.state !== "active"}
            >
              <RefreshCw size={14} />
              <span>检索与获取</span>
            </button>

            <button type="button" className="btn-secondary btn-sm" onClick={onClose}>
              关闭
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};

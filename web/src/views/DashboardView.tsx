import React, { useState, useEffect, useMemo, useCallback, useRef } from "react";
import { useNavigate, Link } from "react-router-dom";
import {
  Search,
  Globe2,
  Film,
  Tv,
  Settings,
  Star,
  RefreshCw,
  ArrowUpRight,
} from "lucide-react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { useToast } from "@/features/toast/ToastContext";
import { useSubscriptionCreation } from "@/features/configuration/useSubscriptionCreation";
import { subscriptionScope } from "@/features/configuration/subscriptionCreationRecovery";
import { MediaDetailModal } from "@/components/dialog/MediaDetailModal";
import type { MediaItem } from "@/components/cards/MediaCard";
import type { paths } from "@/api/client";
import { useDashboardFacts } from "@/features/dashboard/useDashboardFacts";
import { DashboardActivityPanel, DashboardLibraryPanel, DashboardRecentPanel } from "@/features/dashboard/DashboardFactsPanel";
import { DownloadGlobalRatesPanel } from "@/features/dashboard/DownloadGlobalRatesPanel";
import { PendingRequestsPanel } from "@/features/dashboard/PendingRequestsPanel";

type SystemStatus = paths["/api/v1/system/status"]["get"]["responses"]["200"]["content"]["application/json"];

const deniedDashboardRead = (result: PromiseSettledResult<{ response: Response } | null>) => result.status === "fulfilled" && result.value !== null && [401, 403].includes(result.value.response.status);
export const DashboardView: React.FC = () => {
  const navigate = useNavigate();
  const { user } = useAuth();
  const toast = useToast();

  const scope = user ? JSON.stringify([user.id, user.role]) : "";
  const canReadSites = user?.role === "admin";
  const facts = useDashboardFacts({ userId: user?.id, role: user?.role });
  const [dataScope, setDataScope] = useState(scope);
  const sameScope = dataScope === scope;
  const [storedStatus, setStatus] = useState<SystemStatus | null>(null);
  const [storedMovieSubsCount, setMovieSubsCount] = useState<number | null>(null);
  const [storedTvSubsCount, setTvSubsCount] = useState<number | null>(null);
  const [storedOnlineSitesCount, setOnlineSitesCount] = useState<number | null>(null);
  const [storedTotalSitesCount, setTotalSitesCount] = useState<number | null>(null);
  const status = sameScope ? storedStatus : null;
  const movieSubsCount = sameScope ? storedMovieSubsCount : null;
  const tvSubsCount = sameScope ? storedTvSubsCount : null;
  const onlineSitesCount = sameScope ? storedOnlineSitesCount : null;
  const totalSitesCount = sameScope ? storedTotalSitesCount : null;
  const [refreshing, setRefreshing] = useState(false);
  const [refreshFailures, setRefreshFailures] = useState<string[]>([]);
  const refreshRequest = useRef<AbortController | null>(null);

  // Dynamic Recent Posters from real TMDB discover feed
  const [storedRecentItems, setRecentItems] = useState<MediaItem[]>([]);
  const recentItems = sameScope ? storedRecentItems : [];
  const pageRefreshing = refreshing || facts.overviewStatus === "loading" || facts.activityStatus === "loading";

  // Media Detail Modal directly on Dashboard
  const [selectedDetailMedia, setSelectedDetailMedia] = useState<MediaItem | null>(null);
  const [showDetailModal, setShowDetailModal] = useState(false);
  const subscriptionCreation = useSubscriptionCreation({ onSuccess: (result) => {
    setShowDetailModal(false);
    toast.success(result.title ? `《${result.title}》订阅成功` : "订阅成功", "订阅意图已确认，可在订阅页查看季集范围与获取状态。");
    navigate("/subscriptions");
  } });
  const subscribingId = subscriptionCreation.busy ? subscriptionCreation.recovery?.body.tmdb_id ?? null : null;

  const greeting = useMemo(() => {
    const hour = new Date().getHours();
    if (hour < 6) return "夜深了";
    if (hour < 12) return "早上好";
    if (hour < 14) return "中午好";
    if (hour < 18) return "下午好";
    return "晚上好";
  }, []);

  const todayDateStr = useMemo(() => {
    const d = new Date();
    const weekdays = ["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"];
    return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${weekdays[d.getDay()]}`;
  }, []);

  const loadData = useCallback(async () => {
    if (!scope) return null;
    refreshRequest.current?.abort();
    const controller = new AbortController();
    refreshRequest.current = controller;
    setRefreshing(true);
    const [system, subscriptions, indexers, discover] = await Promise.allSettled([
      api.GET("/api/v1/system/status", { signal: controller.signal }),
      api.GET("/api/v1/subscriptions", { signal: controller.signal, cache:"no-store", params:{query:{limit:1}} }),
      canReadSites ? api.GET("/api/v1/indexers", { signal: controller.signal }) : Promise.resolve(null),
      api.GET("/api/v1/discover", {
        params: { query: { category: "trending", page: 1 } },
        signal: controller.signal,
      }),
    ]);
    if (controller.signal.aborted) return null;
    const failures: string[] = [];
    if (system.status === "fulfilled" && system.value.data) {
      setStatus(system.value.data);
    } else {
      if (deniedDashboardRead(system)) setStatus(null);
      failures.push("系统状态");
    }
    if (subscriptions.status === "fulfilled" && subscriptions.value.data) {
      setMovieSubsCount(subscriptions.value.data.movie_count);
      setTvSubsCount(subscriptions.value.data.tv_count);
    } else {
      if (deniedDashboardRead(subscriptions)) { setMovieSubsCount(null); setTvSubsCount(null); }
      failures.push("订阅统计");
    }
    if (!canReadSites) {
      setTotalSitesCount(null);
      setOnlineSitesCount(null);
    } else if (indexers.status === "fulfilled" && indexers.value?.data) {
      setTotalSitesCount(indexers.value.data.items.length);
      setOnlineSitesCount(indexers.value.data.items.filter((item) => item.health === "online").length);
    } else {
      if (deniedDashboardRead(indexers)) { setTotalSitesCount(null); setOnlineSitesCount(null); }
      failures.push("站点状态");
    }
    if (discover.status === "fulfilled" && discover.value.data) {
      setRecentItems((discover.value.data.results ?? []).slice(0, 10).map((item) => ({
        tmdb_id: item.tmdb_id,
        title: item.title,
        year: item.year ?? undefined,
        media_type: item.media_type,
        overview: item.overview ?? undefined,
        poster_url: item.poster_url ?? undefined,
        backdrop_url: item.backdrop_url ?? undefined,
        rating: item.rating ?? undefined,
        genres: item.genres ?? undefined,
      })));
    } else {
      if (deniedDashboardRead(discover)) setRecentItems([]);
      failures.push("影视推荐");
    }
    setRefreshFailures(failures);
    setRefreshing(false);
    return failures;
  }, [scope, canReadSites]);

  useEffect(() => {
    refreshRequest.current?.abort();
    setStatus(null);
    setMovieSubsCount(null);
    setTvSubsCount(null);
    setOnlineSitesCount(null);
    setTotalSitesCount(null);
    setRecentItems([]);
    setShowDetailModal(false);
    setSelectedDetailMedia(null);
    setRefreshFailures([]);
    setDataScope(scope);
    void loadData();
    return () => refreshRequest.current?.abort();
  }, [loadData, scope]);

  const handleManualRefresh = async () => {
    const [failures, factsReady] = await Promise.all([loadData(), facts.refresh()]);
    if (failures === null) return;
    const failed = [...failures, ...(!factsReady ? ["媒体库与活动"] : [])];
    if (failed.length > 0) {
      toast.error("部分数据未刷新", `未能读取${failed.join("、")}，请稍后重试。`);
      return;
    }
    toast.success("控制台状态已刷新", "已读取当前账户可访问的首页数据。");
  };

  const openDetail = (item: MediaItem) => {
    setSelectedDetailMedia(item);
    setShowDetailModal(true);
  };

  const handleSubscribe = (
    m: MediaItem,
    scope: "all" | "selected" = "all",
    seasons: number[] = [],
    episodes: string[] = [],
    includeSpecials = false,
    downloaderId?: string,
    indexerIds?: string[],
    policyId = "default",
    rootId?: string,
    classificationVersion?: number,
    classificationPreviewStamp?: string,
    classificationPreviewRootID?: string
  ) => {
    if (m.media_type !== undefined && m.media_type !== "movie" && m.media_type !== "tv") {
      toast.error("订阅失败", "媒体类型无效，请重新读取影视详情。");
      return;
    }
    void subscriptionCreation.start({
      tmdb_id: m.tmdb_id, media_type: m.media_type,
      ...subscriptionScope(scope, seasons, episodes),
      quality_policy_id: policyId, ...(classificationVersion!==undefined?{classification_version:classificationVersion,classification_preview_stamp:classificationPreviewStamp,classification_preview_root_id:classificationPreviewRootID}:{root_id:rootId}),
      include_specials: includeSpecials, downloader_id: downloaderId, indexer_ids: indexerIds,
    });
    setShowDetailModal(false);
  };

  return (
    <div className="dashboard-container">
      {subscriptionCreation.message && (
        <div className="empty-hint" role="status">
          <p>{subscriptionCreation.message}</p>
          {subscriptionCreation.canRecover && <button type="button" className="btn-secondary btn-sm" onClick={() => void subscriptionCreation.recover()}>读取原订阅结果</button>}
          {subscriptionCreation.canRetryOriginal && <button type="button" className="btn-secondary btn-sm" onClick={() => void subscriptionCreation.retryOriginal()}>重试原订阅请求</button>}
        </div>
      )}
      <div className="page-header">
        <div>
          <p className="page-date">{todayDateStr}</p>
          <h1>{greeting}，{user?.username || "管理员"}</h1>
          <p className="subtitle">媒体收藏、订阅与入库进度，尽在这里。</p>
        </div>
        <div className="header-actions">
          <button type="button" className="btn-secondary" onClick={handleManualRefresh} disabled={pageRefreshing}
            aria-label="刷新首页数据" aria-busy={pageRefreshing} title="刷新系统状态">
            <RefreshCw size={15} className={pageRefreshing ? "spinner-icon" : ""} aria-hidden="true" /><span>刷新数据</span>
          </button>
          <button type="button" className="btn-primary" onClick={() => navigate("/discover")}><Search size={16} aria-hidden="true" />探索发现</button>
        </div>
      </div>

      {/* Anything that needs a human decision comes before the numbers. */}
      {canReadSites ? <PendingRequestsPanel userId={user?.id} role={user?.role} /> : null}
      {refreshFailures.length > 0 && (
        <p className="empty-hint" role="alert">未能刷新：{refreshFailures.join("、")}。已显示的数据保持上次读取结果。</p>
      )}

      <div className="dash-columns">
        <DashboardLibraryPanel facts={facts} />
        {canReadSites ? <DownloadGlobalRatesPanel userId={user?.id} role={user?.role} /> : null}
      </div>

      <DashboardActivityPanel facts={facts} />

      <div className="stats-grid-modern">
        <Link className="stat-card-modern" to="/subscriptions">
          <div className="stat-icon-wrapper movie-gradient">
            <Film size={22} />
          </div>
          <div className="stat-info-col">
            <div className="stat-head">
              <span className="stat-label">电影监控订阅</span>
            </div>
            <div className="stat-value-row">
              <strong>{movieSubsCount ?? "—"}</strong>
              <span className="stat-unit">部影片</span>
            </div>
          </div>
        </Link>

        <Link className="stat-card-modern" to="/subscriptions">
          <div className="stat-icon-wrapper tv-gradient">
            <Tv size={22} />
          </div>
          <div className="stat-info-col">
            <div className="stat-head">
              <span className="stat-label">电视剧连载追更</span>
            </div>
            <div className="stat-value-row">
              <strong>{tvSubsCount ?? "—"}</strong>
              <span className="stat-unit">部剧集</span>
            </div>
          </div>
        </Link>

        {canReadSites && <Link className="stat-card-modern" to="/sites">
          <div className="stat-icon-wrapper site-gradient">
            <Globe2 size={22} />
          </div>
          <div className="stat-info-col">
            <div className="stat-head">
              <span className="stat-label">做种站点连通率</span>
            </div>
            <div className="stat-value-row">
              <strong>{onlineSitesCount ?? "—"}</strong>
              <span className="stat-unit">/ {totalSitesCount ?? "—"} 正常</span>
            </div>
          </div>
        </Link>}
      </div>

      <DashboardRecentPanel facts={facts} />

      {/* Recent Media Poster Wall */}
      <div className="card poster-wall-card-modern">
        <div className="card-header">
          <h3>
            <Star size={18} />
            <span>最新影视推荐与订阅流 (实时 TMDB 精选)</span>
          </h3>
          <Link to="/discover" className="link-more-styled">
            <span>浏览全部影视库</span>
            <ArrowUpRight size={14} />
          </Link>
        </div>

        <div className="wall-carousel-wrap">
          {recentItems.length === 0 && <p className="empty-hint">暂无推荐内容，请检查 TMDB 配置或稍后刷新。</p>}
          {recentItems.map((item) => (
            <button type="button"
              key={`${item.tmdb_id}-${item.title}`}
              className="wall-card-modern"
              onClick={() => openDetail(item)}
              aria-label={`查看《${item.title}》详情`}>
              <div className="wall-poster-box-modern">
                {item.poster_url ? (
                  <img
                    src={item.poster_url}
                    alt={item.title}
                    className="wall-img"
                    loading="lazy"
                    onError={(e) => {
                      (e.currentTarget as HTMLImageElement).style.display = "none";
                    }}
                  />
                ) : null}
                <div className="wall-placeholder">
                  {item.media_type === "tv" ? <Tv size={36} /> : <Film size={36} />}
                  <span className="placeholder-text">{item.title}</span>
                </div>
                <span className={`type-tag ${item.media_type}`}>
                  {item.media_type === "tv" ? "剧集" : "电影"}
                </span>
                {item.rating && item.rating > 0 && (
                  <span className="rating-tag">
                    <Star size={11} fill="currentColor" /> {item.rating.toFixed(1)}
                  </span>
                )}
                <div className="wall-hover-overlay">
                  <span className="wall-hover-btn">查看详情 →</span>
                </div>
              </div>
              <strong className="wall-card-title">{item.title}</strong>
              <div className="wall-card-meta">
                <span>{item.year ? `${item.year} 年` : "年份未知"}</span> · <span>评分: {item.rating ? item.rating.toFixed(1) : "暂无"}</span>
              </div>
            </button>
          ))}
        </div>
      </div>

      {/* System Status Details */}
      {status && (
        <div className="card system-summary-card">
          <div className="card-header">
            <h3>
              <Settings size={18} />
              <span>系统运行微服务环境</span>
            </h3>
            <Link to="/settings" className="link-more-styled">
              <span>详细配置</span>
              <ArrowUpRight size={14} />
            </Link>
          </div>
          <div className="sys-summary-grid">
            <div className="sys-summary-item">
              <span className="sys-item-label">单进程微单体版本</span>
              <strong className="sys-item-val">PulseMedia v{status.version}</strong>
            </div>
            <div className="sys-summary-item">
              <span className="sys-item-label">数据库持久化引擎</span>
              <strong className="sys-item-val">PostgreSQL 18.6 原生并发事务约束</strong>
            </div>
            {status.data_dir && (
              <div className="sys-summary-item">
                <span className="sys-item-label">应用数据主目录</span>
                <strong className="sys-item-val">{status.data_dir}</strong>
              </div>
            )}
            <div className="sys-summary-item">
              <span className="sys-item-label">实时日志输出级别</span>
              <strong className="sys-item-val text-level">{status.log_level.toUpperCase()}</strong>
            </div>
          </div>
        </div>
      )}

      {/* Media Detail Modal directly mounted on Dashboard */}
      {sameScope && showDetailModal && (
        <MediaDetailModal
          media={selectedDetailMedia}
          subscribing={subscribingId === selectedDetailMedia?.tmdb_id}
          onClose={() => setShowDetailModal(false)}
          onSubscribe={handleSubscribe}
          onSearchReleases={(m) => {
            setShowDetailModal(false);
            navigate(`/discover?q=${encodeURIComponent(m.title)}`);
          }}
        />
      )}
    </div>
  );
};

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
  PackageOpen,
} from "lucide-react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { useToast } from "@/features/toast/ToastContext";
import { useSubscriptionCreation } from "@/features/configuration/useSubscriptionCreation";
import { subscriptionScope } from "@/features/configuration/subscriptionCreationRecovery";
import { MediaDetailModal } from "@/components/dialog/MediaDetailModal";
import type { MediaItem } from "@/components/cards/MediaCard";
import type { paths } from "@/api/client";

type SystemStatus = paths["/api/v1/system/status"]["get"]["responses"]["200"]["content"]["application/json"];

export const DashboardView: React.FC = () => {
  const navigate = useNavigate();
  const { user } = useAuth();
  const toast = useToast();

  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [movieSubsCount, setMovieSubsCount] = useState<number | null>(null);
  const [tvSubsCount, setTvSubsCount] = useState<number | null>(null);
  const [onlineSitesCount, setOnlineSitesCount] = useState<number | null>(null);
  const [totalSitesCount, setTotalSitesCount] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshFailures, setRefreshFailures] = useState<string[]>([]);
  const refreshRequest = useRef<AbortController | null>(null);

  // Dynamic Recent Posters from real TMDB discover feed
  const [recentItems, setRecentItems] = useState<MediaItem[]>([]);

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
    refreshRequest.current?.abort();
    const controller = new AbortController();
    refreshRequest.current = controller;
    setRefreshing(true);
    const [system, subscriptions, indexers, discover] = await Promise.allSettled([
      api.GET("/api/v1/system/status", { signal: controller.signal }),
      api.GET("/api/v1/subscriptions", { signal: controller.signal }),
      api.GET("/api/v1/indexers", { signal: controller.signal }),
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
      failures.push("系统状态");
    }
    if (subscriptions.status === "fulfilled" && subscriptions.value.data) {
      setMovieSubsCount(subscriptions.value.data.items.filter((sub) => sub.media_type === "movie").length);
      setTvSubsCount(subscriptions.value.data.items.filter((sub) => sub.media_type === "tv").length);
    } else {
      failures.push("订阅统计");
    }
    if (indexers.status === "fulfilled" && indexers.value.data) {
      setTotalSitesCount(indexers.value.data.items.length);
      setOnlineSitesCount(indexers.value.data.items.filter((item) => item.health === "online").length);
    } else {
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
      failures.push("影视推荐");
    }
    setRefreshFailures(failures);
    setRefreshing(false);
    return failures;
  }, []);

  useEffect(() => {
    void loadData();
    return () => refreshRequest.current?.abort();
  }, [loadData]);

  const handleManualRefresh = async () => {
    const failures = await loadData();
    if (failures === null) return;
    if (failures.length > 0) {
      toast.error("部分数据未刷新", `未能读取${failures.join("、")}，请稍后重试。`);
      return;
    }
    toast.success("控制台状态已刷新", "已读取最新系统、订阅、站点和影视推荐。");
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
    rootId?: string
  ) => {
    if (m.media_type !== undefined && m.media_type !== "movie" && m.media_type !== "tv") {
      toast.error("订阅失败", "媒体类型无效，请重新读取影视详情。");
      return;
    }
    void subscriptionCreation.start({
      tmdb_id: m.tmdb_id, media_type: m.media_type,
      ...subscriptionScope(scope, seasons, episodes),
      quality_policy_id: policyId, root_id: rootId,
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
      {/* Top Ambient Welcome Banner */}
      <div className="welcome-banner-modern">
        <div className="welcome-left">
          <div className="welcome-badge">
            <span className="dot-online"></span>
            <span>{todayDateStr} · 状态以实时查询为准</span>
          </div>
          <h2 className="welcome-title">
            {greeting}，{user?.username || "管理员"}
          </h2>
          <p className="welcome-subtitle">
            在这里查看订阅与站点状态；未接入的数据不会显示为运行中。
          </p>
        </div>

        <div className="welcome-actions">
          <button className="btn-primary btn-banner-action" onClick={() => navigate("/discover")}>
            <Search size={16} />
            <span>探索发现</span>
          </button>
          <button className="btn-secondary btn-banner-action" onClick={() => navigate("/reorganize")}>
            <PackageOpen size={16} />
            <span>整理状态</span>
          </button>
          <button className="btn-secondary btn-banner-action" onClick={() => navigate("/sites")}>
            <Globe2 size={16} />
            <span>管理站点</span>
          </button>
          <button
            className="btn-icon-refresh"
            onClick={handleManualRefresh}
            disabled={refreshing}
            title="刷新系统状态"
          >
            <RefreshCw size={16} className={refreshing ? "spinner-icon" : ""} />
          </button>
        </div>
      </div>

      {refreshFailures.length > 0 && (
        <p className="empty-hint" role="alert">未能刷新：{refreshFailures.join("、")}。已显示的数据保持上次读取结果。</p>
      )}
      {/* 4 Elevated Stat Cards (MoviePilot MediaStatistic Style) */}
      <div className="stats-grid-modern">
        <div className="stat-card-modern" onClick={() => navigate("/subscriptions")}>
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
        </div>

        <div className="stat-card-modern" onClick={() => navigate("/subscriptions")}>
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
        </div>

        <div className="stat-card-modern" onClick={() => navigate("/sites")}>
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
            <div
              key={`${item.tmdb_id}-${item.title}`}
              className="wall-card-modern"
              onClick={() => openDetail(item)}
            >
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
            </div>
          ))}
        </div>
      </div>

      {/* Media Detail Modal directly mounted on Dashboard */}
      {showDetailModal && (
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

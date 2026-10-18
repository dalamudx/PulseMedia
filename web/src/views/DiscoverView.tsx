import React, { useState, useEffect, useMemo, useRef } from "react";
import { useNavigate, useLocation } from "react-router-dom";
import { Search, Loader2, Sparkles, Film, Tv, Trophy, Rss, RefreshCw } from "lucide-react";
import { api } from "@/api/client";
import { useToast } from "@/features/toast/ToastContext";
import { MediaCard, type MediaItem } from "@/components/cards/MediaCard";
import { MediaDetailModal } from "@/components/dialog/MediaDetailModal";
import { TorrentSearchModal } from "@/components/dialog/TorrentSearchModal";
import {useInteractiveReleaseSearch} from "@/features/configuration/useInteractiveReleaseSearch";
import { useSubscriptionCreation } from "@/features/configuration/useSubscriptionCreation";
import { subscriptionScope } from "@/features/configuration/subscriptionCreationRecovery";



export const DiscoverView: React.FC = () => {
  const navigate = useNavigate();
  const location = useLocation();
  const toast = useToast();

  const [activeCategory, setActiveCategory] = useState<"trending" | "movies" | "tv" | "douban">("trending");
  const [selectedGenre, setSelectedGenre] = useState<string>("全部");

  const [searchQuery, setSearchQuery] = useState("");
  const [searchType, setSearchType] = useState<"all" | "movie" | "tv">("all");
  const [searchYear, setSearchYear] = useState<string>("");
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState(false);
  const [movies, setMovies] = useState<MediaItem[]>([]);
  const subscriptionCreation = useSubscriptionCreation({ onSuccess: (result) => {
    setShowDetailModal(false);
    toast.success(result.title ? `《${result.title}》订阅成功` : "订阅成功", "订阅意图已确认，可在订阅页查看季集范围与获取状态。");
    navigate("/subscriptions");
  } });
  const subscribingId = subscriptionCreation.busy ? subscriptionCreation.recovery?.body.tmdb_id ?? null : null;

  // Dynamic Discover Feed (TMDB API 20+ items)
  const [feedItems, setFeedItems] = useState<MediaItem[]>([]);
  const [loadingFeed, setLoadingFeed] = useState(false);

  // Detail Modal
  const [showDetailModal, setShowDetailModal] = useState(false);
  const [selectedDetailMedia, setSelectedDetailMedia] = useState<MediaItem | null>(null);

  // Release Search Modal
  const [showReleaseModal, setShowReleaseModal] = useState(false);
  const [currentMedia, setCurrentMedia] = useState<MediaItem | null>(null);
  const releaseSearch=useInteractiveReleaseSearch({scope:"discover"});

  const [feedError, setFeedError] = useState("");
  const feedRequest = useRef<AbortController | null>(null);

  const switchCategory = (cat: "trending" | "movies" | "tv" | "douban") => {
    setActiveCategory(cat);
    setSelectedGenre("全部");
    setSearched(false);
    setSearchQuery("");
    setMovies([]);
  };

  const refreshDiscoverData = (notify = false) => {
    feedRequest.current?.abort();
    const controller = new AbortController(); feedRequest.current = controller;
    setLoadingFeed(true); setFeedItems([]); setFeedError("");
    let category: "trending" | "movie" | "tv" | "top_rated" = "trending";
    if (activeCategory === "movies") category = "movie";
    if (activeCategory === "tv") category = "tv";
    if (activeCategory === "douban") category = "top_rated";
    api.GET("/api/v1/discover", { params: { query: { category, page: 1 } }, signal: controller.signal })
      .then(({ data, error }) => {
        if (controller.signal.aborted) return;
        if (error || !data) { setFeedError("推荐读取失败，请检查元数据配置或重新加载。"); if (notify) toast.error("刷新失败", "未取得推荐数据。"); return; }
        setFeedItems(data.results.map(item => ({ ...item, year: item.year ?? undefined, overview: item.overview ?? undefined, poster_url: item.poster_url ?? undefined, backdrop_url: item.backdrop_url ?? undefined, rating: item.rating ?? undefined, genres: item.genres ?? undefined })));
        if (notify) toast.success("推荐列表已刷新", data.results.length ? "已读取推荐数据。" : "暂无推荐影视。");
      })
      .catch(() => { if (!controller.signal.aborted) setFeedError("连接失败，未取得推荐数据。"); })
      .finally(() => { if (!controller.signal.aborted) setLoadingFeed(false); });
  };

  useEffect(() => {
    refreshDiscoverData(false);
    return () => feedRequest.current?.abort();
  }, [activeCategory, location.key]);

  // Real genre filtering and display computation
  const currentDisplayList = useMemo(() => {
    const baseList = searched ? movies : feedItems;
    if (selectedGenre === "全部") return baseList;

    return baseList.filter((m) => {
      const inGenres = m.genres?.some(
        (g) => g.includes(selectedGenre) || selectedGenre.includes(g)
      );
      const inOverview = m.overview?.includes(selectedGenre);
      const inTitle = m.title.includes(selectedGenre);
      return inGenres || inOverview || inTitle;
    });
  }, [searched, movies, feedItems, selectedGenre]);

  const spotlightItem = feedItems[0];

  const handleSearch = async (queryText?: string, typeVal?: "all" | "movie" | "tv") => {
    const q = (queryText ?? searchQuery).trim();
    if (!q) return;
    const t = typeVal ?? searchType;

    setSearching(true);
    try {
      const yearNum = searchYear ? parseInt(searchYear, 10) : undefined;
      const { data, error } = await api.GET("/api/v1/search/movies", {
        params: {
          query: {
            query: q,
            type: t,
            year: yearNum,
          },
        },
      });
      if (error) {
        toast.error("搜索失败", error.message || "未能检索到影片");
        setMovies([]);
        return;
      }
      setMovies((data?.results as MediaItem[]) || []);
      setSearched(true);
    } finally {
      setSearching(false);
    }
  };

  const resetSearch = () => {
    setSearched(false);
    setSearchQuery("");
    setSearchYear("");
    setMovies([]);
  };

  const quickSearch = (title: string, t: "all" | "movie" | "tv") => {
    setSearchQuery(title);
    setSearchType(t);
    setSearchYear("");
    handleSearch(title, t);
  };

  const openMediaDetail = (m: MediaItem) => {
    setSelectedDetailMedia(m);
    setShowDetailModal(true);
  };

  const subscribeMedia = (
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

  const openReleaseSearch = async (m: MediaItem) => {
    setShowDetailModal(false);setCurrentMedia(null);setShowReleaseModal(true);
    // Discovery IDs belong to TMDB; global title search carries no local media UUID.
    if(await releaseSearch.start(m.title))setCurrentMedia(m);
  };



  return (
    <div className="discover-container">
      {subscriptionCreation.message && (
        <div className="empty-hint" role="status">
          <p>{subscriptionCreation.message}</p>
          {subscriptionCreation.canRecover && <button type="button" className="btn-secondary btn-sm" onClick={() => void subscriptionCreation.recover()}>读取原订阅结果</button>}
          {subscriptionCreation.canRetryOriginal && <button type="button" className="btn-secondary btn-sm" onClick={() => void subscriptionCreation.retryOriginal()}>重试原订阅请求</button>}
        </div>
      )}
      {/* Top Header */}
      <div className="page-header">
        <div>
          <h2>影视探索与选季订阅</h2>
          <p className="subtitle">
            聚合 TMDB 官方实时推荐与海量榜单，支持电影与电视剧全类型检索、选季订阅与种子下载
          </p>
        </div>
        <div className="header-actions">
          <button
            type="button"
            className="btn-secondary btn-sm"
            disabled={loadingFeed}
            onClick={() => refreshDiscoverData(true)}
            title="刷新推荐数据"
          >
            <RefreshCw size={14} className={loadingFeed ? "spinner-icon" : ""} />
            <span>{loadingFeed ? "刷新中..." : "刷新数据"}</span>
          </button>
        </div>
      </div>

      {/* Featured Spotlight Banner (MoviePilot Style) */}
      {!searched && spotlightItem && (
        <div
          className="spotlight-banner"
          style={{
            backgroundImage: spotlightItem.backdrop_url
              ? `linear-gradient(to right, rgba(9, 13, 22, 0.95) 0%, rgba(9, 13, 22, 0.8) 40%, rgba(9, 13, 22, 0.3) 100%), url(${spotlightItem.backdrop_url})`
              : undefined,
          }}
        >
          <div className="spotlight-content">
            <span className="spotlight-pill">
              🔥 今日热门焦点 · {spotlightItem.media_type === "tv" ? "电视剧" : "电影"}
            </span>
            <h1 className="spotlight-title">{spotlightItem.title}</h1>
            <p className="spotlight-desc">{spotlightItem.overview}</p>
            <div className="spotlight-actions">
              <button
                className="btn-primary"
                onClick={() => openMediaDetail(spotlightItem)}
              >
                <Rss size={16} />
                <span>🌟 立即追更订阅</span>
              </button>
              <button
                className="btn-secondary"
                onClick={() => openReleaseSearch(spotlightItem)}
              >
                <Search size={16} />
                <span>🔍 检索站点种子</span>
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Modern Search Form */}
      <div className="search-box">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            handleSearch();
          }}
          className="search-form"
        >
          <div className="type-filter">
            <button
              type="button"
              className={`type-btn ${searchType === "all" ? "active" : ""}`}
              onClick={() => {
                setSearchType("all");
                if (searchQuery.trim()) handleSearch(searchQuery, "all");
              }}
            >
              全部
            </button>
            <button
              type="button"
              className={`type-btn ${searchType === "movie" ? "active" : ""}`}
              onClick={() => {
                setSearchType("movie");
                if (searchQuery.trim()) handleSearch(searchQuery, "movie");
              }}
            >
              电影
            </button>
            <button
              type="button"
              className={`type-btn ${searchType === "tv" ? "active" : ""}`}
              onClick={() => {
                setSearchType("tv");
                if (searchQuery.trim()) handleSearch(searchQuery, "tv");
              }}
            >
              电视剧
            </button>
          </div>

          <input
            type="text"
            required
            placeholder="输入电影或电视剧名称 (例如 绝命毒师, Breaking Bad, 权力的游戏, 我不是药神)..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
          />
          <input
            type="number"
            placeholder="年份 (可选)"
            className="year-input"
            value={searchYear}
            onChange={(e) => setSearchYear(e.target.value)}
          />
          <button type="submit" className="btn-primary" disabled={searching}>
            {searching ? <Loader2 size={16} className="spinner-icon" /> : <Search size={16} />}
            <span>{searching ? "搜索中..." : "搜索影视"}</span>
          </button>
          {searched && (
            <button type="button" className="btn-secondary" onClick={resetSearch}>
              返回推荐流
            </button>
          )}
        </form>
      </div>

      {/* Quick Search Recommendations */}
      <div className="recommend-box">
        <span className="recommend-label">快捷搜索建议:</span>
        <div className="tag-list">
          <button
            type="button"
            className="tag-btn"
            onClick={() => quickSearch("Breaking Bad", "tv")}
          >
            绝命毒师 (Breaking Bad)
          </button>
          <button
            type="button"
            className="tag-btn"
            onClick={() => quickSearch("Game of Thrones", "tv")}
          >
            权力的游戏 (TV)
          </button>
          <button
            type="button"
            className="tag-btn"
            onClick={() => quickSearch("我不是药神", "movie")}
          >
            我不是药神
          </button>
          <button
            type="button"
            className="tag-btn"
            onClick={() => quickSearch("流浪地球", "movie")}
          >
            流浪地球
          </button>
        </div>
      </div>

      {/* Source Category Tabs (Trending / Movies / TV / Douban) */}
      {!searched && (
        <div className="tabs-bar">
          <button
            className={`tab-btn ${activeCategory === "trending" ? "active" : ""}`}
            onClick={() => switchCategory("trending")}
          >
            <Sparkles size={14} />
            <span>🔥 热门流行 (20+)</span>
          </button>
          <button
            className={`tab-btn ${activeCategory === "movies" ? "active" : ""}`}
            onClick={() => switchCategory("movies")}
          >
            <Film size={14} />
            <span>🎬 热门电影 (20+)</span>
          </button>
          <button
            className={`tab-btn ${activeCategory === "tv" ? "active" : ""}`}
            onClick={() => switchCategory("tv")}
          >
            <Tv size={14} />
            <span>📺 热门剧集 (20+)</span>
          </button>
          <button
            className={`tab-btn tab-calendar ${activeCategory === "douban" ? "active" : ""}`}
            onClick={() => switchCategory("douban")}
          >
            <Trophy size={14} />
            <span>🏆 豆瓣 Top250</span>
          </button>
        </div>
      )}

      {/* Genre Filter Pills with Real Live Filtering */}
      <div className="recommend-box">
        <span className="recommend-label">题材筛选:</span>
        <div className="tag-list">
          {["全部", "剧情", "动作", "科幻", "悬疑", "犯罪", "动画", "喜剧", "惊悚", "冒险", "战争"].map(
            (g) => (
              <button
                key={g}
                type="button"
                className={`tag-btn ${selectedGenre === g ? "tag-btn-active" : ""}`}
                onClick={() => setSelectedGenre(g)}
              >
                {g}
              </button>
            )
          )}
        </div>
      </div>

      {!searched && feedError && <div role="alert" className="config-error">{feedError} <button className="btn-sm btn-secondary" onClick={() => refreshDiscoverData(false)}>重新加载推荐</button></div>}

      {/* Search State / Loading State / Display Grid */}
      {searching || loadingFeed ? (
        <div className="loading-state">
          <div className="spinner"></div>
          <p>正在加载影视数据…</p>
        </div>
      ) : currentDisplayList.length > 0 ? (
        <div className="movie-grid">
          {currentDisplayList.map((m) => (
            <MediaCard
              key={`${m.tmdb_id}-${m.media_type}`}
              media={m}
              subscribing={subscribingId === m.tmdb_id}
              onClick={openMediaDetail}
              onSubscribe={() => openMediaDetail(m)}
              onSearch={openReleaseSearch}
            />
          ))}
        </div>
      ) : feedError && !searched ? null : (
        <div className="empty-state" role="status">
          <h3>{searched || selectedGenre !== "全部" ? "当前筛选没有影视" : "暂无推荐影视"}</h3>
          <p>{searched || selectedGenre !== "全部" ? "可调整题材筛选或更换搜索关键词。" : "元数据源成功返回空列表，可稍后重新加载。"}</p>
        </div>
      )}

      {/* Media Detail & Season Picker Modal */}
      {showDetailModal && (
        <MediaDetailModal
          media={selectedDetailMedia}
          subscribing={subscribingId === selectedDetailMedia?.tmdb_id}
          onClose={() => setShowDetailModal(false)}
          onSubscribe={subscribeMedia}
          onSearchReleases={openReleaseSearch}
        />
      )}

      {releaseSearch.recovery&&!showReleaseModal&&<button type="button" className="btn-secondary" onClick={()=>{setCurrentMedia(null);setShowReleaseModal(true);releaseSearch.refresh();}}>恢复资源搜索观察</button>}
      {/* Torrent Releases Modal */}
      {showReleaseModal && (
        <TorrentSearchModal
          mediaTitle={currentMedia?.title || ""}
          movieId={currentMedia?.tmdb_id}
          mediaType={currentMedia?.media_type??"movie"}
          releases={releaseSearch.results}
          search={releaseSearch}
          loading={releaseSearch.loading}
          onClose={() => {releaseSearch.pause();void releaseSearch.cancel();setShowReleaseModal(false);}}
        />
      )}
    </div>
  );
};

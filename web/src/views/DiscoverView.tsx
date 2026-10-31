import React, { useState, useEffect, useMemo } from "react";
import { useNavigate, useLocation } from "react-router-dom";
import { Search, Loader2, Sparkles, Film, Tv, Trophy, RefreshCw } from "lucide-react";
import { useAuth } from "@/features/auth/AuthContext";
import { useDiscoveryCatalog } from "@/features/configuration/useDiscoveryCatalog";
import { matchesDiscoveryGenre, isDoubanCategory, type DiscoveryCategory } from "@/features/configuration/discoveryCatalog";
import { useToast } from "@/features/toast/ToastContext";
import { MediaCard, type MediaItem } from "@/components/cards/MediaCard";
import { MediaDetailModal } from "@/components/dialog/MediaDetailModal";
import { TorrentSearchModal } from "@/components/dialog/TorrentSearchModal";
import {useInteractiveReleaseSearch} from "@/features/configuration/useInteractiveReleaseSearch";
import { useSubscriptionCreation } from "@/features/configuration/useSubscriptionCreation";
import { subscriptionScope } from "@/features/configuration/subscriptionCreationRecovery";
import { BangumiCalendarPanel } from "@/features/configuration/BangumiCalendarPanel";



export const DiscoverView: React.FC = () => {
  const navigate = useNavigate();
  const location = useLocation();
  const toast = useToast();
  const {user} = useAuth();

  const [activeCategory, setActiveCategory] = useState<DiscoveryCategory>("trending");
  const [selectedGenre, setSelectedGenre] = useState<string>("全部");

  const [searchQuery, setSearchQuery] = useState("");
  const [searchType, setSearchType] = useState<"all" | "movie" | "tv">("all");
  const [searchYear, setSearchYear] = useState<string>("");
  const {feedItems, movies, searching, searched, loadingFeed, feedError, searchError,
    search: searchCatalog, refreshFeed, resetSearch: resetCatalogSearch} = useDiscoveryCatalog({
      category: activeCategory, locationKey: location.key, userId: user?.id, role: user?.role,
    });
  const subscriptionCreation = useSubscriptionCreation({ onSuccess: (result) => {
    setShowDetailModal(false);
    toast.success(result.title ? `《${result.title}》订阅成功` : "订阅成功", "订阅意图已确认，可在订阅页查看季集范围与获取状态。");
    navigate(`/subscriptions?subscription=${result.id}`);
  } });
  const isSubscribing = (media: MediaItem | null) => !!media && subscriptionCreation.busy
    && subscriptionCreation.recovery?.body.tmdb_id === media.tmdb_id
    && (subscriptionCreation.recovery.body.media_type ?? "movie") === (media.media_type ?? "movie");


  // Detail Modal
  const [showDetailModal, setShowDetailModal] = useState(false);
  const [selectedDetailMedia, setSelectedDetailMedia] = useState<MediaItem | null>(null);

  // Release Search Modal
  const [showReleaseModal, setShowReleaseModal] = useState(false);
  const [currentMedia, setCurrentMedia] = useState<MediaItem | null>(null);
  const releaseSearch=useInteractiveReleaseSearch({scope:"discover"});

  useEffect(() => {
    setShowDetailModal(false); setShowReleaseModal(false);
    setSelectedDetailMedia(null); setCurrentMedia(null);
  }, [user?.id, user?.role]);

  const switchCategory = (category: DiscoveryCategory) => {
    resetCatalogSearch();
    setActiveCategory(category);
    setSelectedGenre("全部");
    setSearchQuery("");
    setSearchYear("");
  };

  const refreshDiscoverData = async (notify = false) => {
    const refreshed = await refreshFeed();
    if (notify) {
      if (refreshed) toast.success("推荐列表已刷新", "已读取推荐数据。");
    }
  };

  const currentDisplayList = useMemo(() => (searched ? movies : feedItems)
    .filter(media => matchesDiscoveryGenre(media, selectedGenre)), [searched, movies, feedItems, selectedGenre]);
  const handleSearch = (queryText = searchQuery, type = searchType, year = searchYear) =>
    searchCatalog(queryText, type, year);

  const resetSearch = () => {
    resetCatalogSearch(); setSearchQuery(""); setSearchYear("");
  };

  const quickSearch = (title: string, type: "all" | "movie" | "tv") => {
    setSearchQuery(title); setSearchType(type); setSearchYear("");
    void handleSearch(title, type, "");
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
          <h1>探索发现</h1>
          <p className="subtitle">
            发现值得收藏的电影与剧集，按喜好订阅。
          </p>
        </div>
        <div className="header-actions">
          {activeCategory !== "bangumi" && (
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
          )}
        </div>
      </div>


      {/* Modern Search Form */}
      {/* One control panel: search, data source, ranking and genre stay in a single surface. */}
      <section className="discover-controls" aria-label="探索发现筛选">
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
            aria-label="影视名称"
          />
          <input
            type="number"
            placeholder="年份 (可选)"
            className="year-input"
            value={searchYear}
            aria-label="上映或首播年份" min={1800} max={9999} step={1}
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
        <div className="type-filter source-filter" role="tablist" aria-label="数据源选择" style={{ width: "fit-content", marginBottom: "1rem" }}>
          <button
            type="button"
            className={`type-btn ${activeCategory !== "bangumi" && !isDoubanCategory(activeCategory) ? "active" : ""}`}
            onClick={() => switchCategory("trending")}
            aria-selected={activeCategory !== "bangumi" && !isDoubanCategory(activeCategory)}
          >
            <Sparkles size={14} style={{ marginRight: "0.35rem", verticalAlign: "middle" }} />
            <span>TMDB 影视流</span>
          </button>
          <button
            type="button"
            className={`type-btn ${isDoubanCategory(activeCategory) ? "active" : ""}`}
            onClick={() => switchCategory("douban_hot_movie")}
            aria-selected={isDoubanCategory(activeCategory)}
          >
            <Trophy size={14} style={{ marginRight: "0.35rem", verticalAlign: "middle" }} />
            <span>豆瓣精选榜</span>
          </button>
          <button type="button" role="tab" className={`type-btn ${activeCategory === "bangumi" ? "active" : ""}`} aria-selected={activeCategory === "bangumi"} onClick={() => switchCategory("bangumi")}>动漫新番周历</button>
        </div>
      )}

      {!searched && activeCategory !== "bangumi" && !isDoubanCategory(activeCategory) && (
        <div className="tabs-bar" role="group" aria-label="TMDB榜单">
          <button
            className={`tab-btn ${activeCategory === "trending" ? "active" : ""}`}
            aria-pressed={activeCategory === "trending"}
            onClick={() => switchCategory("trending")}
          >
            <Sparkles size={14} />
            <span>热门流行</span>
          </button>
          <button
            className={`tab-btn ${activeCategory === "movie" ? "active" : ""}`}
            aria-pressed={activeCategory === "movie"}
            onClick={() => switchCategory("movie")}
          >
            <Film size={14} />
            <span>热门电影</span>
          </button>
          <button
            className={`tab-btn ${activeCategory === "tv" ? "active" : ""}`}
            aria-pressed={activeCategory === "tv"}
            onClick={() => switchCategory("tv")}
          >
            <Tv size={14} />
            <span>热门剧集</span>
          </button>
          <button
            type="button"
            className={`tab-btn ${activeCategory === "now_playing" ? "active" : ""}`}
            aria-pressed={activeCategory === "now_playing"}
            onClick={() => switchCategory("now_playing")}
          >
            <Film size={14} />
            <span>正在热映</span>
          </button>
          <button
            className={`tab-btn tab-calendar ${activeCategory === "top_rated" ? "active" : ""}`}
            aria-pressed={activeCategory === "top_rated"}
            onClick={() => switchCategory("top_rated")}
          >
            <Trophy size={14} />
            <span>TMDB 高分</span>
          </button>
        </div>
      )}

      {!searched && isDoubanCategory(activeCategory) && (
        <div className="tabs-bar" role="group" aria-label="豆瓣榜单">
          <button
            className={`tab-btn ${activeCategory === "douban_hot_movie" ? "active" : ""}`}
            aria-pressed={activeCategory === "douban_hot_movie"}
            onClick={() => switchCategory("douban_hot_movie")}
          >
            <Film size={14} />
            <span>豆瓣热门电影</span>
          </button>
          <button
            className={`tab-btn ${activeCategory === "douban_hot_tv" ? "active" : ""}`}
            aria-pressed={activeCategory === "douban_hot_tv"}
            onClick={() => switchCategory("douban_hot_tv")}
          >
            <Tv size={14} />
            <span>豆瓣热门剧集</span>
          </button>
          <button
            className={`tab-btn ${activeCategory === "douban_weekly_chinese" ? "active" : ""}`}
            aria-pressed={activeCategory === "douban_weekly_chinese"}
            onClick={() => switchCategory("douban_weekly_chinese")}
          >
            <Trophy size={14} />
            <span>华语口碑周榜</span>
          </button>
          <button
            className={`tab-btn ${activeCategory === "douban_weekly_global" ? "active" : ""}`}
            aria-pressed={activeCategory === "douban_weekly_global"}
            onClick={() => switchCategory("douban_weekly_global")}
          >
            <Trophy size={14} />
            <span>全球口碑周榜</span>
          </button>
          <button
            className={`tab-btn tab-calendar ${activeCategory === "douban_top250" ? "active" : ""}`}
            aria-pressed={activeCategory === "douban_top250"}
            onClick={() => switchCategory("douban_top250")}
          >
            <Trophy size={14} />
            <span>豆瓣电影 Top 250</span>
          </button>
        </div>
      )}

      {!searched && activeCategory === "bangumi" && <BangumiCalendarPanel key={`${user?.id}:${user?.role}:${location.key}`} onSelect={openMediaDetail} />}
      {!(activeCategory === "bangumi" && !searched) && <>
      {/* Genre Filter Pills with Real Live Filtering */}
      <div className="recommend-box">
        <span className="recommend-label">题材筛选（当前列表）:</span>
        <div className="tag-list">
          {["全部", "剧情", "动作", "科幻", "悬疑", "犯罪", "动画", "喜剧", "惊悚", "冒险", "战争"].map(
            (g) => (
              <button
                key={g}
                type="button"
                className={`tag-btn ${selectedGenre === g ? "tag-btn-active" : ""}`}
                aria-pressed={selectedGenre === g}
                onClick={() => setSelectedGenre(g)}
              >
                {g}
              </button>
            )
          )}
        </div>
      </div>
      </>}

      </section>
      {!searched && feedError && <div role="alert" className="config-error">{feedError} <button className="btn-sm btn-secondary" onClick={() => refreshDiscoverData(false)}>重新加载推荐</button></div>}
      {searched && searchError && <div role="alert" className="config-error">{searchError} <button type="button" className="btn-sm btn-secondary" disabled={searching} onClick={() => void handleSearch()}>重试当前搜索</button></div>}

      {!(activeCategory === "bangumi" && !searched) && <>
      {/* Search State / Loading State / Display Grid */}
      {searching || (!searched && loadingFeed && feedItems.length === 0) ? (
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
              subscribing={isSubscribing(m)}
              onClick={openMediaDetail}
              onSubscribe={() => openMediaDetail(m)}
              onSearch={openReleaseSearch}
            />
          ))}
        </div>
      ) : (searched ? !!searchError : !!feedError) ? null : (
        <div className="empty-state" role="status">
          <h3>{searched || selectedGenre !== "全部" ? "当前筛选没有影视" : "暂无推荐影视"}</h3>
          <p>{searched || selectedGenre !== "全部" ? "可调整题材筛选或更换搜索关键词。" : "元数据源成功返回空列表，可稍后重新加载。"}</p>
        </div>
      )}
      </>}

      {/* Media Detail & Season Picker Modal */}
      {showDetailModal && (
        <MediaDetailModal
          media={selectedDetailMedia}
          subscribing={isSubscribing(selectedDetailMedia)}
          onClose={() => setShowDetailModal(false)}
          onSubscribe={subscribeMedia}
          onSearchReleases={openReleaseSearch}
          onRequestApproval={media => {
            if (!Number.isSafeInteger(media.tmdb_id) || media.tmdb_id <= 0 || (media.media_type !== "movie" && media.media_type !== "tv")) return;
            const query = new URLSearchParams({ media_type: media.media_type, tmdb_id: String(media.tmdb_id), title: media.title });
            setShowDetailModal(false);
            navigate(`/requests?${query.toString()}`);
          }}
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
          onClose={() => {releaseSearch.pause();setShowReleaseModal(false);}}
        />
      )}
    </div>
  );
};

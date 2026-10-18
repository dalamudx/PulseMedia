import React, { useState, useEffect, useMemo, useRef } from "react";
import { useDialogFocus } from "@/features/configuration/useDialogFocus";
import { X, Rss, Search, Star, Clock, Calendar, Check, Layers, Loader2 } from "lucide-react";
import { api } from "@/api/client";
import type { MediaItem } from "@/components/cards/MediaCard";
import type { paths } from "@/api/client";
import { EndpointBindingFields } from "@/features/configuration/EndpointBindingFields";
import { PolicySelector } from "@/features/configuration/PolicySelector";
import { RootSelector } from "@/features/configuration/RootSelector";

type TVDetailsResponse = paths["/api/v1/media/tmdb/tv/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
type MovieDetailsResponse = paths["/api/v1/media/tmdb/movie/{id}"]["get"]["responses"]["200"]["content"]["application/json"];

interface MediaDetailModalProps {
  media: MediaItem | null;
  subscribing?: boolean;
  onClose: () => void;
  onSubscribe: (
    m: MediaItem,
    scope: "all" | "selected",
    seasons: number[],
    episodes: string[],
    includeSpecials: boolean,
    downloaderId?: string,
    indexerIds?: string[],
    policyId?: string,
    rootId?: string
  ) => void;
  onSearchReleases: (m: MediaItem) => void;
}

export const MediaDetailModal: React.FC<MediaDetailModalProps> = ({
  media,
  subscribing,
  onClose,
  onSubscribe,
  onSearchReleases,
}) => {
  const dialog = useRef<HTMLDivElement>(null);
  useDialogFocus(dialog, onClose);
  const [loading, setLoading] = useState(false);
  const [tvDetails, setTvDetails] = useState<TVDetailsResponse | null>(null);
  const [movieDetails, setMovieDetails] = useState<MovieDetailsResponse | null>(null);

  // Subscription options
  const [subscribeMode, setSubscribeMode] = useState<"all" | "selected">("all");
  const [selectedSeasons, setSelectedSeasons] = useState<number[]>([]);
  const [activeSeasonView, setActiveSeasonView] = useState<number>(1);
  const [includeSpecials, setIncludeSpecials] = useState<boolean>(false);
  const [selectedIndexerIds, setSelectedIndexerIds] = useState<string[]>([]);
  const [bindingsReady, setBindingsReady] = useState(false);
  const [policyId,setPolicyId]=useState("default");const [policyReady,setPolicyReady]=useState(false);
const [rootId,setRootId]=useState("");const [rootReady,setRootReady]=useState(false);

  // Selected episodes per season: Record<seasonNumber, number[]>
  const [selectedEpisodesMap, setSelectedEpisodesMap] = useState<Record<number, number[]>>({});
  // For Shift + click range selection
  const [lastClickedEpisode, setLastClickedEpisode] = useState<number | null>(null);
  const [selectedDownloaderId, setSelectedDownloaderId] = useState<string>("");

  useEffect(() => {
    setTvDetails(null); setMovieDetails(null); setSelectedIndexerIds([]); setSelectedDownloaderId(""); setBindingsReady(false);setPolicyId("default");setPolicyReady(false);setRootId("");setRootReady(false);
    setSubscribeMode("all"); setSelectedSeasons([]); setSelectedEpisodesMap({}); setIncludeSpecials(false); setLastClickedEpisode(null);
    if (!media) return;
    const controller = new AbortController();
    setLoading(true);
    const request = media.media_type === "tv"
      ? api.GET("/api/v1/media/tmdb/tv/{id}", { params: { path: { id: media.tmdb_id } }, signal: controller.signal }).then(({ data }) => {
          if (!data || controller.signal.aborted) return;
          setTvDetails(data);
          const regularSeasons = data.seasons.filter(season => season.season_number > 0);
          setSelectedSeasons(regularSeasons.map(season => season.season_number));
          const initialMap: Record<number, number[]> = {};
          for (const season of data.seasons) initialMap[season.season_number] = Array.from({ length: season.episode_count }, (_, index) => index + 1);
          setSelectedEpisodesMap(initialMap); setActiveSeasonView(regularSeasons[0]?.season_number ?? 1);
        })
      : api.GET("/api/v1/media/tmdb/movie/{id}", { params: { path: { id: media.tmdb_id } }, signal: controller.signal }).then(({ data }) => { if (data && !controller.signal.aborted) setMovieDetails(data); });
    request.catch(() => {}).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [media?.tmdb_id, media?.media_type]);

  const currentSeasonSelectedEps = useMemo(() => {
    return selectedEpisodesMap[activeSeasonView] || [];
  }, [selectedEpisodesMap, activeSeasonView]);

  // Total selected episodes count across all chosen seasons
  const totalSelectedEpsCount = useMemo(() => {
    if (subscribeMode === "all") {
      return tvDetails?.seasons?.reduce((acc, s) => acc + (s.season_number > 0 ? s.episode_count : 0), 0) || 0;
    }
    let count = 0;
    for (const sn of selectedSeasons) {
      count += (selectedEpisodesMap[sn] || []).length;
    }
    return count;
  }, [subscribeMode, selectedSeasons, selectedEpisodesMap, tvDetails]);

  if (!media) return null;
  const isTV = media.media_type === "tv";

  const backdropURL = isTV
    ? tvDetails?.backdrop_url || media.poster_url
    : movieDetails?.backdrop_url || media.poster_url;

  const ratingVal = isTV
    ? tvDetails?.rating || media.rating
    : movieDetails?.rating || media.rating;

  const genresList = isTV
    ? tvDetails?.genres || []
    : movieDetails?.genres || [];

  const toggleSeasonSelect = (sn: number) => {
    if (selectedSeasons.includes(sn)) {
      if (selectedSeasons.length > 1) {
        setSelectedSeasons(selectedSeasons.filter((s) => s !== sn));
      }
    } else {
      setSelectedSeasons([...selectedSeasons, sn].sort((a, b) => a - b));
    }
  };

  const activeSeasonData = tvDetails?.seasons?.find(
    (s) => s.season_number === activeSeasonView
  );


  const selectAllInSeason = () => {
    if (!activeSeasonData) return;
    const all = Array.from({ length: activeSeasonData.episode_count }, (_, i) => i + 1);
    setSelectedEpisodesMap((prev) => ({ ...prev, [activeSeasonView]: all }));
  };

  const invertSeasonSelection = () => {
    if (!activeSeasonData) return;
    const current = new Set(selectedEpisodesMap[activeSeasonView] || []);
    const inverted: number[] = [];
    for (let i = 1; i <= activeSeasonData.episode_count; i++) {
      if (!current.has(i)) inverted.push(i);
    }
    setSelectedEpisodesMap((prev) => ({ ...prev, [activeSeasonView]: inverted }));
  };

  const clearSeasonSelection = () => {
    setSelectedEpisodesMap((prev) => ({ ...prev, [activeSeasonView]: [] }));
  };


  const selectFromEpisodeOnwards = (startEp: number) => {
    if (!activeSeasonData) return;
    const newSelected: number[] = [];
    for (let i = startEp; i <= activeSeasonData.episode_count; i++) {
      newSelected.push(i);
    }
    setSelectedEpisodesMap((prev) => ({
      ...prev,
      [activeSeasonView]: newSelected,
    }));
    if (!selectedSeasons.includes(activeSeasonView)) {
      setSelectedSeasons([...selectedSeasons, activeSeasonView]);
    }
    setLastClickedEpisode(startEp);
  };

  // Shift + Click Range Selection implementation
  const handleEpisodeClick = (epNum: number, event: React.MouseEvent) => {
    if (!activeSeasonData) return;
    const currentList = selectedEpisodesMap[activeSeasonView] || [];
    const currentSet = new Set(currentList);

    if (event.shiftKey && lastClickedEpisode !== null && lastClickedEpisode !== epNum) {
      const start = Math.min(lastClickedEpisode, epNum);
      const end = Math.max(lastClickedEpisode, epNum);
      const shouldSelect = currentSet.has(lastClickedEpisode);

      for (let i = start; i <= end; i++) {
        if (shouldSelect) {
          currentSet.add(i);
        } else {
          currentSet.delete(i);
        }
      }
    } else {
      if (currentSet.has(epNum)) {
        currentSet.delete(epNum);
      } else {
        currentSet.add(epNum);
      }
      setLastClickedEpisode(epNum);
    }

    setSelectedEpisodesMap((prev) => ({
      ...prev,
      [activeSeasonView]: Array.from(currentSet).sort((a, b) => a - b),
    }));
  };

  const handleSubscribeSubmit = () => {
    if (subscribeMode === "all") {
      onSubscribe(media, "all", [], [], includeSpecials, selectedDownloaderId || undefined, selectedIndexerIds, policyId,rootId);
      return;
    }

    // Construct array of unit keys: e.g. ["S01E01", "S01E02", "S02E05"]
    const episodesToSend: string[] = [];
    for (const sn of selectedSeasons) {
      const eps = selectedEpisodesMap[sn] || [];
      for (const ep of eps) {
        const sStr = sn < 10 ? `0${sn}` : `${sn}`;
        const epStr = ep < 10 ? `0${ep}` : `${ep}`;
        episodesToSend.push(`S${sStr}E${epStr}`);
      }
    }

    onSubscribe(media, "selected", selectedSeasons, episodesToSend, includeSpecials, selectedDownloaderId || undefined, selectedIndexerIds, policyId,rootId);
  };


  return (
    <div className="modal-overlay" onClick={onClose}>
      <div ref={dialog} role="dialog" aria-modal="true" aria-label={`${media.title}详情`} tabIndex={-1} className="modal-card detail-modal" onClick={(e) => e.stopPropagation()}>
        {/* Banner with Backdrop */}
        <div
          className="detail-banner"
          style={{
            backgroundImage: backdropURL ? `url(${backdropURL})` : "none",
          }}
        >
          <button className="close-btn detail-close" onClick={onClose} title="关闭">
            <X size={18} />
          </button>
          <div className="banner-overlay">
            <div className="banner-meta">
              <span className={`badge ${isTV ? "tv" : "movie"}`}>
                {isTV ? "电视剧 (TV Series)" : "电影 (Movie)"}
              </span>
              {ratingVal && ratingVal > 0 && (
                <span className="badge-rating-lg">
                  <Star size={14} className="star-icon" />
                  <span>{ratingVal.toFixed(1)}</span>
                </span>
              )}
              {isTV && tvDetails && (
                <span className="badge-info-pill">
                  <Layers size={13} />
                  <span>共 {tvDetails.seasons.filter((s) => s.season_number > 0).length} 季</span>
                </span>
              )}
              {!isTV && movieDetails?.runtime && (
                <span className="badge-info-pill">
                  <Clock size={13} />
                  <span>{movieDetails.runtime} 分钟</span>
                </span>
              )}
              {media.year && (
                <span className="badge-info-pill">
                  <Calendar size={13} />
                  <span>{media.year} 年</span>
                </span>
              )}
            </div>

            <h2>{media.title}</h2>
            {isTV && tvDetails?.original_title && (
              <span className="banner-original-title">{tvDetails.original_title}</span>
            )}
            {!isTV && movieDetails?.original_title && (
              <span className="banner-original-title">{movieDetails.original_title}</span>
            )}

            {genresList.length > 0 && (
              <div className="banner-genres">
                {genresList.map((g) => (
                  <span key={g} className="genre-pill">
                    {g}
                  </span>
                ))}
              </div>
            )}
          </div>
        </div>

        {/* Modal Scrollable Body */}
        <div className="detail-body">
          {loading ? (
            <div className="detail-loading-box">
              <Loader2 size={28} className="spinner-icon" />
              <span>正在从 TMDB 获取全量剧集与元数据...</span>
            </div>
          ) : (
            <>
              {/* Plot Overview */}
              <div className="detail-overview">
                <h4>剧情简介</h4>
                <p>
                  {(isTV ? tvDetails?.overview : movieDetails?.overview) ||
                    media.overview ||
                    "暂无详细剧情简介。"}
                </p>
              </div>

              {/* Complete Seasons & Episodes Matrix for TV */}
              {isTV && tvDetails && (
                <div className="season-matrix-section">
                  <div className="section-sub-header">
                    <h4>季集剧集全览与选季选集 (全量 {tvDetails.seasons.length} 季):</h4>
                    <span className="sub-helper">
                      {subscribeMode === "selected"
                        ? `已勾选第 ${selectedSeasons.join("、")} 季 · 共计 ${totalSelectedEpsCount} 集`
                        : "全剧追更监控已开启"}
                    </span>
                  </div>

                  {/* Mode Selector */}
                  <div className="subscribe-mode-bar">
                    <button
                      type="button"
                      className={`mode-btn ${subscribeMode === "all" ? "active" : ""}`}
                      onClick={() => setSubscribeMode("all")}
                    >
                      🌟 全剧追更监控 (自动包含所有已播季与未来连载季)
                    </button>
                    <button
                      type="button"
                      className={`mode-btn ${subscribeMode === "selected" ? "active" : ""}`}
                      onClick={() => {
                        setSubscribeMode("selected");
                        setSelectedSeasons([activeSeasonView]);
                      }}
                    >
                      🎯 自定义指定季与选集监控
                    </button>
                  </div>

                  {/* Season Chips Selector */}
                  <div className="seasons-chips-grid">
                    {tvDetails.seasons.map((s) => {
                      const isSelected = selectedSeasons.includes(s.season_number);
                      const isViewing = activeSeasonView === s.season_number;
                      const epsCount = (selectedEpisodesMap[s.season_number] || []).length;

                      return (
                        <div
                          key={s.season_number}
                          className={`season-chip-card ${isSelected ? "selected" : ""} ${isViewing ? "viewing" : ""}`}
                          onClick={() => {
                            setActiveSeasonView(s.season_number);
                            if (subscribeMode === "selected") {
                              setSelectedSeasons([s.season_number]);
                            }
                          }}
                        >
                          <div className="season-card-top">
                            <span className="season-name">{s.name}</span>
                            {subscribeMode === "selected" && (
                              <button
                                type="button"
                                className={`season-check-btn ${isSelected ? "checked" : ""}`}
                                onClick={(e) => {
                                  e.stopPropagation();
                                  toggleSeasonSelect(s.season_number);
                                }}
                              >
                                {isSelected ? <Check size={12} /> : null}
                              </button>
                            )}
                          </div>
                          <div className="season-card-meta">
                            <span>
                              {subscribeMode === "selected" && isSelected
                                ? `已选 ${epsCount}/${s.episode_count} 集`
                                : `${s.episode_count} 集`}
                            </span>
                            {s.air_date && <span> · {s.air_date.slice(0, 4)}</span>}
                          </div>
                        </div>
                      );
                    })}
                  </div>

                  {/* Episode Matrix for the Active Season (支持单集选择、全选/反选与 Shift+Click 快速连选) */}
                  {activeSeasonData && (
                    <div className="episodes-preview-box">
                      <div className="episodes-preview-header">
                        <div className="episodes-preview-title">
                          <strong>
                            {activeSeasonData.name} · 每集列表与选集 (共 {activeSeasonData.episode_count} 集):
                          </strong>
                          {subscribeMode === "selected" && (
                            <span className="ep-batch-count">
                              已勾选 <strong>{currentSeasonSelectedEps.length}</strong> / {activeSeasonData.episode_count} 集
                            </span>
                          )}
                        </div>

                        {/* Batch Action Buttons (全选、反选、清空) */}
                        {subscribeMode === "selected" && (
                          <div className="episodes-batch-actions">
                            <button
                              type="button"
                              className="ep-batch-btn"
                              onClick={selectAllInSeason}
                              title="全选当前季所有集数"
                            >
                              ✓ 全选
                            </button>
                            <button
                              type="button"
                              className="ep-batch-btn"
                              onClick={invertSeasonSelection}
                              title="反选当前季集数"
                            >
                              ⇄ 反选
                            </button>
                            <button
                              type="button"
                              className="ep-batch-btn"
                              onClick={clearSeasonSelection}
                              title="清空当前季选集"
                            >
                              ✕ 清空
                            </button>
                            <button
                              type="button"
                              className="ep-batch-btn ep-batch-highlight"
                              onClick={() => selectFromEpisodeOnwards(lastClickedEpisode || 13)}
                              title={`从第 ${lastClickedEpisode || 13} 集向后全选至完结`}
                            >
                              ⏩ 选第 {lastClickedEpisode || 13} 集及之后
                            </button>
                          </div>
                        )}
                      </div>

                      {/* Shift + Click Hint */}
                      {subscribeMode === "selected" && (
                        <div className="shift-hint-text">
                          💡 快捷选集技巧：点击勾选单集；按住 <strong>Shift 键</strong> 点击两集可快速连选区间内的所有集数。
                        </div>
                      )}

                      <div className="episodes-chips-grid">
                        {Array.from({ length: activeSeasonData.episode_count }, (_, i) => i + 1).map((ep) => {
                          const isSelected = subscribeMode === "all" || currentSeasonSelectedEps.includes(ep);
                          return (
                            <div
                              key={ep}
                              className={`ep-item-chip selectable ${isSelected ? "selected" : ""}`}
                              onClick={(e) => {
                                if (subscribeMode === "selected") {
                                  handleEpisodeClick(ep, e);
                                }
                              }}
                              title={
                                subscribeMode === "selected"
                                  ? `点击切换勾选，或按住 Shift 点击连选至第 ${ep} 集`
                                  : undefined
                              }
                            >
                              {subscribeMode === "selected" && (
                                <div className={`ep-checkbox ${isSelected ? "checked" : ""}`}>
                                  {isSelected && <Check size={11} />}
                                </div>
                              )}
                              <span className="ep-num">E{ep < 10 ? `0${ep}` : ep}</span>
                              <span className="ep-title">第 {ep} 集</span>
                              {subscribeMode === "selected" && (
                                <button
                                  type="button"
                                  className="chip-quick-from"
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    selectFromEpisodeOnwards(ep);
                                  }}
                                  title={`从第 ${ep} 集向后全选至完结`}
                                >
                                  向后选 →
                                </button>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  )}

                  {/* Specials Option */}
                  <div className="specials-option">
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={includeSpecials}
                        onChange={(e) => setIncludeSpecials(e.target.checked)}
                      />
                      <span>包含特别篇 (S00 Specials / 预告花絮)</span>
                    </label>
                  </div>
                </div>
              )}

              <div className="policy-settings-box">
                <p className="config-description">新订阅冻结所选策略当前版本；后续发布会更新关联订阅。自动升级尚未启用；未选索引器时仅保存订阅。</p>
                <RootSelector key={`root-${media.tmdb_id}`} mediaType={isTV?"tv":"movie"} value={rootId} onChange={setRootId} onReady={setRootReady} disabled={subscribing}/>
                <PolicySelector key={`policy-${media.tmdb_id}`} value={policyId} onChange={setPolicyId} onReady={setPolicyReady} disabled={subscribing}/>
                <EndpointBindingFields key={media.tmdb_id} indexerIds={selectedIndexerIds} downloaderId={selectedDownloaderId} disabled={subscribing} onReady={setBindingsReady} onIndexers={setSelectedIndexerIds} onDownloader={setSelectedDownloaderId} />
              </div>

              {/* Action Buttons */}
              <div className="detail-actions">
                <button
                  className="btn-primary btn-lg"
                  disabled={subscribing || !bindingsReady || !policyReady || !rootReady}
                  onClick={handleSubscribeSubmit}
                >
                  <Rss size={18} />
                  <span>
                    {subscribing
                      ? "正在发起订阅..."
                      : subscribeMode === "selected" && selectedSeasons.length > 0
                      ? `保存指定季集订阅 (已选 ${totalSelectedEpsCount} 集)`
                      : isTV ? "保存剧集订阅（自动获取尚未启用）" : "保存电影订阅"}
                  </span>
                </button>
                <button
                  className="btn-secondary btn-lg"
                  onClick={() => onSearchReleases(media)}
                >
                  <Search size={18} />
                  <span>🔍 检索各大 PT 站点种子</span>
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
};

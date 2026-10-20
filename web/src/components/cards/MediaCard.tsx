import React from "react";
import { Film, Tv, Star, Rss, Search } from "lucide-react";

export interface MediaItem {
  tmdb_id: number;
  title: string;
  year?: number;
  media_type?: string;
  overview?: string;
  poster_url?: string;
  backdrop_url?: string;
  rating?: number;
  genres?: string[];
  rating_source?: "tmdb" | "douban";
  douban_id?: string;
}

interface MediaCardProps {
  media: MediaItem;
  subscribing?: boolean;
  onClick: (m: MediaItem) => void;
  onSubscribe: (m: MediaItem) => void;
  onSearch: (m: MediaItem) => void;
}

export const MediaCard: React.FC<MediaCardProps> = ({
  media,
  subscribing,
  onClick,
  onSubscribe,
  onSearch,
}) => {
  const isTV = media.media_type === "tv";
  const ratingVal = media.rating && media.rating > 0 ? media.rating.toFixed(1) : null;

  return (
    <div className="media-card-wrap" onClick={() => onClick(media)}>
      <div className="poster-container">
        {media.poster_url ? (
          <img src={media.poster_url} alt={media.title} className="poster-image" loading="lazy" />
        ) : (
          <div className="poster-empty-box">
            {isTV ? <Tv size={36} /> : <Film size={36} />}
            <span className="empty-title">{media.title}</span>
          </div>
        )}

        {/* Top Badges */}
        <span className={`badge-type ${isTV ? "tv" : "movie"}`}>
          {isTV ? "电视剧" : "电影"}
        </span>

        {ratingVal && (
          <span className={`badge-rating ${media.rating_source === "douban" ? "douban" : ""}`} title={media.rating_source === "douban" ? "豆瓣评分" : "TMDB评分"}>
            <Star size={11} className="star-icon" fill="currentColor" />
            <span>{ratingVal}{media.rating_source === "douban" ? " 豆瓣" : ""}</span>
          </span>
        )}

        {/* Hover Overlay (MoviePilot Style) */}
        <div className="hover-overlay">
          <div className="hover-content">
            <span className="hover-year">{media.year}</span>
            <h4 className="hover-title">{media.title}</h4>
            {media.genres && media.genres.length > 0 && (
              <div className="hover-genres-list">
                {media.genres.slice(0, 3).map((g) => (
                  <span key={g} className="hover-genre-tag">{g}</span>
                ))}
              </div>
            )}
            <p className="hover-overview">{media.overview || "暂无详细剧情简介。"}</p>

            <div className="hover-actions" onClick={(e) => e.stopPropagation()}>
              <button
                type="button"
                className="btn-action btn-sub"
                disabled={subscribing || !media.tmdb_id}
                onClick={() => onSubscribe(media)}
                title={media.tmdb_id ? "一键追更订阅" : "未匹配到 TMDB 身份，请先搜种"}
              >
                <Rss size={13} />
                <span>{subscribing ? "..." : media.tmdb_id ? "追更" : "未匹配"}</span>
              </button>
              <button
                type="button"
                className="btn-action btn-search"
                onClick={() => onSearch(media)}
                title="检索站点种子"
              >
                <Search size={13} />
                <span>搜种</span>
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Caption Below Card */}
      <div className="card-caption">
        <strong className="caption-title" title={media.title}>
          {media.title}
        </strong>
        <span className="caption-year">{media.year || "未知年份"}</span>
      </div>
    </div>
  );
};

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

export const MediaCard: React.FC<MediaCardProps> = ({ media, subscribing, onClick, onSubscribe, onSearch }) => {
  const isTV = media.media_type === "tv";
  const rating = media.rating && media.rating > 0 ? media.rating.toFixed(1) : null;
  return <article className="media-card-wrap">
    <button type="button" className="media-card-open" onClick={() => onClick(media)} aria-label={`查看《${media.title}》详情`}>
      <div className="poster-container">
        <div className="poster-empty-box" aria-hidden="true">
          {isTV ? <Tv size={32} /> : <Film size={32} />}<span className="empty-title">{media.title}</span>
        </div>
        {media.poster_url ? <img key={media.poster_url} src={media.poster_url} alt="" className="poster-image" loading="lazy" decoding="async" referrerPolicy="no-referrer" onError={event => { event.currentTarget.hidden = true; }} /> : null}
        <span className={`badge-type ${isTV ? "tv" : "movie"}`}>{isTV ? "电视剧" : "电影"}</span>
        {rating ? <span className={`badge-rating ${media.rating_source === "douban" ? "douban" : ""}`} title={media.rating_source === "douban" ? "豆瓣评分" : "TMDB评分"}>
          <Star size={12} aria-hidden="true" fill="currentColor" /><span>{rating}{media.rating_source === "douban" ? " 豆瓣" : ""}</span>
        </span> : null}
        <span className="media-poster-hint">查看详情</span>
      </div>
      <div className="card-caption"><strong className="caption-title" title={media.title}>{media.title}</strong><span className="caption-year">{media.year || "未知年份"} · {isTV ? "电视剧" : "电影"}</span></div>
    </button>
    <div className="media-card-actions">
      <button type="button" className="btn-action btn-sub" disabled={subscribing || !media.tmdb_id} onClick={() => onSubscribe(media)}
        aria-label={`订阅《${media.title}》`} title={media.tmdb_id ? "追更订阅" : "未匹配到 TMDB 身份，请先搜种"}>
        <Rss size={14} aria-hidden="true" /><span>{subscribing ? "提交中…" : media.tmdb_id ? "订阅" : "未匹配"}</span>
      </button>
      <button type="button" className="btn-action btn-search" onClick={() => onSearch(media)} aria-label="搜种" title="检索站点种子">
        <Search size={14} aria-hidden="true" /><span>搜种</span>
      </button>
    </div>
  </article>;
};

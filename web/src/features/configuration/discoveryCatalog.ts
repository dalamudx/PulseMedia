import type { MediaItem } from "@/components/cards/MediaCard";

export type TMDBDiscoveryCategory = "trending" | "movie" | "tv" | "top_rated" | "now_playing";
export type DoubanDiscoveryCategory = "douban_hot_movie" | "douban_hot_tv" | "douban_weekly_chinese" | "douban_weekly_global" | "douban_top250";
export type DiscoveryCategory = TMDBDiscoveryCategory | DoubanDiscoveryCategory | "bangumi";

export const doubanCategoryToList: Record<DoubanDiscoveryCategory, "hot_movie" | "hot_tv" | "weekly_chinese" | "weekly_global" | "top250"> = {
  douban_hot_movie: "hot_movie",
  douban_hot_tv: "hot_tv",
  douban_weekly_chinese: "weekly_chinese",
  douban_weekly_global: "weekly_global",
  douban_top250: "top250",
};

export function isDoubanCategory(category: DiscoveryCategory): category is DoubanDiscoveryCategory {
  return category in doubanCategoryToList;
}
export type DiscoverySearchType = "all" | "movie" | "tv";

// TMDB TV labels combine genres that the discovery filters expose separately.
const compoundGenres: Readonly<Record<string, readonly string[]>> = {
  "动作冒险": ["动作", "冒险"],
  "动作与冒险": ["动作", "冒险"],
  "动作和冒险": ["动作", "冒险"],
  "科幻与奇幻": ["科幻", "奇幻"],
  "科幻和奇幻": ["科幻", "奇幻"],
  "战争与政治": ["战争", "政治"],
  "战争和政治": ["战争", "政治"],
};

export function matchesDiscoveryGenre(media: MediaItem, genre: string): boolean {
  if (genre === "全部" || genre === "All") return true;
  return !!genre && !!media.genres?.some(label =>
    label === genre || (Object.hasOwn(compoundGenres, label) && compoundGenres[label].includes(genre)));
}

export function discoverySearchInput(query: string, type: DiscoverySearchType, year: string):
  { error: string } | { query: string; type: DiscoverySearchType; year?: number } {
  const trimmed = query.trim();
  if (!trimmed) return { error: "请输入搜索关键词。" };
  if (Array.from(trimmed).length > 256) return { error: "搜索关键词不能超过256个字符。" };
  if (type !== "all" && type !== "movie" && type !== "tv") return { error: "请选择有效的媒体类型。" };
  if (year !== "" && (!/^[0-9]{4}$/.test(year) || Number(year) < 1800)) {
    return { error: "年份须为1800至9999之间的四位整数。" };
  }
  return { query: trimmed, type, ...(year === "" ? {} : { year: Number(year) }) };
}

// Search and feed share MediaCard's optional fields; API nulls become absent values.
export function discoveryMediaItem(item: {
  tmdb_id: number; title: string; media_type?: string | null; year?: number | null;
  overview?: string | null; poster_url?: string | null; backdrop_url?: string | null;
  rating?: number | null; genres?: string[] | null;
}): MediaItem {
  return {
    tmdb_id: item.tmdb_id, title: item.title, media_type: item.media_type ?? undefined,
    year: item.year ?? undefined, overview: item.overview ?? undefined,
    poster_url: item.poster_url ?? undefined, backdrop_url: item.backdrop_url ?? undefined,
    rating: item.rating ?? undefined, genres: item.genres ?? undefined,
  };

}

export function doubanMediaItem(item: {
  douban_id: string;
  tmdb_id?: number | null;
  title: string;
  original_title?: string | null;
  media_type: "movie" | "tv";
  year?: number | null;
  rating?: number | null;
  poster_url?: string | null;
}): MediaItem {
  return {
    tmdb_id: item.tmdb_id ?? 0,
    title: item.title,
    media_type: item.media_type,
    year: item.year ?? undefined,
    overview: item.original_title ? `原名：${item.original_title}` : undefined,
    poster_url: item.poster_url ?? undefined,
    rating: item.rating ?? undefined,
    rating_source: "douban",
    douban_id: item.douban_id,
  };
}

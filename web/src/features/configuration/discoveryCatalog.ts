import type { MediaItem } from "@/components/cards/MediaCard";

export type DiscoveryCategory = "trending" | "movie" | "tv" | "top_rated" | "now_playing";
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

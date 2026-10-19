import type { paths } from "@/api/client";

export type TVDetail = paths["/api/v1/media/tmdb/tv/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
export type MovieDetail = paths["/api/v1/media/tmdb/movie/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
export type MediaCredits = paths["/api/v1/media/tmdb/{media_type}/{id}/credits"]["get"]["responses"]["200"]["content"]["application/json"];
export type SeasonDetail = paths["/api/v1/media/tmdb/tv/{id}/seasons/{season}"]["get"]["responses"]["200"]["content"]["application/json"];
export type ReadState<T> = { status: "idle" | "loading" | "error"; data?: undefined; error?: string } | { status: "ready"; data: T; error?: undefined };
export interface MediaSelection {
  mode: "all" | "selected";
  activeSeason: number | null;
  selectedSeasons: number[];
  includeSpecials: boolean;
  episodes: Record<number, number[]>;
  anchor: { season: number; episode: number } | null;
}
export const emptySelection = (): MediaSelection => ({ mode: "all", activeSeason: null, selectedSeasons: [], includeSpecials: false, episodes: {}, anchor: null });
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const integer = (value: unknown, minimum: number): value is number => Number.isSafeInteger(value) && (value as number) >= minimum;
const optionalText = (value: unknown) => value === undefined || typeof value === "string";
const optionalNumber = (value: unknown) => value === undefined || (typeof value === "number" && Number.isFinite(value) && value >= 0);
const date = (value: unknown) => value === undefined || (typeof value === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value) && Number.isFinite(Date.parse(value)) && new Date(value).toISOString().slice(0, 10) === value);

export function validMediaDetail(value: unknown, id: number, type: "movie" | "tv"): value is MovieDetail | TVDetail {
  if (!object(value) || value.tmdb_id !== id || !integer(id, 1) || typeof value.title !== "string" || !value.title.trim()) return false;
  if (![value.original_title, value.overview, value.poster_url, value.backdrop_url].every(optionalText) || !optionalNumber(value.rating) || !optionalNumber(value.year)) return false;
  if (value.genres !== undefined && (!Array.isArray(value.genres) || !value.genres.every(g => typeof g === "string"))) return false;
  if (type === "movie") return optionalNumber(value.runtime);
  if (!Array.isArray(value.seasons) || !integer(value.seasons_count, 0) || !integer(value.episodes_count, 0)) return false;
  const seen = new Set<number>();
  return value.seasons.every(s => {
    if (!object(s) || !integer(s.season_number, 0) || seen.has(s.season_number) || !integer(s.episode_count, 0) || typeof s.name !== "string" || !date(s.air_date) || !optionalText(s.poster_url)) return false;
    seen.add(s.season_number); return true;
  });
}

export function validCredits(value: unknown, id: number, type: "movie" | "tv"): value is MediaCredits {
  return object(value) && value.tmdb_id === id && value.media_type === type && Array.isArray(value.cast) && value.cast.every(c => object(c) && integer(c.tmdb_id, 1) && typeof c.name === "string" && !!c.name.trim() && optionalText(c.character) && optionalText(c.profile_url));
}

export function validSeason(value: unknown, id: number, season: number): value is SeasonDetail {
  if (!object(value) || value.tmdb_id !== id || !integer(id, 1) || value.season_number !== season || !integer(season, 0) || value.complete !== true || !Array.isArray(value.episodes)) return false;
  const numbers = new Set<number>(), ids = new Set<number>();
  return value.episodes.every(e => {
    if (!object(e) || !integer(e.tmdb_id, 1) || ids.has(e.tmdb_id) || !integer(e.episode_number, 1) || numbers.has(e.episode_number) || ![e.name, e.overview, e.still_url].every(optionalText) || !date(e.air_date)) return false;
    numbers.add(e.episode_number); ids.add(e.tmdb_id); return true;
  });
}

export function actualEpisodeNumbers(detail: SeasonDetail): number[] {
  return detail.episodes.map(e => e.episode_number).sort((a, b) => a - b);
}
export function clampEpisodes(selected: readonly number[], actual: readonly number[]): number[] {
  const allowed = new Set(actual);
  return [...new Set(selected)].filter(n => allowed.has(n)).sort((a, b) => a - b);
}
export function changeEpisodes(actual: readonly number[], selected: readonly number[], action: "all" | "invert" | "clear" | "forward" | "toggle", episode?: number, anchor?: number): number[] {
  const current = new Set(clampEpisodes(selected, actual));
  if (action === "all") return [...actual];
  if (action === "clear") return [];
  if (action === "invert") return actual.filter(n => !current.has(n));
  if (episode === undefined || !actual.includes(episode)) return [...current];
  if (action === "forward") return actual.filter(n => n >= episode);
  if (anchor !== undefined && actual.includes(anchor)) {
    const select = current.has(anchor), lo = Math.min(anchor, episode), hi = Math.max(anchor, episode);
    for (const n of actual) if (n >= lo && n <= hi) { if (select) current.add(n); else current.delete(n); }
  } else if (current.has(episode)) current.delete(episode); else current.add(episode);
  return [...current].sort((a, b) => a - b);
}

export function acceptSeason(selection: MediaSelection, detail: SeasonDetail): MediaSelection {
  const actual = actualEpisodeNumbers(detail), sn = detail.season_number;
  return { ...selection, episodes: { ...selection.episodes, [sn]: clampEpisodes(selection.episodes[sn] ?? [], actual) }, anchor: selection.anchor?.season === sn && !actual.includes(selection.anchor.episode) ? null : selection.anchor };
}
export function reconcileSeasons(selection: MediaSelection, detail: TVDetail): MediaSelection {
  const allowed = detail.seasons.map(s => s.season_number).filter(n => n > 0 || selection.includeSpecials).sort((a, b) => a - b);
  return { ...selection, activeSeason: selection.activeSeason !== null && allowed.includes(selection.activeSeason) ? selection.activeSeason : allowed[0] ?? null,
    selectedSeasons: selection.selectedSeasons.filter(n => allowed.includes(n)),
    episodes: Object.fromEntries(Object.entries(selection.episodes).filter(([n]) => allowed.includes(Number(n)))),
    anchor: selection.anchor && allowed.includes(selection.anchor.season) ? selection.anchor : null };
}
export const episodeKey = (season: number, episode: number) => `S${String(season).padStart(2, "0")}E${String(episode).padStart(2, "0")}`;
export function selectedEpisodeKeys(selection: MediaSelection, seasons: Record<number, ReadState<SeasonDetail>>): string[] | null {
  if (!selection.selectedSeasons.length || new Set(selection.selectedSeasons).size !== selection.selectedSeasons.length) return null;
  const keys: string[] = [];
  for (const sn of [...selection.selectedSeasons].sort((a, b) => a - b)) {
    const list = seasons[sn];
    if (!integer(sn, 0) || (sn === 0 && !selection.includeSpecials) || list?.status !== "ready" || !validSeason(list.data, list.data.tmdb_id, sn)) return null;
    const selected = selection.episodes[sn] ?? [], actual = actualEpisodeNumbers(list.data);
    if (!selected.length || new Set(selected).size !== selected.length || clampEpisodes(selected, actual).length !== selected.length) return null;
    keys.push(...[...selected].sort((a, b) => a - b).map(n => episodeKey(sn, n)));
  }
  return keys.length ? keys : null;
}

// Only the fixed TMDB image projection may be used as an image source.
export function safeTMDBImage(value: string | undefined): string | undefined {
  return value && /^https:\/\/image\.tmdb\.org\/t\/p\/(?:original|w\d+|h\d+)\/[A-Za-z0-9_-]+\.(?:jpg|jpeg|png|webp)$/.test(value) ? value : undefined;
}

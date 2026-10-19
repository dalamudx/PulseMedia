import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import type { MediaItem } from "@/components/cards/MediaCard";
import { acceptSeason, actualEpisodeNumbers, changeEpisodes, emptySelection, reconcileSeasons, selectedEpisodeKeys, validCredits, validMediaDetail, validSeason } from "./mediaDetailSelection";
import type { MediaCredits, MediaSelection, MovieDetail, ReadState, SeasonDetail, TVDetail } from "./mediaDetailSelection";

type Scope = { key: string; epoch: number; alive: boolean; requests: Map<string, AbortController> };
type State = { scope: Scope; detail: ReadState<MovieDetail | TVDetail>; credits: ReadState<MediaCredits>; seasons: Record<number, ReadState<SeasonDetail>>; selection: MediaSelection };
const initial = (scope: Scope): State => ({ scope, detail: { status: "idle" }, credits: { status: "idle" }, seasons: {}, selection: emptySelection() });

// The public hook owns observations and local selection only; creation stays in the parent.
export function useMediaDetail(media: MediaItem | null, disabled = false) {
  const { user } = useAuth();
  const type = media?.media_type ?? "movie", id = media?.tmdb_id;
  const key = JSON.stringify([user?.id, user?.role, type, id]);
  const scopeRef = useRef<Scope | null>(null);
  if (scopeRef.current?.key !== key) scopeRef.current = { key, epoch: 0, alive: false, requests: new Map() };
  const scope = scopeRef.current;
  const [state, setState] = useState<State>(() => initial(scope));
  const view = state.scope === scope ? state : initial(scope);
  const latest = useRef(view); latest.current = view;
  const locked = useRef(disabled); locked.current = disabled;
  const allowed = !!user && !!media && Number.isSafeInteger(id) && id! > 0 && (type === "tv" || type === "movie");
  const current = () => scopeRef.current === scope && scope.alive;
  const edit = (fn: (s: State) => State) => {
    if (!current() || locked.current) return;
    setState(s => s.scope === scope ? fn(s) : s);
  };
  const putRead = (s: State, slot: string, read: ReadState<never>): State => {
    if (slot === "detail" || slot === "credits") return { ...s, [slot]: read };
    return { ...s, seasons: { ...s.seasons, [Number(slot)]: read } };
  };
  async function read<T>(slot: string, request: (signal: AbortSignal) => Promise<{ data?: unknown; error?: unknown; response: Response }>, validate: (data: unknown) => data is T, accept: (s: State, data: T) => State) {
    if (!allowed || !current()) return;
    scope.requests.get(slot)?.abort();
    const controller = new AbortController(), epoch = scope.epoch;
    scope.requests.set(slot, controller);
    const live = () => current() && scope.epoch === epoch && scope.requests.get(slot) === controller && !controller.signal.aborted;
    setState(s => s.scope === scope ? putRead(s, slot, { status: "loading" }) : s);
    try {
      const result = await request(controller.signal);
      if (!live()) return;
      if (result.response.status === 401 || result.response.status === 403) {
        // A current permission denial invalidates all observations, never parent commands.
        ++scope.epoch;
        for (const c of scope.requests.values()) c.abort();
        scope.requests.clear();
        setState(s => s.scope === scope ? { ...initial(scope), detail: { status: "error", error: "当前账户无权读取此影视，请确认登录与权限后重试。" }, credits: { status: "error", error: "演员信息读取被拒绝。" } } : s);
        return;
      }
      if (!result.response.ok || result.error || !validate(result.data)) throw new Error("unavailable");
      setState(s => s.scope === scope ? accept(s, result.data as T) : s);
    } catch {
      if (!live()) return;
      const message = slot === "detail" ? "影视详情读取失败，暂不能订阅。" : slot === "credits" ? "演员信息读取失败。" : "本季完整剧集列表读取失败，暂不能提交本季选集。";
      setState(s => s.scope === scope ? putRead(s, slot, { status: "error", error: message }) : s);
    } finally {
      if (scope.requests.get(slot) === controller) scope.requests.delete(slot);
    }
  }
  function loadDetail() {
    if (id === undefined || (type !== "tv" && type !== "movie")) return;
    void read("detail", signal => type === "tv"
      ? api.GET("/api/v1/media/tmdb/tv/{id}", { params: { path: { id } }, signal, cache: "no-store" })
      : api.GET("/api/v1/media/tmdb/movie/{id}", { params: { path: { id } }, signal, cache: "no-store" }),
    (data): data is MovieDetail | TVDetail => validMediaDetail(data, id, type), (s, data) => {
      if (type !== "tv") return { ...s, detail: { status: "ready", data } };
      const tv = data as TVDetail, numbers = new Set(tv.seasons.map(season => season.season_number));
      return { ...s, detail: { status: "ready", data }, selection: reconcileSeasons(s.selection, tv), seasons: Object.fromEntries(Object.entries(s.seasons).filter(([n]) => numbers.has(Number(n)))) };
    });
  }
  function loadCredits() {
    if (id === undefined || (type !== "tv" && type !== "movie")) return;
    void read("credits", signal => api.GET("/api/v1/media/tmdb/{media_type}/{id}/credits", { params: { path: { media_type: type, id } }, signal, cache: "no-store" }),
      (data): data is MediaCredits => validCredits(data, id, type), (s, data) => ({ ...s, credits: { status: "ready", data } }));
  }
  function loadSeason(sn: number) {
    const snapshot = latest.current;
    if (id === undefined || type !== "tv" || snapshot.detail.status !== "ready" || !(snapshot.detail.data as TVDetail).seasons.some(s => s.season_number === sn) || (sn === 0 && !snapshot.selection.includeSpecials)) return;
    void read(String(sn), signal => api.GET("/api/v1/media/tmdb/tv/{id}/seasons/{season}", { params: { path: { id, season: sn } }, signal, cache: "no-store" }),
      (data): data is SeasonDetail => validSeason(data, id, sn), (s, data) => ({ ...s, seasons: { ...s.seasons, [sn]: { status: "ready", data } }, selection: acceptSeason(s.selection, data) }));
  }
  useEffect(() => {
    scope.alive = true; ++scope.epoch;
    setState(initial(scope));
    if (allowed) { loadDetail(); loadCredits(); }
    return () => {
      scope.alive = false; ++scope.epoch;
      for (const c of scope.requests.values()) c.abort();
      scope.requests.clear();
    };
    // Each render scope is a unique account/role/type/id generation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope]);
  const activeSeason = view.selection.activeSeason;
  useEffect(() => {
    if (view.detail.status === "ready" && activeSeason !== null && !view.seasons[activeSeason]) loadSeason(activeSeason);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, activeSeason, view.detail.status]);

  const tv = type === "tv" && view.detail.status === "ready" ? view.detail.data as TVDetail : null;
  const episodeKeys = selectedEpisodeKeys(view.selection, view.seasons);
  const canSubscribe = allowed && view.detail.status === "ready" && (type !== "tv" || view.selection.mode === "all" || episodeKeys !== null) && !disabled;
  const selectSeason = (sn: number) => edit(s => {
    if (!tv?.seasons.some(season => season.season_number === sn) || (sn === 0 && !s.selection.includeSpecials)) return s;
    return { ...s, selection: { ...s.selection, activeSeason: sn, anchor: null } };
  });
  const toggleSeason = (sn: number) => edit(s => {
    if (!tv?.seasons.some(season => season.season_number === sn) || (sn === 0 && !s.selection.includeSpecials)) return s;
    const selectedSeasons = s.selection.selectedSeasons.includes(sn) ? s.selection.selectedSeasons.filter(n => n !== sn) : [...s.selection.selectedSeasons, sn].sort((a, b) => a - b);
    return { ...s, selection: { ...s.selection, selectedSeasons } };
  });
  const setMode = (mode: "all" | "selected") => edit(s => ({ ...s, selection: { ...s.selection, mode, anchor: null } }));
  const setIncludeSpecials = (includeSpecials: boolean) => edit(s => {
    const selection = { ...s.selection, includeSpecials, anchor: null };
    return { ...s, selection: tv ? reconcileSeasons(selection, tv) : selection };
  });
  const selectEpisodes = (action: "all" | "invert" | "clear" | "forward" | "toggle", episode?: number, shift = false) => edit(s => {
    const sn = s.selection.activeSeason;
    if (s.selection.mode !== "selected" || sn === null || !s.selection.selectedSeasons.includes(sn)) return s;
    const list = s.seasons[sn]; if (list?.status !== "ready") return s;
    const anchor = s.selection.anchor?.season === sn ? s.selection.anchor.episode : undefined;
    const actual = actualEpisodeNumbers(list.data);
    if ((action === "toggle" || action === "forward") && (episode === undefined || !actual.includes(episode))) return s;
    const selected = changeEpisodes(actual, s.selection.episodes[sn] ?? [], action, episode, shift ? anchor : undefined);
    return { ...s, selection: { ...s.selection, episodes: { ...s.selection.episodes, [sn]: selected }, anchor: action === "toggle" || action === "forward" ? { season: sn, episode: episode! } : null } };
  });
  const getSubscription = () => {
    const snapshot = latest.current;
    if (!allowed || !current() || locked.current || scope.requests.has("detail") || snapshot.detail.status !== "ready") return null;
    const chosen = snapshot.selection;
    if (type === "tv" && chosen.mode === "selected" && chosen.selectedSeasons.some(sn => scope.requests.has(String(sn)))) return null;
    const keys = type === "tv" && chosen.mode === "selected" ? selectedEpisodeKeys(chosen, snapshot.seasons) : [];
    if (keys === null) return null;
    return { scope: type === "tv" ? chosen.mode : "all" as const, seasons: type === "tv" && chosen.mode === "selected" ? [...chosen.selectedSeasons] : [], episodes: keys, includeSpecials: type === "tv" && chosen.includeSpecials };
  };
  return { detail: view.detail, credits: view.credits, seasons: view.seasons, selection: view.selection, tv, episodeKeys, canSubscribe, getSubscription,
    selectSeason, toggleSeason, setMode, setIncludeSpecials, selectEpisodes,
    retryDetail: () => { if (!locked.current) loadDetail(); },
    retryCredits: () => { if (!locked.current) loadCredits(); },
    retrySeason: (sn: number) => { if (!locked.current) loadSeason(sn); },
  };
}

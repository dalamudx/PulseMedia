import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { byteFact, decimalFact } from "./useDashboardFacts";

export type GlobalRateSnapshot = components["schemas"]["DownloadGlobalRatesOverview"];
type Client = GlobalRateSnapshot["clients"][number];
export const downloadClientLabels: Record<Client["client_type"], string> = {
  qbittorrent: "qBittorrent", transmission: "Transmission", rtorrent: "rTorrent",
};
type Direction = "download" | "upload";
const validTime = (v: unknown): v is string => typeof v === "string" && Number.isFinite(Date.parse(v));
const object = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const nullableRate = (v: unknown) => v === null || decimalFact(v) !== null;
const validRates = (v: Record<string, unknown>) => nullableRate(v.download_bytes_per_second) && nullableRate(v.upload_bytes_per_second);
const states = new Set(["disabled", "unobserved", "configuration_changed", "unknown", "failed", "stale", "observed"]);
const failures = new Set(["", "unavailable", "auth_required", "timeout", "blocked", "bad_response", "rate_limited"]);
export function validGlobalRateSnapshot(value: unknown): value is GlobalRateSnapshot {
  if (!object(value) || typeof value.enabled !== "boolean" || !validTime(value.observed_at) || value.freshness_seconds !== 45 || !validRates(value) || !Array.isArray(value.clients) || !Array.isArray(value.series) || value.series.length > 62) return false;
  const known = (n: unknown) => typeof n === "number" && Number.isInteger(n) && n >= 0 && n <= (value.clients as unknown[]).length;
  if (!known(value.download_known_clients) || !known(value.upload_known_clients)) return false;
  const ids = new Set<string>();
  if (!value.clients.every(client => {
    if (!object(client) || typeof client.id !== "string" || ids.has(client.id) || typeof client.name !== "string" || !Object.hasOwn(downloadClientLabels, String(client.client_type)) || !states.has(String(client.state)) || !failures.has(String(client.failure_code)) || !(client.observed_at === null || validTime(client.observed_at)) || !validRates(client)) return false;
    ids.add(client.id);
    return client.last_good === undefined || (object(client.last_good) && validTime(client.last_good.observed_at) && validRates(client.last_good));
  })) return false;
  let previous = -Infinity;
  return value.series.every(point => {
    if (!object(point) || !validTime(point.at) || !validRates(point) || !known(point.download_known_clients) || !known(point.upload_known_clients)) return false;
    const at = Date.parse(point.at);
    if (at <= previous || at > Date.parse(value.observed_at as string)) return false;
    previous = at; return true;
  });
}
export function globalRateText(v: unknown): string { const bytes = decimalFact(v); return bytes === null ? "未知" : `${byteFact(bytes)}/s`; }
export function currentClientRate(data: GlobalRateSnapshot, client: Client, direction: Direction, elapsedMs: number): string | null {
  if (!data.enabled || client.state !== "observed" || !client.observed_at) return null;
  const age = Date.parse(data.observed_at) - Date.parse(client.observed_at) + Math.max(0, elapsedMs);
  if (!Number.isFinite(age) || age < 0 || age > data.freshness_seconds * 1000) return null;
  const value = client[`${direction}_bytes_per_second`]; return decimalFact(value) === null ? null : value;
}
export function currentGlobalRate(data: GlobalRateSnapshot, direction: Direction, elapsedMs: number) {
  const known = data.clients.filter(client => currentClientRate(data, client, direction, elapsedMs) !== null).length;
  return { value: data.enabled && known === data.clients.length ? data[`${direction}_bytes_per_second`] : null, known };
}
// Normalize in fixed-point bigint before converting bounded coordinates to Number.
export function globalRateCurve(series: GlobalRateSnapshot["series"], direction: Direction) {
  let max = 1n;
  for (const point of series) { const v = decimalFact(point[`${direction}_bytes_per_second`]); if (v !== null && v > max) max = v; }
  const start = series.length ? Date.parse(series[0].at) : 0, end = series.length ? Date.parse(series[series.length - 1].at) : start;
  const segments: { x: number; y: number }[][] = []; let segment: { x: number; y: number }[] = [];
  for (const point of series) {
    const v = decimalFact(point[`${direction}_bytes_per_second`]);
    if (v === null) { if (segment.length) segments.push(segment); segment = []; continue; }
    segment.push({ x: 8 + (end === start ? 0 : (Date.parse(point.at) - start) / (end - start)) * 604, y: 120 - Number(v * 1000n / max) / 1000 * 112 });
  }
  if (segment.length) segments.push(segment); return { max: max.toString(), segments };
}

type Scope = { userId: string | undefined; role: string | undefined; active: boolean; blocked: boolean; request: AbortController | null; timeout: ReturnType<typeof setTimeout> | null };
type State = { scope: Scope | null; data: GlobalRateSnapshot | null; error: string; busy: boolean; receivedAt: number };
const initial = (scope: Scope | null): State => ({ scope, data: null, error: "", busy: false, receivedAt: 0 });
export function useDownloadGlobalRates({ userId, role }: { userId: string | undefined; role: string | undefined }) {
  const scope = useMemo<Scope>(() => ({ userId, role, active: false, blocked: false, request: null, timeout: null }), [userId, role]);
  const current = useRef(scope); current.current = scope;
  const [state, setState] = useState<State>(() => initial(null)), [now, setNow] = useState(() => performance.now());
  const read = useCallback(async (owner: Scope, supersede: boolean): Promise<boolean> => {
    if (current.current !== owner || !owner.active || !owner.userId || owner.role !== "admin" || (owner.blocked && !supersede) || (owner.request && !supersede)) return false;
    if (owner.timeout !== null) clearTimeout(owner.timeout);
    owner.request?.abort(); owner.blocked = false;
    const controller = new AbortController(); owner.request = controller;
    const owns = () => current.current === owner && owner.active && owner.request === controller && !controller.signal.aborted;
    setState(previous => ({ ...(previous.scope === owner ? previous : initial(owner)), busy: true }));
    const fail = (status = 0) => {
      if (!owns()) return false; owner.blocked = status === 401 || status === 403;
      setState({ ...initial(owner), error: status === 401 ? "会话已失效，请重新登录。" : status === 403 ? "当前账号无权查看全局速率。" : "速率读取失败，请稍后重试。" }); return false;
    };
    const timeout = setTimeout(() => { if (owns()) { fail(); owner.request = null; owner.timeout = null; controller.abort(); } }, 10000); owner.timeout = timeout;
    try {
      const result = await api.GET("/api/v1/download-clients/global-rates", { signal: controller.signal, cache: "no-store" });
      if (!owns()) return false;
      if (result.error || !result.response.ok) return fail(result.response.status);
      if (!validGlobalRateSnapshot(result.data)) return fail();
      const receivedAt = performance.now(); setNow(receivedAt); setState({ scope: owner, data: result.data, error: "", busy: false, receivedAt }); return true;
    } catch { return fail(); }
    finally { clearTimeout(timeout); if (owner.request === controller) { owner.request = null; owner.timeout = null; } }
  }, []);
  const refresh = useCallback(() => read(scope, true), [read, scope]);
  useEffect(() => {
    scope.active = true; setState(initial(scope));
    let poll: ReturnType<typeof setInterval> | undefined, age: ReturnType<typeof setInterval> | undefined;
    const stop = () => { clearInterval(poll); clearInterval(age); };
    const abort = () => { if (scope.timeout !== null) clearTimeout(scope.timeout); scope.timeout = null; scope.request?.abort(); scope.request = null; };
    const visible = () => {
      stop(); setNow(performance.now());
      if (document.visibilityState !== "visible") { abort(); return; }
      if (!scope.userId || scope.role !== "admin") return;
      void read(scope, false); poll = setInterval(() => { void read(scope, false); }, 5000); age = setInterval(() => setNow(performance.now()), 1000);
    };
    visible(); document.addEventListener("visibilitychange", visible);
    return () => { scope.active = false; stop(); abort(); document.removeEventListener("visibilitychange", visible); };
  }, [scope, read]);
  const visible = state.scope === scope && !!userId && role === "admin";
  return { data: visible ? state.data : null, error: visible ? state.error : "", busy: visible && state.busy, elapsedMs: visible ? Math.max(0, now - state.receivedAt) : 0, refresh };
}
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { mergeSteps, validDetail, validTimeline } from "./operationActivityModel";
import type { OperationDetails, OperationTimelineEntry, OperationTimelinePage } from "./operationActivityModel";
export type { OperationDetails, OperationTimelineEntry, OperationTimelinePage } from "./operationActivityModel";

export type OperationLiveState = {
  detail: OperationDetails | null;
  timeline: OperationTimelinePage | null;
  steps: OperationTimelineEntry[];
  loading: boolean;
  loadingMore: boolean;
  error: string;
  timelineError: string;
  detailStale: boolean;
  timelineStale: boolean;
  canAct: boolean;
  refresh: () => Promise<boolean>;
  loadMore: () => Promise<boolean>;
};
type Snapshot = Omit<OperationLiveState, "refresh" | "loadMore" | "canAct"> & { version: number; timelineConfirmed: boolean };
type Owner = {
  key: string; id: string | undefined; alive: boolean; epoch: number; version: number;
  data: Snapshot; controllers: Set<AbortController>; reading?: Promise<boolean>; paging?: Promise<boolean>;
};
const empty = (): Snapshot => ({ detail: null, timeline: null, steps: [], loading: false, loadingMore: false,
  error: "", timelineError: "", detailStale: false, timelineStale: false, timelineConfirmed: false, version: -1 });
const detailError = "操作详情读取失败，请重新读取。";
const timelineError = "整理步骤读取失败，请重试。";

export function useOperationLive({ operationId, userId, role, refreshVersion }: {
  operationId: string | undefined; userId: string | undefined; role: string | undefined; refreshVersion: number;
}): OperationLiveState {
  const key = JSON.stringify([operationId, userId, role]);
  const ownerRef = useRef<Owner | null>(null);
  if (ownerRef.current?.key !== key) ownerRef.current = {
    key, id: operationId, alive: false, epoch: 0, version: refreshVersion, data: empty(), controllers: new Set(),
  };
  const owner = ownerRef.current;
  const latestVersion = useRef(refreshVersion); latestVersion.current = refreshVersion;
  const [, rerender] = useState(0);
  const allowed = !!operationId && !!userId && !!role;
  const current = (o: Owner, epoch = o.epoch) => ownerRef.current === o && o.alive && o.epoch === epoch && o.version === latestVersion.current;
  const publish = (o: Owner, patch: Partial<Snapshot>) => {
    if (!current(o)) return;
    o.data = { ...o.data, ...patch }; rerender(n => n + 1);
  };
  function stop(o: Owner) {
    ++o.epoch;
    for (const c of o.controllers) c.abort();
    o.controllers.clear(); o.reading = undefined; o.paging = undefined;
  }
  function denied(o: Owner) {
    stop(o);
    publish(o, { ...empty(), error: "此操作不可访问或已不存在，请确认账户与权限后重试。", timelineError: "此操作的整理步骤不可访问。" });
  }
  async function read(o: Owner, lane: "detail" | "timeline", epoch: number, cursor?: string): Promise<OperationDetails | OperationTimelinePage | null> {
    const controller = new AbortController(); o.controllers.add(controller);
    const live = () => current(o, epoch) && !controller.signal.aborted;
    try {
      const options = { signal: controller.signal, cache: "no-store" as const, params: { path: { id: o.id! } } };
      const result = lane === "detail"
        ? await api.GET("/api/v1/operations/{id}", options)
        : await api.GET("/api/v1/operations/{id}/timeline", { ...options, params: { ...options.params, query: { limit: cursor ? 100 : 50, cursor } } });
      if (!live()) return null;
      const status = result.response?.status;
      if (status === 401 || status === 403 || status === 404) { denied(o); return null; }
      if (status !== 200 || result.error || !(lane === "detail" ? validDetail(result.data, o.id!) : validTimeline(result.data, o.id!))) throw Error("unavailable");
      return result.data!;
    } catch {
      if (live()) publish(o, lane === "detail" ? { error: detailError, detailStale: !!o.data.detail } : { timelineError, timelineStale: !!o.data.timeline });
      return null;
    } finally { o.controllers.delete(controller); }
  }
  function start(o: Owner, background = false): Promise<boolean> {
    if (!current(o) || !o.id) return Promise.resolve(false);
    if (o.reading) return o.reading;
    // Refresh invalidates any captured pagination cursor, even when the plan stays the same.
    stop(o);
    const epoch = o.epoch;
    const retain = background && o.data.version === o.version && o.data.timelineConfirmed &&
      !!o.data.detail && !!o.data.timeline && !o.data.error && !o.data.timelineError && !o.data.detailStale && !o.data.timelineStale;
    publish(o, { loading: true, loadingMore: false, timelineConfirmed: retain, detailStale: !retain && !!o.data.detail, timelineStale: !retain && !!o.data.timeline });
    const work = (async () => {
      const [detail, timeline] = await Promise.all([read(o, "detail", epoch), read(o, "timeline", epoch)]);
      if (!current(o, epoch)) return false;
      const d = detail as OperationDetails | null, t = timeline as OperationTimelinePage | null;
      const matching = !!d && !!t && d.plan_id === t.plan_id && d.plan_status === t.plan_status;
      publish(o, {
        loading: false, version: o.version,
        ...(d ? { detail: d, error: "", detailStale: false } : {}),
        ...(t ? { timeline: t, steps: mergeSteps([], t.items), timelineError: "", timelineStale: false, timelineConfirmed: true } : {}),
        ...(d && t && !matching ? { detailStale: true, timelineStale: true, timelineConfirmed: false, timelineError: "计划已变化，请重新读取详情与步骤。" } : {}),
      });
      return matching;
    })();
    o.reading = work;
    void work.finally(() => { if (o.reading === work) o.reading = undefined; });
    return work;
  }
  function more(o: Owner): Promise<boolean> {
    if (!current(o) || o.reading || !o.data.timelineConfirmed || !o.data.timeline?.next_cursor) return Promise.resolve(false);
    if (o.paging) return o.paging;
    const epoch = o.epoch, page = o.data.timeline, cursor = page.next_cursor!;
    publish(o, { loadingMore: true });
    const work = (async () => {
      const result = await read(o, "timeline", epoch, cursor) as OperationTimelinePage | null;
      if (!current(o, epoch) || o.data.timeline !== page) return false;
      if (!result) { publish(o, { loadingMore: false }); return false; }
      if (result.plan_id !== page.plan_id || result.plan_status !== page.plan_status || result.next_cursor === cursor) {
        publish(o, { loadingMore: false, timelineStale: true, timelineConfirmed: false, timelineError: "计划或分页位置已变化，请重新读取整理步骤。" }); return false;
      }
      publish(o, { timeline: { ...result, items: page.items }, steps: mergeSteps(o.data.steps, result.items), loadingMore: false,
        timelineStale: false, timelineError: "" });
      return true;
    })();
    o.paging = work;
    void work.finally(() => { if (o.paging === work) o.paging = undefined; });
    return work;
  }
  const actions = useRef({ refresh: () => Promise.resolve(false), loadMore: () => Promise.resolve(false) });
  actions.current = { refresh: () => allowed ? start(owner) : Promise.resolve(false), loadMore: () => allowed ? more(owner) : Promise.resolve(false) };
  const refresh = useCallback(() => actions.current.refresh(), []);
  const loadMore = useCallback(() => actions.current.loadMore(), []);

  useEffect(() => {
    owner.alive = true; owner.version = refreshVersion;
    const visible = () => document.visibilityState === "visible";
    let timer: ReturnType<typeof setInterval> | undefined;
    const visibility = () => {
      if (timer !== undefined) { clearInterval(timer); timer = undefined; }
      if (allowed && visible()) {
        void start(owner, true);
        timer = setInterval(() => { if (visible() && !owner.paging) void start(owner, true); }, 5000);
      }
    };
    visibility();
    document.addEventListener("visibilitychange", visibility);
    return () => {
      owner.alive = false; stop(owner);
      if (timer !== undefined) clearInterval(timer);
      document.removeEventListener("visibilitychange", visibility);
    };
    // Ownership is a unique render scope; version changes replace only read contexts.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [owner, refreshVersion]);

  const data = owner.data;
  const confirmed = allowed && owner.alive && data.version === refreshVersion;
  return { detail: data.detail, timeline: data.timeline, steps: data.steps, loading: data.loading, loadingMore: data.loadingMore,
    error: data.error, timelineError: data.timelineError,
    detailStale: data.detailStale || (!!data.detail && !confirmed), timelineStale: data.timelineStale || (!!data.timeline && !confirmed),
    canAct: !!(confirmed && data.detail && data.timeline && data.detail.plan_id === data.timeline.plan_id && data.detail.plan_status === data.timeline.plan_status &&
      data.timelineConfirmed && !data.error && !data.timelineError && !data.detailStale && !data.timelineStale), refresh, loadMore };
}

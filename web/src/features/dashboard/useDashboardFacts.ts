import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";

export type LibraryOverview = components["schemas"]["LibraryOverview"];
export type DashboardActivityPage = components["schemas"]["DashboardActivityPage"];
type FactStatus = "loading" | "ready" | "error";
export type DashboardFacts = {
  overview: LibraryOverview | null;
  activity: DashboardActivityPage | null;
  overviewStatus: FactStatus;
  activityStatus: FactStatus;
  errors: string[];
  refresh: () => Promise<boolean>;
};

// Keep integers exact, and bound parsing work at the response boundary.
export function decimalFact(value: unknown): bigint | null {
  return typeof value === "string" && value.length <= 128 && /^(0|[1-9][0-9]*)$/.test(value)
    ? BigInt(value) : null;
}
export function countFact(value: unknown): string {
  const count = decimalFact(value);
  return count === null ? "未知" : count.toLocaleString("zh-CN");
}
export function factTime(value: unknown): string {
  if (typeof value !== "string" || !value.trim()) return "时间未知";
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date.toLocaleString("zh-CN", { hour12: false }) : "时间未知";
}
export function trustedPoster(value: unknown): string | undefined {
  return typeof value === "string" && /^https:\/\/image\.tmdb\.org\/t\/p\/w342\/[A-Za-z0-9_-]+\.(jpg|jpeg|png|webp)$/.test(value)
    ? value : undefined;
}
export function capacityFact(root: LibraryOverview["roots"][number]) {
  const total = decimalFact(root.total_bytes), available = decimalFact(root.available_bytes);
  if (root.health === "offline" || root.availability !== "available" || !["fresh", "stale"].includes(root.capacity_state)
      || factTime(root.sampled_at) === "时间未知" || total === null || total === 0n
      || available === null || available > total) return null;
  const used = total - available;
  const percent = Number(used * 10000n / total) / 100;
  return { total, available, used, percent };
}
export function byteFact(bytes: bigint): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"];
  let unit = 0, scale = 1n;
  while (unit < units.length - 1 && bytes >= scale * 1024n) { scale *= 1024n; unit++; }
  const tenths = bytes * 10n / scale;
  return `${tenths / 10n}${unit ? `.${tenths % 10n}` : ""} ${units[unit]}`;
}
export function activityProgress(item: DashboardActivityPage["items"][number]): number | null {
  const fraction = item.progress;
  if (typeof fraction !== "number" || !Number.isFinite(fraction) || fraction < 0 || fraction > 1
      || (item.progress_source !== "native_percent" && item.progress_source !== "verified_bytes")) return null;
  // A fraction below one must never be presented as completed by rounding.
  return fraction === 1 ? 100 : Math.min(99.99, Math.floor(fraction * 10000) / 100);
}
export function activityStatus(status: string): string {
  const labels: Record<string, string> = {
    queued: "排队中", pending: "待处理", submitting: "提交中", tracking: "跟踪中",
    downloading: "下载中", completed: "已完成", succeeded: "已完成", failed: "失败",
    dead: "失败待处理", unknown: "状态未知", review: "待人工处理", needs_review: "待人工处理",
    review_required: "待人工处理", waiting_organization: "等待整理", partial: "部分完成", conflict: "存在冲突",
    organizing: "整理中", importing: "入库中", paused: "已暂停", cancelled: "已取消",
    canceled: "已取消", withdrawn: "已撤回", running: "执行中",
  };
  return Object.hasOwn(labels, status) ? labels[status] : "状态未知";
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function validOverview(value: unknown): value is LibraryOverview {
  if (!record(value) || !record(value.counts) || !Array.isArray(value.roots) || !Array.isArray(value.recent)
      || typeof value.observed_at !== "string") return false;
  if (!["movies", "tv_shows", "episodes", "files"].every(key => decimalFact(value.counts && (value.counts as Record<string, unknown>)[key]) !== null)) return false;
  return value.roots.every(root => record(root) && typeof root.root_id === "string" && typeof root.name === "string"
    && (root.media_type === "movie" || root.media_type === "tv") && typeof root.health === "string"
    && (root.availability === "available" || root.availability === "unknown")
    && ["fresh", "stale", "unknown"].includes(String(root.capacity_state)))
    && value.recent.every(item => record(item) && typeof item.media_id === "string" && typeof item.title === "string"
      && (item.media_type === "movie" || item.media_type === "tv") && typeof item.imported_at === "string");
}
function validActivity(value: unknown): value is DashboardActivityPage {
  return record(value) && typeof value.observed_at === "string" && typeof value.more === "boolean"
    && Array.isArray(value.items) && value.items.every(item => record(item) && typeof item.operation_id === "string"
      && typeof item.title === "string" && typeof item.status === "string");
}

type Lane = "overview" | "activity";
type Scope = {
  userId: string | undefined; role: string | undefined; active: boolean;
  overview: AbortController | null; activity: AbortController | null;
};
type Snapshot = {
  scope: Scope | null; overview: LibraryOverview | null; activity: DashboardActivityPage | null;
  overviewStatus: FactStatus; activityStatus: FactStatus;
  overviewError: string; activityError: string;
};
const initial = (scope: Scope | null): Snapshot => ({
  scope, overview: null, activity: null, overviewStatus: "loading", activityStatus: "loading",
  overviewError: "", activityError: "",
});

export function useDashboardFacts({ userId, role }: {
  userId: string | undefined; role: string | undefined;
}): DashboardFacts {
  const scope = useMemo<Scope>(() => ({ userId, role, active: false, overview: null, activity: null }), [userId, role]);
  const current = useRef(scope);
  current.current = scope;
  const [snapshot, setSnapshot] = useState<Snapshot>(() => initial(null));

  const read = useCallback(async (owner: Scope, lane: Lane, supersede: boolean): Promise<boolean> => {
    if (current.current !== owner || !owner.active || !owner.userId || !owner.role) return false;
    if (owner[lane] && !supersede) return false;
    owner[lane]?.abort();
    const controller = new AbortController();
    owner[lane] = controller;
    const owns = () => current.current === owner && owner.active && owner[lane] === controller && !controller.signal.aborted;
    const statusKey = lane === "overview" ? "overviewStatus" : "activityStatus";
    const errorKey = lane === "overview" ? "overviewError" : "activityError";
    const label = lane === "overview" ? "媒体库概览" : "活动记录";
    setSnapshot(previous => ({ ...(previous.scope === owner ? previous : initial(owner)), [statusKey]: "loading" }));
    const failure = (denied: boolean, unauthorized = false) => {
      if (!owns()) return false;
      if (unauthorized) {
        owner.overview?.abort(); owner.activity?.abort();
        owner.overview = null; owner.activity = null;
        setSnapshot({ ...initial(owner), overviewStatus: "error", activityStatus: "error",
          overviewError: "会话已失效，请重新登录。", activityError: "会话已失效，请重新登录。" });
      } else {
        setSnapshot(previous => ({ ...previous, [lane]: denied ? null : previous[lane], [statusKey]: "error",
          [errorKey]: denied ? `${label}无访问权限。` : `${label}读取失败，请稍后刷新。` }));
      }
      return false;
    };
    try {
      const result = lane === "overview"
        ? await api.GET("/api/v1/library/overview", { signal: controller.signal, cache: "no-store" })
        : await api.GET("/api/v1/dashboard/activity", { signal: controller.signal, cache: "no-store" });
      if (!owns()) return false;
      const status = result.response?.status;
      if (status === 401 || status === 403) return failure(true, status === 401);
      if (result.error || (status !== undefined && status >= 400)) return failure(false);
      const valid = lane === "overview" ? validOverview(result.data) : validActivity(result.data);
      if (!valid) return failure(false);
      setSnapshot(previous => ({ ...previous, [lane]: result.data, [statusKey]: "ready", [errorKey]: "" }));
      return true;
    } catch {
      return failure(false);
    } finally {
      if (owns()) owner[lane] = null;
    }
  }, []);

  const refresh = useCallback(async (): Promise<boolean> => {
    const owner = current.current;
    const results = await Promise.all([read(owner, "overview", true), read(owner, "activity", true)]);
    return results.every(Boolean) && current.current === owner && owner.active;
  }, [read]);

  useEffect(() => {
    scope.active = true;
    setSnapshot(initial(scope));
    void refresh();
    let activityTimer: ReturnType<typeof setInterval> | undefined;
    let overviewTimer: ReturnType<typeof setInterval> | undefined;
    const clearTimers = () => { clearInterval(activityTimer); clearInterval(overviewTimer); };
    const visibility = () => {
      clearTimers();
      if (document.visibilityState !== "visible" || !scope.userId || !scope.role) return;
      activityTimer = setInterval(() => { void read(scope, "activity", false); }, 5000);
      overviewTimer = setInterval(() => { void read(scope, "overview", false); }, 30000);
    };
    const onVisible = () => {
      visibility();
      if (document.visibilityState === "visible") {
        void read(scope, "overview", false); void read(scope, "activity", false);
      }
    };
    visibility();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      scope.active = false;
      clearTimers();
      document.removeEventListener("visibilitychange", onVisible);
      scope.overview?.abort(); scope.activity?.abort();
      scope.overview = null; scope.activity = null;
    };
  }, [scope, read, refresh]);

  // Scope comparison masks private data before passive cleanup, including A→B→A.
  const visible = snapshot.scope === scope && !!userId && !!role;
  return {
    overview: visible ? snapshot.overview : null,
    activity: visible ? snapshot.activity : null,
    overviewStatus: visible ? snapshot.overviewStatus : "loading",
    activityStatus: visible ? snapshot.activityStatus : "loading",
    errors: visible ? [...new Set([snapshot.overviewError, snapshot.activityError].filter(Boolean))] : [],
    refresh,
  };
}

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { displayTime, formatRate, nativeRate, observationFresh, progressSnapshot } from "./operationActivityModel";
import "./operation-activity.css";

type Page = paths["/api/v1/operations"]["get"]["responses"]["200"]["content"]["application/json"];
type View = "active" | "review" | "all";
type Snapshot = { scope: string; items: Page["items"]; cursor?: string; busy: boolean; error: string };
const labels: Record<string, string> = {
  unknown: "结果待确认", review_required: "需要复核", submitting: "正在提交", submitted: "已提交",
  tracking: "下载跟踪", completed: "已完成", organizing: "正在整理", waiting_organization: "等待整理",
  queued: "已排队", running: "执行中", failed: "执行失败", dead: "重试已耗尽", partial: "部分完成",
  conflict: "目标冲突", cancelled: "已取消", pending: "等待处理", approved: "已批准", paused: "已暂停",
};
export function operationStatusLabel(status: string): string {
  return Object.hasOwn(labels, status) ? labels[status] : "状态待确认";
}

export function OperationInbox({ onSelect, refreshVersion }: { onSelect: (id: string) => void; refreshVersion: number }) {
  const { user } = useAuth();
  const [view, setView] = useState<View>("active");
  const scope = JSON.stringify([user?.id, user?.role, view]);
  const scopeRef = useRef(scope); scopeRef.current = scope;
  const [snapshot, setSnapshot] = useState<Snapshot>({ scope, items: [], busy: false, error: "" });
  const stateRef = useRef(snapshot); stateRef.current = snapshot;
  const owner = useRef<AbortController | null>(null);
  const epoch = useRef(0);
  const state: Snapshot = snapshot.scope === scope ? snapshot : { scope, items: [], busy: false, error: "" };

  const load = useCallback(async (next?: string, background = false) => {
    if (!user?.id) return;
    owner.current?.abort();
    const controller = new AbortController(), generation = ++epoch.current;
    owner.current = controller;
    const current = () => !controller.signal.aborted && epoch.current === generation && scopeRef.current === scope;
    setSnapshot(old => ({ scope, items: old.scope === scope && (next || background) ? old.items : [],
      cursor: old.scope === scope && (next || background) ? old.cursor : undefined, busy: true, error: "" }));
    try {
      const { data, error } = await api.GET("/api/v1/operations", {
        signal: controller.signal, cache: "no-store", params: { query: { view, limit: 25, cursor: next } },
      });
      if (!current()) return;
      if (error || !data || !Array.isArray(data.items)) {
        setSnapshot({ scope, items: [], busy: false, error: "操作列表读取失败，请重新读取，不能据此判断没有待处理任务。" });
        return;
      }
      setSnapshot(old => ({ scope, items: next && old.scope === scope
        ? [...old.items, ...data.items.filter(item => !old.items.some(value => value.operation_id === item.operation_id))]
        : data.items, cursor: data.next_cursor, busy: false, error: "" }));
    } catch {
      if (current()) setSnapshot({ scope, items: [], busy: false, error: "连接中断，请重新读取操作列表。" });
    } finally {
      if (owner.current === controller) owner.current = null;
    }
  }, [scope, user?.id, view]);

  useEffect(() => {
    void load();
    const visible = () => document.visibilityState === "visible";
    const refresh = () => {
      // Keep a manually expanded page in place; its explicit refresh returns to page one.
      if (visible() && !owner.current && stateRef.current.scope === scope && stateRef.current.items.length <= 25) void load(undefined, true);
    };
    const timer = setInterval(refresh, 5000);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      clearInterval(timer); document.removeEventListener("visibilitychange", refresh);
      owner.current?.abort(); owner.current = null; epoch.current++;
    };
  }, [load, refreshVersion, scope]);

  return <section className="config-editor operation-inbox" aria-label="持久操作列表" aria-busy={state.busy}>
    <div className="config-heading"><h3>操作列表</h3>
      <button type="button" className="btn-secondary" disabled={state.busy} onClick={() => void load()}>刷新操作列表</button>
    </div>
    <div className="config-actions" role="group" aria-label="操作范围">
      {([["active", "进行中"], ["review", "待处理"], ["all", "全部"]] as const).map(([value, label]) =>
        <button key={value} type="button" className={value === view ? "btn-primary" : "btn-secondary"}
          aria-pressed={view === value} onClick={() => setView(value)}>{label}</button>)}
    </div>
    {state.error ? <p role="alert" className="config-error">{state.error}</p> : null}
    {state.busy && state.items.length === 0 ? <p role="status">正在读取操作…</p> : null}
    {!state.busy && !state.error && state.items.length === 0 ? <p role="status">
      {view === "review" ? "当前没有需要复核的获取操作。" : view === "active" ? "当前没有进行中的获取操作。" : "当前没有可查看的获取操作。"}
    </p> : null}
    <ul className="operation-inbox-list">{state.items.map(item => {
      const progress = progressSnapshot(item.telemetry);
      const rate = (item.telemetry?.rate_status === "current" || item.telemetry?.rate_status === "stale") && nativeRate(item.telemetry?.download_rate_bytes_per_second) !== null;
      const stale = item.telemetry?.rate_status !== "current" || !observationFresh(item.telemetry?.rate_observed_at);
      return <li key={item.operation_id}>
        <button type="button" className="btn-secondary operation-inbox-select" onClick={() => onSelect(item.operation_id)}>
          {item.title || "未命名媒体"} · {operationStatusLabel(item.status)}
        </button>
        <p className="operation-live-meta"><time dateTime={item.created_at}>{displayTime(item.created_at)}</time> · {item.operation_id}</p>
        {progress ? <div className="operation-inbox-progress"><progress max={100} value={progress.percent} aria-label={`${item.title || "操作"}下载进度`} />
          <span>{progress.percent}% · {progress.source}</span></div> : <p className="operation-live-meta">进度未知</p>}
        <p className="operation-live-meta">{rate ? `${formatRate(item.telemetry?.download_rate_bytes_per_second)} · ${stale ? "上次观测" : "当前观测"}` : "速度未知"}</p>
      </li>;
    })}</ul>
    {state.cursor ? <button type="button" className="btn-secondary" disabled={state.busy} onClick={() => void load(state.cursor)}>加载更多操作</button> : null}
  </section>;
}

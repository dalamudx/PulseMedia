import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/api/client";

type Summary = { pending_count: string; observed_at: string };
export function validInboxSummary(value: unknown): value is Summary {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return typeof v.pending_count === "string" && /^(0|[1-9][0-9]{0,18})$/.test(v.pending_count) && BigInt(v.pending_count) <= 9223372036854775807n && typeof v.observed_at === "string" && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(v.observed_at) && Number.isFinite(Date.parse(v.observed_at));
}
type Scope = { userId?: string; role?: string; active: boolean; blocked: boolean; request: AbortController | null; timeout: ReturnType<typeof setTimeout> | null };
type State = { scope: Scope | null; data: Summary | null; error: string; busy: boolean; receivedAt: number };
const empty = (scope: Scope | null): State => ({ scope, data: null, error: "", busy: false, receivedAt: 0 });

export function usePendingRequests({ userId, role }: { userId?: string; role?: string }) {
  const scope = useMemo<Scope>(() => ({ userId, role, active: false, blocked: false, request: null, timeout: null }), [userId, role]);
  const current = useRef(scope); current.current = scope;
  const [state, setState] = useState<State>(() => empty(null)), [now, setNow] = useState(() => performance.now());
  const read = useCallback(async (owner: Scope, explicit: boolean): Promise<boolean> => {
    if (current.current !== owner || !owner.active || !owner.userId || owner.role !== "admin" || (!explicit && (owner.blocked || owner.request))) return false;
    if (owner.timeout !== null) clearTimeout(owner.timeout);
    owner.request?.abort(); owner.blocked = false;
    const controller = new AbortController(); owner.request = controller;
    const owns = () => current.current === owner && owner.active && owner.request === controller && !controller.signal.aborted;
    setState(old => ({ ...(old.scope === owner ? old : empty(owner)), busy: true }));
    const fail = (status = 0) => {
      if (!owns()) return false;
      owner.blocked = status === 401 || status === 403;
      setState({ ...empty(owner), error: status === 401 ? "会话已失效，请重新登录。" : status === 403 ? "当前账号无权查看审批提醒。" : "审批提醒读取失败，请重试。" });
      return false;
    };
    const timeout = setTimeout(() => { if (owns()) { fail(); owner.request = null; owner.timeout = null; controller.abort(); } }, 10000);
    owner.timeout = timeout;
    try {
      const result = await api.GET("/api/v1/media-requests/summary", { signal: controller.signal, cache: "no-store" });
      if (!owns()) return false;
      if (!result.response.ok || result.error) return fail(result.response.status);
      if (!validInboxSummary(result.data)) return fail();
      const receivedAt = performance.now(); setNow(receivedAt);
      setState({ scope: owner, data: result.data, error: "", busy: false, receivedAt }); return true;
    } catch { return fail(); }
    finally { clearTimeout(timeout); if (owner.request === controller) { owner.request = null; owner.timeout = null; } }
  }, []);
  const refresh = useCallback(() => read(scope, true), [read, scope]);
  useEffect(() => {
    scope.active = true; setState(empty(scope));
    let poll: ReturnType<typeof setInterval> | undefined, age: ReturnType<typeof setInterval> | undefined;
    const stop = () => { clearInterval(poll); clearInterval(age); };
    const abort = () => { if (scope.timeout !== null) clearTimeout(scope.timeout); scope.timeout = null; scope.request?.abort(); scope.request = null; };
    const visible = () => {
      stop(); setNow(performance.now());
      if (document.visibilityState !== "visible") { abort(); return; }
      if (!scope.userId || scope.role !== "admin") return;
      void read(scope, false);
      poll = setInterval(() => { void read(scope, false); }, 15000);
      age = setInterval(() => setNow(performance.now()), 1000);
    };
    visible(); document.addEventListener("visibilitychange", visible);
    return () => { scope.active = false; stop(); abort(); document.removeEventListener("visibilitychange", visible); };
  }, [scope, read]);
  const visible = state.scope === scope && !!userId && role === "admin";
  const data = visible ? state.data : null;
  const expired = !!data && now - state.receivedAt >= 45000;
  return { count: data && !expired ? data.pending_count : null, observedAt: data?.observed_at ?? null, error: visible ? state.error : "", expired, busy: visible && state.busy, refresh };
}

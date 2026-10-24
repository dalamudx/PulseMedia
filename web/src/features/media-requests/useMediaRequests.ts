import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import type { paths } from "@/api/client";
import type { ApproveBody, CloseBody, Command, MediaIntent, MediaRequest, Phase, Role } from "./types";
import { clearVerifiedJournal, journalKey, makeCommand, readJournal, sameCommand, sameIntent, saveJournal, uuid, validReceipt, validRequest } from "./commandJournal";

type Root = paths["/api/v1/library-roots"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type Policy = paths["/api/v1/policies"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type Indexer = paths["/api/v1/indexers"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
type Downloader = paths["/api/v1/download-clients"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];
export type ApprovalOptions = { roots: Root[]; policies: Policy[]; indexers: Indexer[]; downloaders: Downloader[] };
type View = { epoch: number; items: MediaRequest[]; loaded: boolean; loading: boolean; listError: string; cursor: string | null; next: string | null; options: ApprovalOptions | null; optionsError: string; optionsLoading: boolean; pending: Command | null; phase: Phase; busy: boolean; message: string; current: MediaRequest | null };
const blank = (epoch: number): View => ({ epoch, items: [], loaded: false, loading: false, listError: "", cursor: null, next: null, options: null, optionsError: "", optionsLoading: false, pending: null, phase: "idle", busy: false, message: "", current: null });

export function useMediaRequests({ userId = "", role = "", pendingOnly = false }: { userId?: string; role?: string; pendingOnly?: boolean }) {
  const filtered = role === "admin" && pendingOnly;
  const scope = `${userId}:${role}:${filtered}`;
  const live = useRef({ scope, epoch: 0 });
  const owned = useRef(new Set<AbortController>());
  const mounted = useRef(false);
  const generation = useRef(0);
  const pending = useRef<Command | null>(null);
  const blocked = useRef(false);
  const operation = useRef(false);
  const listSequence = useRef(0), optionSequence = useRef(0);
  if (live.current.scope !== scope) {
    live.current = { scope, epoch: live.current.epoch + 1 };
    for (const ctl of owned.current) ctl.abort();
    pending.current = null; blocked.current = false; operation.current = false;
  }
  const epoch = live.current.epoch;
  const [snapshot, setSnapshot] = useState<View>(() => blank(epoch));
  const view = snapshot.epoch === epoch ? snapshot : blank(epoch);
  const allowed = uuid(userId) && (role === "admin" || role === "member");
  const active = () => mounted.current && live.current.epoch === epoch && live.current.scope === scope;
  const update = (patch: Partial<View>) => { if (active()) setSnapshot(old => ({ ...(old.epoch === epoch ? old : blank(epoch)), ...patch })); };

  async function bounded<T>(work: (signal: AbortSignal) => Promise<T>): Promise<T> {
    const ctl = new AbortController(); owned.current.add(ctl);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let cancelled: (() => void) | undefined;
    try {
      return await Promise.race([work(ctl.signal), new Promise<never>((_, reject) => {
        cancelled = () => reject(Error("cancelled")); ctl.signal.addEventListener("abort", cancelled, { once: true });
        timer = setTimeout(() => ctl.abort(), 15000);
      })]);
    } finally {
      clearTimeout(timer); if (cancelled) ctl.signal.removeEventListener("abort", cancelled); owned.current.delete(ctl);
    }
  }
  async function refresh(before: string | null = null) {
    if (!active() || !allowed || (before !== null && !uuid(before))) return false;
    const sequence = ++listSequence.current;
    update({ loading: true, loaded: false, items: [], listError: "", next: null, cursor: before });
    try {
      const r = await bounded(signal => api.GET("/api/v1/media-requests", { params: { query: { limit: 25, ...(filtered ? { state: "pending" as const } : {}), ...(before ? { before_id: before } : {}) } }, signal, cache: "no-store" }));
      if (!active() || sequence !== listSequence.current) return false;
      if (r.response.status !== 200 || !r.data || !Array.isArray(r.data.items) || r.data.items.length > 25 || !r.data.items.every(item => validRequest(item, userId, role) && (!filtered || item.state === "pending")) || new Set(r.data.items.map(item => item.id)).size !== r.data.items.length) throw Error();
      update({ loaded: true, items: r.data.items, next: r.data.items.length === 25 ? r.data.items[24].id : null }); return true;
    } catch { if (sequence === listSequence.current) update({ listError: "请求列表读取失败，请手动刷新。" }); return false; }
    finally { if (sequence === listSequence.current) update({ loading: false }); }
  }
  async function loadOptions() {
    if (!active() || !allowed || role !== "admin") return false;
    const sequence = ++optionSequence.current;
    update({ options: null, optionsLoading: true, optionsError: "" });
    try {
      const [r, p, i, d] = await bounded(signal => Promise.all([
        api.GET("/api/v1/library-roots", { signal, cache: "no-store" }), api.GET("/api/v1/policies", { signal, cache: "no-store" }),
        api.GET("/api/v1/indexers", { signal, cache: "no-store" }), api.GET("/api/v1/download-clients", { signal, cache: "no-store" }),
      ]));
      if (!active() || sequence !== optionSequence.current) return false;
      if (!r.response.ok || !p.response.ok || !i.response.ok || !d.response.ok || !r.data || !p.data || !i.data || !d.data || ![r.data.items, p.data.items, i.data.items, d.data.items].every(Array.isArray)) throw Error();
      update({ options: { roots: r.data.items.filter(x => x.health === "online" && x.baseline_completed), policies: p.data.items, indexers: i.data.items, downloaders: d.data.items } }); return true;
    } catch { if (sequence === optionSequence.current) update({ optionsError: "审批配置读取失败，请重新读取。" }); return false; }
    finally { if (sequence === optionSequence.current) update({ optionsLoading: false }); }
  }
  function readOriginal(): Command | null {
    const disk = readJournal(localStorage, userId, role);
    if (pending.current && (!disk || !sameCommand(disk, pending.current))) throw Error("journal changed");
    return disk;
  }
  async function lookup(c: Command, signal: AbortSignal) {
    return c.kind === "approve"
      ? api.GET("/api/v1/media-requests/{request_id}/approval-commands/{idempotency_key}", { params: { path: { request_id: c.requestId, idempotency_key: c.key } }, signal, cache: "no-store" })
      : api.GET("/api/v1/media-requests/commands/{idempotency_key}", { params: { path: { idempotency_key: c.key } }, signal, cache: "no-store" });
  }
  async function post(c: Command, signal: AbortSignal) {
    const params = { header: { "Idempotency-Key": c.key } };
    if (c.kind === "create") return api.POST("/api/v1/media-requests", { params, body: c.body, signal });
    if (c.kind === "close") return api.POST("/api/v1/media-requests/{request_id}/close", { params: { ...params, path: { request_id: c.requestId } }, body: c.body, signal });
    return api.POST("/api/v1/media-requests/{request_id}/approve", { params: { ...params, path: { request_id: c.requestId } }, body: c.body, signal });
  }
  async function run(c: Command, mode: "fresh" | "receipt" | "retry") {
    if (!active() || !allowed || operation.current || (mode !== "receipt" && blocked.current)) return false;
    const lifecycle = generation.current;
    const current = () => active() && generation.current === lifecycle;
    operation.current = true; update({ busy: true, message: "", current: null });
    try {
      // Holding this lock through receipt verification prevents another tab from issuing a new command.
      // A browser without Web Locks can still read receipts, but cannot issue a POST.
      const action = async (lockSignal?: AbortSignal) => {
        if (!current() || lockSignal?.aborted) return false;
        if (mode !== "receipt") {
          if (blocked.current) throw Error("blocked");
          const disk = readOriginal();
          if (mode === "fresh" ? disk !== null : !disk || !sameCommand(disk, c)) throw Error("pending changed");
          pending.current = c; update({ pending: c, phase: "unknown" });
          saveJournal(localStorage, c);
        }
        pending.current = c; update({ pending: c, phase: "unknown" });
        const r = await bounded(signal => mode === "receipt" ? lookup(c, signal) : post(c, signal));
        if (!current() || lockSignal?.aborted) return false;
        const status = r.response.status;
        const successStatus = mode === "receipt" || c.kind === "close" ? 200 : 201;
        if (status === successStatus && validReceipt(r.data, c)) {
          const disk = readJournal(localStorage, userId, role);
          if (disk && !sameCommand(disk, c)) {
            pending.current = disk; blocked.current = false;
            update({ pending: disk, phase: "unknown", message: "原回执已确认；另一标签页留下了不同命令，请继续读取该命令的原回执。" });
            return true;
          }
          clearVerifiedJournal(localStorage, c);
          pending.current = null; blocked.current = false;
          update({ pending: null, phase: "confirmed", message: c.kind === "approve" ? "原批准回执已确认：订阅已创建，可由后台调度。此回执不表示下载成功，请刷新查看当前状态。" : "原命令回执已确认；回执是历史快照，请刷新查看当前状态。" });
          return true;
        }
        if (status === 409) update({ phase: "conflict", message: "命令发生冲突。请读取当前请求，再明确结束本地冲突。" });
        else if (mode === "receipt" && status === 404) {
          const disk = readJournal(localStorage, userId, role);
          blocked.current = !disk || !sameCommand(disk, c);
          update({ phase: blocked.current ? "blocked" : "missing", message: blocked.current ? "原回执未找到，且本地记录已变化。保留原键，继续只读核实。" : "尚未找到原回执。可明确使用相同命令键与原始内容重试。" });
        }
        else if (status === 401 || status === 403 || status === 503) update({ phase: "protected", message: "原结果未确认，权限或服务暂不可用。保留原键，仅可重新读取回执。" });
        else update({ phase: "unknown", message: "原结果未确认，已保留原键。请读取原回执。" });
        return false;
      };
      if (typeof navigator !== "undefined" && navigator.locks) return await bounded(signal => navigator.locks.request(journalKey(userId), { mode: "exclusive", signal }, () => action(signal)));
      if (mode === "receipt") return await action();
      throw Error("lock unavailable");
    } catch {
      if (current()) {
        blocked.current = true;
        update({ pending: pending.current, phase: "blocked", message: "操作结果或本地存储未确认。已阻止新写入；请读取原回执核实。" });
      }
      return false;
    } finally { if (current()) { operation.current = false; update({ busy: false }); } }
  }
  function start(kind: Command["kind"], body: Command["body"], row?: MediaRequest) {
    if (!active() || !allowed || operation.current || pending.current || blocked.current) return Promise.resolve(false);
    try {
      if (readOriginal()) throw Error("pending exists");
      if (row && (!validRequest(row, userId, role) || row.state !== "pending" || !("expected_version" in body) || body.expected_version !== row.version || !view.items.some(item => item.id === row.id && item.version === row.version && sameIntent(item.intent, row.intent) && item.owner_id === row.owner_id))) throw Error("stale row");
      if (kind === "approve") {
        const b = body as ApproveBody, o = view.options;
        if (role !== "admin" || !row || !o || !o.roots.some(r => r.id === b.root_id && r.media_type === row.intent.media_type) || !o.policies.some(p => p.id === b.quality_policy_id) || !o.downloaders.some(d => d.id === b.downloader_id) || !b.indexer_ids.every(id => o.indexers.some(i => i.id === id))) throw Error("invalid options");
      }
      return run(makeCommand(userId, role as Role, kind, body, row), "fresh");
    } catch { blocked.current = true; update({ phase: "blocked", message: "输入、当前版本或存储未通过校验，未发送请求。请刷新核实。" }); return Promise.resolve(false); }
  }
  async function readReceipt() {
    if (!active() || !allowed || operation.current) return false;
    try {
      const c = pending.current ?? readOriginal();
      if (!c) return false;
      return await run(c, "receipt");
    } catch { blocked.current = true; update({ phase: "blocked", message: "本地命令无法安全读取，已阻止新写入。" }); return false; }
  }
  async function retryOriginal() {
    if (view.phase !== "missing" || !pending.current || blocked.current) return false;
    return run(pending.current, "retry");
  }
  async function readCurrent(requestId?: string) {
    const c = pending.current, target = c?.requestId ?? requestId;
    if (!active() || operation.current || !c || !uuid(target) || !["conflict", "verified-conflict"].includes(view.phase)) return false;
    const lifecycle = generation.current;
    const alive = () => active() && generation.current === lifecycle;
    operation.current = true; update({ busy: true, current: null, phase: "conflict" });
    try {
      const r = await bounded(signal => api.GET("/api/v1/media-requests/{request_id}", { params: { path: { request_id: target } }, signal, cache: "no-store" }));
      if (!alive()) return false;
      if (r.response.status !== 200 || !validRequest(r.data, userId, role) || r.data.id !== target || r.data.owner_id !== c.ownerId || !sameIntent(r.data.intent, c.intent)) throw Error();
      update({ current: r.data, phase: "verified-conflict", message: "当前请求已读取。结束冲突仅移除本地记录，不会执行新命令。" }); return true;
    } catch { if (alive()) update({ message: "当前请求读取失败，原键继续保留。" }); return false; }
    finally { if (alive()) { operation.current = false; update({ busy: false }); } }
  }
  async function finishConflict() {
    const c = pending.current;
    if (!active() || operation.current || !c || view.phase !== "verified-conflict" || !view.current) return false;
    const lifecycle = generation.current;
    const alive = () => active() && generation.current === lifecycle;
    operation.current = true;
    try {
      if (!navigator.locks) throw Error();
      return await bounded(signal => navigator.locks.request(journalKey(userId), { mode: "exclusive", signal }, async () => {
        if (!alive() || signal.aborted) return false;
        clearVerifiedJournal(localStorage, c); pending.current = null; blocked.current = false;
        update({ phase: "idle", pending: null, current: null, message: "本地冲突已结束。请刷新后重新选择操作。" }); return true;
      }));
    } catch { if (alive()) update({ phase: "blocked", message: "本地记录无法安全清除，原键继续保留。" }); return false; }
    finally { if (alive()) operation.current = false; }
  }
  useEffect(() => {
    mounted.current = true; pending.current = null; blocked.current = false; operation.current = false;
    update(blank(epoch));
    if (allowed) {
      try { const c = readJournal(localStorage, userId, role); pending.current = c; if (c) update({ pending: c, phase: "unknown", message: "有未确认命令，请明确读取原回执。不会自动重发。" }); }
      catch { blocked.current = true; update({ phase: "blocked", message: "存储记录损坏、不可读或属于其他角色。已阻止新写入。" }); }
      void refresh(); if (role === "admin") void loadOptions();
    }
    const changed = (event: StorageEvent) => {
      if (!active() || (event.key !== null && event.key !== journalKey(userId))) return;
      try {
        const c = readJournal(localStorage, userId, role);
        if (pending.current && (!c || !sameCommand(c, pending.current))) throw Error();
        if (c) { pending.current = c; update({ pending: c, phase: "unknown", current: null, message: "另一标签页存在未确认命令，请读取原回执。" }); }
      } catch { blocked.current = true; update({ phase: "blocked", current: null, message: "命令记录被其他标签页移除或替换，保留已知原键。请读取原回执。" }); }
    };
    window.addEventListener("storage", changed);
    return () => { mounted.current = false; generation.current++; for (const ctl of owned.current) ctl.abort(); window.removeEventListener("storage", changed); };
    // Identity transitions synchronously advance epoch before passive cleanup.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, epoch]);
  return { ...view, refresh, loadOptions, create: (body: MediaIntent) => start("create", body), close: (row: MediaRequest, body: CloseBody) => start("close", body, row), approve: (row: MediaRequest, body: ApproveBody) => start("approve", body, row), readReceipt, retryOriginal, readCurrent, finishConflict, canWrite: allowed && !view.busy && !view.pending && !["blocked", "conflict", "protected", "unknown"].includes(view.phase) };
}

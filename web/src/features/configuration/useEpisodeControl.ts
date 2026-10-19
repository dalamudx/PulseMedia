import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import {
  cleanEpisodeControlBody, clearEpisodeControlRecovery, episodeControlRecoveryKey,
  isEpisodeControlReceipt, parseEpisodeControlRecovery, saveEpisodeControlRecovery,
  type EpisodeControlAction, type EpisodeControlDetail, type EpisodeControlFeedbackKind, type EpisodeControlRecovery,
} from "./episodeControlRecovery";

export type EpisodeControlOptions = {
  subscriptionId: string;
  expectedVersion: number;
  onUpdated: (subscription: EpisodeControlDetail) => void;
};
export type EpisodeControl = {
  execute: (unitId: string, action: EpisodeControlAction) => Promise<boolean>;
  retryOriginal: () => Promise<boolean>;
  refresh: () => Promise<boolean>;
  resolveConflict: () => Promise<boolean>;
  busy: boolean;
  blocked: boolean;
  pending: EpisodeControlRecovery | null;
  message: string;
  feedbackKind: EpisodeControlFeedbackKind;
  canRetry: boolean;
  canResolveConflict: boolean;
};

type View = { identity: string; busy: boolean; blocked: boolean; pending: EpisodeControlRecovery | null; message: string; feedbackKind: EpisodeControlFeedbackKind; canRetry: boolean; canResolveConflict: boolean };

export function useEpisodeControl(options: EpisodeControlOptions): EpisodeControl {
  const { user } = useAuth();
  const userId = user?.id ?? "", role = user?.role ?? "", { subscriptionId } = options;
  const identity = `${userId}:${role}:${subscriptionId}`;
  const live = useRef(identity); live.current = identity;
  const callbacks = useRef(options); callbacks.current = options;
  const mounted = useRef(false), scope = useRef(identity);
  const request = useRef<AbortController | null>(null);
  const record = useRef<EpisodeControlRecovery | null>(null);
  const paused = useRef(false), confirmed = useRef(false), needsDetail = useRef(false), storageBlocked = useRef(false);
  const conflict = useRef(false);
  const [view, setView] = useState<View>({ identity, busy: false, blocked: false, pending: null, message: "", feedbackKind: "idle", canRetry: false, canResolveConflict: false });
  const current = () => mounted.current && live.current === identity && scope.current === identity && !!userId;
  function update(message: string, feedbackKind: EpisodeControlFeedbackKind) {
    setView({ identity, busy: !!request.current, blocked: paused.current || storageBlocked.current || needsDetail.current || !!record.current,
      pending: record.current ? structuredClone(record.current) : null, message, feedbackKind,
      canRetry: !!record.current && !confirmed.current && !paused.current && !storageBlocked.current && !request.current,
      canResolveConflict: !!record.current && conflict.current && !storageBlocked.current && !request.current });
  }
  function readPending() {
    const saved = parseEpisodeControlRecovery(sessionStorage.getItem(episodeControlRecoveryKey(userId, role, subscriptionId)), userId, subscriptionId);
    if (record.current && JSON.stringify(saved) !== JSON.stringify(record.current)) throw Error("original episode control changed");
    return saved;
  }
  async function refresh(): Promise<boolean> {
    if (!current() || request.current) return false;
    const controller = new AbortController(); request.current = controller;
    const active = () => current() && request.current === controller && !controller.signal.aborted;
    needsDetail.current = true;
    update("正在读取订阅最新状态。", confirmed.current ? "confirmed" : record.current ? "pending" : "idle");
    let message = "订阅详情读取失败，请刷新；已确认的命令不会再次发送。";
    let kind: EpisodeControlFeedbackKind = "error";
    try {
      const r = await api.GET("/api/v1/subscriptions/{id}", { params: { path: { id: subscriptionId } }, signal: controller.signal, cache: "no-store" });
      if (!active()) return false;
      if ([401, 403, 404, 409].includes(r.response.status)) { paused.current = true; message = "订阅权限或版本已变化，操作已暂停，原请求已保留。"; return false; }
      if (r.response.status !== 200 || r.error || !r.data || r.data.id !== subscriptionId || (role !== "admin" && r.data.user_id !== userId) || !Number.isSafeInteger(r.data.intent_version) || r.data.intent_version < 1) throw Error("invalid subscription detail");
      // Consumers get their own copy; pending commands retain their original version and body.
      callbacks.current.onUpdated(structuredClone(r.data));
      if (!active()) return false;
      needsDetail.current = false;
      message = record.current ? "原请求尚未确认；读取详情不代表命令成功。" : confirmed.current ? "操作已确认，最新状态已更新。" : "订阅状态已更新。";
      kind = record.current ? "pending" : confirmed.current ? "confirmed" : "idle";
      return true;
    } catch { return false; }
    finally { if (active()) { request.current = null; update(message, kind); } }
  }
  async function send(original: EpisodeControlRecovery, fresh: boolean): Promise<boolean> {
    if (!current() || request.current || paused.current || confirmed.current && needsDetail.current || storageBlocked.current) return false;
    const controller = new AbortController(); request.current = controller;
    const active = () => current() && request.current === controller && !controller.signal.aborted;
    record.current = structuredClone(original); confirmed.current = false; conflict.current = false;
    update("正在提交逐集操作，等待原请求确认。", "pending");
    let message = "操作结果未确认，原请求已保留。可明确重试原请求。";
    let kind: EpisodeControlFeedbackKind = "pending";
    let received = false;
    try {
      try { saveEpisodeControlRecovery(sessionStorage, original, role, fresh); }
      catch { storageBlocked.current = true; message = "恢复记录无法可靠保存，操作已暂停。"; kind = "error"; return false; }
      if (!active()) return false;
      const r = await api.PUT("/api/v1/subscriptions/{id}/control", {
        params: { path: { id: subscriptionId }, header: { "Idempotency-Key": original.key } },
        body: structuredClone(original.body), signal: controller.signal,
      });
      if (!active()) return false;
      if ([401, 403, 404, 409].includes(r.response.status)) { paused.current = true; conflict.current = r.response.status === 409; message = "订阅权限或版本已变化，操作已暂停，原请求已保留。请刷新核对。"; kind = "error"; return false; }
      if (r.response.status !== 200 || r.error || !isEpisodeControlReceipt(r.data, original)) return false;
      confirmed.current = true; needsDetail.current = true;
      try { clearEpisodeControlRecovery(sessionStorage, original, role); record.current = null; }
      catch { storageBlocked.current = true; message = "命令已确认，但本地恢复记录清理失败；操作已暂停，请刷新核对。"; kind = "error"; return true; }
      received = true;
    } catch { return false; }
    finally { if (active()) { request.current = null; update(message, kind); } }
    if (received && current()) { await refresh(); return true; }
    return false;
  }
  async function execute(unitId: string, action: EpisodeControlAction): Promise<boolean> {
    if (!current() || request.current || paused.current || storageBlocked.current || needsDetail.current || record.current) return false;
    try {
      const saved = readPending();
      if (saved) { record.current = saved; update("存在未确认的原请求，请先处理原请求。", "pending"); return false; }
      if (!["skip", "resume", "recheck"].includes(action)) return false;
      const body = cleanEpisodeControlBody({ expected_version: callbacks.current.expectedVersion, unit_id: unitId,
        ...(action === "recheck" ? { action: "recheck" } : { action: "skip", skipped: action === "skip" }) });
      return await send({ version: 1, userId, subscriptionId, key: crypto.randomUUID(), body }, true);
    } catch { storageBlocked.current = true; update("恢复记录或操作参数校验失败，操作已暂停。", "error"); return false; }
  }
  async function retryOriginal(): Promise<boolean> {
    if (!current() || request.current || paused.current || storageBlocked.current || confirmed.current) return false;
    try {
      const saved = readPending();
      if (!saved) return false;
      return await send(saved, false);
    } catch { storageBlocked.current = true; update("原请求恢复记录已变化，操作已暂停。", "error"); return false; }
  }
  async function resolveConflict(): Promise<boolean> {
    if (!current() || request.current || !conflict.current || storageBlocked.current || !record.current) return false;
    const original = structuredClone(record.current);
    if (!await refresh() || !current() || request.current || !conflict.current) return false;
    try {
      const saved = readPending();
      if (!saved || JSON.stringify(saved) !== JSON.stringify(original)) throw Error("conflicting command changed");
      clearEpisodeControlRecovery(sessionStorage, original, role);
      record.current = null; paused.current = false; conflict.current = false; confirmed.current = false; needsDetail.current = false;
      update("冲突命令已结束，当前状态已更新。请重新选择操作。", "idle");
      return true;
    } catch {
      storageBlocked.current = true;
      update("原请求记录已变化，操作保持暂停。", "error");
      return false;
    }
  }
  useEffect(() => {
    mounted.current = true; scope.current = identity;
    record.current = null; paused.current = false; confirmed.current = false; needsDetail.current = false; storageBlocked.current = false; conflict.current = false;
    if (userId) {
      try { record.current = readPending(); }
      catch { storageBlocked.current = true; }
    }
    update(storageBlocked.current ? "恢复记录不可用，操作已暂停。" : record.current ? "存在未确认的原请求，可明确重试原请求。" : "",
      storageBlocked.current ? "error" : record.current ? "pending" : "idle");
    return () => { mounted.current = false; request.current?.abort(); request.current = null; };
  }, [identity]);
  const visible = view.identity === identity && !!userId;
  return { execute, retryOriginal, refresh, resolveConflict,
    busy: visible && view.busy, blocked: !visible || view.blocked, pending: visible && view.pending ? structuredClone(view.pending) : null,
    message: visible ? view.message : "", feedbackKind: visible ? view.feedbackKind : "idle", canRetry: visible && view.canRetry, canResolveConflict: visible && view.canResolveConflict };
}

import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import {
  cleanSubscriptionIntent, clearSubscriptionRecovery, parseSubscriptionRecovery,
  subscriptionCommand, subscriptionCreationMessage, subscriptionRecoveryKey, subscriptionUUID,
  type CreationPhase, type SubscriptionIntent, type SubscriptionRecovery, type SubscriptionResult,
} from "./subscriptionCreationRecovery";

export function useSubscriptionCreation(options: { onSuccess: (result: SubscriptionResult, intent: SubscriptionIntent) => void }) {
  const { user } = useAuth();
  const userId = user?.id ?? "";
  const owner = useRef(userId); owner.current = userId;
  const callbacks = useRef(options); callbacks.current = options;
  const mounted = useRef(false);
  const denied = useRef(false);
  const pending = useRef<SubscriptionRecovery | null>(null);
  const request = useRef<AbortController | null>(null);
  const joined = useRef<Promise<boolean> | null>(null);
  const [view, setView] = useState<{ userId: string; phase: CreationPhase; recovery: SubscriptionRecovery | null }>({ userId, phase: "idle", recovery: null });
  const current = view.userId === userId && !!userId;
  const phase = current ? view.phase : "idle";
  const recovery = current ? view.recovery : null;

  function update(next: CreationPhase, record = pending.current) {
    setView({ userId, phase: next, recovery: record });
  }
  function readPending() {
    const saved = parseSubscriptionRecovery(sessionStorage.getItem(subscriptionRecoveryKey(userId)), userId);
    const original = pending.current;
    if (original && (!saved || saved.key !== original.key || JSON.stringify(saved.body) !== JSON.stringify(original.body))) throw Error("original command changed");
    return saved;
  }
  async function run(record: SubscriptionRecovery, fresh = false, retryOriginal = false): Promise<boolean> {
    if (!mounted.current || owner.current !== userId || denied.current || request.current || record.userId !== userId) return false;
    const controller = new AbortController();
    request.current = controller;
    const active = () => mounted.current && owner.current === userId && request.current === controller && !controller.signal.aborted;
    pending.current = record;
    update(fresh ? "creating" : "recovering", record);
    try {
      const outcome = await subscriptionCommand({
        record, storage: sessionStorage, fresh, retryOriginal, active,
        lookup: async () => {
          const r = await api.GET("/api/v1/subscription-commands/{idempotency_key}", {
            params: { path: { idempotency_key: record.key } }, signal: controller.signal, cache: "no-store",
          });
          return { status: r.response.status, data: r.data };
        },
        send: async () => {
          const r = await api.POST("/api/v1/subscriptions", {
            params: { header: { "Idempotency-Key": record.key } }, body: record.body, signal: controller.signal,
          });
          return { status: r.response.status, data: r.data };
        },
      });
      if (!active()) return false;
      if (outcome.phase === "succeeded") {
        // Complete local recovery before exposing a success or allowing a new command.
        try { clearSubscriptionRecovery(sessionStorage, record); }
        catch { update("blocked", record); return false; }
        pending.current = null;
        update("succeeded", null);
        callbacks.current.onSuccess(outcome.result, record.body);
        return true;
      }
      if (outcome.phase === "auth") denied.current = true;
      update(outcome.phase, record);
      return false;
    } catch {
      if (active()) update("blocked", record);
      return false;
    } finally {
      if (request.current === controller) request.current = null;
    }
  }
  function join(task: Promise<boolean>) {
    joined.current = task;
    void task.finally(() => { if (joined.current === task) joined.current = null; });
    return task;
  }
  function recover(retryOriginal = false): Promise<boolean> {
    if (!mounted.current || owner.current !== userId || !userId || denied.current || request.current) return Promise.resolve(false);
    try {
      const record = readPending();
      if (!record) { pending.current = null; update("idle", null); return Promise.resolve(false); }
      // A stored record always wins over a newly selected target or changed options.
      return join(run(record, false, retryOriginal && phase === "missing"));
    } catch { update("blocked"); return Promise.resolve(false); }
  }
  function start(body: SubscriptionIntent): Promise<boolean> {
    if (!mounted.current || owner.current !== userId || !subscriptionUUID(userId) || denied.current || request.current) return Promise.resolve(false);
    try {
      const record = readPending();
      if (record) return join(run(record));
      if (pending.current || phase === "blocked") { update("blocked"); return Promise.resolve(false); }
      const original = cleanSubscriptionIntent(body);
      return join(run({ version: 1, userId, key: crypto.randomUUID(), body: original }, true));
    } catch { update("blocked"); return Promise.resolve(false); }
  }

  useEffect(() => {
    mounted.current = true;
    denied.current = false;
    pending.current = null;
    setView({ userId, phase: "idle", recovery: null });
    if (userId) void recover();
    return () => {
      mounted.current = false;
      request.current?.abort();
      request.current = null;
      // Every owned request is awaited by run; late completions are fenced by owner/controller.
      const task = joined.current;
      if (task) void task.then(() => { if (joined.current === task) joined.current = null; });
    };
  }, [userId]);

  const busy = phase === "creating" || phase === "recovering";
  return {
    start, recover: () => recover(), retryOriginal: () => recover(true), phase, busy, recovery,
    message: subscriptionCreationMessage(phase),
    canRecover: !!userId && !busy && phase !== "auth" && phase !== "idle" && phase !== "succeeded",
    canRetryOriginal: phase === "missing" && !!recovery,
  };
}

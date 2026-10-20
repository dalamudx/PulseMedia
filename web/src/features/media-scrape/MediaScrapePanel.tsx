import {useCallback, useLayoutEffect, useRef, useState} from "react";
import {api} from "@/api/client";
import {useAuth} from "@/features/auth/AuthContext";
import {scrapeCommand, type ScrapeOutcome} from "./recovery";
import "./media-scrape.css";

const messages: Partial<Record<ScrapeOutcome["phase"], string>> = {
  acknowledged: "批次已确认受理，仅表示任务已排队。请刷新回执查看记录进度。",
  missing: "尚未查到原命令回执，结果仍未确认。可继续读取，或明确重试原批次；重试沿用原命令键。",
  unknown: "读取失败或提交结果未确认，操作就绪状态已撤回。原命令键如已保存将继续保留，请重新读取回执。",
  blocked: "恢复记录不可用或已变化，批量操作已暂停。请恢复浏览器存储后重新读取。",
  auth: "当前管理员授权不可用，操作已暂停。请重新认证后读取原回执。",
};
const previewLabels = [["total", "符合条件文件"], ["missing", "待补齐"], ["queued", "排队中"], ["running", "执行中"], ["complete", "历史任务成功"], ["review", "待人工复核"]] as const;
const batchLabels = [["queued", "排队中"], ["running", "执行中"], ["succeeded", "历史任务成功"], ["review", "待人工复核"]] as const;

export function MediaScrapePanel() {
  const {user} = useAuth();
  return user?.role === "admin" ? <AdminScrapePanel key={`${user.id}:${user.role}`} userId={user.id}/> : null;
}

function AdminScrapePanel({userId}: {userId: string}) {
  const {user} = useAuth();
  const identity = useRef("");
  identity.current = user?.role === "admin" ? user.id : "";
  const live = useRef(false);
  const request = useRef<AbortController | null>(null);
  const receiptKey = useRef<string | undefined>(undefined);
  const [outcome, setOutcome] = useState<ScrapeOutcome>({phase: "unknown"});
  const [busy, setBusy] = useState(false);
  const [confirmed, setConfirmed] = useState(false);

  const run = useCallback(async (mode: "read" | "start" | "retry") => {
    if (!live.current || identity.current !== userId || request.current) return;
    const controller = new AbortController();
    request.current = controller;
    const active = () => live.current && identity.current === userId && !controller.signal.aborted;
    setBusy(true);
    setConfirmed(false);
    // Drop old counters and ready state before every read or write.
    setOutcome({phase: "unknown"});
    try {
      const result = await scrapeCommand({
        mode, userId, storage: window.localStorage, active,
        receiptKey: mode === "read" ? receiptKey.current : undefined,
        newKey: () => crypto.randomUUID(),
        get: async commandKey => {
          const result = await api.GET("/api/v1/media/scrape", {
            signal: controller.signal, cache: "no-store",
            params: {query: {command_key: commandKey}},
          });
          return {status: result.response.status, data: result.data};
        },
        post: async commandKey => {
          if (!active()) throw Error("inactive account");
          const result = await api.POST("/api/v1/media/scrape", {
            signal: controller.signal, params: {header: {"Idempotency-Key": commandKey}}, body: {all: true},
          });
          return {status: result.response.status, data: result.data};
        },
      });
      if (!active() || result.phase === "inactive") return;
      if (result.commandKey) receiptKey.current = result.commandKey;
      setOutcome(result);
    } catch {
      if (active()) setOutcome({phase: "blocked"});
    } finally {
      if (request.current === controller) request.current = null;
      if (active()) setBusy(false);
    }
  }, [userId]);

  useLayoutEffect(() => {
    live.current = true;
    void run("read");
    return () => {
      live.current = false;
      request.current?.abort();
      request.current = null;
    };
  }, [run]);

  const snapshot = outcome.snapshot;
  const batch = outcome.batch ?? snapshot?.batch;
  const retry = outcome.phase === "missing" && !!outcome.pending;
  const ready = outcome.phase === "ready" || outcome.phase === "acknowledged";
  const eligible = snapshot ? snapshot.preview.missing + snapshot.preview.complete : 0;
  const canSubmit = !busy && (retry || (ready && !!snapshot && eligible > 0));
  const message = busy ? "正在读取或提交批次，请等待确认…" : messages[outcome.phase];
  const failed = ["unknown", "blocked", "auth"].includes(outcome.phase);
  return <section className="config-editor media-scrape" aria-labelledby="media-scrape-title" aria-busy={busy}>
    <div className="media-scrape-heading">
      <h2 id="media-scrape-title">文件刮削与元数据补齐</h2>
      <button type="button" className="btn-secondary" disabled={busy} onClick={() => void run("read")}>刷新状态与回执</button>
    </div>
    <p>对当前媒体库中已入库且符合条件的文件补齐 NFO 与海报，保留所有既有文件。排队中、执行中及待人工复核的文件不会重复排队；已完成的文件可再次检查以补齐缺失海报。</p>
    <p className="config-description">以下计数均为已记录的任务状态；任务完成不证明文件目前仍存在，也不表示已有文件会被覆盖。</p>
    {snapshot ? <dl className="media-scrape-counts" aria-label="媒体库任务状态记录">
      {previewLabels.map(([key, label]) => <div key={key}><dt>{label}</dt><dd><span className={`media-scrape-badge media-scrape-${key}`}>{snapshot.preview[key].toLocaleString()}</span></dd></div>)}
    </dl> : null}
    {batch ? <div className="media-scrape-receipt">
      <h3>批次回执</h3>
      <p>批次编号：<code>{batch.id}</code></p>
      <p>受理时间：<time dateTime={batch.created_at}>{new Date(batch.created_at).toLocaleString()}</time> · 共 {batch.total.toLocaleString()} 项任务</p>
      <dl className="media-scrape-counts" aria-label="批次任务状态记录">
        {batchLabels.map(([key, label]) => <div key={key}><dt>{label}</dt><dd><span className={`media-scrape-badge media-scrape-${key}`}>{batch[key].toLocaleString()}</span></dd></div>)}
      </dl>
      <p>已结束 {(batch.succeeded + batch.review).toLocaleString()} / {batch.total.toLocaleString()} 项（包含待人工复核）。刷新回执可查看最新记录。</p>
    </div> : snapshot && !outcome.pending ? <p>暂无批次回执。</p> : null}
    {message ? <p role={failed && !busy ? "alert" : "status"} className={failed && !busy ? "config-error" : "config-description"}>{message}</p> : null}
    <div className="media-scrape-actions">
      <label className="media-scrape-confirm"><input type="checkbox" checked={confirmed} disabled={!canSubmit} onChange={event => setConfirmed(event.target.checked)}/><span>{retry ? "我确认仅重试原批次，沿用原命令键" : "我确认对全部符合条件的已入库文件执行补齐检查"}</span></label>
      <button type="button" className="btn-secondary" disabled={!canSubmit || !confirmed} onClick={() => {if (canSubmit && confirmed) void run(retry ? "retry" : "start");}}>{retry ? "明确重试原批次" : "批量补齐 NFO 与海报"}</button>
    </div>
    {ready && snapshot && !eligible ? <p className="config-description">当前没有可新排队的文件。</p> : null}
  </section>;
}

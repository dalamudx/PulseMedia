import type { OperationLiveState } from "./useOperationLive";
import { displayTime, planStateLabel, safeFileName, stepActionLabel, stepStateLabel } from "./operationActivityModel";
import "./operation-activity.css";

export function OperationTimeline({ live }: { live: OperationLiveState }) {
  const busy = live.loading || live.loadingMore;
  return <section className="operation-live" aria-label="整理步骤" aria-busy={busy}>
    <h3>整理步骤</h3>
    <p>计划状态：{live.timeline ? live.timeline.plan_id ? planStateLabel(live.timeline.plan_status) : "暂无整理计划" : "尚未读取"}</p>
    {live.timelineStale ? <p role="status">步骤尚未重新确认，显示上次读取的记录。</p> : null}
    {live.timelineError ? <div role="alert" className="operation-live-error">
      <p>{live.timelineError}</p>
      <button type="button" className="btn-secondary" disabled={busy} onClick={() => void live.refresh()}>重新读取整理步骤</button>
    </div> : null}
    {live.loading ? <p role="status">正在读取整理步骤…</p> : null}
    {!live.loading && !live.timelineError && live.timeline && live.steps.length === 0 ? <p role="status">当前计划没有整理步骤。</p> : null}
    {live.steps.length ? <ol className="operation-live-steps">{live.steps.map(step => <li key={step.step_id} value={step.order}>
      <div className="operation-live-heading"><strong>{safeFileName(step.file_name)}</strong><span>{stepStateLabel(step.state)}</span></div>
      <p>{stepActionLabel(step.action)}</p>
      <p className="operation-live-meta">更新时间：{displayTime(step.updated_at)}{step.finished_at ? <> · 结束时间：{displayTime(step.finished_at)}</> : null}</p>
    </li>)}</ol> : null}
    {live.timeline?.next_cursor ? <button type="button" className="btn-secondary" disabled={busy} onClick={() => void live.loadMore()}>{live.loadingMore ? "正在读取更多步骤…" : live.timelineError ? "重试加载更多步骤" : "加载更多步骤"}</button> : null}
    {live.loadingMore ? <p role="status">正在读取更多步骤…</p> : null}
  </section>;
}

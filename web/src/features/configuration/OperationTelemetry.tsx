import type { OperationDetails } from "./operationActivityModel";
import { displayTime, formatRate, nativeRate, observationFresh, progressSnapshot } from "./operationActivityModel";
import "./operation-activity.css";

export function OperationTelemetry({ telemetry, stale = false }: {
  telemetry: OperationDetails["telemetry"] | undefined; stale?: boolean;
}) {
  const progress = progressSnapshot(telemetry);
  const progressStale = stale || !observationFresh(telemetry?.progress_observed_at);
  const rateKnown = (telemetry?.rate_status === "current" || telemetry?.rate_status === "stale") && nativeRate(telemetry?.download_rate_bytes_per_second) !== null;
  const rateStale = stale || telemetry?.rate_status !== "current" || !observationFresh(telemetry?.rate_observed_at);
  return <section className="operation-live" aria-label="下载观测">
    <h3>下载观测</h3>
    <div className="operation-live-heading"><span>下载进度</span><strong>{progress ? `${progress.percent}%` : "进度未知"}</strong></div>
    {progress ? <>
      <progress max={100} value={progress.percent} aria-label="下载进度" />
      <p className="operation-live-meta">{progress.source} · {progressStale ? "观测已过期" : "当前观测"} · {displayTime(telemetry?.progress_observed_at)}</p>
    </> : <p className="operation-live-meta">暂无可信进度观测</p>}
    <div className="operation-live-heading"><span>下载速度</span><strong>{rateKnown ? formatRate(telemetry?.download_rate_bytes_per_second) : "速度未知"}</strong></div>
    <p className="operation-live-meta">下载器记录 · {rateKnown ? rateStale ? "观测已过期" : "当前观测" : "暂无可信速度观测"} · {displayTime(telemetry?.rate_observed_at)}</p>
    {stale ? <p role="status">读取尚未确认，显示上次观测。</p> : null}
  </section>;
}

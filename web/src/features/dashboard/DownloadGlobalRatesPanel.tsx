import { useMemo } from "react";
import { Link } from "react-router-dom";
import { RefreshCw } from "lucide-react";
import { factTime } from "./useDashboardFacts";
import { downloadClientLabels, currentClientRate, currentGlobalRate, globalRateCurve, globalRateText, useDownloadGlobalRates, type GlobalRateSnapshot } from "./useDownloadGlobalRates";
import "./dashboard-facts.css";
import "./download-global-rates.css";

type Direction = "download" | "upload";
function RateChart({ data, direction, elapsedMs }: { data: GlobalRateSnapshot; direction: Direction; elapsedMs: number }) {
  const curve = useMemo(() => globalRateCurve(data.series, direction), [data.series, direction]);
  const label = direction === "download" ? "下载" : "上传", current = currentGlobalRate(data, direction, elapsedMs);
  return <figure className={`global-rate-chart global-rate-${direction}`}>
    <figcaption><h3>{label}</h3><strong className="global-rate-value">{globalRateText(current.value)}</strong></figcaption>
    <p className="facts-note">有效观测：{current.known}/{data.clients.length} 个下载器</p>
    <div className="global-rate-axis"><span>{globalRateText(curve.max)}</span><span>独立刻度</span></div>
    <svg viewBox="0 0 620 128" preserveAspectRatio="none" role="img" aria-label={`${label}最近10分钟，缺失观测处断线，当前${globalRateText(current.value)}`}>
      <path className="global-rate-baseline" d="M8 120H612" vectorEffect="non-scaling-stroke" />
      {curve.segments.map((segment, index) => segment.length === 1 ? <circle key={index} cx={segment[0].x} cy={segment[0].y} r={3} />
        : <path key={index} d={segment.map((point, i) => `${i ? "L" : "M"}${point.x.toFixed(2)} ${point.y.toFixed(2)}`).join(" ")} fill="none" strokeWidth={2} vectorEffect="non-scaling-stroke" />)}
    </svg>
    <div className="global-rate-axis"><span>10分钟前 · 0 B/s</span><span>读取时</span></div>
    {!curve.segments.length ? <p className="facts-note">暂无完整的历史观测。</p> : null}
  </figure>;
}
const stateLabels: Record<GlobalRateSnapshot["clients"][number]["state"], string> = { disabled: "采集未开启", unobserved: "尚无观测", configuration_changed: "配置已变化，等待新观测", unknown: "观测未知", failed: "最近采集失败", stale: "观测已过期", observed: "已有观测" };
const failures: Record<GlobalRateSnapshot["clients"][number]["failure_code"], string> = { "": "", unavailable: "暂不可用", auth_required: "需要重新验证账号", timeout: "采集超时", blocked: "请求被拒绝", bad_response: "响应无法读取", rate_limited: "下载器要求稍后读取" };
export function DownloadGlobalRatesPanel({ userId, role }: { userId: string | undefined; role: string | undefined }) {
  const view = useDownloadGlobalRates({ userId, role });
  if (!userId || role !== "admin") return null;
  const data = view.data;
  return <div className="dashboard-facts download-global-rates"><section className="facts-section" aria-label="全局传输速率" aria-busy={view.busy}>
    <div className="facts-heading"><h2>全局传输速率</h2><button className="global-rate-refresh" type="button" onClick={() => { void view.refresh(); }} disabled={view.busy}><RefreshCw size={16} aria-hidden="true" />{view.busy ? "读取中…" : "刷新速率"}</button></div>
    <p className="facts-note">下载器报告的全部传输 · 最近10分钟 · 未知区间不连线</p>
    {view.error ? <p className="facts-notice" role="alert">{view.error}</p> : null}
    {!data && !view.error ? <p className="facts-note" role="status">正在读取全局速率…</p> : null}
    {data ? <>
      {!data.enabled ? <p className="facts-note">速率采集尚未开启。</p> : data.clients.length === 0 ? <p className="facts-note">尚未配置下载器。<Link to="/settings">前往设置</Link></p> : <div className="global-rate-charts"><RateChart data={data} direction="download" elapsedMs={view.elapsedMs} /><RateChart data={data} direction="upload" elapsedMs={view.elapsedMs} /></div>}
      <p className="facts-note">读取时间：{factTime(data.observed_at)} · 观测超过{data.freshness_seconds}秒后显示未知</p>
      <details className="global-rate-details"><summary>下载器观测（{data.clients.length}）</summary><ul className="global-rate-clients">{data.clients.map(client => {
        const down = currentClientRate(data, client, "download", view.elapsedMs), up = currentClientRate(data, client, "upload", view.elapsedMs), expired = client.state === "observed" && down === null && up === null;
        return <li key={client.id}><strong>{client.name}</strong><p className="facts-note">{downloadClientLabels[client.client_type]} · {expired ? "当前速率未知" : stateLabels[client.state]}{client.failure_code ? ` · ${failures[client.failure_code]}` : ""}</p>
          <p>下载 {globalRateText(down)} · 上传 {globalRateText(up)}</p><p className="facts-note">观测时间：{factTime(client.observed_at)}</p>
          {client.last_good ? <p className="facts-note">最后成功：{factTime(client.last_good.observed_at)} · 下载 {globalRateText(client.last_good.download_bytes_per_second)} · 上传 {globalRateText(client.last_good.upload_bytes_per_second)}</p> : null}</li>;
      })}</ul></details>
      {data.enabled && data.series.length ? <details className="global-rate-details"><summary>历史观测数据</summary><table className="global-rate-table"><caption>全局合计；任一下载器缺少有效观测时，该方向为未知。</caption><thead><tr><th scope="col">时间</th><th scope="col">下载</th><th scope="col">上传</th></tr></thead><tbody>{data.series.map(point => <tr key={point.at}><th scope="row">{factTime(point.at)}</th><td>{globalRateText(point.download_bytes_per_second)}</td><td>{globalRateText(point.upload_bytes_per_second)}</td></tr>)}</tbody></table></details> : null}
    </> : null}
  </section></div>;
}
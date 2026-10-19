import { useRef } from "react";
import type { KeyboardEvent } from "react";
import { Link } from "react-router-dom";
import { ChevronLeft, ChevronRight, Film, Tv } from "lucide-react";
import {
  activityProgress, activityStatus, byteFact, capacityFact, countFact, factTime, trustedPoster,
  type DashboardFacts, type LibraryOverview,
} from "./useDashboardFacts";
import "./dashboard-facts.css";

const countCards = [
  ["movies", "电影"], ["tv_shows", "电视剧"], ["episodes", "集数（含特别篇）"], ["files", "媒体文件"],
] as const;

function ReadState({ status, retained }: { status: DashboardFacts["overviewStatus"]; retained: boolean }) {
  if (status === "error") return <p className="facts-notice" role="status">{retained ? "读取失败，显示上次读取结果。请稍后刷新。" : "读取失败，暂无可显示的数据。请稍后刷新。"}</p>;
  if (status === "loading") return <p className="facts-note" role="status">{retained ? "正在刷新，当前为上次读取结果。" : "正在读取…"}</p>;
  return null;
}

function RootCapacity({ root }: { root: LibraryOverview["roots"][number] }) {
  const capacity = capacityFact(root);
  return <li className="facts-root">
    <div className="facts-row"><strong>{root.name}</strong><span className="facts-note">{root.media_type === "tv" ? "剧集根" : "电影根"}</span></div>
    <p className="facts-note">{root.availability === "available" ? "最近扫描已完成" : "扫描可用性待确认"} · {root.health === "online" ? "根在线" : root.health === "offline" ? "根离线" : "根健康未知"}</p>
    {capacity ? <>
      <div className="facts-row"><span>已用 {byteFact(capacity.used)}</span><span>可用 {byteFact(capacity.available)}</span></div>
      <progress className="facts-progress" max={100} value={capacity.percent} aria-label={`${root.name}已用容量`} aria-valuetext={`已用 ${capacity.percent}%，可用 ${byteFact(capacity.available)}`} />
      <p className="facts-note">{root.capacity_state === "stale" ? "容量样本已过期" : "容量样本有效"} · 总量 {byteFact(capacity.total)}</p>
    </> : <p className="facts-note">容量未知{root.capacity_state === "stale" ? " · 已过期样本不可用" : ""}</p>}
    <p className="facts-note">采样时间：{factTime(root.sampled_at)}</p>
  </li>;
}

function RecentImports({ recent }: { recent: LibraryOverview["recent"] }) {
  const rail = useRef<HTMLUListElement>(null);
  const move = (direction: number) => {
    const node = rail.current;
    if (node) node.scrollBy({ left: direction * Math.max(1, node.clientWidth * 0.8), behavior: "auto" });
  };
  const keyboard = (event: KeyboardEvent<HTMLUListElement>) => {
    if (event.target !== event.currentTarget) return;
    if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
      event.preventDefault(); move(event.key === "ArrowLeft" ? -1 : 1);
    } else if (event.key === "Home" || event.key === "End") {
      event.preventDefault();
      rail.current?.scrollTo({ left: event.key === "Home" ? 0 : rail.current.scrollWidth, behavior: "auto" });
    }
  };
  return <>
    <div className="facts-heading">
      <h2>近期入库</h2>
      <div className="facts-carousel-controls" role="group" aria-label="近期入库轮播控制">
        <button type="button" onClick={() => move(-1)} disabled={recent.length < 2} aria-label="上一组入库媒体"><ChevronLeft size={18} aria-hidden="true" /></button>
        <button type="button" onClick={() => move(1)} disabled={recent.length < 2} aria-label="下一组入库媒体"><ChevronRight size={18} aria-hidden="true" /></button>
      </div>
      <Link to="/media">媒体库</Link>
    </div>
    {recent.length === 0 ? <p className="facts-note">暂无入库记录。</p> : <ul className="facts-carousel" ref={rail} tabIndex={0} onKeyDown={keyboard} aria-label="近期入库媒体，使用左右方向键翻页，Home 或 End 跳到首尾">
      {recent.map(item => {
        const poster = trustedPoster(item.poster_url);
        return <li className="facts-import" key={`${item.media_type}-${item.media_id}`}>
          <Link to="/media" aria-label={`在媒体库查看：${item.title}`}>
            <div className="facts-poster">
              <div className="wall-placeholder" aria-hidden="true">
                {item.media_type === "tv" ? <Tv size={32} /> : <Film size={32} />}
                <span className="placeholder-text">暂无海报</span>
              </div>
              {poster ? <img key={poster} src={poster} alt="" loading="lazy" decoding="async" referrerPolicy="no-referrer" onError={event => { event.currentTarget.hidden = true; }} /> : null}
            </div>
            <strong>{item.title}</strong>
          </Link>
          <p className="facts-note">{item.media_type === "tv" ? "电视剧" : "电影"}{typeof item.year === "number" && Number.isInteger(item.year) && item.year > 0 && item.year <= 9999 ? ` · ${item.year}` : ""}</p>
          <p className="facts-note">首次入库：{factTime(item.imported_at)}</p>
        </li>;
      })}
    </ul>}
  </>;
}

export function DashboardFactsPanel({ facts }: { facts: DashboardFacts }) {
  const { overview, activity, overviewStatus, activityStatus: status, errors } = facts;
  return <div className="dashboard-facts">
    {errors.length > 0 ? <div className="facts-errors" role="alert">{errors.map((error, index) => <p key={index}>{error}</p>)}</div> : null}
    <section className="facts-section" aria-label="共享媒体库当前入库统计" aria-busy={overviewStatus === "loading"}>
      <div className="facts-heading"><h2>共享媒体库</h2><Link to="/media">浏览媒体库</Link></div>
      <p className="facts-note">当前已入库 · 集数包含特别篇</p>
      <ReadState status={overviewStatus} retained={overview !== null} />
      <dl className="facts-counts">
        {countCards.map(([key, label]) => <div className="facts-count" key={key}><dt>{label}</dt><dd>{overview ? countFact(overview.counts[key]) : "—"}</dd></div>)}
      </dl>
      {overview ? <p className="facts-note">概览读取时间：{factTime(overview.observed_at)}</p> : null}
    </section>
    <div className="facts-columns">
      <section className="facts-section" aria-label="存储根容量">
        <div className="facts-heading"><h2>存储根</h2></div>
        <ReadState status={overviewStatus} retained={overview !== null} />
        {overview ? (overview.roots.length ? <ul className="facts-roots">{overview.roots.map(root => <RootCapacity key={root.root_id} root={root} />)}</ul> : <p className="facts-note">暂无存储根。</p>) : null}
      </section>
      <section className="facts-section" aria-label="活动记录" aria-busy={status === "loading"}>
        <div className="facts-heading"><h2>活动记录</h2><Link to="/activity">全部活动</Link></div>
        <ReadState status={status} retained={activity !== null} />
        {activity ? <>
          {activity.items.length ? <ul className="facts-activities">{activity.items.map(item => {
            const percent = activityProgress(item);
            return <li className="facts-activity" key={item.operation_id}>
              <div className="facts-row"><strong>{item.title}</strong><span>{activityStatus(item.status)}</span></div>
              {percent === null ? <p className="facts-note">进度未知</p> : <>
                <div className="facts-row"><span className="facts-note">{item.progress_source === "verified_bytes" ? "TV 观测" : "任务记录"}</span><span>{percent}%</span></div>
                <progress className="facts-progress" max={100} value={percent} aria-label={`${item.title}记录进度`} />
              </>}
              <p className="facts-note">记录更新时间：{factTime(item.updated_at)}</p>
            </li>;
          })}</ul> : <p className="facts-note">暂无活动记录。</p>}
          {activity.more ? <p className="facts-note">还有更多记录，<Link to="/activity">前往活动页</Link>。</p> : null}
          <p className="facts-note">列表读取时间：{factTime(activity.observed_at)}</p>
        </> : null}
      </section>
    </div>
    <section className="facts-section" aria-label="近期真实入库">
      <ReadState status={overviewStatus} retained={overview !== null} />
      {overview ? <RecentImports recent={overview.recent} /> : <h2>近期入库</h2>}
    </section>
  </div>;
}

import { useEffect, useId, useMemo, useState } from "react";
import {api} from "@/api/client";
import {
  calendarDays,
  moveCalendarAnchor,
  subscriptionCalendarItems,
  type CalendarEpisode,
  type CalendarSubscription,
} from "./subscriptionCalendar";
import "./subscription-calendar.css";

const weekdays = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];
const statusLabels = { pending: "待满足", satisfied: "已满足", skipped: "已跳过" };

function localToday(): string {
  const now = new Date();
  return `${String(now.getFullYear()).padStart(4, "0")}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}

function EpisodeList({ items, onOpen }: {
  items: readonly CalendarEpisode[];
  onOpen: (id: string) => void;
}) {
  return (
    <ul className="calendar-episodes">
      {items.map(item => (
        <li key={JSON.stringify([item.subscriptionId, item.unitId])}>
          <button
            type="button"
            className="calendar-episode"
            onClick={() => onOpen(item.subscriptionId)}
            aria-label={`${item.title} ${item.unitKey} ${item.date ?? "播出日期未知"} ${item.state === "paused" ? "已暂停 " : ""}${statusLabels[item.status]}`}
          >
            <span className="calendar-episode-title">{item.title}</span>
            <span className="calendar-episode-meta">{item.unitKey}</span>
            <span className="calendar-episode-meta">
              {item.state === "paused" ? "已暂停 · " : ""}{statusLabels[item.status]}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

export function SubscriptionCalendar({ onOpen }: {
  onOpen: (id: string) => void;
}) {
  const headingId = useId();
  const unknownId = useId();
  const [anchor, setAnchor] = useState(localToday);
  const [mode, setMode] = useState<"week" | "month">("month");
  const today = localToday();
  const days = calendarDays(anchor, mode);
  const [offset,setOffset]=useState(0),[unknownOffset,setUnknownOffset]=useState(0),[revision,setRevision]=useState(0);
  const start=days[0],end=days[days.length-1];
  const queryKey=`${start}:${end}:${offset}:${unknownOffset}:${revision}`;
  const [snapshot,setSnapshot]=useState<{key:string;subscriptions:CalendarSubscription[];total:number;unknownTotal:number}|null>(null);
  const [failure,setFailure]=useState<{key:string;message:string}|null>(null);
  useEffect(()=>{setOffset(0);setUnknownOffset(0)},[start,end]);
  useEffect(()=>{
    const controller=new AbortController();setFailure(null);
    if(!start||!end)return ()=>controller.abort();
    void Promise.all([
      api.GET("/api/v1/subscriptions",{signal:controller.signal,cache:"no-store",params:{query:{view:"calendar",start,end,limit:100,offset}}}),
      api.GET("/api/v1/subscriptions",{signal:controller.signal,cache:"no-store",params:{query:{view:"calendar",start,end,limit:100,offset:unknownOffset,unknown_dates:true}}}),
    ]).then(([dated,unknown])=>{
      if(controller.signal.aborted)return;
      if(dated.error||unknown.error||!dated.data||!unknown.data){setFailure({key:queryKey,message:"日历读取失败，未沿用旧排期。请重试。"});return}
      setSnapshot({key:queryKey,subscriptions:[...dated.data.items,...unknown.data.items],total:dated.data.total,unknownTotal:unknown.data.total});
    }).catch(()=>{if(!controller.signal.aborted)setFailure({key:queryKey,message:"日历连接失败，请重试。"})});
    return ()=>controller.abort();
  },[queryKey,start,end,offset,unknownOffset]);
  const current=snapshot?.key===queryKey?snapshot:null;
  const error=failure?.key===queryKey?failure.message:"";
  const loading=!current&&!error;
  const total=current?.total??0,unknownTotal=current?.unknownTotal??0;
  const {dated,unknown}=useMemo(()=>subscriptionCalendarItems(current?.subscriptions??[]),[current]);
  const byDate = useMemo(() => {
    const groups = new Map<string, CalendarEpisode[]>();
    for (const item of dated) {
      if (item.date === null) continue;
      const group = groups.get(item.date);
      if (group) group.push(item);
      else groups.set(item.date, [item]);
    }
    return groups;
  }, [dated]);
  const hasScheduled = days.some(day => byDate.has(day));
  const month = anchor.slice(0, 7);
  const monthLabel = `${Number(anchor.slice(0, 4))}年${Number(anchor.slice(5, 7))}月`;
  const previous = moveCalendarAnchor(anchor, mode, -1);
  const next = moveCalendarAnchor(anchor, mode, 1);

  return (
    <section className="subscription-calendar" aria-labelledby={headingId}>
      <header className="calendar-header">
        <div className="calendar-heading" aria-live="polite" aria-atomic="true">
          <h2 id={headingId}>追剧日历 · {mode === "month" ? monthLabel : "周视图"}</h2>
          <p className="calendar-range">
            {mode === "month" ? "月视图" : "周一至周日"} · {" "}
            <time dateTime={days[0] ?? anchor}>{days[0] ?? anchor}</time>
            {" 至 "}
            <time dateTime={days[days.length - 1] ?? anchor}>{days[days.length - 1] ?? anchor}</time>
          </p>
        </div>
        <div className="calendar-controls">
          <div className="calendar-navigation" role="group" aria-label="日历日期导航">
            <button type="button" disabled={previous === anchor || calendarDays(previous, mode).length === 0} onClick={() => setAnchor(previous)}>
              {mode === "month" ? "上个月" : "上一周"}
            </button>
            <button type="button" onClick={() => setAnchor(localToday())}>今天</button>
            <button type="button" disabled={next === anchor || calendarDays(next, mode).length === 0} onClick={() => setAnchor(next)}>
              {mode === "month" ? "下个月" : "下一周"}
            </button>
          </div>
          <div className="calendar-modes" role="group" aria-label="日历视图">
            <button type="button" aria-pressed={mode === "week"} onClick={() => setMode("week")}>周</button>
            <button type="button" aria-pressed={mode === "month"} onClick={() => setMode("month")}>月</button>
          </div>
        </div>
      </header>

      {error&&<p role="alert">{error} <button type="button" onClick={()=>setRevision(value=>value+1)}>重新读取日历</button></p>}
      {loading?<p role="status">正在读取日历…</p>:!error&&!hasScheduled?<p className="calendar-empty" role="status">当前日期范围的本页暂无已排期剧集。</p>:null}
      <nav className="config-actions" aria-label="排期分页">
        <button type="button" disabled={loading||offset===0} onClick={()=>setOffset(value=>Math.max(0,value-100))}>上一页排期</button>
        <span>共 {loading||error?"—":total} 集 · 第 {Math.floor(offset/100)+1} 页</span>
        <button type="button" disabled={loading||offset+100>=total} onClick={()=>setOffset(value=>value+100)}>下一页排期</button>
      </nav>

      <div className={`calendar-days calendar-days-${mode}`}>
        {days.map((day, index) => {
          const items = byDate.get(day) ?? [];
          return (
            <section
              className="calendar-day"
              key={day}
              aria-label={`${day} ${weekdays[index % 7]}`}
              data-outside-month={mode === "month" && day.slice(0, 7) !== month ? "true" : undefined}
            >
              <h3>
                <time dateTime={day} aria-current={day === today ? "date" : undefined}>{day}</time>
                <span>{weekdays[index % 7]}{day === today ? " · 今天" : ""}</span>
              </h3>
              {items.length > 0 ? <EpisodeList items={items} onOpen={onOpen} /> : !loading&&!error?<p className="calendar-day-empty">本页暂无排期</p>:null}
            </section>
          );
        })}
      </div>

      {unknownTotal > 0 || unknownOffset > 0 ? (
        <section className="calendar-unknown" aria-labelledby={unknownId}>
          <h3 id={unknownId}>播出日期未知（共 {loading||error?"—":unknownTotal} 集）</h3>
          <EpisodeList items={unknown} onOpen={onOpen} />
          <nav className="config-actions" aria-label="未知日期分页">
            <button type="button" disabled={loading||unknownOffset===0} onClick={()=>setUnknownOffset(value=>Math.max(0,value-100))}>上一页未知日期</button>
            <button type="button" disabled={loading||unknownOffset+100>=unknownTotal} onClick={()=>setUnknownOffset(value=>value+100)}>下一页未知日期</button>
          </nav>
        </section>
      ) : null}
    </section>
  );
}

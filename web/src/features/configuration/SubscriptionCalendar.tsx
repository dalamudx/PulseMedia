import { useId, useMemo, useState } from "react";
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

export function SubscriptionCalendar({ subscriptions, onOpen }: {
  subscriptions: readonly CalendarSubscription[];
  onOpen: (id: string) => void;
}) {
  const headingId = useId();
  const unknownId = useId();
  const [anchor, setAnchor] = useState(localToday);
  const [mode, setMode] = useState<"week" | "month">("month");
  const today = localToday();
  const days = calendarDays(anchor, mode);
  const { dated, unknown } = useMemo(() => subscriptionCalendarItems(subscriptions), [subscriptions]);
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

      {!hasScheduled ? <p className="calendar-empty" role="status">当前日期范围内暂无已排期剧集。</p> : null}

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
              {items.length > 0 ? <EpisodeList items={items} onOpen={onOpen} /> : <p className="calendar-day-empty">暂无排期</p>}
            </section>
          );
        })}
      </div>

      {unknown.length > 0 ? (
        <section className="calendar-unknown" aria-labelledby={unknownId}>
          <h3 id={unknownId}>播出日期未知（{unknown.length} 集）</h3>
          <EpisodeList items={unknown} onOpen={onOpen} />
        </section>
      ) : null}
    </section>
  );
}

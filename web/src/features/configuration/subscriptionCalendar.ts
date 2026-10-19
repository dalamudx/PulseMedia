import type { paths } from "@/api/client";

export type CalendarSubscription = paths["/api/v1/subscriptions"]["get"]["responses"]["200"]["content"]["application/json"]["items"][number];

export type CalendarEpisode = {
  subscriptionId: string;
  unitId: string;
  unitKey: string;
  title: string;
  date: string | null;
  state: "active" | "paused";
  status: "pending" | "satisfied" | "skipped";
};

type CalendarMode = "week" | "month";

export function subscriptionProgress(sub: CalendarSubscription): {mediaType: "movie" | "tv" | "unknown"; total: number; satisfied: number; percent: number | null} {
  const mediaType = sub.media_type === "movie" || sub.media_type === "tv" ? sub.media_type : "unknown";
  const items = (sub.wanted_items ?? []).filter(item => !item.is_skipped && !item.scope_excluded && !item.retired);
  // is_satisfied is the API projection, not a claim of actual file ownership.
  const satisfied = items.filter(item => item.is_satisfied).length;
  return { mediaType, total: items.length, satisfied, percent: items.length ? Math.round(satisfied * 100 / items.length) : null };
}

// The current API serializes air_date as a date, not a timestamp. Never infer a date.
function civilDate(value: string): Date | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return null;
  const [year, month, day] = value.split("-").map(Number);
  if (year < 1) return null;
  const date = new Date(0);
  date.setUTCFullYear(year, month - 1, day);
  date.setUTCHours(0, 0, 0, 0);
  return date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day ? date : null;
}

function civilString(date: Date): string | null {
  if (!Number.isFinite(date.getTime()) || date.getUTCFullYear() < 1 || date.getUTCFullYear() > 9999) return null;
  return date.toISOString().slice(0, 10);
}

const compareText = (a: string, b: string): number => a < b ? -1 : a > b ? 1 : 0;
function compareEpisode(a: CalendarEpisode, b: CalendarEpisode): number {
  return compareText(a.date ?? "9999-99-99", b.date ?? "9999-99-99") || compareText(a.title, b.title) ||
    compareText(a.subscriptionId, b.subscriptionId) || compareText(a.unitKey, b.unitKey) || compareText(a.unitId, b.unitId) ||
    compareText(a.state, b.state) || compareText(a.status, b.status);
}

export function subscriptionCalendarItems(subscriptions: readonly CalendarSubscription[]): {dated: CalendarEpisode[]; unknown: CalendarEpisode[]} {
  const episodes: CalendarEpisode[] = [];
  for (const sub of subscriptions) {
    if (sub.media_type !== "tv" || (sub.state !== "active" && sub.state !== "paused")) continue;
    for (const item of sub.wanted_items ?? []) {
      if (item.retired || item.scope_excluded) continue;
      episodes.push({
        subscriptionId: sub.id, unitId: item.unit_id, unitKey: item.unit_key ?? "", title: sub.title ?? "",
        date: item.air_date && civilDate(item.air_date) ? item.air_date : null,
        state: sub.state, status: item.is_skipped ? "skipped" : item.is_satisfied ? "satisfied" : "pending",
      });
    }
  }
  const dated: CalendarEpisode[] = [], unknown: CalendarEpisode[] = [];
  const seen = new Set<string>();
  // Sorting first also resolves conflicting duplicates independently of input order.
  for (const episode of episodes.sort(compareEpisode)) {
    const identity = JSON.stringify([episode.subscriptionId, episode.unitId]);
    if (seen.has(identity)) continue;
    seen.add(identity);
    (episode.date === null ? unknown : dated).push(episode);
  }
  return { dated, unknown };
}

// Invalid anchors or grids outside years 0001..9999 return [], without a fallback date.
export function calendarDays(anchor: string, mode: CalendarMode): string[] {
  const date = civilDate(anchor);
  if (!date) return [];
  if (mode === "month") date.setUTCDate(1);
  const month = date.getUTCMonth();
  const mondayOffset = (date.getUTCDay() + 6) % 7;
  let count = 7;
  if (mode === "month") {
    const last = new Date(date);
    last.setUTCMonth(month + 1, 0);
    count = Math.ceil((mondayOffset + last.getUTCDate()) / 7) * 7;
  }
  date.setUTCDate(date.getUTCDate() - mondayOffset);
  const days: string[] = [];
  for (let i = 0; i < count; i++) {
    const day = civilString(date);
    if (!day) return [];
    days.push(day);
    date.setUTCDate(date.getUTCDate() + 1);
  }
  return days;
}

// Invalid anchors, noninteger steps and out-of-range results leave the anchor unchanged.
export function moveCalendarAnchor(anchor: string, mode: CalendarMode, step: number): string {
  const date = civilDate(anchor);
  if (!date || !Number.isSafeInteger(step)) return anchor;
  if (mode === "week") {
    date.setUTCDate(date.getUTCDate() + step * 7);
  } else {
    const day = date.getUTCDate();
    date.setUTCDate(1);
    date.setUTCMonth(date.getUTCMonth() + step);
    const last = new Date(date);
    last.setUTCMonth(last.getUTCMonth() + 1, 0);
    date.setUTCDate(Math.min(day, last.getUTCDate()));
  }
  return civilString(date) ?? anchor;
}

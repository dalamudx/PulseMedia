export type LogLevel = "DEBUG" | "INFO" | "WARN" | "ERROR";

export type LiveLogRecord = { at: string; level: LogLevel; line: string };

const ranks: Record<LogLevel, number> = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

export const logLevels: LogLevel[] = ["DEBUG", "INFO", "WARN", "ERROR"];

export const maxLiveLogLines = 500;

export function isLogLevel(value: unknown): value is LogLevel {
  return value === "DEBUG" || value === "INFO" || value === "WARN" || value === "ERROR";
}

export function levelRank(level: LogLevel): number {
  return ranks[level];
}

/**
 * Parse one SSE payload into a log record. Malformed payloads return null so a
 * broken frame cannot inject arbitrary text into the viewer.
 */
export function parseLogRecord(data: string): LiveLogRecord | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(data);
  } catch {
    return null;
  }
  if (!parsed || typeof parsed !== "object") return null;
  const candidate = parsed as Record<string, unknown>;
  if (!isLogLevel(candidate.level)) return null;
  if (typeof candidate.line !== "string" || candidate.line.trim() === "") return null;
  const at = typeof candidate.at === "string" ? candidate.at : "";
  return { at, level: candidate.level, line: candidate.line };
}

/**
 * Append a record, keeping only lines at or above the current filter and
 * bounding the buffer so the viewer cannot grow without limit.
 */
export function appendRecord(current: LiveLogRecord[], record: LiveLogRecord, minimum: LogLevel): LiveLogRecord[] {
  if (levelRank(record.level) < levelRank(minimum)) return current;
  const next = current.concat(record);
  return next.length > maxLiveLogLines ? next.slice(next.length - maxLiveLogLines) : next;
}

/**
 * Re-filter an existing buffer when the filter changes. Records below the new
 * minimum are dropped; they are not recoverable from memory by design.
 */
export function applyMinimumLevel(current: LiveLogRecord[], minimum: LogLevel): LiveLogRecord[] {
  return current.filter(record => levelRank(record.level) >= levelRank(minimum));
}

/**
 * A viewer sticks to the newest line only while it was already at the bottom.
 * Used to avoid yanking the scroll position while the user reads history.
 */
export function sticksToBottom(scrollTop: number, scrollHeight: number, clientHeight: number): boolean {
  return scrollHeight - (scrollTop + clientHeight) <= 24;
}

export function formatLogTime(at: string): string {
  if (!at) return "";
  const date = new Date(at);
  return Number.isNaN(date.valueOf()) ? "" : date.toLocaleTimeString();
}

export function logStreamURL(level: LogLevel): string {
  return `/api/v1/system/logs?level=${level}`;
}

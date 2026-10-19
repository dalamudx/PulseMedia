import type { components } from "@/api/schema";

export type EpisodeControlAction = "skip" | "resume" | "recheck";
export type EpisodeControlBody = { expected_version: number; unit_id: string } & (
  { action: "skip"; skipped: boolean } | { action: "recheck" }
);
export type EpisodeControlRecovery = { version: 1; userId: string; subscriptionId: string; key: string; body: EpisodeControlBody };
export type EpisodeControlDetail = components["schemas"]["SubscriptionDetails"];
export type EpisodeControlFeedbackKind = "idle" | "pending" | "confirmed" | "error";
export type EpisodeControlStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const uuid = (v: unknown): v is string => typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v) && !/^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(v);
const positive = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v > 0;

export function cleanEpisodeControlBody(v: unknown): EpisodeControlBody {
  if (!object(v) || !positive(v.expected_version) || !uuid(v.unit_id) ||
      Object.keys(v).some(k => !["expected_version", "unit_id", "action", ...(v.action === "skip" ? ["skipped"] : [])].includes(k))) throw Error("invalid episode control body");
  const base = { expected_version: v.expected_version, unit_id: v.unit_id };
  if (v.action === "skip" && typeof v.skipped === "boolean") return { ...base, action: "skip", skipped: v.skipped };
  if (v.action === "recheck") return { ...base, action: "recheck" };
  throw Error("invalid episode control action");
}

export function episodeControlRecoveryKey(userId: string, role: string, subscriptionId: string): string {
  if (!uuid(userId) || !uuid(subscriptionId) || !["admin", "member"].includes(role)) throw Error("invalid episode control owner");
  return `pulse:episode-control:v1:${userId}:${role}:${subscriptionId}`;
}

export function parseEpisodeControlRecovery(raw: string | null, userId: string, subscriptionId: string): EpisodeControlRecovery | null {
  if (!uuid(userId) || !uuid(subscriptionId)) throw Error("invalid episode control owner");
  if (raw === null) return null;
  if (raw.length > 4096) throw Error("invalid episode control journal size");
  const v: unknown = JSON.parse(raw);
  if (!object(v) || Object.keys(v).some(k => !["version", "userId", "subscriptionId", "key", "body"].includes(k)) ||
      v.version !== 1 || v.userId !== userId || v.subscriptionId !== subscriptionId || !uuid(v.key)) throw Error("invalid episode control journal");
  return { version: 1, userId, subscriptionId, key: v.key, body: cleanEpisodeControlBody(v.body) };
}

export function saveEpisodeControlRecovery(storage: EpisodeControlStorage, record: EpisodeControlRecovery, role: string, fresh = false): void {
  const key = episodeControlRecoveryKey(record.userId, role, record.subscriptionId);
  const value = JSON.stringify({ version: 1, userId: record.userId, subscriptionId: record.subscriptionId, key: record.key, body: cleanEpisodeControlBody(record.body) });
  const clean = parseEpisodeControlRecovery(value, record.userId, record.subscriptionId);
  const saved = parseEpisodeControlRecovery(storage.getItem(key), record.userId, record.subscriptionId);
  if ((!fresh && !saved) || (saved && JSON.stringify(saved) !== JSON.stringify(clean))) throw Error("original episode control changed");
  storage.setItem(key, value);
  if (storage.getItem(key) !== value) throw Error("episode control journal not saved");
}

export function clearEpisodeControlRecovery(storage: EpisodeControlStorage, record: EpisodeControlRecovery, role: string): void {
  const key = episodeControlRecoveryKey(record.userId, role, record.subscriptionId);
  const saved = parseEpisodeControlRecovery(storage.getItem(key), record.userId, record.subscriptionId);
  if (!saved || JSON.stringify(saved) !== JSON.stringify(record)) throw Error("original episode control changed");
  storage.removeItem(key);
  if (storage.getItem(key) !== null) throw Error("episode control journal not cleared");
}

export function isEpisodeControlReceipt(v: unknown, record: EpisodeControlRecovery): boolean {
  if (!object(v) || v.id !== record.subscriptionId || v.unit_id !== record.body.unit_id || !positive(v.intent_version) ||
      !["active", "paused", "archived"].includes(String(v.state))) return false;
  return record.body.action === "recheck"
    ? v.intent_version === record.body.expected_version
    : typeof v.skipped === "boolean" && v.skipped === record.body.skipped &&
      (v.intent_version === record.body.expected_version || v.intent_version === record.body.expected_version + 1);
}

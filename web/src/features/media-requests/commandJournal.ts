import type { Command, MediaIntent, MediaRequest, Role } from "./types";

export const uuid = (v: unknown): v is string => typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v) && !/^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(v);
export const positive = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v > 0;
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const exact = (v: Record<string, unknown>, keys: string[]) => Object.keys(v).length === keys.length && keys.every(k => Object.hasOwn(v, k));
const bounded = (v: unknown, max: number): v is string => typeof v === "string" && v.length <= max && !/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(v);
const ids = (v: unknown) => Array.isArray(v) && v.length >= 1 && v.length <= 64 && v.every(uuid) && new Set(v).size === v.length;
export function validIntent(v: unknown): v is MediaIntent {
  if (!object(v) || !exact(v, ["media_type", "tmdb_id", "seasons", "include_specials"]) || !["movie", "tv"].includes(String(v.media_type)) || !positive(v.tmdb_id) || typeof v.include_specials !== "boolean" || !Array.isArray(v.seasons)) return false;
  return v.seasons.length <= 100 && v.seasons.every(n => Number.isSafeInteger(n) && n >= 0 && n <= 10000 && (n !== 0 || v.include_specials === true)) && new Set(v.seasons).size === v.seasons.length && (v.media_type !== "movie" || (!v.include_specials && v.seasons.length === 0));
}
export function validCommand(v: unknown, userId: string, role: string): v is Command {
  if (!object(v) || !exact(v, ["schema", "userId", "role", "key", "ownerId", "intent", "kind", "requestId", "body"]) || v.schema !== 1 || !uuid(userId) || v.userId !== userId || !["admin", "member"].includes(role) || v.role !== role || !uuid(v.key) || !uuid(v.ownerId) || !validIntent(v.intent) || !object(v.body)) return false;
  if (v.kind === "create") return v.ownerId === userId && v.requestId === null && validIntent(v.body) && sameIntent(v.body, v.intent);
  if (!uuid(v.requestId) || !positive(v.body.expected_version)) return false;
  if (v.kind === "close") return exact(v.body, ["expected_version", "action", "reason"]) && bounded(v.body.reason, 2000) && ((v.body.action === "reject" && role === "admin" && v.body.reason.trim().length > 0) || (v.body.action === "withdraw" && v.ownerId === userId));
  return v.kind === "approve" && role === "admin" && exact(v.body, ["expected_version", "root_id", "quality_policy_id", "indexer_ids", "downloader_id"]) && uuid(v.body.root_id) && bounded(v.body.quality_policy_id, 128) && v.body.quality_policy_id.trim().length > 0 && ids(v.body.indexer_ids) && uuid(v.body.downloader_id);
}
export const sameIntent = (a: MediaIntent, b: MediaIntent) => a.media_type === b.media_type && a.tmdb_id === b.tmdb_id && a.include_specials === b.include_specials && a.seasons.length === b.seasons.length && a.seasons.every((n, i) => n === b.seasons[i]);
export const journalKey = (userId: string) => `pulsemedia.media-requests.v1.${userId}`;
export function parseJournal(raw: string | null, userId: string, role: string): Command | null {
  if (raw === null) return null;
  if (raw.length > 20000) throw Error("journal size");
  const value: unknown = JSON.parse(raw);
  if (!validCommand(value, userId, role)) throw Error("journal scope or shape");
  return value;
}
export function readJournal(storage: Storage, userId: string, role: string) { return parseJournal(storage.getItem(journalKey(userId)), userId, role); }
export const sameCommand = (a: Command, b: Command) => JSON.stringify(a) === JSON.stringify(b);
export function saveJournal(storage: Storage, command: Command) {
  if (!validCommand(command, command.userId, command.role)) throw Error("invalid command");
  const old = readJournal(storage, command.userId, command.role);
  if (old && !sameCommand(old, command)) throw Error("pending command");
  const raw = JSON.stringify(command);
  storage.setItem(journalKey(command.userId), raw);
  if (storage.getItem(journalKey(command.userId)) !== raw) throw Error("journal changed");
}
// Call only after a validated original receipt or an explicitly verified conflict.
export function clearVerifiedJournal(storage: Storage, command: Command) {
  const old = readJournal(storage, command.userId, command.role);
  if (old && !sameCommand(old, command)) throw Error("journal replaced");
  storage.removeItem(journalKey(command.userId));
  if (storage.getItem(journalKey(command.userId)) !== null) throw Error("journal not cleared");
}
const date = (v: unknown) => typeof v === "string" && v.length <= 64 && Number.isFinite(Date.parse(v));
export function validRequest(v: unknown, userId: string, role: string): v is MediaRequest {
  return object(v) && uuid(v.id) && uuid(v.owner_id) && (role === "admin" || v.owner_id === userId) && validIntent(v.intent) && ["pending", "approved", "rejected", "withdrawn"].includes(String(v.state)) && positive(v.version) && date(v.created_at) && date(v.updated_at) && bounded(v.reason, 2000) && (v.subscription_id === undefined || uuid(v.subscription_id)) && (v.decided_by === undefined || uuid(v.decided_by)) && (v.decided_at === undefined || date(v.decided_at));
}
export function validReceipt(v: unknown, c: Command): boolean {
  if (!object(v)) return false;
  if (c.kind === "approve") {
    if (v.request_id !== c.requestId || !object(v.subscription)) return false;
    const s = v.subscription;
    return uuid(s.id) && s.user_id === c.ownerId && s.root_id === c.body.root_id && s.quality_policy_id === c.body.quality_policy_id && s.downloader_id === c.body.downloader_id && Array.isArray(s.indexer_ids) && s.indexer_ids.length === c.body.indexer_ids.length && c.body.indexer_ids.every(id => (s.indexer_ids as unknown[]).includes(id)) && (s.tmdb_id === undefined || s.tmdb_id === c.intent.tmdb_id) && (s.media_type === undefined || s.media_type === c.intent.media_type) && positive(s.intent_version) && positive(s.quality_policy_revision) && uuid(s.media_id) && typeof s.state === "string" && typeof s.scope_type === "string";
  }
  if (v.command_key !== c.key || !validRequest(v.request, c.userId, c.role)) return false;
  const r = v.request;
  if (r.owner_id !== c.ownerId || !sameIntent(r.intent, c.intent)) return false;
  return c.kind === "create" ? r.state === "pending" && r.version === 1 : r.id === c.requestId && r.version === c.body.expected_version + 1 && r.state === (c.body.action === "reject" ? "rejected" : "withdrawn") && r.reason === c.body.reason;
}
export function intentFromInputs(mediaType: string, tmdb: string, seasons: string, specials: boolean): MediaIntent | null {
  if (!/^[1-9]\d*$/.test(tmdb) || !["movie", "tv"].includes(mediaType)) return null;
  if (mediaType === "tv" && seasons.trim() && !/^\d+(\s*,\s*\d+)*$/.test(seasons.trim())) return null;
  const value = { media_type: mediaType, tmdb_id: Number(tmdb), seasons: mediaType === "tv" && seasons.trim() ? seasons.trim().split(",").map(Number) : [], include_specials: mediaType === "tv" && specials };
  return validIntent(value) ? value : null;
}
export function discoveryPrefill(search: string) {
  const q = new URLSearchParams(search), type = q.get("media_type") ?? "", tmdb = q.get("tmdb_id") ?? "";
  const valid = q.getAll("media_type").length === 1 && q.getAll("tmdb_id").length === 1 && intentFromInputs(type, tmdb, "", false);
  return { mediaType: valid ? type : "", tmdbId: valid ? tmdb : "", title: valid ? (q.get("title") ?? "").slice(0, 300) : "" };
}
export function makeCommand(userId: string, role: Role, kind: Command["kind"], body: Command["body"], row?: MediaRequest): Command {
  const command = { schema: 1, userId, role, key: crypto.randomUUID(), kind, requestId: row?.id ?? null, ownerId: row?.owner_id ?? userId, intent: row?.intent ?? body, body };
  if (!validCommand(command, userId, role)) throw Error("invalid input");
  return JSON.parse(JSON.stringify(command)) as Command;
}

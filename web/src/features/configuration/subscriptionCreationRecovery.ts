import type { components } from "@/api/schema";

export type SubscriptionIntent = Omit<components["schemas"]["CreateSubscriptionRequest"], "title" | "year">;
export type SubscriptionResult = components["schemas"]["SubscriptionDetails"];
export type SubscriptionRecovery = { version: 1; userId: string; key: string; body: SubscriptionIntent };
export type SubscriptionStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;
export type SubscriptionReply = { status: number; data?: SubscriptionResult };
export type CreationPhase = "idle" | "creating" | "recovering" | "unknown" | "missing" | "conflict" | "rejected" | "auth" | "blocked" | "succeeded";

const fields = new Set(["media_id", "tmdb_id", "media_type", "root_id", "classification_version", "classification_preview_stamp", "classification_preview_root_id", "quality_policy_id", "indexer_ids", "downloader_id", "scope_type", "seasons", "episodes", "selected_unit_ids", "include_specials", "expected_intent_version"]);
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
export const subscriptionUUID = (v: unknown): v is string => typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v) && !/^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(v);
const positive = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v > 0;
const uuidList = (v: unknown): v is string[] => Array.isArray(v) && v.length <= 10000 && v.every(subscriptionUUID) && new Set(v).size === v.length;
const seasonsList = (v: unknown): v is number[] => Array.isArray(v) && v.length <= 10000 && v.every(n => typeof n === "number" && Number.isSafeInteger(n) && n >= 0) && new Set(v).size === v.length;
const episodesList = (v: unknown): v is string[] => Array.isArray(v) && v.length <= 10000 && v.every(e => typeof e === "string" && /^S\d{2,}E\d{2,}$/.test(e)) && new Set(v).size === v.length;

function parseIntent(v: unknown): SubscriptionIntent {
  if (!object(v) || Object.keys(v).some(k => !fields.has(k)) ||
      (v.media_id !== undefined && !subscriptionUUID(v.media_id)) ||
      (v.tmdb_id !== undefined && !positive(v.tmdb_id)) || (!v.media_id && !v.tmdb_id) ||
      (v.media_type !== undefined && v.media_type !== "movie" && v.media_type !== "tv") ||
      (v.root_id !== undefined && !subscriptionUUID(v.root_id)) ||
      (v.classification_version !== undefined && (!positive(v.classification_version) || v.root_id !== undefined || v.expected_intent_version !== undefined || !subscriptionUUID(v.classification_preview_root_id) || typeof v.classification_preview_stamp!=="string" || !/^[0-9a-f]{64}$/.test(v.classification_preview_stamp))) ||
      (v.classification_version===undefined&&(v.classification_preview_root_id!==undefined||v.classification_preview_stamp!==undefined)) ||
      (v.downloader_id !== undefined && !subscriptionUUID(v.downloader_id)) ||
      typeof v.quality_policy_id !== "string" || !/^[a-zA-Z0-9_-]{1,160}$/.test(v.quality_policy_id) ||
      (v.scope_type !== "all" && v.scope_type !== "selected") || typeof v.include_specials !== "boolean" ||
      (v.indexer_ids !== undefined && !uuidList(v.indexer_ids)) ||
      (v.selected_unit_ids !== undefined && !uuidList(v.selected_unit_ids)) ||
      (v.seasons !== undefined && !seasonsList(v.seasons)) ||
      (v.episodes !== undefined && !episodesList(v.episodes)) ||
      (v.expected_intent_version !== undefined && !positive(v.expected_intent_version))) throw Error("invalid subscription intent");
  // Construct a new typed value; parsed objects and hint fields never pass through.
  return {
    scope_type: v.scope_type, quality_policy_id: v.quality_policy_id, include_specials: v.include_specials,
    ...(subscriptionUUID(v.media_id) ? { media_id: v.media_id } : {}),
    ...(positive(v.tmdb_id) ? { tmdb_id: v.tmdb_id } : {}),
    ...(v.media_type === "movie" || v.media_type === "tv" ? { media_type: v.media_type } : {}),
    ...(subscriptionUUID(v.root_id) ? { root_id: v.root_id } : {}),
    ...(positive(v.classification_version) ? { classification_version: v.classification_version,classification_preview_stamp:v.classification_preview_stamp as string,classification_preview_root_id:v.classification_preview_root_id as string } : {}),
    ...(subscriptionUUID(v.downloader_id) ? { downloader_id: v.downloader_id } : {}),
    ...(uuidList(v.indexer_ids) ? { indexer_ids: [...v.indexer_ids] } : {}),
    ...(uuidList(v.selected_unit_ids) ? { selected_unit_ids: [...v.selected_unit_ids] } : {}),
    ...(seasonsList(v.seasons) ? { seasons: [...v.seasons] } : {}),
    ...(episodesList(v.episodes) ? { episodes: [...v.episodes] } : {}),
    ...(positive(v.expected_intent_version) ? { expected_intent_version: v.expected_intent_version } : {}),
  };
}

export function cleanSubscriptionIntent(body: SubscriptionIntent): SubscriptionIntent {
  return parseIntent(Object.fromEntries(Object.entries(body).filter(([key, value]) => fields.has(key) && value !== undefined)));
}
export function subscriptionScope(scope: "all" | "selected", seasons: number[], episodes: string[]): Pick<SubscriptionIntent, "scope_type" | "seasons" | "episodes"> {
  return {
    scope_type: scope === "selected" && episodes.length > 0 ? "selected" : "all",
    ...(scope === "selected" && seasons.length > 0 ? { seasons: [...seasons] } : {}),
    ...(scope === "selected" && episodes.length > 0 ? { episodes: [...episodes] } : {}),
  };
}
export const subscriptionRecoveryKey = (userId: string) => `pulse:subscription-create:v1:${userId}`;
export function parseSubscriptionRecovery(raw: string | null, userId: string): SubscriptionRecovery | null {
  if (!subscriptionUUID(userId)) throw Error("invalid owner");
  if (raw === null) return null;
  if (raw.length > 1000000) throw Error("invalid recovery size");
  const v: unknown = JSON.parse(raw);
  if (!object(v) || Object.keys(v).some(k => !["version", "userId", "key", "body"].includes(k)) || v.version !== 1 || v.userId !== userId || typeof v.key !== "string" || !/^[\x20-\x7e]{1,160}$/.test(v.key)) throw Error("invalid subscription recovery");
  return { version: 1, userId, key: v.key, body: parseIntent(v.body) };
}
export function saveSubscriptionRecovery(storage: SubscriptionStorage, record: SubscriptionRecovery): void {
  const value = JSON.stringify({ version: 1, userId: record.userId, key: record.key, body: cleanSubscriptionIntent(record.body) });
  parseSubscriptionRecovery(value, record.userId);
  const key = subscriptionRecoveryKey(record.userId);
  storage.setItem(key, value);
  if (storage.getItem(key) !== value) throw Error("subscription recovery not saved");
}
export function clearSubscriptionRecovery(storage: SubscriptionStorage, record: SubscriptionRecovery): void {
  const key = subscriptionRecoveryKey(record.userId);
  const saved = parseSubscriptionRecovery(storage.getItem(key), record.userId);
  if (saved?.key === record.key && JSON.stringify(saved.body) === JSON.stringify(record.body)) {
    storage.removeItem(key);
    if (storage.getItem(key) !== null) throw Error("subscription recovery not cleared");
  }
}

export type CreationOutcome = { phase: "succeeded"; result: SubscriptionResult } | { phase: "unknown" | "missing" | "conflict" | "rejected" | "auth" | "blocked" };
export async function subscriptionCommand(options: {
  record: SubscriptionRecovery; storage: SubscriptionStorage; fresh?: boolean; retryOriginal?: boolean;
  lookup: () => Promise<SubscriptionReply>; send: () => Promise<SubscriptionReply>; active: () => boolean;
}): Promise<CreationOutcome> {
  const { record, storage, fresh, retryOriginal, lookup, send, active } = options;
  const outcome = (reply: SubscriptionReply, status: number): CreationOutcome => {
    const result = reply.data;
    if (reply.status === status && result && subscriptionUUID(result.id) && result.user_id === record.userId && subscriptionUUID(result.media_id) && subscriptionUUID(result.root_id) && positive(result.intent_version) && (!record.body.media_id || result.media_id === record.body.media_id)) return { phase: "succeeded", result };
    if (reply.status === 401 || reply.status === 403) return { phase: "auth" };
    if (reply.status === 409) return { phase: "conflict" };
    if (reply.status === 400) return { phase: "rejected" };
    return { phase: "unknown" };
  };
  try {
    if (!active()) return { phase: "unknown" };
    if (!fresh) {
      const reply = await lookup();
      if (!active()) return { phase: "unknown" };
      if (reply.status !== 404) return outcome(reply, 200);
      if (!retryOriginal) return { phase: "missing" };
    }
    // Recheck storage after a lookup: an altered or removed original must never be overwritten.
    try {
      const saved = parseSubscriptionRecovery(storage.getItem(subscriptionRecoveryKey(record.userId)), record.userId);
      if ((!fresh && !saved) || (saved && (saved.key !== record.key || JSON.stringify(saved.body) !== JSON.stringify(record.body)))) return { phase: "blocked" };
      saveSubscriptionRecovery(storage, record);
    } catch { return { phase: "blocked" }; }
    if (!active()) return { phase: "unknown" };
    return outcome(await send(), 201);
  } catch { return { phase: "unknown" }; }
}

export function subscriptionCreationMessage(phase: CreationPhase): string {
  switch (phase) {
    case "creating": return "正在提交订阅，等待确认结果。";
    case "recovering": return "正在读取原订阅命令结果。";
    case "unknown": return "订阅结果未确认，已保留原请求。请先读取原命令结果。";
    case "missing": return "尚未查到原命令回执，发布结果仍未确认。可再次读取，或明确重试原请求。";
    case "conflict": return "原命令存在键冲突或意图版本已变化。请读取原结果并核对订阅当前版本，原请求已保留。";
    case "rejected": return "原请求的范围、目标根或版本未通过校验。请核对当前订阅，原请求已保留。";
    case "auth": return "订阅授权已失效，操作已暂停。请重新认证后恢复原命令。";
    case "blocked": return "订阅恢复记录不可用或校验失败，操作已暂停。请恢复浏览器存储后重试读取。";
    default: return "";
  }
}

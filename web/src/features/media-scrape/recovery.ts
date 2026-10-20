import type {components} from "@/api/schema";

export type ScrapeStatus = components["schemas"]["MediaScrapeStatus"];
export type ScrapeBatch = components["schemas"]["MediaScrapeBatch"];
export type PendingScrape = {version: 1; userId: string; key: string};
type StoragePort = Pick<Storage, "getItem" | "setItem" | "removeItem">;
type Reply = {status: number; data?: unknown};
export type ScrapeOutcome = {
  phase: "ready" | "acknowledged" | "missing" | "unknown" | "blocked" | "auth" | "inactive";
  pending?: PendingScrape;
  snapshot?: ScrapeStatus;
  batch?: ScrapeBatch;
  commandKey?: string;
};
const uuid = (v: unknown): v is string => typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(v);
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const count = (v: unknown) => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
export const scrapeStorageKey = (userId: string) => `pulse:media-scrape:pending:v1:${userId}`;
export function parsePendingScrape(raw: string | null, userId: string): PendingScrape | null {
  if (!uuid(userId)) throw Error("invalid owner");
  if (raw === null) return null;
  if (raw.length > 512) throw Error("invalid recovery size");
  const value: unknown = JSON.parse(raw);
  if (!object(value) || Object.keys(value).some(k => !["version", "userId", "key"].includes(k)) || value.version !== 1 || value.userId !== userId || !uuid(value.key)) throw Error("invalid recovery record");
  return {version: 1, userId, key: value.key};
}
export function validScrapeBatch(value: unknown): value is ScrapeBatch {
  return object(value) && uuid(value.id) && typeof value.created_at === "string" && Number.isFinite(Date.parse(value.created_at)) && ["total", "queued", "running", "succeeded", "review"].every(k => count(value[k]));
}
export function validScrapeStatus(value: unknown): value is ScrapeStatus {
  return object(value) && object(value.preview) && ["total", "missing", "queued", "running", "complete", "review"].every(k => count((value.preview as Record<string, unknown>)[k])) &&
    (value.batch == null || validScrapeBatch(value.batch));
}

// Only the explicit start/retry modes can send. Reads never replay a mutation.
export async function scrapeCommand(options: {
  mode: "read" | "start" | "retry";
  userId: string;
  storage: StoragePort;
  newKey: () => string;
  receiptKey?: string;
  get: (key?: string) => Promise<Reply>;
  post: (key: string) => Promise<Reply>;
  active: () => boolean;
}): Promise<ScrapeOutcome> {
  const {mode, userId, storage, active} = options;
  let pending: PendingScrape | undefined;
  const inactive = (): ScrapeOutcome => ({phase: "inactive"});
  const failure = (status?: number): ScrapeOutcome => ({phase: status === 401 || status === 403 ? "auth" : "unknown", pending});
  try {
    if (!active()) return inactive();
    const storageKey = scrapeStorageKey(userId);
    pending = parsePendingScrape(storage.getItem(storageKey), userId) ?? undefined;
    const acknowledge = (batch: ScrapeBatch, snapshot?: ScrapeStatus): ScrapeOutcome => {
      if (!active()) return inactive();
      if (pending) {
        const saved = parsePendingScrape(storage.getItem(storageKey), userId);
        if (saved?.key !== pending.key) return {phase: "blocked", pending};
        storage.removeItem(storageKey);
        if (storage.getItem(storageKey) !== null) throw Error("recovery not cleared");
      }
      return {phase: "acknowledged", batch, snapshot, commandKey: pending?.key};
    };
    if (mode !== "start") {
      const reply = await options.get(pending?.key ?? options.receiptKey);
      if (!active()) return inactive();
      if (reply.status !== 200 || !validScrapeStatus(reply.data)) return failure(reply.status);
      const snapshot = reply.data;
      if (pending && snapshot.batch) return acknowledge(snapshot.batch, snapshot);
      if (!pending) return {phase: "ready", snapshot};
      if (mode === "read") return {phase: "missing", pending, snapshot};
    } else {
      if (pending) return {phase: "blocked", pending};
      pending = {version: 1, userId, key: options.newKey()};
      // Persist only account and command identity, before any request can be sent.
      const raw = JSON.stringify({version: 1, userId, key: pending.key});
      parsePendingScrape(raw, userId);
      storage.setItem(storageKey, raw);
      if (storage.getItem(storageKey) !== raw) throw Error("recovery not saved");
    }
    if (!active()) return inactive();
    const saved = parsePendingScrape(storage.getItem(storageKey), userId);
    if (!pending || saved?.key !== pending.key) return {phase: "blocked", pending};
    const reply = await options.post(pending.key);
    if (!active()) return inactive();
    if (reply.status === 200 && validScrapeBatch(reply.data)) return acknowledge(reply.data);
    return failure(reply.status);
  } catch {
    if (!active()) return inactive();
    // Storage errors and uncertain transports both retain the original account record.
    return {phase: "unknown", pending};
  }
}

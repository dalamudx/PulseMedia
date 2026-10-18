export type SiteAction = "create" | "update" | "delete";
export interface SitePendingRecord {
  key: string;
  action: SiteAction;
  targetId?: string;
  expectedVersion?: number;
  userID: string;
}
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const fields = new Set(["key", "action", "targetId", "expectedVersion", "userID"]);
export const sitePendingStorageKey = (userID: string) => `pulsemedia:managed-sites:pending:v1:${userID}`;

// Always construct an allowlisted record, even if the caller carries an in-memory body.
export function serializeSitePending(record: SitePendingRecord): string {
  const safe = { key: record.key, action: record.action, targetId: record.targetId, expectedVersion: record.expectedVersion, userID: record.userID };
  const text = JSON.stringify(safe);
  if (!parseSitePending(text, record.userID)) throw new Error("Invalid site recovery metadata");
  return text;
}
export function parseSitePending(text: string, userID: string): SitePendingRecord | null {
  if (text.length > 1024 || !uuid.test(userID)) return null;
  try {
    const value: unknown = JSON.parse(text);
    if (!value || typeof value !== "object" || Array.isArray(value)) return null;
    if (Object.keys(value).some(key => !fields.has(key))) return null;
    if (!("key" in value) || typeof value.key !== "string" || !uuid.test(value.key)) return null;
    if (!("userID" in value) || value.userID !== userID) return null;
    if (!("action" in value) || (value.action !== "create" && value.action !== "update" && value.action !== "delete")) return null;
    if (value.action === "create") {
      if ("targetId" in value || "expectedVersion" in value) return null;
      return { key: value.key, action: value.action, userID };
    }
    if (!("targetId" in value) || typeof value.targetId !== "string" || !uuid.test(value.targetId)) return null;
    if (!("expectedVersion" in value) || typeof value.expectedVersion !== "number" || !Number.isSafeInteger(value.expectedVersion) || value.expectedVersion < 1) return null;
    return { key: value.key, action: value.action, targetId: value.targetId, expectedVersion: value.expectedVersion, userID };
  } catch { return null; }
}

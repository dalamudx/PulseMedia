import type { paths } from "@/api/client";

export type TVUpgradeSubscription = paths["/api/v1/subscriptions/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
export type MappingOptions = paths["/api/v1/library/mapping-options"]["get"]["responses"]["200"]["content"]["application/json"];
export type Mapping = paths["/api/v1/library/files/{id}/mapping"]["get"]["responses"]["200"]["content"]["application/json"];
export type Locator = { unitId: string; currentFileId: string; candidateId: string; mediaId: string; rootId: string };
const fields = ["unitId", "currentFileId", "candidateId", "mediaId", "rootId"] as const;
const uuid = (v: unknown): v is string => typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v);
export function selectionKey(actor: string, subscriptionId: string) {
  if (!uuid(actor) || !uuid(subscriptionId)) throw Error("Invalid selection owner");
  return `pulsemedia:tv-episode-selection:v1:${actor}:${subscriptionId}`;
}
export function parseLocators(raw: string | null): Locator[] {
  if (raw === null) return [];
  if (raw.length > 32768) throw Error("Selection record too large");
  const value: unknown = JSON.parse(raw);
  if (!Array.isArray(value) || value.length > 32) throw Error("Invalid selection record");
  return value.map(item => {
    if (!item || typeof item !== "object" || Object.keys(item).length !== fields.length || !Object.keys(item).every(k => fields.includes(k as typeof fields[number])) || !fields.every(k => uuid(item[k]))) throw Error("Invalid locator");
    return item as Locator;
  });
}
export function saveLocator(storage: Pick<Storage, "getItem" | "setItem">, key: string, locator: Locator): Locator[] {
  const clean = Object.fromEntries(fields.map(k => [k, locator[k]])) as Locator;
  const records = parseLocators(storage.getItem(key));
  const next = [clean, ...records.filter(r => !fields.every(k => r[k] === clean[k]))];
  // Never evict an older locator: its phase journal may still contain an unknown command.
  parseLocators(JSON.stringify(next));
  storage.setItem(key, JSON.stringify(next));
  return next;
}
export function sameSubscription(expected: TVUpgradeSubscription, actual: TVUpgradeSubscription) {
  return actual.id === expected.id && actual.media_type === "tv" && actual.media_id === expected.media_id && actual.root_id === expected.root_id && actual.intent_version === expected.intent_version;
}
export function singleMappedUnit(subscription: TVUpgradeSubscription, file: MappingOptions["files"][number], mapping: Mapping) {
  if (subscription.media_type !== "tv" || !mapping.can_correct || mapping.hold_reason || file.root_id !== subscription.root_id || mapping.root_id !== subscription.root_id || mapping.file_id !== file.id || mapping.rel_path !== file.rel_path || mapping.media_id !== subscription.media_id || !uuid(file.id)) return null;
  // Count every active association before excluding retired or foreign units; either makes a shared file unsafe.
  const active = mapping.associations.filter(a => a.active);
  if (active.length !== 1) return null;
  const unit = active[0].unit;
  return !unit.retired && unit.media_id === subscription.media_id && uuid(unit.id) && !!unit.unit_key ? unit : null;
}

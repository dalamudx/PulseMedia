import type { paths } from "@/api/client";

type ReviewRoute = paths["/api/v1/subscriptions/{id}/tv-coverage-candidate-reviews"]["post"];
type AcquireRoute = paths["/api/v1/subscriptions/{id}/complete-tv-acquisitions"]["post"];
export type ReviewReceipt = ReviewRoute["responses"]["200"]["content"]["application/json"];
export type AcquisitionReceipt = AcquireRoute["responses"]["202"]["content"]["application/json"];
export type Coverage = { unitId: string; unitKey: string }[];
type Base = { materialStamp: string; userId: string; subscriptionId: string; coverage: Coverage; key: string; signature: string; candidateId: string; phase: "pending" | "confirmed" | "rejected" };
export type Entry = Base & (
  { kind: "review"; body: ReviewRoute["requestBody"]["content"]["application/json"]; receipt?: ReviewReceipt } |
  { kind: "acquire"; body: AcquireRoute["requestBody"]["content"]["application/json"]; receipt?: AcquisitionReceipt }
);
export type Journal = { version: 2; entries: Entry[] };
export type StoragePort = Pick<Storage, "getItem" | "setItem">;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const stamp = /^[0-9a-f]{64}$/;
export const journalKey = (user: string, subscription: string) => `pulse:complete-tv:v2:${user}:${subscription}`;

const exact = (value: object, keys: string[]) => Object.keys(value).every(k => keys.includes(k));
export function validCoverage(rows: Coverage): boolean {
  return Array.isArray(rows) && rows.length > 0 && rows.length <= 10000 && rows.every(r => r && exact(r, ["unitId", "unitKey"]) && uuid.test(r.unitId) && /^S\d{2,4}E\d{2,4}$/.test(r.unitKey)) && new Set(rows.map(r => r.unitId)).size === rows.length && new Set(rows.map(r => r.unitKey)).size === rows.length;
}

// Corrupt or inaccessible storage must block new commands, never silently forget an unknown write.
export function readJournal(storage: StoragePort, key: string): Journal {
  const raw = storage.getItem(key);
  if (raw === null) {
    // Legacy journals lack an owner-bound episode snapshot. Never silently bypass an old unknown POST.
    if (storage.getItem(key.replace(":v2:", ":v1:")) !== null) throw new Error("legacy recovery requires original command");
    return { version: 2, entries: [] };
  }
  if (raw.length > 4000000) throw new Error("oversized journal");
  const value = JSON.parse(raw) as Journal;
  if (value?.version !== 2 || !exact(value, ["version", "entries"]) || !Array.isArray(value.entries) || value.entries.length > 200 || !value.entries.every(e => validEntry(e) && journalKey(e.userId, e.subscriptionId) === key) ||
      new Set(value.entries.map(e => e.key)).size !== value.entries.length ||
      value.entries.filter(e => e.phase === "pending").length > 1) throw new Error("invalid journal");
  return value;
}
function validEntry(e: Entry): boolean {
  if (!e || !exact(e, ["materialStamp", "userId", "subscriptionId", "coverage", "key", "signature", "candidateId", "phase", "kind", "body", "receipt"]) || !stamp.test(e.materialStamp) || !uuid.test(e.userId) || !uuid.test(e.subscriptionId) || !validCoverage(e.coverage) || !uuid.test(e.key) || !uuid.test(e.candidateId) || typeof e.signature !== "string" || e.signature.length > 500000 ||
      !["pending", "confirmed", "rejected"].includes(e.phase) || !e.body ||
      e.body.confirmed !== true || !stamp.test(e.body.expected_input_stamp)) return false;
  if (e.kind === "review") {
    if (!exact(e.body, ["candidate_id", "expected_input_stamp", "confirmed", "reason"]) || e.body.candidate_id !== e.candidateId || typeof e.body.reason !== "string" || !e.body.reason.trim() || e.body.reason.length > 240) return false;
    if (e.phase === "confirmed") return !!e.receipt && exact(e.receipt, ["id", "subscription_id", "candidate_id", "input_stamp", "material_stamp", "status"]) && uuid.test(e.receipt.id) && e.receipt.subscription_id === e.subscriptionId &&
      e.receipt.candidate_id === e.candidateId && e.receipt.input_stamp === e.body.expected_input_stamp &&
      e.receipt.material_stamp === e.materialStamp && e.receipt.status === "reviewed_not_executable";
  } else if (e.kind === "acquire") {
    if (!exact(e.body, ["review_id", "expected_input_stamp", "expected_material_stamp", "confirmed"]) || !uuid.test(e.body.review_id) || e.body.expected_material_stamp !== e.materialStamp) return false;
    if (e.phase === "confirmed") return !!e.receipt && exact(e.receipt, ["operation_id", "job_id", "source_candidate_id", "reservations", "status"]) && uuid.test(e.receipt.operation_id) && uuid.test(e.receipt.job_id) &&
      e.receipt.source_candidate_id === e.candidateId && Array.isArray(e.receipt.reservations) &&
      e.receipt.reservations.length === e.coverage.length && new Set(e.receipt.reservations).size === e.receipt.reservations.length && e.receipt.reservations.every(id => uuid.test(id)) &&
      ["queued", "reserved_not_executable"].includes(e.receipt.status);
  } else return false;
  return e.receipt === undefined;
}
export function saveEntry(storage: StoragePort, key: string, entry: Entry): Journal {
  if (!validEntry(entry) || journalKey(entry.userId, entry.subscriptionId) !== key) throw new Error("invalid entry");
  const current = readJournal(storage, key);
  const previous = current.entries.find(e => e.key === entry.key);
  if (previous && (JSON.stringify(previous.body) !== JSON.stringify(entry.body) || previous.kind !== entry.kind ||
      previous.candidateId !== entry.candidateId || previous.signature !== entry.signature || previous.materialStamp !== entry.materialStamp || previous.userId !== entry.userId || previous.subscriptionId !== entry.subscriptionId || JSON.stringify(previous.coverage) !== JSON.stringify(entry.coverage) ||
      previous.phase !== "pending" && JSON.stringify(previous) !== JSON.stringify(entry))) throw new Error("frozen command");
  if (!previous && current.entries.some(e => e.phase === "pending")) throw new Error("pending command");
  if (!previous && current.entries.some(e => e.phase !== "rejected" && e.kind === entry.kind &&
      (entry.kind === "acquire" && e.kind === "acquire" ? e.body.review_id === entry.body.review_id :
        e.candidateId === entry.candidateId && e.signature === entry.signature && e.body.expected_input_stamp === entry.body.expected_input_stamp))) throw new Error("command already recorded");
  const next: Journal = { version: 2, entries: previous ? current.entries.map(e => e.key === entry.key ? entry : e) : [...current.entries, entry] };
  const raw = JSON.stringify(next);
  if (next.entries.length > 200 || raw.length > 4000000) throw new Error("journal full");
  storage.setItem(key, raw);
  if (storage.getItem(key) !== raw) throw new Error("journal not durable");
  return next;
}

// Receipts are committed to the captured account namespace even if the selection changed or the view unmounted.
// A confirmed entry never invokes transport again. GET refresh is deliberately outside this function.
export async function sendEntry(storage: StoragePort, key: string, entry: Entry,
  transport: (entry: Entry) => Promise<{ status: number; data?: ReviewReceipt | AcquisitionReceipt }>,
  onConfirmed: (entry: Entry) => void): Promise<Journal> {
  const journal = readJournal(storage, key);
  const previous = journal.entries.find(e => e.key === entry.key);
  const original = previous ?? entry;
  if (original.phase !== "pending") return journal;
  saveEntry(storage, key, original);
  const result = await transport(original);
  if (result.status >= 200 && result.status < 300 && result.data) {
    const confirmed = { ...original, phase: "confirmed", receipt: result.data } as Entry;
    if (!validEntry(confirmed)) throw new Error("invalid receipt");
    onConfirmed(confirmed); // In-memory receipt also fences replay if storage becomes unavailable after POST.
    return saveEntry(storage, key, confirmed);
  }
  // A rejected recovery cannot disprove an earlier ambiguous commit. Keep its exact original key.
  if ([400, 404, 409].includes(result.status) && !previous) return saveEntry(storage, key, { ...original, phase: "rejected" });
  throw new Error([401, 403].includes(result.status) ? "authentication required" : "receipt unconfirmed");
}


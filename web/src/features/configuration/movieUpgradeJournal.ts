import type { components } from "@/api/schema";
export type UUID = string;
export type Mode = components["schemas"]["UpgradeModeCommand"]["mode"];
export type Bodies = {
  mode: components["schemas"]["UpgradeModeCommand"];
  reservation: components["schemas"]["UpgradeReservationCommand"];
  preparation: components["schemas"]["UpgradePreparationCommand"];
  send: components["schemas"]["UpgradeSendCommand"];
  organizationAuthorization: components["schemas"]["UpgradeOrganizationAuthorizationCommand"];
  organizationPreparation: components["schemas"]["UpgradeOrganizationPreparationCommand"];
  organizationJob: components["schemas"]["UpgradeOrganizationJobCommand"];
  organizationRetirement: components["schemas"]["UpgradeRetirementCommand"];
};
export type Receipts = {
  mode: components["schemas"]["UpgradeModeReceipt"];
  reservation: components["schemas"]["UpgradeReservationReceipt"];
  preparation: components["schemas"]["UpgradePreparationReceipt"];
  send: components["schemas"]["UpgradeSendReceipt"];
  organizationAuthorization: components["schemas"]["UpgradeOrganizationAuthorization"];
  organizationPreparation: components["schemas"]["UpgradeOrganizationPreparation"];
  organizationJob: components["schemas"]["UpgradeOrganizationJobReceipt"];
  organizationRetirement: components["schemas"]["UpgradeRetirementReceipt"];
};
export type Kind = keyof Bodies;
export type Entry<K extends Kind = Kind> = {
  [P in K]: {
    kind: P; key: UUID; body: Bodies[P]; phase: "pending" | "closed";
    receipt?: Receipts[P];
  }
}[K];
export type Journal = {
  version: 1; userId: UUID; subscriptionId: UUID;
  mode?: Entry<"mode">; reservation?: Entry<"reservation">;
  preparation?: Entry<"preparation">; send?: Entry<"send">;
  organizationAuthorization?: Entry<"organizationAuthorization">;
  organizationPreparation?: Entry<"organizationPreparation">;
  organizationJob?: Entry<"organizationJob">;
  organizationRetirement?: Entry<"organizationRetirement">;
};
export interface Storage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}
export interface Locks {
  request<T>(name: string, callback: () => Promise<T>): Promise<T>;
}
export type Transport = (entry: Entry) => Promise<{ status: number; data: unknown }>;
export type RunOptions = {
  storage: Storage; locks: Locks | undefined; journal: Journal; entry: Entry; userId: UUID; subscriptionId: UUID;
  transport: Transport; onJournal: (journal: Journal) => void; replaceMode?: boolean; replayClosed?: boolean;
};
export const kinds: Kind[] = ["mode", "reservation", "preparation", "send", "organizationAuthorization", "organizationPreparation", "organizationJob", "organizationRetirement"];
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const digest = /^[0-9a-f]{64}$/;
function check(value: unknown, message = "Invalid journal"): asserts value {
  if (!value) throw new Error(message);
}
function object(value: unknown, allowed: string[], required = allowed) {
  check(value !== null && typeof value === "object");
  check(Object.getPrototypeOf(value) === Object.prototype);
  const descriptors = Object.getOwnPropertyDescriptors(value);
  check(Reflect.ownKeys(value).every(key =>
    typeof key === "string" && allowed.includes(key) &&
    descriptors[key].enumerable && Object.hasOwn(descriptors[key], "value")));
  check(required.every(key => Object.hasOwn(value, key)));
  return value as Record<string, unknown>;
}
function id(value: unknown) { check(typeof value === "string" && uuid.test(value) && value !== "00000000-0000-0000-0000-000000000000"); }
function hash(value: unknown) { check(typeof value === "string" && digest.test(value)); }
function positive(value: unknown) { check(Number.isSafeInteger(value) && Number(value) > 0); }
function mode(value: unknown) { check(value === "disabled" || value === "stop_at_cutoff" || value === "replace_if_better"); }
function timestamp(value: unknown) {
  check(typeof value === "string");
  const m = /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:[Zz]|[+-](\d{2}):(\d{2}))$/.exec(value);
  check(m);
  const [y, mo, d, h, mi, s] = m.slice(1, 7).map(Number);
  const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
  check(mo >= 1 && mo <= 12 && d >= 1 &&
    d <= [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][mo - 1] &&
    h <= 23 && mi <= 59 && s <= 59 && Number(m[7] ?? 0) <= 23 && Number(m[8] ?? 0) <= 59);
}
function validateBody(kind: Kind, value: unknown) {
  const fields = {
    mode: ["expected_intent", "mode", "confirmed"],
    reservation: ["current_file_id", "candidate_id", "expected", "expected_client_digest", "confirmed"],
    preparation: ["operation_id", "confirmed"], send: ["operation_id", "task_id", "confirmed"],
    organizationAuthorization: ["operation_id", "observation_id", "input_stamp", "confirmed"],
    organizationPreparation: ["operation_id", "authorization_id", "confirmed"],
    organizationJob: ["operation_id", "preparation_id", "confirmed"],
    organizationRetirement: ["operation_id", "input_stamp", "confirmed"],
  };
  const b = object(value, fields[kind]);
  check(b.confirmed === true);
  if (kind === "mode") {
    positive(b.expected_intent);
    mode(b.mode);
  } else if (kind === "reservation") {
    id(b.current_file_id); id(b.candidate_id); hash(b.expected_client_digest);
    const e = object(b.expected, ["actor_id", "subscription_id", "media_id", "unit_id", "root_id",
      "intent_version", "policy_id", "policy_revision", "input_stamp"]);
    for (const key of ["actor_id", "subscription_id", "media_id", "unit_id", "root_id"]) id(e[key]);
    positive(e.intent_version); positive(e.policy_revision); hash(e.input_stamp);
    check(typeof e.policy_id === "string" && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,239}$/.test(e.policy_id));
  } else {
    id(b.operation_id);
    if (kind === "send") id(b.task_id);
    if (kind === "organizationAuthorization") { id(b.observation_id); hash(b.input_stamp); }
    if (kind === "organizationPreparation") id(b.authorization_id);
    if (kind === "organizationJob") id(b.preparation_id);
    if (kind === "organizationRetirement") hash(b.input_stamp);
  }
}
function validateReceipt(entry: Entry, value: unknown, sub: UUID) {
  if (entry.kind === "mode") {
    const r = object(value, ["subscription_id", "intent_version", "mode"]);
    id(r.subscription_id); positive(r.intent_version); mode(r.mode);
    check(r.subscription_id === sub && r.mode === entry.body.mode &&
      r.intent_version === entry.body.expected_intent + 1, "Mode receipt binding mismatch");
    return;
  }
  const fields = {
    reservation: ["operation_id", "reservation_id", "status", "execution_allowed"],
    preparation: ["operation_id", "task_id", "raw_sha256", "status", "execution_allowed"],
    send: ["operation_id", "task_id", "command_id", "job_id", "queued_at", "status", "execution_allowed"],
    organizationAuthorization: ["authorization_id", "operation_id", "observation_id", "task_id", "status", "authorized_at", "execution_allowed"],
    organizationPreparation: ["preparation_id", "operation_id", "authorization_id", "task_id", "plan_id", "step_id", "status", "prepared_at", "execution_allowed"],
    organizationJob: ["command_id", "job_id", "operation_id", "preparation_id", "queued_at", "status", "execution_allowed"],
    organizationRetirement: ["operation_id", "command_id", "queued_at", "status", "execution_allowed"],
  };
  const r = object(value, fields[entry.kind]);
  id(r.operation_id); check(r.execution_allowed === false);
  check(r.status === {
    reservation: "reserved_not_executable", preparation: "prepared_not_sent", send: "queued",
    organizationAuthorization: "organization_authorized_not_executable",
    organizationPreparation: "organization_prepared_not_executable", organizationJob: "queued",
    organizationRetirement: "retirement_queued",
  }[entry.kind]);
  if (entry.kind === "organizationRetirement") {
    id(r.command_id); timestamp(r.queued_at); check(r.operation_id === entry.body.operation_id);
    return;
  }
  if (entry.kind === "organizationAuthorization") {
    id(r.authorization_id); id(r.observation_id); id(r.task_id); timestamp(r.authorized_at);
    check(r.operation_id === entry.body.operation_id && r.observation_id === entry.body.observation_id);
    return;
  }
  if (entry.kind === "organizationPreparation") {
    for (const key of ["preparation_id", "authorization_id", "task_id", "plan_id", "step_id"]) id(r[key]);
    timestamp(r.prepared_at);
    check(r.operation_id === entry.body.operation_id && r.authorization_id === entry.body.authorization_id);
    return;
  }
  if (entry.kind === "organizationJob") {
    id(r.command_id); id(r.job_id); id(r.preparation_id); timestamp(r.queued_at);
    check(r.operation_id === entry.body.operation_id && r.preparation_id === entry.body.preparation_id);
    return;
  }
  if (entry.kind === "reservation") id(r.reservation_id);
  else {
    id(r.task_id); check(r.operation_id === entry.body.operation_id);
    if (entry.kind === "preparation") hash(r.raw_sha256);
    else {
      check(r.task_id === entry.body.task_id);
      id(r.command_id); id(r.job_id); timestamp(r.queued_at);
    }
  }
}
function validateEntry(kind: Kind, value: unknown, sub: UUID): Entry {
  const e = object(value, ["kind", "key", "body", "phase", "receipt"], ["kind", "key", "body", "phase"]);
  check(e.kind === kind); id(e.key); validateBody(kind, e.body);
  check(e.phase === "pending" || e.phase === "closed");
  check(Object.hasOwn(e, "receipt") === (e.phase === "closed"));
  const entry = e as unknown as Entry;
  if (entry.phase === "closed") validateReceipt(entry, e.receipt, sub);
  return entry;
}
function validateJournal(value: unknown, user: UUID, sub: UUID): Journal {
  id(user); id(sub);
  const j = object(value, ["version", "userId", "subscriptionId", ...kinds],
    ["version", "userId", "subscriptionId"]);
  check(j.version === 1 && j.userId === user && j.subscriptionId === sub, "Journal scope mismatch");
  let pending = false;
  for (const kind of kinds) {
    if (!Object.hasOwn(j, kind)) continue;
    check(!pending, "A pending stage blocks subsequent stages");
    pending = validateEntry(kind, j[kind], sub).phase === "pending";
  }
  const result = j as unknown as Journal;
  if (result.reservation) {
    const e = result.reservation.body.expected;
    check(e.actor_id === user && e.subscription_id === sub, "Reservation scope mismatch");
  }
  if (result.preparation) {
    check(result.reservation?.phase === "closed");
    check(result.preparation.body.operation_id === result.reservation.receipt!.operation_id);
  }
  if (result.send) {
    check(result.preparation?.phase === "closed");
    check(result.send.body.operation_id === result.preparation.receipt!.operation_id &&
      result.send.body.task_id === result.preparation.receipt!.task_id);
  }
  if (result.organizationAuthorization) {
    check(result.send?.phase === "closed");
    check(result.organizationAuthorization.body.operation_id === result.send.receipt!.operation_id);
  }
  if (result.organizationPreparation) {
    check(result.organizationAuthorization?.phase === "closed");
    check(result.organizationPreparation.body.operation_id === result.organizationAuthorization.receipt!.operation_id &&
      result.organizationPreparation.body.authorization_id === result.organizationAuthorization.receipt!.authorization_id);
  }
  if (result.organizationJob) {
    check(result.organizationPreparation?.phase === "closed");
    check(result.organizationJob.body.operation_id === result.organizationPreparation.receipt!.operation_id &&
      result.organizationJob.body.preparation_id === result.organizationPreparation.receipt!.preparation_id);
  }
  if (result.organizationRetirement) {
    check(result.organizationJob?.phase === "closed");
    check(result.organizationRetirement.body.operation_id === result.organizationJob.receipt!.operation_id);
  }
  return result;
}
function clone<T>(value: T): T { return JSON.parse(JSON.stringify(value)); }
function canonical(value: unknown): string {
  if (value !== null && typeof value === "object")
    return `{${Object.keys(value).sort().map(key =>
      `${JSON.stringify(key)}:${canonical((value as Record<string, unknown>)[key])}`).join(",")}}`;
  return JSON.stringify(value)!;
}
function same(a: unknown, b: unknown) { return canonical(a) === canonical(b); }
function merge(stored: Journal, incoming: Journal): Journal {
  const merged = clone(stored);
  for (const kind of kinds) {
    const a = stored[kind], b = incoming[kind];
    if (!b) continue;
    if (a) {
      check(a.key === b.key && same(a.body, b.body), "Conflicting journal entry");
      if (a.phase === "closed") {
        if (b.phase === "closed") check(same(a.receipt, b.receipt), "Conflicting receipt");
        continue;
      }
    }
    Object.assign(merged, { [kind]: clone(b) });
  }
  return validateJournal(merged, stored.userId, stored.subscriptionId);
}
function adopt(target: Journal, source: Journal) {
  for (const kind of kinds) delete target[kind];
  Object.assign(target, clone(source));
}
function locked<T>(locks: Locks | undefined, key: string, work: () => Promise<T>): Promise<T> {
  check(locks && typeof locks.request === "function", "Browser Web Locks are required");
  return locks.request(key, work);
}
export function journalKey(user: UUID, sub: UUID): string {
  id(user); id(sub);
  return `pulsemedia:movie-upgrade:v1:${encodeURIComponent(user)}:${encodeURIComponent(sub)}`;
}
export function readJournal(storage: Storage, user: UUID, sub: UUID): Journal {
  const raw = storage.getItem(journalKey(user, sub));
  check(raw === null || raw.length <= 98304, "Journal too large");
  return raw === null ? { version: 1, userId: user, subscriptionId: sub } :
    validateJournal(JSON.parse(raw), user, sub);
}
export async function persistJournal(storage: Storage, locks: Locks | undefined, journal: Journal, userId: UUID, subscriptionId: UUID): Promise<Journal> {
  const incoming = clone(validateJournal(journal, userId, subscriptionId));
  const key = journalKey(incoming.userId, incoming.subscriptionId);
  return locked(locks, key, async () => {
    const merged = merge(readJournal(storage, incoming.userId, incoming.subscriptionId), incoming);
    storage.setItem(key, JSON.stringify(merged)); adopt(journal, merged);
    return clone(merged);
  });
}
export async function runEntry(options: RunOptions): Promise<Receipts[Kind]> {
  const { storage, locks, journal, transport, onJournal } = options;
  const incoming = clone(validateJournal(journal, options.userId, options.subscriptionId));
  const entry = clone(validateEntry(options.entry.kind, options.entry, incoming.subscriptionId));
  check(entry.phase === "pending", "Submit a pending entry");
  const key = journalKey(incoming.userId, incoming.subscriptionId);
  return locked(locks, key, async () => {
    const current = merge(readJournal(storage, incoming.userId, incoming.subscriptionId), incoming);
    const old = current[entry.kind];
    if (old && (old.key !== entry.key || !same(old.body, entry.body))) {
      check(options.replaceMode === true && entry.kind === "mode" &&
        old.phase === "closed" && old.key !== entry.key && !current.reservation, "Immutable entry");
    }
    const chosen = old?.key === entry.key ? old : entry;
    Object.assign(current, { [entry.kind]: chosen });
    validateJournal(current, incoming.userId, incoming.subscriptionId);
    storage.setItem(key, JSON.stringify(current)); adopt(journal, current);
    if (chosen.phase === "closed" && !options.replayClosed) return clone(chosen.receipt!);
    const response = await transport({kind:chosen.kind,key:chosen.key,body:clone(chosen.body),phase:"pending"} as Entry);
    check(response.status === 202, "Uncertain command outcome; retain pending entry");
    validateReceipt(chosen, response.data, incoming.subscriptionId);
    if (chosen.phase === "closed") check(same(chosen.receipt, response.data), "Historical receipt mismatch");
    const closed = clone(current);
    Object.assign(closed, { [entry.kind]: { ...chosen, phase: "closed", receipt: clone(response.data) } });
    validateJournal(closed, incoming.userId, incoming.subscriptionId);
    adopt(journal, closed); onJournal(clone(closed));
    // Failure here is repair-only: journal already contains the validated closed receipt.
    storage.setItem(key, JSON.stringify(closed));
    return clone(response.data) as Receipts[Kind];
  });
}

// Local DTOs until Main generates the MP22 OpenAPI paths.
export type Scope = { subscription_id: string; unit_id: string; current_file_id: string; candidate_id: string };
export const stages = ["reserve", "prepare", "send", "organization-confirm", "organize", "retirement-confirm", "retire"] as const;
export type Stage = typeof stages[number];
export type Action = Stage | "observe" | "organization-preview" | "retirement-preview";
export type Body = { action: Action; expected_version: string; confirmed: true; unit_id: string; current_file_id: string; candidate_id: string; operation_id?: string };
export type Result = { status: string; operation_id?: string; unit_id: string; current_file_id: string; candidate_id: string; version: string; execution_allowed: false; reason?: string; comparable?: boolean; improvement?: boolean; current_score?: number; candidate_score?: number; new_file_id?: string };
export type Entry = { key: string; body: Body; result?: Result };
export type Journal = { version: 2; actor: string; scope: Scope; entries: Entry[] };
export type Storage = Pick<globalThis.Storage, "getItem" | "setItem">;
export type Locks = { request<T>(name: string, callback: () => Promise<T>): Promise<T> };
export const isUUID = (v: unknown): v is string => typeof v === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v);
export const isVersion = (v: unknown): v is string => typeof v === "string" && v.length > 0 && v.length <= 1024 && !/[\u0000-\u001f\u007f]/.test(v);
function check(v: unknown): asserts v { if (!v) throw Error("Invalid or conflicting TV upgrade evidence"); }
const scopeKeys = ["subscription_id", "unit_id", "current_file_id", "candidate_id"] as const;
function object(v: unknown, keys: readonly string[]): asserts v is Record<string, unknown> {
  check(v !== null && typeof v === "object" && Object.getPrototypeOf(v) === Object.prototype);
  check(Reflect.ownKeys(v).every(k => typeof k === "string" && keys.includes(k) && "value" in Object.getOwnPropertyDescriptor(v, k)!));
}
export function sameScope(a: Pick<Scope, "unit_id" | "current_file_id" | "candidate_id">, b: Scope) {
  return a.unit_id === b.unit_id && a.current_file_id === b.current_file_id && a.candidate_id === b.candidate_id;
}
export function validateResult(v: unknown, scope: Scope, operationId?: string): Result {
  object(v, ["status", "operation_id", "unit_id", "current_file_id", "candidate_id", "version", "execution_allowed", "reason", "comparable", "improvement", "current_score", "candidate_score", "new_file_id"]);
  check(sameScope(v as Result, scope) && isVersion(v.version) && v.execution_allowed === false && typeof v.status === "string" && /^[a-z_]{1,96}$/.test(v.status));
  if (operationId) check(v.operation_id === operationId);
  if (v.operation_id !== undefined) check(isUUID(v.operation_id));
  if (v.new_file_id !== undefined) check(isUUID(v.new_file_id));
  for (const k of ["comparable", "improvement"] as const) if (v[k] !== undefined) check(typeof v[k] === "boolean");
  for (const k of ["current_score", "candidate_score"] as const) if (v[k] !== undefined) check(Number.isSafeInteger(v[k]));
  if (v.reason !== undefined) check(typeof v.reason === "string" && /^[a-z0-9_]{0,160}$/.test(v.reason));
  return v as Result;
}
export function validateBody(v: unknown, scope: Scope, operationId?: string): Body {
  object(v, ["action", "expected_version", "confirmed", "unit_id", "current_file_id", "candidate_id", "operation_id"]);
  check([...stages, "observe", "organization-preview", "retirement-preview"].includes(v.action as Action));
  check(v.confirmed === true && isVersion(v.expected_version));
  check(sameScope(v as unknown as Scope, scope));
  if (v.action === "reserve") check(v.operation_id === undefined);
  else check(isUUID(v.operation_id) && v.operation_id === operationId);
  return v as Body;
}
const outcomes: Record<Action, readonly string[]> = {
  reserve: ["reserved_not_executable"], prepare: ["prepared_not_sent"],
  send: ["tracking", "send_unknown_requires_observation"], observe: ["tracking", "organization_review_required", "observation_unknown"],
  "organization-preview": ["organization_preview_not_executable"],
  "organization-confirm": ["organization_confirmed_not_executable"],
  organize: ["imported_pending_retirement", "organization_unknown_requires_observation"],
  "retirement-preview": ["retirement_preview_not_executable"],
  "retirement-confirm": ["retirement_confirmed_not_executable"],
  retire: ["retired", "retirement_unknown_requires_observation"],
};
export function successful(result?: Result) { return !!result && !result.reason && !/unknown|manual|failed|dead|conflict|reject/.test(result.status); }
export function completedStages(journal: Journal): Stage[] {
  const completed: Stage[] = [];
  for (const entry of journal.entries) {
    if (stages.includes(entry.body.action as Stage) && successful(entry.result) && outcomes[entry.body.action].includes(entry.result!.status)) completed.push(entry.body.action as Stage);
    // Observation proves progress of the original send without manufacturing its lost receipt.
    if (entry.body.action === "observe" && successful(entry.result) && ["tracking", "organization_review_required"].includes(entry.result!.status) && !completed.includes("send") && completed.at(-1) === "prepare") completed.push("send");
  }
  return completed;
}
export function pendingEntry(journal: Journal) {
  const sent = completedStages(journal).includes("send");
  return journal.entries.find(e => (!e.result || /unknown/.test(e.result.status)) && !(sent && e.body.action === "send"));
}
export function latestResult(journal: Journal) { return journal.entries.filter(e => e.result).at(-1)?.result; }
export function validateJournal(v: unknown, actor: string, scope: Scope): Journal {
  scopeKeys.forEach(k => check(isUUID(scope[k]))); check(isUUID(actor));
  object(v, ["version", "actor", "scope", "entries"]); object(v.scope, scopeKeys);
  check(v.version === 2 && v.actor === actor && v.scope.subscription_id === scope.subscription_id && sameScope(v.scope as Scope, scope));
  check(Array.isArray(v.entries) && v.entries.length <= 128);
  const journal = v as unknown as Journal;
  let operationId: string | undefined;
  const keys = new Set<string>();
  for (let i = 0; i < journal.entries.length; i++) {
    const entry = journal.entries[i]; object(entry, ["key", "body", "result"]);
    check(isUUID(entry.key) && !keys.has(entry.key)); keys.add(entry.key);
    validateBody(entry.body, scope, operationId);
    const prior = { ...journal, entries: journal.entries.slice(0, i) };
    const pending = pendingEntry(prior), complete = completedStages(prior), action = entry.body.action;
    if (pending) check(pending.body.action === "send" && action === "observe");
    if (stages.includes(action as Stage)) check(stages[complete.length] === action && !prior.entries.some(e => e.body.action === action));
    if (action === "observe") check(complete.includes("prepare") && journal.entries.slice(0, i).some(e => e.body.action === "send") && !complete.includes("organize"));
    if (action === "organization-preview") check(complete.includes("send") && !complete.includes("organization-confirm"));
    if (action === "organization-confirm") check(latestResult(prior)?.status === "organization_preview_not_executable");
    if (action === "retirement-preview") check(complete.includes("organize") && !complete.includes("retirement-confirm"));
    if (action === "retirement-confirm") check(latestResult(prior)?.status === "retirement_preview_not_executable");
    if (i > 0) check(entry.body.expected_version === latestResult(prior)?.version);
    if (entry.result) {
      validateResult(entry.result, scope, operationId);
      check(isUUID(entry.result.operation_id) && outcomes[action].includes(entry.result.status));
      if (!operationId) operationId = entry.result.operation_id;
    }
  }
  return journal;
}
export function journalKey(actor: string, scope: Scope) {
  check(isUUID(actor)); scopeKeys.forEach(k => check(isUUID(scope[k])));
  return `pulsemedia:tv-episode-upgrade:v1:${actor}:${scope.subscription_id}:${scope.unit_id}`;
}
export function readJournal(storage: Storage, actor: string, scope: Scope): Journal {
  const raw = storage.getItem(journalKey(actor, scope)); check(raw === null || raw.length <= 131072);
  // Keep the old key: a legacy unknown command must block, never disappear through a version bump.
  return raw === null ? { version: 2, actor, scope: { ...scope }, entries: [] } : validateJournal(JSON.parse(raw), actor, scope);
}
export async function submit(options: { storage: Storage; locks?: Locks; actor: string; scope: Scope; entry: Entry; transport: (entry: Entry) => Promise<unknown>; onSaved: (journal: Journal) => void }) {
  const { storage, locks, actor, scope, entry, transport, onSaved } = options;
  check(locks && typeof locks.request === "function");
  return locks.request(journalKey(actor, scope), async () => {
    const journal = readJournal(storage, actor, scope);
    let original = journal.entries.find(e => e.key === entry.key);
    {
      check(!original && !entry.result);
      const pending = pendingEntry(journal);
      check(!pending || (pending.body.action === "send" && entry.body.action === "observe"));
      original = structuredClone(entry); journal.entries.push(original);
      validateJournal(journal, actor, scope);
      storage.setItem(journalKey(actor, scope), JSON.stringify(journal)); onSaved(structuredClone(journal));
    }
    const result = validateResult(await transport(structuredClone(original!)), scope, original!.body.operation_id);
    check(isUUID(result.operation_id) && outcomes[original!.body.action].includes(result.status));
    original!.result = result; validateJournal(journal, actor, scope);
    storage.setItem(journalKey(actor, scope), JSON.stringify(journal)); onSaved(structuredClone(journal));
    return result;
  });
}

export async function recover(options: { storage: Storage; locks?: Locks; actor: string; scope: Scope; entry: Entry; readReceipt: (key: string) => Promise<unknown>; onSaved: (journal: Journal) => void }) {
  const { storage, locks, actor, scope, entry, readReceipt, onSaved } = options;
  check(locks && typeof locks.request === "function");
  return locks.request(journalKey(actor, scope), async () => {
    const journal = readJournal(storage, actor, scope);
    const original = journal.entries.find(e => e.key === entry.key);
    check(original && (!original.result || /unknown/.test(original.result.status)) && JSON.stringify(original.body) === JSON.stringify(entry.body));
    const result = validateResult(await readReceipt(original.key), scope, original.body.operation_id);
    if (result.status === "pending_requires_observation") return result;
    check(isUUID(result.operation_id) && outcomes[original.body.action].includes(result.status));
    original.result = result; validateJournal(journal, actor, scope);
    storage.setItem(journalKey(actor, scope), JSON.stringify(journal)); onSaved(structuredClone(journal));
    return result;
  });
}

import { api } from '@/api/client';

export const base = '/api/v1/library-maintenance';
export type CommandBody = { action: 'reorganize'; file_id: string; target_relative_path: string; expected_version: string; confirmed: true } | { action: 'restore' | 'cleanup'; recycle_id: string; expected_version: string; confirmed: true };
export type Receipt = { command_id: string; action: CommandBody['action']; status: 'completed' | 'unknown_requires_review'; file_id: string; version: string; execution_allowed: false };
export type Journal = { schema: 1; account: string; resource: string; key: string; fileID: string; body: CommandBody; receipt?: Receipt };
type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem'>;
type Locks = { request<T>(name: string, callback: () => Promise<T>): Promise<T> };
const uuid = (v: unknown): v is string => typeof v === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(v);
export const versionOK = (v: unknown): v is string => typeof v === 'string' && v.length > 0 && v.length <= 1024 && !/[\u0000-\u001f\u007f]/.test(v);
export function relativePathOK(v: string) { return v.length > 0 && v.length <= 4096 && !/[\\\u0000-\u001f\u007f:]/.test(v) && v.split('/').every(part => part !== '' && part !== '.' && part !== '..'); }
function check(value: unknown): asserts value { if (!value) throw Error('Invalid maintenance evidence'); }
export function storageKey(account: string, resource: string) { return `pulsemedia:library-maintenance:v1:${encodeURIComponent(account)}:${encodeURIComponent(resource)}`; }
export function validateReceipt(value: unknown, entry: Journal): Receipt {
  check(value && typeof value === 'object'); const r = value as Receipt;
  check(uuid(r.command_id) && r.action === entry.body.action && r.version === entry.body.expected_version && r.execution_allowed === false && ['completed', 'unknown_requires_review'].includes(r.status));
  if (r.action === 'restore' && r.status === 'completed') check(uuid(r.file_id) && r.file_id !== '00000000-0000-0000-0000-000000000000' && r.file_id.toLowerCase() !== entry.fileID.toLowerCase());
  else check(r.file_id === entry.fileID);
  if (entry.receipt) {
    const previous = validateReceipt(entry.receipt, { ...entry, receipt: undefined });
    check(r.command_id === previous.command_id && r.action === previous.action && r.version === previous.version);
    check(previous.status === 'unknown_requires_review' || r.status === 'completed' && r.file_id === previous.file_id);
  }
  return r;
}
export function readJournal(storage: Storage, account: string, resource: string): Journal | null {
  const raw = storage.getItem(storageKey(account, resource)); if (raw === null) return null;
  check(raw.length <= 32768); const j = JSON.parse(raw) as Journal;
  check(j && j.schema === 1 && j.account === account && j.resource === resource && uuid(j.key) && uuid(j.fileID));
  const b = j.body; check(b && b.confirmed === true && versionOK(b.expected_version));
  if (b.action === 'reorganize') check(resource === `file:${b.file_id}` && b.file_id === j.fileID && relativePathOK(b.target_relative_path) && !('recycle_id' in b));
  else check(['restore', 'cleanup'].includes(b.action) && /^(movie|tv):[0-9a-f-]{36}$/i.test(b.recycle_id) && resource === `recycle:${b.recycle_id}` && !('target_relative_path' in b) && !('file_id' in b));
  if (j.receipt) validateReceipt(j.receipt, j); return j;
}
// Keep the shared client's Cookie/CSRF and expired-session middleware while Main generates these paths.
type Options = { signal?: AbortSignal; cache?: RequestCache; body?: unknown; params?: { header?: Record<string, string> } };
const client = api as unknown as Record<'GET' | 'POST', (path: string, options: Options) => Promise<{ data?: unknown; error?: unknown; response: Response }>>;
export async function request(method: 'GET' | 'POST', path: string, options: Options = {}) {
  const result = await client[method](base + path, { ...options, cache: 'no-store' });
  if (!result.response.ok || result.error || !result.data) throw Error('Maintenance request unavailable');
  return result.data;
}
export async function runCommand(o: { storage: Storage; locks?: Locks; account: string; resource: string; fileID: string; body?: CommandBody; live: () => boolean; saved: (entry: Journal) => void; transport: (entry: Journal, sending: boolean) => Promise<unknown> }) {
  check(o.locks?.request);
  return o.locks.request(storageKey(o.account, o.resource), async () => {
    if (!o.live()) return;
    let j = readJournal(o.storage, o.account, o.resource);
    if (o.body) {
      if (j) { o.saved(j); return; }
      j = { schema: 1, account: o.account, resource: o.resource, fileID: o.fileID, key: crypto.randomUUID(), body: structuredClone(o.body) };
      o.storage.setItem(storageKey(o.account, o.resource), JSON.stringify(j));
      readJournal(o.storage, o.account, o.resource); o.saved(j);
    }
    if (!j || j.receipt?.status === 'completed' || !o.live()) return;
    const receipt = validateReceipt(await o.transport(j, Boolean(o.body)), j);
    // Close durable state even if the component unmounted while the physical operation finished.
    j = { ...j, receipt }; o.storage.setItem(storageKey(o.account, o.resource), JSON.stringify(j));
    if (o.live()) o.saved(j); return receipt;
  });
}
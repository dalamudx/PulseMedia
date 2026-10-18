import type { components } from "@/api/schema";

export type SearchReceipt = components["schemas"]["InteractiveReleaseSearchReceipt"];
export type SearchSnapshot = components["schemas"]["InteractiveReleaseSearchSnapshot"];
export type SearchInput = components["schemas"]["InteractiveReleaseSearchRequest"];
export type Recovery = { version: 1; userId: string; scope: string; key: string; action: "create" | "cancel"; searchId?: string; subscriptionId?: string; mediaId?: string };
export type SearchStorage = Pick<Storage, "getItem" | "setItem">;
export const isUUID = (value: unknown): value is string => typeof value === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value);
export const recoveryKey = (userId: string, scope: string) => `pulse:interactive-search:v1:${userId}:${scope}`;
const fields = new Set(["version", "userId", "scope", "key", "action", "searchId", "subscriptionId", "mediaId"]);
export function parseRecovery(raw: string | null, userId: string, scope: string): Recovery | null {
  if (raw === null) return null;
  const v: unknown = JSON.parse(raw);
  if (!v || typeof v !== "object" || Array.isArray(v)) throw Error("invalid recovery");
  const r = v as Recovery;
  if (Object.keys(r).some(k => !fields.has(k)) || r.version !== 1 || r.userId !== userId || r.scope !== scope || !isUUID(r.userId) || !isUUID(r.key) || !["create", "cancel"].includes(r.action) || [r.searchId,r.subscriptionId,r.mediaId].some(id => id !== undefined && !isUUID(id)) || (r.action === "cancel" && !r.searchId)) throw Error("invalid recovery");
  return r;
}
// Explicit projection: query text, candidates and remote addresses never enter storage.
export function saveRecovery(storage: SearchStorage, record: Recovery): void {
  const {version,userId,scope,key,action,searchId,subscriptionId,mediaId} = record;
  const clean = {version,userId,scope,key,action,searchId,subscriptionId,mediaId};
  parseRecovery(JSON.stringify(clean), userId, scope);
  storage.setItem(recoveryKey(userId,scope), JSON.stringify(clean));
}
export type Reply = { status: number; data?: SearchReceipt };
export type CommandOutcome = { kind: "confirmed"; record: Recovery; receipt: SearchReceipt; persisted: boolean } | { kind: "unknown" | "missing" | "auth" | "not_sent"; record: Recovery };
export function captureReceipt(record: Recovery, reply: Reply, storage: SearchStorage): CommandOutcome | null {
  if (reply.status >= 200 && reply.status < 300 && reply.data && isUUID(reply.data.id) && (!record.searchId || record.searchId === reply.data.id) && record.subscriptionId === reply.data.subscription_id && record.mediaId === reply.data.media_id) {
    const next = {...record,searchId:reply.data.id};
    let persisted = false;
    try { saveRecovery(storage,next); persisted = true; } catch { /* The stable receipt remains closed in memory. */ }
    return {kind:"confirmed",record:next,receipt:reply.data,persisted};
  }
  return null;
}
export async function searchCommand(options: { record: Recovery; storage: SearchStorage; lookup: () => Promise<Reply>; send: () => Promise<Reply>; fresh?: boolean; retryMissing?: boolean }): Promise<CommandOutcome> {
  const {record,storage,lookup,send,fresh,retryMissing} = options;
  try {
    if (!fresh) {
      const reply = await lookup();
      const captured = captureReceipt(record,reply,storage);
      if (captured) return captured;
      if (reply.status === 401 || reply.status === 403) return {kind:"auth",record};
      if (reply.status !== 404) return {kind:"unknown",record};
      if (!retryMissing) return {kind:"missing",record};
    }
    try { saveRecovery(storage,record); } catch { return {kind:"not_sent",record}; }
    const reply = await send();
    // Capture successful submission before the caller observes abort or account changes.
    const captured = captureReceipt(record,reply,storage);
    if (captured) return captured;
    return {kind:reply.status === 401 || reply.status === 403 ? "auth" : "unknown",record};
  } catch { return {kind:"unknown",record}; }
}
export const searchRunning = (state: string) => state === "queued" || state === "running";
const states: Record<string,string> = {queued:"等待检索",running:"正在检索",succeeded:"已完成",partial:"部分完成",failed:"检索失败",deferred:"等待站点预算",timeout:"已超时",cancelled:"已取消",stale:"观察已过期"};
export const searchStateLabel = (state: string) => Object.hasOwn(states,state) ? states[state] : "状态待确认";
export function snapshotMessage(snapshot: SearchSnapshot | null, phase: string): string {
  if (phase === "auth") return "授权已失效，请重新登录后恢复原搜索。";
  if (phase === "stale") return "当前观察读取失败；已显示的资源可能过期，请刷新授权观察后再选择获取。";
  if (["recovering","unknown","missing"].includes(phase)) return "原搜索命令待确认；恢复只读取原回执，不会重新访问站点。";
  if (phase === "blocked") return "浏览器恢复记录不可用，搜索已暂停；请恢复存储后重试。";
  if (!snapshot) return phase === "creating" ? "正在提交搜索，等待首批资源…" : "";
  if (searchRunning(snapshot.state)) return snapshot.results.length ? "首批资源已返回，其余站点仍在检索…" : "站点仍在检索，尚未返回资源…";
  if (snapshot.state === "failed") return "检索失败，不能据此判断没有资源。";
  if (snapshot.state === "timeout") return "观察期限已到；仅展示已收到的资源。";
  if (snapshot.state === "cancelled") return "搜索需求已取消；已开始的站点请求可能仍在结束中。";
  if (snapshot.partial || snapshot.truncated || snapshot.state === "partial") return "结果不完整或已达展示上限，仅显示已收到的资源。";
  return snapshot.results.length ? "检索已完成。" : "检索已完成，没有匹配资源。";
}

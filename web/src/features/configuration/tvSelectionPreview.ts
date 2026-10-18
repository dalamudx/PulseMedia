import type { paths } from "@/api/client";

type Route = paths["/api/v1/subscriptions/{id}/tv-selection-preview"]["post"];
export type SelectionRequest = Route["requestBody"]["content"]["application/json"];
export type SelectionResponse = Route["responses"]["200"]["content"]["application/json"];
export type SelectionScope = { userId: string; subscriptionId: string; signature: string };
export type ReviewAlternative = SelectionScope & {
  kind: "pack" | "episode";
  candidateId: string;
  reviewId: string;
  season: number;
  unitKeys: string[];
  title: string;
};
export const selectionModes = [
  ["none", "不偏好格式：按当前品质比较"],
  ["prefer_pack", "优先完整包：缺失比例达到门槛时优先"],
  ["prefer_episodes", "优先单集：须覆盖全部待补目标"],
  ["require_pack", "必须完整包：无合格包则等待"],
] as const;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export function sameScope(a: SelectionScope, b: SelectionScope): boolean {
  return a.userId === b.userId && a.subscriptionId === b.subscriptionId && a.signature === b.signature;
}
// Called only with independently successful receipts plus their current matching observation.
// The collection is a convenience for choosing references, never authorization or evidence.
export function reviewAlternative(scope: SelectionScope, kind: ReviewAlternative["kind"],
  receipt: { id: string; subscription_id: string; candidate_id: string; status: string },
  unitKeys: string[], title: string): ReviewAlternative | null {
  if (receipt.status !== "reviewed_not_executable" || receipt.subscription_id !== scope.subscriptionId ||
      !uuid.test(receipt.id) || !uuid.test(receipt.candidate_id) || !unitKeys.length || unitKeys.length > 10000 ||
      (kind === "episode" && unitKeys.length !== 1) || new Set(unitKeys).size !== unitKeys.length) return null;
  const coordinates = unitKeys.map(key => /^S(\d{2,4})E(\d{2,4})$/.exec(key));
  if (coordinates.some(row => !row || Number(row[1]) < 1 || Number(row[2]) < 1)) return null;
  const season = Number(coordinates[0]![1]);
  if (season > 999 || coordinates.some(row => Number(row![1]) !== season)) return null;
  return { ...scope, kind, candidateId: receipt.candidate_id, reviewId: receipt.id, season, unitKeys: [...unitKeys], title: title.slice(0, 240) };
}
export function collectReview(rows: ReviewAlternative[], next: ReviewAlternative): ReviewAlternative[] {
  const current = rows.filter(row => sameScope(row, next));
  const previous = current.find(row => row.reviewId === next.reviewId);
  if (previous && JSON.stringify(previous) === JSON.stringify(next)) return rows;
  return [...current.filter(row => row.kind !== next.kind || row.candidateId !== next.candidateId), next].slice(-64);
}
export function selectionRequest(scope: SelectionScope, rows: ReviewAlternative[], selected: string[],
  season: string, mode: string, ratio: string, expected?: SelectionResponse["version"]): SelectionRequest | null {
  if (!/^\d+$/.test(season) || Number(season) < 1 || Number(season) > 999 ||
      !selectionModes.some(([value]) => value === mode) || !/^(?:0(?:\.\d+)?|1(?:\.0+)?)$/.test(ratio) ||
      !selected.length || selected.length > 64 || new Set(selected).size !== selected.length) return null;
  const chosen = selected.map(id => rows.find(row => row.reviewId === id && sameScope(row, scope) && row.season === Number(season)));
  if (chosen.some(row => !row)) return null;
  const refs = (kind: ReviewAlternative["kind"]) => chosen.filter(row => row!.kind === kind)
    .map(row => ({ candidate_id: row!.candidateId, review_id: row!.reviewId }));
  return { season: Number(season), pack_policy: { mode: mode as SelectionRequest["pack_policy"]["mode"], min_missing_ratio: Number(ratio) },
    packs: refs("pack"), episodes: refs("episode"), ...(expected ? { expected } : {}) };
}
export function validSelectionResponse(value: SelectionResponse, request: SelectionRequest): boolean {
  const ids = new Set([...request.packs, ...request.episodes].map(row => row.candidate_id));
  const chosen = value?.decision?.candidate_ids, v = value?.version;
  const versionKeys = ["media_id", "intent_version", "media_version", "policy_id", "policy_revision", "scope_stamp", "configuration_stamp", "selection_policy_stamp"] as const;
  const validVersion = v && uuid.test(v.media_id) && typeof v.policy_id === "string" && v.policy_id.length > 0 &&
    [v.intent_version, v.media_version, v.policy_revision].every(n => Number.isSafeInteger(n) && n > 0) &&
    [v.scope_stamp, v.configuration_stamp, v.selection_policy_stamp].every(s => typeof s === "string" && /^[0-9a-f]{64}$/.test(s)) &&
    (!request.expected || versionKeys.every(key => request.expected![key] === v[key]));
  return !!validVersion && value?.status === "preview_not_executable" && value.execution_allowed === false && !!value.version &&
    ["pack", "episodes", "hold"].includes(value.decision?.kind) && typeof value.decision.reason === "string" && Array.isArray(chosen) &&
    new Set(chosen).size === chosen.length && chosen.every(id => ids.has(id)) &&
    (value.decision.kind === "hold" ? chosen.length === 0 : value.decision.kind === "pack" ? chosen.length === 1 && request.packs.some(row => row.candidate_id === chosen[0]) : chosen.length > 0 && chosen.every(id => request.episodes.some(row => row.candidate_id === id))) &&
    Array.isArray(value.excluded) && value.excluded.every(row => !!row && ids.has(row.candidate_id) && typeof row.reason === "string");
}
// Abort is paired with a generation fence: transports may ignore cancellation.
export class SelectionReadGate {
  private epoch = 0;
  private controller: AbortController | null = null;
  invalidate() { this.epoch++; this.controller?.abort(); this.controller = null; }
  begin() { this.invalidate(); const controller = new AbortController(); this.controller = controller; return { epoch: this.epoch, signal: controller.signal }; }
  current(ticket: { epoch: number; signal: AbortSignal }) { return ticket.epoch === this.epoch && !ticket.signal.aborted; }
}
export function selectionFailure(status: number): string {
  return ({ 401: "会话已失效，请重新登录后读取。", 403: "当前账号无权比较此订阅，请核对账号与订阅范围。", 409: "审阅、来源或版本已变化，请重新核验独立审阅后读取最新比较。", 503: "当前比较不可用，请稍后重新读取。" }[status] ?? "比较读取失败，请核对审阅范围后重新读取。") + "旧比较已清除，不会提交任何获取命令。";
}

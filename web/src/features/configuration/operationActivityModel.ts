import type { components } from "@/api/schema";

export type OperationDetails = components["schemas"]["OperationDetails"];
export type OperationTelemetry = components["schemas"]["OperationTelemetry"];
export type OperationTimelinePage = components["schemas"]["OperationTimelinePage"];
export type OperationTimelineEntry = components["schemas"]["OperationTimelineEntry"];

const maxRate = 9223372036854775807n;
export function nativeRate(value: unknown): bigint | null {
  if (typeof value !== "string" || !/^(0|[1-9][0-9]{0,18})$/.test(value)) return null;
  const rate = BigInt(value);
  return rate <= maxRate ? rate : null;
}

export function formatRate(value: unknown): string {
  const rate = nativeRate(value);
  if (rate === null) return "速度未知";
  const units = ["B/s", "KiB/s", "MiB/s", "GiB/s", "TiB/s"];
  let divisor = 1n, index = 0;
  while (index < units.length - 1 && rate >= divisor * 1024n) { divisor *= 1024n; index++; }
  const tenths = rate * 10n / divisor;
  return `${tenths / 10n}${index && tenths % 10n ? `.${tenths % 10n}` : ""} ${units[index]}`;
}

export function observationFresh(stamp: unknown, now = Date.now()): boolean {
  if (typeof stamp !== "string") return false;
  const time = Date.parse(stamp), age = now - time;
  return Number.isFinite(time) && age >= 0 && age <= 45000;
}

export function progressSnapshot(telemetry: OperationDetails["telemetry"]) {
  const value = telemetry?.progress, source = telemetry?.progress_source;
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0 || value > 1 || (source !== "native_percent" && source !== "verified_bytes")) return null;
  return { percent: value === 1 ? 100 : Math.min(99, Math.floor(value * 100)), source: source === "verified_bytes" ? "已验证字节" : "下载器记录" };
}

export function displayTime(stamp: unknown): string {
  if (typeof stamp !== "string" || !Number.isFinite(Date.parse(stamp))) return "时间未知";
  return new Date(stamp).toLocaleString("zh-CN", { hour12: false });
}

export function safeFileName(name: string): string {
  return !name || /[\\/\u0000-\u001f\u007f]|:/.test(name) || name === "." || name === ".." ? "文件名不可用" : name;
}

const states: Record<string, string> = {
  pending: "等待执行", queued: "已排队", executing: "执行中", running: "执行中",
  unknown: "结果待确认", review_required: "需要人工复核", retry_wait: "等待重试",
  paused: "已暂停", succeeded: "此步骤完成", completed: "此步骤完成", skipped: "已跳过", failed: "执行失败", aborted: "已中止",
};
const plans: Record<string, string> = {
  created: "已创建", draft: "草稿", pending: "等待审批", pending_approval: "等待审批", approved: "已批准", queued: "已排队",
  executing: "执行中", running: "执行中", unknown: "结果待确认", review_required: "需要人工复核",
  retry_wait: "等待重试", paused: "已暂停", succeeded: "计划步骤完成", completed: "计划步骤完成",
  failed: "执行失败", aborted: "已中止", cancelled: "已取消", superseded: "已被替代", rejected: "已拒绝",
};
const actions: Record<string, string> = {
  hardlink: "硬链接", copy: "复制", move: "移动", rename: "重命名", mkdir: "创建目录",
  verify: "校验", import: "登记入库", refresh: "刷新媒体服务器", reflink: "克隆文件",
};
export const stepStateLabel = (value: string) => Object.hasOwn(states, value) ? states[value] : "步骤状态未知";
export const planStateLabel = (value: string | undefined) => value && Object.hasOwn(plans, value) ? plans[value] : "计划状态未知";
export const stepActionLabel = (value: string) => Object.hasOwn(actions, value) ? actions[value] : "整理操作";

export function mergeSteps(previous: OperationTimelineEntry[], incoming: OperationTimelineEntry[]): OperationTimelineEntry[] {
  const steps = new Map(previous.map(step => [step.step_id, step]));
  for (const step of incoming) steps.set(step.step_id, step);
  return [...steps.values()].sort((a, b) => a.order - b.order || a.step_id.localeCompare(b.step_id));
}

const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
export function validDetail(value: unknown, id: string): value is OperationDetails {
  return object(value) && value.operation_id === id && typeof value.status === "string" &&
    (value.plan_id === undefined || typeof value.plan_id === "string") &&
    (value.plan_status === undefined || typeof value.plan_status === "string");
}
export function validTimeline(value: unknown, id: string): value is OperationTimelinePage {
  return object(value) && value.operation_id === id && typeof value.observed_at === "string" &&
    (value.plan_id === undefined || typeof value.plan_id === "string") &&
    (value.plan_status === undefined || typeof value.plan_status === "string") &&
    (value.next_cursor === undefined || typeof value.next_cursor === "string") &&
    Array.isArray(value.items) && value.items.length <= 100 && value.items.every(step => object(step) &&
      typeof step.step_id === "string" && Number.isSafeInteger(step.order) && (step.order as number) >= 0 &&
      typeof step.action === "string" && typeof step.state === "string" && typeof step.file_name === "string" &&
      typeof step.updated_at === "string" && (step.finished_at === undefined || typeof step.finished_at === "string"));
}

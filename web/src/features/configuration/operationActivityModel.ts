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

export function recoveryGuidance(operation: OperationDetails, stale: boolean): { title: string; steps: string[] } {
  if (stale) return { title: "先更新操作状态", steps: ["当前显示的是上次读取结果。请查询 / 刷新，确认最新下载与整理状态后再处理。", "读取失败不表示下载失败，也不表示文件不存在。"] };
  const states = [operation.status, operation.download_status, operation.plan_status];
  if (operation.download_status === "unknown" || operation.download_status === "submitting" || (!operation.plan_id && (operation.status === "unknown" || operation.status === "submitting"))) return {
    title: "先核对原任务，避免重复下载",
    steps: ["下载器可能已经接收请求。请在原下载器核对任务，并刷新此操作查看观测结果。", "保留原操作和已有文件，不要换资源重下或删除任务来解除等待。", "如已有未确认命令，请使用下方原命令恢复入口；是否允许继续由最新核对结果决定。"],
  };
  if (states.some(state => state === "review_required" || state === "conflict" || state === "failed" || state === "dead" || state === "unknown")) return {
    title: "核对阻塞步骤后处理",
    steps: operation.plan_id
      ? ["在整理步骤中核对最后完成的步骤、源文件与目标文件，并检查目标目录是否在线。", "管理员可在下方观察原计划，按最新结果恢复原硬链接或修订目标；已有文件不要自行覆盖。"]
      : ["先核对原下载器中的任务和下载目录，确认请求是否已发生。", "管理员可使用下方原提交检查入口。未确认结果之前，保留原任务和获取保护。"],
  };
  if (operation.status === "completed") return { title: "操作已完成", steps: ["可到媒体库核对文件。媒体服务器刷新与确认独立推进，下载进度本身不能证明媒体服务器已收录。"] };
  if (operation.status === "organizing" || operation.status === "waiting_organization" || operation.download_status === "completed") return {
    title: "等待整理和入库核验", steps: ["下载已完成，接下来核对整理步骤。文件核验和入库记录完成前，请保留下载目录中的源文件。"],
  };
  return { title: "查看原操作进度", steps: ["根据下载观测和整理步骤确认进展；需要复核时在此处理，状态未知时先刷新。"] };
}

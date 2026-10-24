import type { components, operations } from "@/api/schema";

export type MediaIntent = components["schemas"]["MediaRequestIntent"];
export type MediaRequest = components["schemas"]["MediaRequestDetails"];
export type ApprovalResult = components["schemas"]["MediaRequestApprovalResult"];
export type CloseBody = operations["closeMediaRequest"]["requestBody"]["content"]["application/json"];
export type ApproveBody = operations["approveMediaRequest"]["requestBody"]["content"]["application/json"];
export type Role = "admin" | "member";
export type Command = {
  schema: 1; userId: string; role: Role; key: string; ownerId: string; intent: MediaIntent;
} & ({ kind: "create"; requestId: null; body: MediaIntent } | { kind: "close"; requestId: string; body: CloseBody } | { kind: "approve"; requestId: string; body: ApproveBody });
export type Phase = "idle" | "unknown" | "missing" | "protected" | "conflict" | "verified-conflict" | "blocked" | "confirmed";
export const requestStateLabel = { pending: "待审核", approved: "已批准", rejected: "已拒绝", withdrawn: "已撤回" };

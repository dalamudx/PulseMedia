import React, { useCallback, useState } from "react";
import { useAuth } from "@/features/auth/AuthContext";
import { CompleteTVOrganizationPanel } from "@/features/configuration/CompleteTVOrganizationPanel";
import { TransferRecoveryPanel } from "@/features/configuration/TransferRecoveryPanel";
import { TransferRevisionPanel } from "@/features/configuration/TransferRevisionPanel";
import { useActivityInvalidation } from "@/features/configuration/useActivityInvalidation";
import { OperationInbox, operationStatusLabel } from "@/features/configuration/OperationInbox";
import { OperationTelemetry } from "@/features/configuration/OperationTelemetry";
import { OperationTimeline } from "@/features/configuration/OperationTimeline";
import {OperationDownloadControl} from "@/features/configuration/OperationDownloadControl";
import {OperationSeedingInspection} from "@/features/configuration/OperationSeedingInspection";
import { useOperationLive } from "@/features/configuration/useOperationLive";
import { SubmissionRetryPanel } from "@/features/configuration/SubmissionRetryPanel";
import { RequirementPanel } from "@/features/configuration/RequirementPanel";
import { OperationRecoveryGuide } from "@/features/configuration/OperationRecoveryGuide";

export const ActivityView: React.FC = () => {
  const { user } = useAuth();
  return user ? <ActivityContent key={`${user.id}:${user.role}`} userId={user.id} role={user.role} /> : null;
};

function ActivityContent({ userId, role }: { userId: string; role: string }) {
  const { version, connected } = useActivityInvalidation();
  const [operationId, setOperationId] = useState("");
  const [selectedOperationId, setSelectedOperationId] = useState("");
  const live = useOperationLive({ operationId: selectedOperationId || undefined, userId, role, refreshVersion: version });
  const operation = live.detail;
  const select = useCallback((id: string) => {
    setOperationId(id);
    if (id === selectedOperationId) void live.refresh();
    else setSelectedOperationId(id);
  }, [selectedOperationId, live.refresh]);

  function lookUp(event: React.FormEvent) {
    event.preventDefault();
    const id = operationId.trim();
    if (id) select(id);
  }

  return <div className="activity-container operation-activity">
    <div className="page-header"><div>
      <h2>活动</h2>
      <p className="subtitle">查看下载观测和整理步骤，处理自己的需求、人工复核与原命令恢复。</p>
    </div></div>
    <p role="status">{connected ? "活动更新已连接，展示来自最近持久观测。" : "活动更新未连接，可刷新列表或重新读取操作。"}</p>
    {/* Operation list on the left, the selected operation's steps on the right. */}
    <div className="activity-split">
      <OperationInbox refreshVersion={version} onSelect={select} />
      <div className="activity-detail">
        <form onSubmit={lookUp} className="media-control-bar">
          <label htmlFor="operation-id">操作 ID</label>
          <input id="operation-id" type="text" className="media-filter-input" value={operationId}
            onChange={event => { setSelectedOperationId(""); setOperationId(event.target.value); }}
            placeholder="也可直接输入操作 ID" autoComplete="off" />
          <button type="submit" className="btn-primary" disabled={live.loading || !operationId.trim()}>
            {live.loading ? "读取中…" : "查询 / 刷新"}
          </button>
        </form>
        {live.error ? <p role="alert" className="config-error">{live.error}</p> : null}
        {live.loading && !operation ? <p role="status">正在读取操作与整理步骤…</p> : null}
        {!live.loading && !operation && !selectedOperationId ? <p className="activity-empty">从左侧选择一个操作查看下载观测与整理步骤，或在上方输入操作 ID。</p> : null}
        {operation ? <section className="card op-card" aria-label="操作详情">
          <h3>操作 {operation.operation_id}</h3>
          <dl><dt>当前状态</dt><dd>{operationStatusLabel(operation.status)}</dd>
            {operation.download_status ? <><dt>下载状态</dt><dd>{operationStatusLabel(operation.download_status)}</dd></> : null}
          </dl>
          <OperationRecoveryGuide operation={operation} stale={live.detailStale || live.timelineStale} isAdmin={role === "admin"} />
          <OperationTelemetry telemetry={operation.telemetry} stale={live.detailStale} />
          <OperationTimeline live={live} />
          <fieldset disabled={!live.canAct} className="operation-activity-actions" aria-label="操作处理">
            <RequirementPanel key={operation.operation_id} operationId={operation.operation_id} />
<OperationDownloadControl key={`control:${operation.operation_id}`} operationId={operation.operation_id} contextReady={live.canAct} />
<OperationSeedingInspection key={`seeding:${operation.operation_id}`} operationId={operation.operation_id} contextReady={live.canAct} />
            <SubmissionRetryPanel key={`retry:${operation.operation_id}`} operationId={operation.operation_id} />
            {operation.plan_id ? <>
              <TransferRecoveryPanel key={operation.plan_id} planId={operation.plan_id} />
              <TransferRevisionPanel key={`revision:${operation.plan_id}`} planId={operation.plan_id} operationId={operation.operation_id} />
            </> : null}
          </fieldset>
        </section> : selectedOperationId && live.timelineError ? <OperationTimeline live={live} /> : null}
      </div>
    </div>
    {selectedOperationId ? <CompleteTVOrganizationPanel operationId={selectedOperationId} contextReady={live.canAct} refreshVersion={version} /> : null}
  </div>;
}

import type { OperationDetails } from "./operationActivityModel";
import { recoveryGuidance } from "./operationActivityModel";

export function OperationRecoveryGuide({ operation, stale, isAdmin }: {
  operation: OperationDetails; stale: boolean; isAdmin: boolean;
}) {
  const guidance = recoveryGuidance(operation, stale);
  return <section className="config-editor" aria-label="核对与恢复指引">
    <h4>{guidance.title}</h4>
    {operation.plan_id ? <p className="config-description">原整理计划：{operation.plan_id}</p> : null}
    <ol>{guidance.steps.map(step => <li key={step}>{step}</li>)}</ol>
    {!isAdmin ? <p className="config-description">需要人工恢复时，请将上方操作 ID 提供给管理员。</p> : null}
  </section>;
}

import React from "react";
import { Link } from "react-router-dom";
import { usePendingRequests } from "./usePendingRequests";
import "./pending-requests.css";

export function PendingRequestsPanel({ userId, role }: { userId?: string; role?: string }): React.ReactElement | null {
  const inbox = usePendingRequests({ userId, role });
  if (!userId || role !== "admin") return null;
  return <div className="dashboard-facts"><section className="facts-section pending-requests-panel" aria-label="待审批提醒" aria-busy={inbox.busy}>
    <div className="facts-heading"><h2>待审批请求</h2><button type="button" onClick={() => { void inbox.refresh(); }} disabled={inbox.busy}>刷新提醒</button></div>
    <div className="pending-requests-line"><strong className="pending-requests-count" aria-label={inbox.count === null ? "待审批数量未知" : `待审批 ${inbox.count} 项`}>{inbox.count ?? "—"}</strong><Link to="/requests?state=pending">处理待审批请求</Link></div>
    {inbox.error ? <p className="facts-notice" role="alert">{inbox.error}</p> : inbox.expired ? <p className="facts-note" role="status">审批数量已过期，请刷新后查看。</p> : inbox.count === null ? <p className="facts-note" role="status">{inbox.busy ? "正在读取审批提醒…" : "审批数量未知。"}</p> : <p className="facts-note">{inbox.count === "0" ? "目前没有待审批请求。" : "所有成员的待审批请求，需逐项确认后批准。"}</p>}
  </section></div>;
}

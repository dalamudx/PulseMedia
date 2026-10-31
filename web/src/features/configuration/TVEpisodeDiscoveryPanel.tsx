import { useEffect, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { useActivityInvalidation } from "./useActivityInvalidation";

type Snapshot = paths["/api/v1/release-searches/{id}"]["get"]["responses"]["200"]["content"]["application/json"];
type Candidate = Snapshot["results"][number];
export type TVEpisodeDiscoverySelection = { candidate: Candidate; currentFileId: string; unitId: string };
type Props = { subscriptionId: string; unitId: string; intentVersion: number; policyRevision: number; onSelect: (selection: TVEpisodeDiscoverySelection) => void };
type Status = paths["/api/v1/subscriptions/{id}/tv-episode-discovery/{unit_id}"]["get"]["responses"]["200"]["content"]["application/json"];
type Preview = paths["/api/v1/subscriptions/{id}/tv-episode-upgrade-previews"]["post"]["responses"]["200"]["content"]["application/json"];
const labels: Record<string, string> = { not_scheduled: "等待首次检查", waiting: "等待下一次检查", running: "正在检索候选", succeeded: "本轮检索已完成", partial: "本轮检索结果不完整", failed: "本轮检索失败", expired: "本轮检索窗口已结束", stale: "订阅、集数或文件已改变，等待下次检查", cancelled: "本轮检索已停止" };

export function TVEpisodeDiscoveryPanel(props: Props) {
  const { user, checking, statusError } = useAuth();
  if (!user || checking || statusError || user.role !== "admin" || !props.unitId) return null;
  return <Discovery key={[user.id, props.subscriptionId, props.unitId, props.intentVersion, props.policyRevision].join(":")} {...props} />;
}
function Discovery({ subscriptionId, unitId, onSelect }: Props) {
  const activity = useActivityInvalidation();
  const [revision, setRevision] = useState(0);
  const [view, setView] = useState<{ status: Status | null; snapshot: Snapshot | null; loading: boolean; error: string }>({ status: null, snapshot: null, loading: true, error: "" });
  const [preview, setPreview] = useState<Preview | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);
  const scope = useRef("");
  useEffect(() => () => { request.current?.abort(); request.current = null; }, []);
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const started = Date.now();
    setView(previous => ({ ...previous, loading: true, error: "" }));
    async function read() {
      try {
        const result = await api.GET("/api/v1/subscriptions/{id}/tv-episode-discovery/{unit_id}", { params: { path: { id: subscriptionId, unit_id: unitId } }, signal: controller.signal, cache: "no-store" });
        if (controller.signal.aborted) return;
        const status = result.data;
        if (result.error || !status || status.subscription_id !== subscriptionId || status.unit_id !== unitId || status.execution_allowed !== false) throw Error("scope");
        let snapshot: Snapshot | null = null;
        if (status.search_id && status.current_file_id) {
          const search = await api.GET("/api/v1/release-searches/{id}", { params: { path: { id: status.search_id } }, signal: controller.signal, cache: "no-store" });
          if (controller.signal.aborted) return;
          if (search.error || !search.data || search.data.search.id !== status.search_id || search.data.search.subscription_id !== subscriptionId) throw Error("search");
          snapshot = search.data;
        }
        const nextScope = status.search_id && status.current_file_id ? `${status.search_id}:${status.current_file_id}` : "";
        if (nextScope !== scope.current) {
          request.current?.abort(); request.current = null; setPreview(null); setBusy(false); setError("");
        }
        scope.current = nextScope;
        setView({ status, snapshot, loading: false, error: "" });
        if (status.state === "running" && Date.now() - started < 35_000) timer = setTimeout(() => void read(), 2000);
      } catch {
        if (!controller.signal.aborted) { scope.current = ""; request.current?.abort(); request.current = null; setPreview(null); setBusy(false); setView({ status: null, snapshot: null, loading: false, error: "发现结果暂不可用，或当前集数条件已改变。请刷新重试。" }); }
      }
    }
    void read();
    return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [subscriptionId, unitId, activity.version, revision]);
  async function compare(candidate: Candidate) {
    if (!view.status?.current_file_id || request.current) return;
    const controller = new AbortController(); request.current = controller;
    const fileId = view.status.current_file_id;
    setBusy(true); setPreview(null); setError("");
    try {
      const result = await api.POST("/api/v1/subscriptions/{id}/tv-episode-upgrade-previews", { params: { path: { id: subscriptionId } }, body: { unit_id: unitId, current_file_id: fileId, candidate_id: candidate.id }, signal: controller.signal, cache: "no-store" });
      if (controller.signal.aborted) return;
      const data = result.data;
      if (result.error || !data || data.unit_id !== unitId || data.current_file_id !== fileId || data.candidate_id !== candidate.id || data.execution_allowed !== false || typeof data.version !== "string" || !data.version) throw Error("preview");
      setPreview(data);
    } catch { if (!controller.signal.aborted) setError("比较失败或当前材料已改变，请刷新后重新比较。"); }
    finally { if (request.current === controller) { request.current = null; if (!controller.signal.aborted) setBusy(false); } }
  }
  return <section className="config-editor" aria-label="持续剧集候选发现">
    <div className="config-heading"><h4>本集持续候选发现</h4><button type="button" className="btn-secondary" style={{ minWidth: 44, minHeight: 44 }} disabled={view.loading || busy} onClick={() => { request.current?.abort(); request.current = null; setPreview(null); setError(""); scope.current = ""; setRevision(n => n + 1); }}>刷新本集发现结果</button></div>
    <p className="config-description">升级模式启用后，符合条件的已播出标准集每六小时检查一次。候选需先比较品质，再逐步确认升级。</p>
    <p role="status">{view.loading ? "正在读取本集发现状态…" : labels[view.status?.state ?? ""] ?? "发现状态未知"}{view.status?.next_discovery_at ? `；下次检查：${new Date(view.status.next_discovery_at).toLocaleString()}` : ""}</p>
    {view.error ? <p role="alert">{view.error}</p> : null}
    {view.snapshot ? <>
      {view.snapshot.partial || view.snapshot.truncated ? <p>检索结果不完整，未列出不代表不存在。</p> : null}
      {view.snapshot.results.length === 0 ? <p>本轮尚无已保存的候选。</p> : <ul>{view.snapshot.results.map(candidate => <li key={candidate.id}>
        <span style={{ overflowWrap: "anywhere" }}>{candidate.title}</span>{" "}
        <button type="button" className="btn-secondary" style={{ minWidth: 44, minHeight: 44 }} disabled={view.loading || busy || !view.status?.current_file_id} onClick={() => void compare(candidate)}>比较本集候选品质</button>
        {preview?.candidate_id === candidate.id ? <div><p>{preview.comparable && preview.improvement ? "当前证据表明此候选可改善本集品质。" : "当前证据未证明此候选可改善本集品质。"}</p>
          {preview.comparable && preview.improvement && view.status?.current_file_id ? <button type="button" className="btn-secondary" style={{ minWidth: 44, minHeight: 44 }} disabled={view.loading || busy} onClick={() => onSelect({ candidate, currentFileId: preview.current_file_id, unitId })}>选择并进入本集升级确认</button> : null}
        </div> : null}
      </li>)}</ul>}
    </> : null}
    {busy ? <p role="status">正在比较本集当前文件与候选…</p> : null}
    {error ? <p role="alert">{error}</p> : null}
  </section>;
}

import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { statusReason } from "./statusReasons";
const comparisonReasons: Record<string, string> = {
  no_eligible_alternative: "暂无合格的完整方案", no_missing_targets: "当前没有待补目标",
  required_pack_unavailable: "必须完整包，但当前没有合格完整包", required_complete_pack: "完整包满足本次强制偏好",
  only_eligible_alternative: "当前只有这一类方案满足条件", pack_preference: "缺失比例达到门槛，优先完整包",
  episode_preference: "单集组合覆盖全部待补目标，按本次偏好优先", quality_then_stable_id: "按当前品质与稳定排序比较",
  pack_review_changed: "完整包审阅或当前材料已变化", full_set_unavailable: "完整季目标当前不可获取，可能已满足、未播出或受保护",
  incomplete_pack_coverage: "完整包明确覆盖不完整", quality_rejected: "未通过当前品质规则",
  episode_review_changed: "单集审阅或当前材料已变化", episode_target_unavailable: "单集当前不在待补范围、未播出或受保护",
};
function comparisonReason(value: string): string {
  const message = Object.hasOwn(comparisonReasons, value) ? comparisonReasons[value] : statusReason(value);
  return typeof message === "string" ? message : "当前状态需复核";
}
import { sameScope, selectionModes, selectionRequest, validSelectionResponse, SelectionReadGate, selectionFailure,
  type ReviewAlternative, type SelectionScope, type SelectionResponse } from "./tvSelectionPreview";

type Props = SelectionScope & { alternatives: ReviewAlternative[]; disabled?: boolean };
export function TVSelectionPreviewPanel(props: Props) {
  // Synchronous remount prevents even one render of an old result after scope changes.
  return <SelectionComparison key={JSON.stringify([props.userId, props.subscriptionId, props.signature])} {...props} />;
}
function SelectionComparison({ alternatives, disabled, ...scope }: Props) {
  const [season, setSeason] = useState(""), [mode, setMode] = useState(""), [ratio, setRatio] = useState("");
  const [selected, setSelected] = useState<string[]>([]), [result, setResult] = useState<SelectionResponse | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const gate = useRef(new SelectionReadGate()), alert = useRef<HTMLParagraphElement>(null);
  const version = useRef<SelectionResponse["version"] | undefined>(undefined);
  const rows = alternatives.filter(row => sameScope(row, scope));
  const fingerprint = JSON.stringify([rows, selected, season, mode, ratio, !!disabled]);
  const [observed, setObserved] = useState("");
  const live = useRef(fingerprint); live.current = fingerprint;
  const request = selectionRequest(scope, rows, selected, season, mode, ratio);
  useEffect(() => {
    gate.current.invalidate(); setResult(null); setBusy(false); setError(""); version.current = undefined;
    setSelected(current => {
      const valid = current.filter(id => rows.some(row => row.reviewId === id && row.season === Number(season)));
      return valid.length === current.length ? current : valid;
    });
  }, [fingerprint]);
  useEffect(() => () => gate.current.invalidate(), []);
  useEffect(() => { if (error) alert.current?.focus(); }, [error]);
  function change(action: () => void) {
    gate.current.invalidate(); setResult(null); setBusy(false); setError(""); version.current = undefined; action();
  }
  async function read(latest: boolean) {
    if (!request || disabled) return;
    const ticket = gate.current.begin(), captured = fingerprint;
    setResult(null); setBusy(true); setError("");
    const body = { ...request, ...(!latest && version.current ? { expected: version.current } : {}) };
    try {
      const r = await api.POST("/api/v1/subscriptions/{id}/tv-selection-preview", {
        signal: ticket.signal, cache: "no-store", params: { path: { id: scope.subscriptionId } }, body,
      });
      if (!gate.current.current(ticket) || live.current !== captured) return;
      if (r.error || !r.data) { version.current = undefined; setError(selectionFailure(r.response.status)); return; }
      if (!validSelectionResponse(r.data, body)) throw new Error("invalid preview");
      version.current = r.data.version; setObserved(captured); setResult(r.data);
    } catch {
      if (gate.current.current(ticket) && live.current === captured) { version.current = undefined; setError(selectionFailure(0)); }
    } finally { if (gate.current.current(ticket) && live.current === captured) setBusy(false); }
  }
  const shown = !disabled && observed === fingerprint ? result : null;
  const label = (id: string) => rows.find(row => row.candidateId === id)?.title || "已选审阅资源";
  return <section className="complete-tv-workflow" aria-label="包与单集只读比较">
    <h4>包与单集只读比较</h4>
    <p className="config-description">选择独立审阅的方案，比较当前季的完整包与单集组合。只读取当前结果，不保存偏好、不创建下载任务；审阅与比较都不是执行许可。</p>
    <label>比较季<select aria-label="比较季" value={season} disabled={disabled} onChange={e => change(() => { setSeason(e.target.value); setSelected([]); })}>
      <option value="">请选择已审阅的季</option>{[...new Set(rows.map(row => row.season))].sort((a,b) => a-b).map(value => <option key={value} value={value}>第 {value} 季</option>)}
    </select></label>
    <label>本次比较偏好<select aria-label="本次比较偏好" value={mode} disabled={disabled} onChange={e => change(() => setMode(e.target.value))}>
      <option value="">请明确选择偏好</option>{selectionModes.map(([value, text]) => <option key={value} value={value}>{text}</option>)}
    </select></label>
    <label>缺失比例门槛（0 至 1）<input aria-label="缺失比例门槛" type="number" min="0" max="1" step="any" value={ratio} disabled={disabled} placeholder="请明确输入，例如 0.5" onChange={e => change(() => setRatio(e.target.value))} /></label>
    <p className="config-description">0.5 表示缺失一半。门槛仅影响优先完整包模式；其它模式也需明确输入。必须完整包时不回退到单集，降低门槛不会扩大获取范围。</p>
    {!rows.length && <p>尚无当前账号与订阅下可比较的独立审阅。先在现有面板完成人工审阅，并读取当前覆盖；不会为比较自动创建审阅。旧版单集缓存不作为本账号的审阅来源。</p>}
    {(["pack", "episode"] as const).map(kind => <fieldset className="config-fieldset" key={kind} disabled={disabled || !season}>
      <legend>{kind === "pack" ? "完整包备选" : "单集组合备选"}</legend>
      {rows.filter(row => row.kind === kind && row.season === Number(season)).map(row => <label className="config-check" key={row.reviewId}>
        <input type="checkbox" checked={selected.includes(row.reviewId)} onChange={e => change(() => setSelected(current => e.target.checked ? [...current, row.reviewId] : current.filter(id => id !== row.reviewId)))} />
        <span>{row.title || "已审阅资源"} · {row.unitKeys.slice(0, 8).join("、")}{row.unitKeys.length > 8 ? ` 等 ${row.unitKeys.length} 集` : ""} · 审阅已保存，当前有效性待服务端核验</span>
      </label>)}
      {!rows.some(row => row.kind === kind && row.season === Number(season)) && <p>此季暂无{kind === "pack" ? "完整包" : "单集"}审阅备选。</p>}
    </fieldset>)}
    <p className="config-description">本页最多保留 64 条审阅，关闭页面或切换账号、订阅版本后清空。可勾选多个独立单集，组合是否完整由服务端判断。</p>
    <div className="config-actions"><button type="button" className="btn-secondary" disabled={disabled || busy || !request} onClick={() => void read(true)}>读取最新比较</button>
      <button type="button" className="btn-secondary" disabled={disabled || busy || !request || !shown} onClick={() => void read(false)}>按本次版本重新读取</button></div>
    {busy && <p role="status">正在核验当前账号、订阅范围与独立审阅…</p>}
    {error && <p role="alert" tabIndex={-1} ref={alert} className="config-error">{error}</p>}
    {shown && <div role="status" className="mapping-preview"><p>{shown.decision.kind === "pack" ? "当前比较倾向完整包" : shown.decision.kind === "episodes" ? "当前比较倾向单集组合" : "当前需等待或重新核验"}：{comparisonReason(shown.decision.reason)}</p>
      <p>仅预览，不可执行。不会创建占位、下载或整理任务。</p>
      {shown.decision.candidate_ids.length > 0 && <ul>{shown.decision.candidate_ids.map(id => <li key={id}>{label(id)}</li>)}</ul>}
      {shown.excluded.length > 0 && <ul aria-label="未纳入方案的原因">{shown.excluded.map((row,i) => <li key={`${row.candidate_id}:${i}`}>{label(row.candidate_id)}：{comparisonReason(row.reason)}</li>)}</ul>}
      <p className="config-description">意图版本 {shown.version.intent_version} · 元数据版本 {shown.version.media_version} · 品质策略版本 {shown.version.policy_revision}。刷新不会发起获取。</p>
    </div>}
  </section>;
}

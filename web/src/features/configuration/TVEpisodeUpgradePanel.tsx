import { useEffect, useId, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import { completedStages, isUUID, journalKey, latestResult, pendingEntry, readJournal, recover, stages, submit, successful, validateResult, type Action, type Body, type Entry, type Journal, type Result, type Scope } from "./tvEpisodeUpgradeJournal";

export interface TVEpisodeUpgradePanelProps {
  subscriptionId: string;
  unitId: string;
  currentFileId: string;
  candidateId: string;
  onUpdated?: () => void;
}
const labels: Record<Action, string> = {
  reserve: "冻结单集升级预留", prepare: "取源准备", send: "发送升级下载", observe: "观察原下载与文件清单",
  "organization-preview": "读取整理复核", "organization-confirm": "确认并准备整理计划", organize: "执行整理并核验入库",
  "retirement-preview": "读取旧版本退休复核", "retirement-confirm": "确认旧版本退休计划", retire: "执行旧版本退休",
};
const messages: Record<string, string> = {
  reserved_not_executable: "预留已冻结，尚未准备或发送。", prepared_not_sent: "取源准备完成，尚未发送。",
  tracking: "已确认原下载身份；完成清单仍需显式观察。", send_unknown_requires_observation: "发送结果不明，只能观察原下载，不会重发。",
  observation_unknown: "原下载状态仍不明，请检查下载器和原任务。", organization_review_required: "下载清单已核验，等待整理复核。",
  organization_preview_not_executable: "整理复核已读取，尚未授权。", organization_authorized_not_executable: "整理计划已确认，尚未执行。",
  organization_confirmed_not_executable: "整理计划已确认，尚未执行。", organization_prepared_not_executable: "整理计划已准备，尚未执行。",
  imported_pending_retirement: "新文件已入库，旧版本等待退休复核。", retirement_preview_not_executable: "旧版本退休材料已读取，尚未授权。",
  retirement_confirmed_not_executable: "退休计划已确认，尚未执行。", retirement_authorized_not_executable: "退休计划已确认，尚未执行。",
  retired: "旧版本已移入受控回收区。", manual_review: "需要人工复核，原保护保留。",
};
// Local typing keeps the shared cookie/auth middleware; generated OpenAPI files are owned by Main.
const client = api as unknown as {
  GET(path: string, options: Record<string, unknown>): Promise<{ data?: unknown; error?: unknown; response: Response }>;
  POST(path: string, options: Record<string, unknown>): Promise<{ data?: unknown; error?: unknown; response: Response }>;
};
const contains = (value: string | undefined, values: string[]) => !!value && values.includes(value);
export function TVEpisodeUpgradePanel(props: TVEpisodeUpgradePanelProps) {
  const { user, checking, statusError } = useAuth();
  if (!user || user.role !== "admin" || checking || statusError) return null;
  if (![props.subscriptionId, props.unitId, props.currentFileId, props.candidateId].every(isUUID)) return <p role="alert" className="config-error">请选择一个标准集、一个旧文件和一个候选资源。</p>;
  return <EpisodeUpgrade key={[user.id, props.subscriptionId, props.unitId, props.currentFileId, props.candidateId].join(":")} {...props} actor={user.id} />;
}
function EpisodeUpgrade({ subscriptionId, unitId, currentFileId, candidateId, actor, onUpdated }: TVEpisodeUpgradePanelProps & { actor: string }) {
  const scope: Scope = { subscription_id: subscriptionId, unit_id: unitId, current_file_id: currentFileId, candidate_id: candidateId };
  const uid = useId(), request = useRef<AbortController | null>(null), alert = useRef<HTMLParagraphElement>(null);
  const mounted = useRef(false), changed = useRef(false);
  const [journal, setJournal] = useState<Journal | null>(null), [blocked, setBlocked] = useState(false);
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [preview, setPreview] = useState<Result | null>(null), [ack, setAck] = useState<string | null>(null);
  function load() {
    try { setJournal(readJournal(localStorage, actor, scope)); setBlocked(false); }
    catch { setBlocked(true); setError("本地记录不可读、使用旧合同或此集已有其它文件/候选的升级记录。请在原选择中核实；不要清除未决命令。"); }
  }
  function discard() { setPreview(null); setAck(null); }
  useEffect(() => {
    mounted.current = true; load();
    const storageChanged = (event: StorageEvent) => {
      if (event.key === null || event.key === journalKey(actor, scope)) { changed.current = true; discard(); if (!request.current) load(); }
    };
    window.addEventListener("storage", storageChanged);
    return () => { mounted.current = false; request.current?.abort(); window.removeEventListener("storage", storageChanged); };
  }, [actor, subscriptionId, unitId, currentFileId, candidateId]);
  useEffect(() => { if (error) alert.current?.focus(); }, [error]);
  const entries = journal?.entries ?? [], complete = journal ? completedStages(journal) : [];
  const pending = journal ? pendingEntry(journal) : undefined, latest = journal ? latestResult(journal) : undefined;
  const operationId = entries[0]?.result?.operation_id, next = stages[complete.length];
  const locked = busy || blocked || !journal;
  async function call(suffix: "previews" | "commands", body: unknown, signal: AbortSignal, key?: string) {
    const r = await client.POST(`/api/v1/subscriptions/{id}/tv-episode-upgrade-${suffix}`, {
      signal, cache: "no-store", params: { path: { id: subscriptionId } }, body,
      ...(key ? { headers: { "Idempotency-Key": key } } : {}),
    });
    if (r.error || !r.data || ![200, 202].includes(r.response.status)) throw Error("Unconfirmed response");
    return r.data;
  }
  async function run(work: (signal: AbortSignal) => Promise<void>) {
    if (request.current || blocked || !journal) return;
    const c = new AbortController(); request.current = c; changed.current = false;
    const timer = window.setTimeout(() => c.abort(), 30000);
    setBusy(true); setError(""); setNotice(""); setAck(null);
    try { await work(c.signal); }
    catch { if (mounted.current) { discard(); load(); setError("结果尚未核实或条件已改变。原键和原参数已保留；发送不再重试。请读取原命令回执；发送结果不明时可显式观察原下载。"); } }
    finally { window.clearTimeout(timer); if (request.current === c) request.current = null; if (mounted.current) { setBusy(false); if (changed.current) { discard(); load(); } } }
  }
  function saved(value: Journal) { if (mounted.current) setJournal(value); }
  function compare() {
    discard();
    void run(async signal => {
      const data = validateResult(await call("previews", { unit_id: unitId, current_file_id: currentFileId, candidate_id: candidateId }, signal), scope);
      if (data.status !== "preview_not_executable") throw Error("Invalid preview status");
      if (mounted.current && !changed.current) setPreview(data);
    });
  }
  function allowed(action: Action) {
    if (action === "observe") return !!operationId && complete.includes("prepare") && entries.some(e => e.body.action === "send") && !complete.includes("organize") && (!pending || pending.body.action === "send");
    if (pending) return false;
    if (action === "organization-preview") return next === "organization-confirm" && latest?.status === "organization_review_required";
    if (action === "retirement-preview") return next === "retirement-confirm" && latest?.status === "imported_pending_retirement";
    if (next !== action) return false;
    if (action === "reserve") return preview?.status === "preview_not_executable" && preview.comparable === true && preview.improvement === true && !preview.reason;
    if (action === "prepare") return latest?.status === "reserved_not_executable";
    if (action === "send") return latest?.status === "prepared_not_sent";
    if (action === "organization-confirm") return latest?.status === "organization_preview_not_executable";
    if (action === "organize") return contains(latest?.status, ["organization_authorized_not_executable", "organization_confirmed_not_executable", "organization_prepared_not_executable"]);
    if (action === "retirement-confirm") return latest?.status === "retirement_preview_not_executable";
    if (action === "retire") return contains(latest?.status, ["retirement_authorized_not_executable", "retirement_confirmed_not_executable"]);
    return false;
  }
  function execute(action: Action) {
    const reviewAction = ["observe", "organization-preview", "retirement-preview"].includes(action);
    if (locked || (!allowed(action) || (!reviewAction && ack !== action))) return;
    const version = action === "reserve" ? preview?.version : latest?.version;
    if (!version) return;
    let entry: Entry;
    try {
      const body: Body = {
        action, unit_id: unitId, current_file_id: currentFileId, candidate_id: candidateId,
        expected_version: version!, confirmed: true, ...(action === "reserve" ? {} : { operation_id: operationId }),
      };
      entry = { key: crypto.randomUUID(), body };
    } catch { setError("浏览器无法生成安全确认编号，请使用支持此功能的浏览器。"); return; }
    discard();
    void run(async signal => {
      const data = await submit({ storage: localStorage, locks: navigator.locks, actor, scope, entry, onSaved: saved,
        transport: value => call("commands", value.body, signal, value.key) });
      if (mounted.current && !changed.current) {
        setNotice(successful(data) ? "原命令回执已保存，请继续核对下一步。" : "当前结果不授予下一步操作；原保护和确认记录继续保留。");
        onUpdated?.();
      }
    });
  }
  function readReceipt(entry: Entry) {
    if (locked) return;
    discard();
    void run(async signal => {
      const data = await recover({ storage: localStorage, locks: navigator.locks, actor, scope, entry, onSaved: saved,
        readReceipt: async key => {
          const r = await client.GET("/api/v1/subscriptions/{id}/tv-episode-upgrade-commands/{key}", {
            signal, cache: "no-store", params: { path: { id: subscriptionId, key } },
          });
          if (r.error || !r.data || ![200, 202].includes(r.response.status)) throw Error("Unconfirmed receipt");
          return r.data;
        } });
      if (mounted.current && !changed.current) {
        setNotice(data.status === "pending_requires_observation" ? "原命令仍待观察，未获得下一步许可。" : "原命令回执已保存，请继续核对下一步。");
        onUpdated?.();
      }
    });
  }
  const scores = latest?.current_score !== undefined || latest?.candidate_score !== undefined ? latest : preview;
  return <section className="config-editor" aria-labelledby={`${uid}-title`} aria-busy={busy} style={{ overflowWrap: "anywhere", minWidth: 0 }}>
    <h3 id={`${uid}-title`}>TV 单集品质升级</h3>
    <p className="config-description">仅升级当前标准集与旧文件。预留、准备、发送、整理计划与入库、旧版本退休逐步确认；旧文件保护持续保留。</p>
    <dl><dt>标准集 UUID</dt><dd>{unitId}</dd><dt>旧文件 UUID</dt><dd>{currentFileId}</dd><dt>候选 UUID</dt><dd>{candidateId}</dd></dl>
    {error && <p role="alert" className="config-error" tabIndex={-1} ref={alert}>{error}</p>}
    {notice && <p role="status">{notice}</p>}{busy && <p role="status">正在核验，请稍候…</p>}
    <button type="button" className="btn-secondary" disabled={busy} onClick={() => { discard(); load(); }}>重新读取本地记录</button>
    {latest && <p role="status">{Object.hasOwn(messages, latest.status) ? messages[latest.status] : "服务器已返回状态，请核对原任务；未提供下一步许可时保持保护。"}</p>}
    {scores && <p>当前文件分数 {scores.current_score ?? "未知"} · 候选分数 {scores.candidate_score ?? "未知"}</p>}
    {preview && <p role="status">{allowed("reserve") ? "候选严格优于旧文件。比较结果尚未授权下载。" : "当前证据不能支持升级，请检查策略、唯一映射与候选范围。"}</p>}
    {entries.length > 0 && <ol aria-label="升级确认记录">{entries.map(entry => <li key={entry.key}>{labels[entry.body.action]}：{entry.result ? "原回执已保存" : "结果待核实"}</li>)}</ol>}
    {pending && <div>
      <p role="status">{labels[pending.body.action]}结果待核实，原幂等编号：{pending.key}。</p>
      <p>读取原回执只使用原编号查询，不会重新提交命令或发送下载。</p>
      <button type="button" className="btn-secondary" disabled={locked} onClick={() => readReceipt(pending)}>读取原命令回执</button>
      {pending.body.action === "send" && <p>发送不会重试。请观察原下载；无法确认时保留原保护并转人工处理。</p>}
    </div>}
    <div className="config-actions">
      {!entries.length && <button type="button" className="btn-secondary" disabled={locked} onClick={compare}>读取升级比较</button>}
      {allowed("observe") && <button type="button" className="btn-secondary" disabled={locked} onClick={() => execute("observe")}>{labels.observe}</button>}
      {(["organization-preview", "retirement-preview"] as const).map(action => allowed(action) ? <button type="button" className="btn-secondary" key={action} disabled={locked} onClick={() => execute(action)}>{labels[action]}</button> : null)}
    </div>
    {next && allowed(next) && !pending && <div>
      <h4>{labels[next]}</h4>
      {next === "send" && <p>向冻结的下载器发送一次；未知结果仅观察，不会自动或手动重发。</p>}
      {next === "organize" && <p>执行已确认的整理计划并核验入库；旧文件继续保留。</p>}
      {next === "retire" && <p>仅对已核验新文件入库的原旧版本执行受控退休。</p>}
      <label htmlFor={`${uid}-confirm`}><input id={`${uid}-confirm`} type="checkbox" checked={ack === next} disabled={locked} onChange={e => setAck(e.target.checked ? next : null)} />我确认：{labels[next]}</label>{" "}
      <button type="button" className="btn-primary" disabled={locked || ack !== next} onClick={() => execute(next)}>{labels[next]}</button>
    </div>}
  </section>;
}

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { useAuth } from "@/features/auth/AuthContext";

import "./mp-business-panels.css";

type Configuration = components["schemas"]["SeerrConfiguration"];
type Receipt = components["schemas"]["SeerrReceipt"];
export function SeerrSettingsPanel() {
  const { user } = useAuth();
  if (!user || user.role !== "admin") return null;
  return <SeerrSettings key={user.id} accountId={user.id} />;
}
function SeerrSettings({ accountId }: { accountId: string }) {
  const [configuration, setConfiguration] = useState<Configuration | null>(null);
  const [receipts, setReceipts] = useState<Receipt[]>([]);
  const [owner, setOwner] = useState(accountId);
  const [enabled, setEnabled] = useState(false);
  const [credential, setCredential] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [readable, setReadable] = useState(false);
  const alive = useRef(false), writing = useRef(false), generation = useRef(0);
  const load = useCallback(async () => {
    const epoch = ++generation.current;
    setReadable(false);
    try {
      const [configResult, receiptResult] = await Promise.all([
        api.GET("/api/v1/settings/seerr", { cache: "no-store" }),
        api.GET("/api/v1/seerr/receipts", { cache: "no-store" }),
      ]);
      if (!alive.current || epoch !== generation.current) return;
      if (!configResult.data || configResult.error) throw new Error();
      setConfiguration(configResult.data); setEnabled(configResult.data.enabled);
      setOwner(configResult.data.owner_id ?? accountId); setReadable(true);
      setReceipts(receiptResult.data?.items ?? []);
      setMessage(receiptResult.error ? "配置已读取，接收记录读取失败，请刷新。" : "");
    } catch {
      if (alive.current && epoch === generation.current) setMessage("Seerr 配置读取失败，请刷新后再保存。");
    }
  }, [accountId]);
  useEffect(() => { alive.current = true; void load(); return () => { alive.current = false; generation.current++; }; }, [load]);
  async function save(event: FormEvent) {
    event.preventDefault();
    if (writing.current || !configuration || !readable) return;
    writing.current = true; setBusy(true); setMessage("");
    const input = { enabled, expected_version: configuration.version, owner_id: owner, ...(credential ? { credential } : {}) };
    try {
      const { data, error, response } = await api.PUT("/api/v1/settings/seerr", { body: input });
      if (!alive.current) return;
      if (!data || error) {
        setReadable(false);
        setMessage(response.status === 409 ? "配置已变化，请重新读取并核对。" : "保存尚未确认，请重新读取配置状态后再操作。");
        return;
      }
      setConfiguration(data); setMessage("Seerr 配置已保存。");
    } catch {
      if (alive.current) { setReadable(false); setMessage("保存结果尚未确认，请重新读取配置状态。不会自动重试。"); }
    } finally {
      writing.current = false;
      if (alive.current) { setBusy(false); setCredential(""); }
    }
  }
  return <section className="card seerr-settings-panel" aria-labelledby="seerr-title" aria-busy={busy}>
    <div className="card-header config-heading"><h3 id="seerr-title">Seerr 求片接入</h3>
      <button type="button" className="btn-secondary" disabled={busy} onClick={() => void load()}>重新读取配置与接收记录</button>
    </div>
    <p className="card-desc">Seerr 手动或自动批准的请求都会进入本地求片审批，批准后才创建订阅。</p>
    <p role="status">{configuration ? `${configuration.enabled ? "已启用" : "未启用"} · ${configuration.credential_present ? "已设置专用凭据" : "未设置凭据"}` : "配置状态未知"}</p>
    {message ? <p role="status">{message}</p> : null}
    <form className="form" onSubmit={save}>
      <div className="form-group"><label htmlFor="seerr-owner">本地请求所属账号 ID</label>
        <input id="seerr-owner" required value={owner} readOnly={!!configuration?.owner_id} disabled={busy || !readable} onChange={event => setOwner(event.target.value)} aria-describedby="seerr-owner-note" />
        <p id="seerr-owner-note" className="card-desc">默认归属当前账号。首次保存后固定归属；该账号停用后将拒绝接收。</p>
      </div>
      <div className="form-group"><label htmlFor="seerr-credential">专用 Webhook 凭据{configuration?.credential_present ? "（留空保留）" : " *"}</label>
        <input id="seerr-credential" type="password" autoComplete="new-password" minLength={32} maxLength={256} required={!configuration?.credential_present} value={credential} disabled={busy || !readable} onChange={event => setCredential(event.target.value)} aria-describedby="seerr-endpoint" />
        <p id="seerr-endpoint" className="card-desc">在 Seerr 的 Webhook Authorization Header 中填写相同凭据。接收地址：<code>/api/v1/integrations/seerr</code></p>
      </div>
      <label className="seerr-enabled"><input type="checkbox" checked={enabled} disabled={busy || !readable} onChange={event => setEnabled(event.target.checked)} /> 启用接收已批准的求片通知</label>
      <button type="submit" className="btn-primary" disabled={busy || !readable}>{busy ? "保存中…" : "保存 Seerr 配置"}</button>
    </form>
    <h4>最近接收记录</h4>
    {receipts.length ? <ul>{receipts.map(receipt => <li key={receipt.request_id}>Seerr #{receipt.external_request_id} · 已进入本地审批 · {new Date(receipt.created_at).toLocaleString()}</li>)}</ul> : <p className="card-desc">暂无已接收请求。</p>}
  </section>;
}

import { useEffect, useRef, useState, type FormEvent } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { useAuth } from "@/features/auth/AuthContext";
import "./account-tokens.css";

type Token = components["schemas"]["ManagedToken"];
type Create = components["schemas"]["CreateOwnTokenRequest"];
type Pending = { key: string; action: "create"; body: Create } | { key: string; action: "revoke"; id: string; name: string };
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
function readPending(storageKey: string): Pending | null {
  const raw = localStorage.getItem(storageKey);
  if (!raw) return null;
  const value: unknown = JSON.parse(raw);
  if (!value || typeof value !== "object") throw new Error("invalid recovery");
  const p = value as Record<string, unknown>;
  if (typeof p.key !== "string" || !uuid.test(p.key)) throw new Error("invalid recovery");
  if (p.action === "create" && Object.keys(p).sort().join() === "action,body,key" && p.body && typeof p.body === "object") {
    const b = p.body as Record<string, unknown>;
    if (Object.keys(b).some(k => k !== "name" && k !== "expires_at") || typeof b.name !== "string" || !b.name.trim() || new TextEncoder().encode(b.name).length > 120 || /[\u0000-\u001f\u007f]/.test(b.name) || (b.expires_at !== undefined && (typeof b.expires_at !== "string" || !Number.isFinite(Date.parse(b.expires_at))))) throw new Error("invalid recovery");
    return { key: p.key, action: "create", body: { name: b.name, ...(typeof b.expires_at === "string" ? { expires_at: b.expires_at } : {}) } };
  }
  if (p.action === "revoke" && Object.keys(p).sort().join() === "action,id,key,name" && typeof p.id === "string" && uuid.test(p.id) && typeof p.name === "string") return { key: p.key, action: "revoke", id: p.id, name: p.name };
  throw new Error("invalid recovery");
}
const date = (value?: string) => value ? new Date(value).toLocaleString("zh-CN") : "不限期";

export function AccountTokensView() {
  const { user } = useAuth();
  const userID = user?.id;
  const storageKey = `pulse.own-token-command.v1:${userID}`;
  const [items, setItems] = useState<Token[]>([]);
  const [loading, setLoading] = useState(true);
  const [readable, setReadable] = useState(false);
  const [busy, setBusy] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);
  const [name, setName] = useState("");
  const [expires, setExpires] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
  const [visible, setVisible] = useState(false);
  const [confirmID, setConfirmID] = useState<string | null>(null);
  const alive = useRef(false);
  const writing = useRef(false);
  const reader = useRef<AbortController | null>(null);
  const errorBox = useRef<HTMLDivElement>(null);
  const locked = busy || blocked || pending !== null || loading || !readable;
  function fail(text: string) { setError(text); requestAnimationFrame(() => errorBox.current?.focus()); }
  async function load() {
    reader.current?.abort();
    const ctl = new AbortController(); reader.current = ctl;
    setItems([]); setReadable(false); setLoading(true);
    try {
      const result = await api.GET("/api/v1/auth/tokens", { signal: ctl.signal, cache: "no-store" });
      if (!alive.current || ctl.signal.aborted) return;
      if (result.error || !result.data) { setSecret(null); fail("令牌列表读取失败，旧状态已清除。请重新读取。"); return; }
      setItems(result.data.items); setReadable(true);
    } catch { if (alive.current && !ctl.signal.aborted) { setSecret(null); fail("令牌列表连接失败，旧状态已清除。请重新读取。"); } }
    finally { if (reader.current === ctl) reader.current = null; if (alive.current && !ctl.signal.aborted) setLoading(false); }
  }
  useEffect(() => {
    alive.current = true;
    setSecret(null); setVisible(false); setPending(null); setBlocked(false); setError(""); setNotice("");
    const restore = () => {
      try { const p = readPending(storageKey); setPending(p); setBlocked(false); if (p) setNotice("有结果未确认的原请求，请恢复后再创建或撤销。"); }
      catch { setBlocked(true); fail("无法读取本地恢复记录。请保留记录并核对账户状态，暂不能发起新命令。"); }
    };
    restore(); void load();
    const changed = (event: StorageEvent) => { if (event.key === storageKey) { setSecret(null); restore(); void load(); } };
    window.addEventListener("storage", changed);
    return () => { alive.current = false; reader.current?.abort(); window.removeEventListener("storage", changed); };
  }, [userID]);
  async function run(cmd: Pending) {
    if (writing.current || blocked) return;
    writing.current = true; setBusy(true); setSecret(null); setVisible(false); setError(""); setNotice("");
    try {
      const original = readPending(storageKey);
      if (original && original.key !== cmd.key) { setPending(original); fail("另一个页面已有未确认请求，请先恢复原请求。"); return; }
      localStorage.setItem(storageKey, JSON.stringify(cmd)); setPending(cmd);
      const result = cmd.action === "create"
        ? await api.POST("/api/v1/auth/tokens", { params: { header: { "Idempotency-Key": cmd.key } }, body: cmd.body, cache: "no-store" })
        : await api.POST("/api/v1/auth/tokens/{id}/revoke", { params: { path: { id: cmd.id }, header: { "Idempotency-Key": cmd.key } }, cache: "no-store" });
      if (result.error || !result.data) { if (alive.current) fail("请求结果未确认。保留了原请求，请恢复；不要重复创建或撤销。"); return; }
      if (readPending(storageKey)?.key === cmd.key) localStorage.removeItem(storageKey);
      if (!alive.current) return;
      setPending(null); setConfirmID(null);
      if (cmd.action === "create") {
        // Only a non-replayed successful create may display its first secret.
        if (!result.data.replayed && result.data.secret) {
          setSecret({ name: result.data.item.name, value: result.data.secret }); setNotice("令牌已创建。请保存本次秘密，离开或刷新后不会再次显示。");
        } else setNotice("原令牌已确认，首次秘密无法恢复。若尚未保存，请撤销此令牌后重新创建。");
        setName(""); setExpires("");
      } else setNotice("撤销已确认。该令牌不能再次启用。");
      await load();
    } catch { if (alive.current) fail("连接或本地恢复记录操作失败。请恢复原请求后再继续。"); }
    finally { writing.current = false; if (alive.current) setBusy(false); }
  }
  function create(event: FormEvent) {
    event.preventDefault(); if (locked) return;
    const label = name.trim();
    if (!label || new TextEncoder().encode(label).length > 120 || /[\u0000-\u001f\u007f]/.test(label)) { fail("名称不能为空，且不得超过 120 字节或包含控制字符。"); return; }
    const time = expires ? new Date(expires) : null;
    if (time && (!Number.isFinite(time.getTime()) || time.getTime() <= Date.now())) { fail("请选择未来的到期时间。"); return; }
    void run({ key: crypto.randomUUID(), action: "create", body: { name: label, ...(time ? { expires_at: time.toISOString() } : {}) } });
  }
  async function copySecret() {
    if (!secret) return;
    try { await navigator.clipboard.writeText(secret.value); setNotice("已复制秘密。请保存在安全位置。"); }
    catch { fail("剪贴板不可用。请显示秘密并手动复制。"); }
  }
  return <section className="account-tokens" aria-labelledby="account-token-title">
    <div className="account-token-heading"><h1 id="account-token-title">API 令牌</h1><button className="btn" disabled={busy || loading} onClick={() => { setSecret(null); setVisible(false); setError(""); void load(); }}>重新读取</button></div>
    <p className="account-token-description">管理自己的只读令牌。令牌遵循当前账户权限，不能创建任务或修改配置。</p>
    {error && <div className="account-token-error" role="alert" tabIndex={-1} ref={errorBox}>{error}</div>}
    {notice && <p className="account-token-notice" role="status">{notice}</p>}
    {pending && <div className="account-token-recovery"><p>待确认：{pending.action === "create" ? `创建 ${pending.body.name}` : `撤销 ${pending.name}`}</p><button className="btn" disabled={busy || blocked} onClick={() => void run(pending)}>恢复原请求</button></div>}
    {secret && <section className="account-token-secret" aria-label="首次令牌秘密"><h2>{secret.name}：首次秘密</h2><p>此秘密只保留在当前页面内存中，离开、刷新或重新读取后清除。</p><label htmlFor="token-secret">新令牌秘密</label><input id="token-secret" type={visible ? "text" : "password"} readOnly value={secret.value} autoComplete="off" spellCheck={false} /><div className="account-token-actions"><button className="btn" onClick={() => setVisible(!visible)}>{visible ? "隐藏秘密" : "显示秘密"}</button><button className="btn" onClick={() => void copySecret()}>复制秘密</button><button className="btn" onClick={() => { setSecret(null); setVisible(false); }}>已保存，清除秘密</button></div></section>}
    <form className="account-token-form" onSubmit={create}><h2>创建只读令牌</h2><fieldset disabled={locked}><label htmlFor="token-name">名称</label><input id="token-name" value={name} onChange={e => setName(e.target.value)} required maxLength={120} autoComplete="off" placeholder="例如：媒体查询脚本" /><label htmlFor="token-expires">到期时间（本地时间，可留空）</label><input id="token-expires" type="datetime-local" value={expires} onChange={e => setExpires(e.target.value)} /><button className="btn btn-primary" type="submit">{busy ? "正在确认…" : "创建令牌"}</button></fieldset></form>
    <section aria-labelledby="token-list-title"><h2 id="token-list-title">我的令牌</h2>{loading ? <p role="status">正在读取令牌…</p> : !readable ? <p>尚未取得当前令牌列表。</p> : <><p>{items.length} 个令牌</p><ul className="account-token-list">{items.map(token => <li key={token.id}><div className="account-token-record"><h3>{token.name}</h3><p>{token.revoked_at ? "已撤销" : token.expires_at && Date.parse(token.expires_at) <= Date.now() ? "已过期" : "未撤销"} · {token.scopes.includes("read") ? "只读" : "无读取 scope"}</p><dl><div><dt>创建时间</dt><dd>{date(token.created_at)}</dd></div><div><dt>到期时间</dt><dd>{date(token.expires_at)}</dd></div>{token.revoked_at && <div><dt>撤销时间</dt><dd>{date(token.revoked_at)}</dd></div>}</dl></div>{!token.revoked_at && <div className="account-token-revoke">{confirmID === token.id ? <><p>撤销后不能恢复，确认撤销“{token.name}”？</p><div className="account-token-actions"><button className="btn" disabled={locked} onClick={() => void run({ key: crypto.randomUUID(), action: "revoke", id: token.id, name: token.name })}>确认撤销</button><button className="btn" disabled={busy} onClick={() => setConfirmID(null)}>取消</button></div></> : <button className="btn" disabled={locked} onClick={() => { setSecret(null); setConfirmID(token.id); }}>撤销 {token.name}</button>}</div>}</li>)}</ul>{!items.length && !error && <p>尚无令牌。需要只读 API 访问时可在上方创建。</p>}</>}</section>
  </section>;
}

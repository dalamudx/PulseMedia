import { useEffect, useRef, useState, type FormEvent } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { useAuth } from "./AuthContext";
import { useAccountSecurityCommand } from "./useAccountSecurityCommand";
import "@/views/account-tokens.css";
import "./account-security.css";

type Account = components["schemas"]["ManagedAccount"];
export function AccountManagementPanel({ isAdmin }: { isAdmin?: boolean }) {
  const { user } = useAuth();
  if (user?.role !== "admin" || isAdmin === false) return null;
  return <MemberManagement key={user.id} />;
}
function MemberManagement() {
  const { user, checkStatus } = useAuth();
  const command = useAccountSecurityCommand("members");
  const [items, setItems] = useState<Account[]>([]);
  const [loading, setLoading] = useState(true);
  const [readable, setReadable] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"admin" | "member">("member");
  const [editing, setEditing] = useState<{ id: string; role: "admin" | "member"; is_active: boolean } | null>(null);
  const [resetTarget, setResetTarget] = useState<Account | null>(null);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [factorMode, setFactorMode] = useState<"otp" | "recovery_code">("otp");
  const [factor, setFactor] = useState("");
  const [readError, setReadError] = useState("");
  const reader = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  function clearReset() { setCurrentPassword(""); setNewPassword(""); setFactor(""); setFactorMode("otp"); }
  function closeReset() { clearReset(); setResetTarget(null); }
  async function load() {
    reader.current?.abort(); const ctl = new AbortController(); reader.current = ctl;
    setLoading(true); setReadable(false); setItems([]); setReadError("");
    try {
      const r = await api.GET("/api/v1/accounts", { signal: ctl.signal, cache: "no-store" });
      if (ctl.signal.aborted) return;
      if (r.error || !r.data) { setReadError("账户列表读取失败，请重新读取后再操作。"); return; }
      setItems(r.data.items); setReadable(true);
    } catch { if (!ctl.signal.aborted) setReadError("账户列表连接失败，请重新读取。"); }
    finally { if (!ctl.signal.aborted) setLoading(false); }
  }
  useEffect(() => { mounted.current = true; void load(); return () => { mounted.current = false; reader.current?.abort(); }; }, []);
  const locked = command.busy || command.blocked || loading || !readable;
  const allowed = (action: string) => !command.pending || command.pending.action === action;
  const eligible = (account: Account) => account.role === "member" && account.id !== user?.id;
  const resetAction = resetTarget ? `account.member_password.reset:${resetTarget.id}` : "";
  const currentTarget = items.find(account => account.id === resetTarget?.id);
  async function create(e: FormEvent) {
    e.preventDefault();
    if (locked || !allowed("account.create")) return;
    const body = { username, password, role };
    const resultPromise = command.run("account.create", key => api.POST("/api/v1/accounts", { params: { header: { "Idempotency-Key": key } }, body, cache: "no-store" }));
    setPassword("");
    const result = await resultPromise;
    if (result && mounted.current) { setUsername(""); await load(); }
  }
  async function update(e: FormEvent) {
    e.preventDefault(); if (!editing || locked || !allowed(`account.update:${editing.id}`)) return;
    const target = editing;
    const result = await command.run(`account.update:${target.id}`, key => api.PUT("/api/v1/accounts/{id}", { params: { path: { id: target.id }, header: { "Idempotency-Key": key } }, body: { role: target.role, is_active: target.is_active }, cache: "no-store" }));
    if (result && mounted.current) { setEditing(null); if (target.id === user?.id) { await checkStatus(); return; } await load(); }
  }
  async function resetPassword(e: FormEvent) {
    e.preventDefault();
    if (locked || !currentTarget || !eligible(currentTarget) || !allowed(resetAction) || !currentPassword || newPassword.length < 8) return;
    const target = currentTarget;
    const body: components["schemas"]["ChangeOwnPasswordRequest"] = { current_password: currentPassword, new_password: newPassword, ...(factor ? { [factorMode]: factor } : {}) };
    const resultPromise = command.run(`account.member_password.reset:${target.id}`, key => api.POST("/api/v1/accounts/{id}/password", { params: { path: { id: target.id }, header: { "Idempotency-Key": key } }, body, cache: "no-store" }));
    // The request owns its captured body; neither the form nor the journal retains secrets.
    clearReset();
    const result = await resultPromise;
    if (result && mounted.current) { closeReset(); command.setNotice("成员密码已重置；该成员的全部会话与 API 令牌已撤销。双因素与停用状态保持不变，待确认的双因素绑定已清除。"); await load(); }
  }
  return <section className="account-tokens account-security" aria-labelledby="account-members-title">
    <div className="account-token-heading"><h2 id="account-members-title">家庭账户</h2><button className="btn" disabled={command.busy || loading} onClick={() => { closeReset(); void load(); }}>重新读取账户</button></div>
    <p>管理员可创建、停用和调整成员角色。停用或更改角色会撤销该账户的现有会话与 API 令牌。</p>
    {(command.error || readError) && <p role="alert">{command.error || readError}</p>}
    {command.notice && <p role="status">{command.notice}</p>}
    {command.pending && <div className="account-token-recovery"><p>有待确认的账户操作。只保留原命令键，不保存密码。结果确认前不能进行其它操作；重新提交须重新填写原输入并显式使用原键。</p><button className="btn" disabled={command.busy} onClick={async () => { clearReset(); if (await command.recover() && mounted.current) { setPassword(""); closeReset(); setEditing(null); await load(); } }}>读取原命令回执</button></div>}
    <form className="account-token-form" onSubmit={create}><h3>创建成员</h3><fieldset disabled={locked || !allowed("account.create")}>
      <label htmlFor="member-name">用户名</label><input id="member-name" value={username} onChange={e => setUsername(e.target.value)} minLength={3} maxLength={80} required autoComplete="off" />
      <label htmlFor="member-password">初始密码</label><input id="member-password" type="password" value={password} onChange={e => setPassword(e.target.value)} minLength={8} maxLength={1024} required autoComplete="new-password" />
      <label htmlFor="member-role">角色</label><select id="member-role" value={role} onChange={e => setRole(e.target.value as "admin" | "member")}><option value="member">成员</option><option value="admin">管理员</option></select>
      <button className="btn btn-primary" type="submit">{command.pending?.action === "account.create" ? "使用原键提交创建" : "创建账户"}</button>
    </fieldset></form>
    {loading ? <p role="status">正在读取账户…</p> : readable ? <ul className="account-token-list">{items.map(account => <li key={account.id}><div className="account-token-record"><h3>{account.username}{account.id === user?.id ? "（本人）" : ""}</h3><p>{account.role === "admin" ? "管理员" : "成员"} · {account.is_active ? "活动" : "已停用"}</p></div><div className="account-token-actions"><button className="btn" disabled={locked || !allowed(`account.update:${account.id}`)} onClick={() => { closeReset(); setEditing({ id: account.id, role: account.role, is_active: account.is_active }); }}>管理 {account.username}</button>{eligible(account) && <button className="btn" disabled={locked || !allowed(`account.member_password.reset:${account.id}`)} onClick={() => { clearReset(); setPassword(""); setEditing(null); setResetTarget(account); }}>重置密码 {account.username}</button>}</div></li>)}</ul> : <p>尚未取得当前账户列表。</p>}
    {editing && <form className="account-token-form" onSubmit={update}><h3>调整 {items.find(a => a.id === editing.id)?.username}</h3><fieldset disabled={locked || !allowed(`account.update:${editing.id}`)}>
      <label htmlFor="edit-member-role">角色</label><select id="edit-member-role" value={editing.role} onChange={e => setEditing({ ...editing, role: e.target.value as "admin" | "member" })}><option value="member">成员</option><option value="admin">管理员</option></select>
      <label className="account-security-check"><input type="checkbox" checked={editing.is_active} onChange={e => setEditing({ ...editing, is_active: e.target.checked })} />允许账户登录</label>
      <p>保存后，该账户需要重新登录。必须保留至少一位活动管理员。</p><div className="account-token-actions"><button className="btn btn-primary" type="submit">确认保存账户</button><button className="btn" type="button" onClick={() => setEditing(null)}>取消</button></div>
    </fieldset></form>}
    {resetTarget && <form className="account-token-form" onSubmit={resetPassword}><h3>重置成员密码：{resetTarget.username}</h3><p>将撤销该成员的全部会话与 API 令牌，并清除待确认的双因素绑定。已启用的双因素保持不变；已停用的成员不会因此启用。</p><fieldset disabled={locked || !allowed(resetAction) || !currentTarget || !eligible(currentTarget)}>
      <label htmlFor="member-reset-current">管理员本人的当前密码</label><input id="member-reset-current" style={{ minHeight: 44 }} type="password" autoComplete="current-password" required maxLength={1024} value={currentPassword} onChange={e => setCurrentPassword(e.target.value)} />
      <label htmlFor="member-reset-new">成员的新密码（至少八个字符）</label><input id="member-reset-new" style={{ minHeight: 44 }} type="password" autoComplete="new-password" required minLength={8} maxLength={1024} value={newPassword} onChange={e => setNewPassword(e.target.value)} />
      <p>若管理员本人已启用双因素，须填写本人的验证码或一次性恢复码。不要填写成员的第二因素。</p>
      <label htmlFor="member-reset-mode">管理员本人的第二因素</label><select id="member-reset-mode" value={factorMode} onChange={e => { setFactorMode(e.target.value as "otp" | "recovery_code"); setFactor(""); }}><option value="otp">验证器验证码</option><option value="recovery_code">一次性恢复码</option></select>
      <label htmlFor="member-reset-factor">{factorMode === "otp" ? "管理员的六位验证码（已启用时必填）" : "管理员的一次性恢复码"}</label><input id="member-reset-factor" style={{ minHeight: 44 }} type={factorMode === "otp" ? "text" : "password"} inputMode={factorMode === "otp" ? "numeric" : "text"} autoComplete="one-time-code" spellCheck={false} minLength={factorMode === "otp" ? 6 : undefined} maxLength={factorMode === "otp" ? 6 : 64} value={factor} onChange={e => setFactor(e.target.value)} />
      <button className="btn btn-primary" type="submit">{command.pending?.action === resetAction ? "使用原键提交密码重置" : "确认重置成员密码"}</button>
    </fieldset><button className="btn" type="button" disabled={command.busy} onClick={closeReset}>取消密码重置</button></form>}
  </section>;
}

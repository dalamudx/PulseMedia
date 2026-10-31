import { useEffect, useRef, useState, type FormEvent } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { useAuth } from "./AuthContext";
import { useAccountSecurityCommand } from "./useAccountSecurityCommand";
import { MFAEnrollmentSecret } from "./MFAEnrollmentSecret";
import "@/views/account-tokens.css";
import "./account-security.css";

type Session = components["schemas"]["ManagedSession"];
type Status = components["schemas"]["OwnMFAStatus"];
type Setup = components["schemas"]["OwnMFASetupResult"];
type Proof = { mode: "otp" | "recovery_code"; value: string };
const emptyProof: Proof = { mode: "otp", value: "" };
const factorBody = (p: Proof) => p.value ? { [p.mode]: p.value } : {};
function ProofFields({ prefix, proof, change }: { prefix: string; proof: Proof; change: (value: Proof) => void }) {
  return <><label htmlFor={`${prefix}-factor-mode`}>第二因素</label><select id={`${prefix}-factor-mode`} value={proof.mode} onChange={e => change({ mode: e.target.value as Proof["mode"], value: "" })}><option value="otp">验证器验证码</option><option value="recovery_code">一次性恢复码</option></select><label htmlFor={`${prefix}-factor`}>{proof.mode === "otp" ? "六位验证码" : "一次性恢复码"}</label><input id={`${prefix}-factor`} type={proof.mode === "otp" ? "text" : "password"} inputMode={proof.mode === "otp" ? "numeric" : "text"} value={proof.value} onChange={e => change({ ...proof, value: e.target.value })} maxLength={proof.mode === "otp" ? 6 : 64} minLength={proof.mode === "otp" ? 6 : undefined} autoComplete="one-time-code" required spellCheck={false} /></>;
}
export function AccountSecurityPanel() {
  const { user, checkStatus } = useAuth();
  const command = useAccountSecurityCommand("security");
  const [sessions, setSessions] = useState<Session[]>([]);
  const [status, setStatus] = useState<Status | null>(null);
  const [loading, setLoading] = useState(true);
  const [readError, setReadError] = useState("");
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [passwordProof, setPasswordProof] = useState<Proof>(emptyProof);
  const [mfaPassword, setMFAPassword] = useState("");
  const [disableProof, setDisableProof] = useState<Proof>(emptyProof);
  const [refreshPassword, setRefreshPassword] = useState("");
  const [refreshProof, setRefreshProof] = useState<Proof>(emptyProof);
  const [refreshConfirmed, setRefreshConfirmed] = useState(false);
  const [setup, setSetup] = useState<Setup | null>(null);
  const [confirmOTP, setConfirmOTP] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [showCodes, setShowCodes] = useState(false);
  const [disableConfirm, setDisableConfirm] = useState(false);
  const [revokeID, setRevokeID] = useState<string | null>(null);
  const [otherSessionsOpen, setOtherSessionsOpen] = useState(false);
  const [otherSessionsPassword, setOtherSessionsPassword] = useState("");
  const [otherSessionsProof, setOtherSessionsProof] = useState<Proof>(emptyProof);
  const [otherSessionsConfirmed, setOtherSessionsConfirmed] = useState(false);
  const reader = useRef<AbortController | null>(null);
  function clearSecrets() { setCurrentPassword(""); setNewPassword(""); setPasswordProof(emptyProof); setMFAPassword(""); setDisableProof(emptyProof); setConfirmOTP(""); setRefreshPassword(""); setRefreshProof(emptyProof); setRefreshConfirmed(false); setOtherSessionsPassword(""); setOtherSessionsProof(emptyProof); setOtherSessionsConfirmed(false); }
  async function load() {
    reader.current?.abort(); const ctl = new AbortController(); reader.current = ctl;
    setLoading(true); setStatus(null); setSessions([]); setReadError("");
    try {
      const [mfa, session] = await Promise.all([api.GET("/api/v1/auth/mfa", { signal: ctl.signal, cache: "no-store" }), api.GET("/api/v1/auth/sessions", { signal: ctl.signal, cache: "no-store" })]);
      if (ctl.signal.aborted) return;
      if (mfa.error || !mfa.data || session.error || !session.data) { setReadError("安全状态读取失败。旧状态已清除，请重新读取后操作。"); return; }
      setStatus(mfa.data); setSessions(session.data.items);
    } catch { if (!ctl.signal.aborted) setReadError("安全状态连接失败，请重新读取。"); }
    finally { if (!ctl.signal.aborted) setLoading(false); }
  }
  useEffect(() => { clearSecrets(); setSetup(null); setRecoveryCodes([]); setShowCodes(false); setDisableConfirm(false); setRevokeID(null); setOtherSessionsOpen(false); void load(); return () => reader.current?.abort(); }, [user?.id]);
  const locked = command.busy || command.blocked || loading || !status;
  const allowed = (action: string) => !command.pending || command.pending.action === action;
  const regenerationAllowed = !command.pending || (command.pending.action === "account.mfa.recovery.regenerate" && command.canRetryOriginal);
  const otherSessionsPending = command.pending?.action === "account.sessions.revoke_others";
  const otherSessionsAllowed = !command.pending || (otherSessionsPending && command.canRetryOriginal);
  const pendingChallenge = command.pending?.action.startsWith("account.mfa.confirm:") ? command.pending.action.slice("account.mfa.confirm:".length) : null;
  const challenge = setup?.challenge_id ?? pendingChallenge;
  const confirmAction = `account.mfa.confirm:${challenge}`;
  async function changePassword(e: FormEvent) {
    e.preventDefault();
    const result = await command.run("account.password.change", key => api.POST("/api/v1/auth/password", { params: { header: { "Idempotency-Key": key } }, body: { current_password: currentPassword, new_password: newPassword, ...factorBody(passwordProof) }, cache: "no-store" }));
    if (result) { clearSecrets(); setSetup(null); await load(); command.setNotice("密码已更新，其它会话和 API 令牌已撤销。当前会话保留。"); }
  }
  async function begin(e: FormEvent) {
    e.preventDefault(); setSetup(null); setRecoveryCodes([]);
    const result = await command.run("account.mfa.setup", key => api.POST("/api/v1/auth/mfa/setup", { params: { header: { "Idempotency-Key": key } }, body: { current_password: mfaPassword }, cache: "no-store" }), !!pendingChallenge);
    if (result) { setMFAPassword(""); setSetup(result); if (!result.secret) command.setNotice("原绑定请求已确认，但密钥不能再次读取。请重新发起绑定。"); }
  }
  async function confirm(e: FormEvent) {
    e.preventDefault(); if (!challenge) return;
    const result = await command.run(confirmAction, key => api.POST("/api/v1/auth/mfa/confirm", { params: { header: { "Idempotency-Key": key } }, body: { challenge_id: challenge, otp: confirmOTP }, cache: "no-store" }));
    if (result) { setSetup(null); clearSecrets(); setRecoveryCodes(result.recovery_codes ?? []); setShowCodes(false); await load(); command.setNotice(result.recovery_codes?.length ? "双因素已启用。请立即安全保存恢复码；每个恢复码只能使用一次，其它会话和 API 令牌已撤销。" : "双因素已启用，恢复码不能再次显示。请使用当前密码和有效的验证器验证码或未使用的恢复码重新生成。"); }
  }
  async function regenerate(e: FormEvent) {
    e.preventDefault();
    if (locked || !regenerationAllowed || !status?.enabled || !refreshConfirmed || !refreshPassword || !refreshProof.value) return;
    const body = { current_password: refreshPassword, ...factorBody(refreshProof) };
    clearSecrets(); setRecoveryCodes([]); setShowCodes(false); setSetup(null);
    const result = await command.run("account.mfa.recovery.regenerate", key => api.POST("/api/v1/auth/mfa/recovery-codes", { params: { header: { "Idempotency-Key": key } }, body, cache: "no-store" }));
    if (result) {
      setRecoveryCodes(result.replayed ? [] : result.recovery_codes ?? []);
      await load();
      command.setNotice(!result.replayed && result.recovery_codes?.length ? "恢复码已更新，所有旧恢复码已失效，其它会话和 API 令牌已撤销，当前会话保留，验证器绑定不变。请立即安全保存新恢复码。" : "恢复码更新已确认，但新恢复码不能再次显示。若首次响应丢失，请使用当前密码和有效的验证器验证码或未使用的新恢复码重新生成。");
    }
  }
  async function disable(e: FormEvent) {
    e.preventDefault();
    const result = await command.run("account.mfa.disable", key => api.POST("/api/v1/auth/mfa/disable", { params: { header: { "Idempotency-Key": key } }, body: { current_password: mfaPassword, ...factorBody(disableProof) }, cache: "no-store" }));
    if (result) { clearSecrets(); setRecoveryCodes([]); setDisableConfirm(false); await load(); command.setNotice("双因素已停用，旧恢复码失效，其它会话和 API 令牌已撤销。"); }
  }
  async function revokeOthers(e: FormEvent) {
    e.preventDefault();
    if (locked || !otherSessionsAllowed || !otherSessionsConfirmed || !otherSessionsPassword || (status?.enabled && !otherSessionsProof.value)) return;
    const body = { current_password: otherSessionsPassword, ...(status?.enabled ? factorBody(otherSessionsProof) : {}) };
    clearSecrets(); setSetup(null); setRecoveryCodes([]); setShowCodes(false);
    const result = await command.run("account.sessions.revoke_others", key => api.POST("/api/v1/auth/sessions/revoke-others", { params: { header: { "Idempotency-Key": key } }, body, cache: "no-store" }));
    if (result) {
      setOtherSessionsOpen(false); await load();
      command.setNotice("原撤销其它会话操作已确认，保留当前会话和全部 API 令牌。此结果不表示之后新登录的会话已被撤销；请查看重新读取的会话列表。");
    }
  }
  async function revoke(id: string) {
    const current = sessions.find(s => s.id === id)?.current;
    const result = await command.run(`account.session.revoke:${id}`, key => api.POST("/api/v1/auth/sessions/{id}/revoke", { params: { path: { id }, header: { "Idempotency-Key": key } }, cache: "no-store" }));
    if (result) { setRevokeID(null); if (current) { await checkStatus(); return; } await load(); }
  }
  return <section className="account-tokens account-security" aria-labelledby="account-security-title">
    <div className="account-token-heading"><h2 id="account-security-title">账户安全</h2><button className="btn" disabled={command.busy || loading} onClick={() => { setSetup(null); setRecoveryCodes([]); clearSecrets(); void load(); }}>重新读取安全状态</button></div>
    {(command.error || readError) && <p role="alert">{command.error || readError}</p>}{command.notice && <p role="status">{command.notice}</p>}
    {command.pending && <div className="account-token-recovery"><p>有结果待确认的安全操作。只保留原命令键；密码、验证码和恢复码不会写入本地存储。</p><button className="btn" disabled={command.busy} onClick={async () => { clearSecrets(); if (await command.recover()) { clearSecrets(); setSetup(null); setRecoveryCodes([]); await load(); } }}>读取原安全命令回执</button></div>}
    <form className="account-token-form" onSubmit={changePassword}><h3>修改密码</h3><fieldset disabled={locked || !allowed("account.password.change")}>
      <label htmlFor="account-current-password">当前密码</label><input id="account-current-password" type="password" autoComplete="current-password" maxLength={1024} required value={currentPassword} onChange={e => setCurrentPassword(e.target.value)} />
      <label htmlFor="account-new-password">新密码（至少八个字符）</label><input id="account-new-password" type="password" autoComplete="new-password" minLength={8} maxLength={1024} required value={newPassword} onChange={e => setNewPassword(e.target.value)} />
      {status?.enabled && <ProofFields prefix="password" proof={passwordProof} change={setPasswordProof} />}<p>更新密码会撤销其它会话和所有 API 令牌，保留当前会话。</p><button type="submit" className="btn btn-primary">{command.pending?.action === "account.password.change" ? "使用原键提交改密" : "确认修改密码"}</button>
    </fieldset></form>
    <section className="account-token-form" aria-labelledby="account-mfa-title"><h3 id="account-mfa-title">双因素认证</h3>{loading ? <p role="status">正在读取双因素状态…</p> : status ? <p>{status.enabled ? `已启用 · 剩余 ${status.recovery_codes_remaining} 个一次性恢复码` : "未启用。绑定验证器并确认六位验证码后生效。"}</p> : <p>状态未知，请重新读取。</p>}
      {status && !status.enabled && <form onSubmit={begin}><fieldset disabled={locked || (!allowed("account.mfa.setup") && !pendingChallenge)}><label htmlFor="mfa-setup-password">当前密码（绑定验证）</label><input id="mfa-setup-password" type="password" autoComplete="current-password" required maxLength={1024} value={mfaPassword} onChange={e => setMFAPassword(e.target.value)} /><button className="btn" type="submit">{command.pending?.action === "account.mfa.setup" ? "使用原键提交绑定" : pendingChallenge ? "使用原键重新绑定" : "开始绑定验证器"}</button></fieldset></form>}
      {challenge && !status?.enabled && <form className="account-token-secret" onSubmit={confirm}><h3>{setup?.secret ? "在验证器中添加 PulseMedia" : "继续确认原绑定"}</h3>{setup?.secret ? <MFAEnrollmentSecret key={`${user?.id}:${setup.challenge_id}`} setup={setup} /> : <p>输入原验证器的验证码，使用原命令键继续确认。若原绑定已过期或密钥丢失，可使用原键重新绑定；已成功的原确认只能读取回执。</p>}<fieldset disabled={locked || !allowed(confirmAction)}><label htmlFor="mfa-confirm-otp">验证器中的六位验证码</label><input id="mfa-confirm-otp" inputMode="numeric" minLength={6} maxLength={6} required autoComplete="one-time-code" value={confirmOTP} onChange={e => setConfirmOTP(e.target.value)} /><button className="btn btn-primary" type="submit">确认启用双因素</button></fieldset></form>}
      {status?.enabled && <form onSubmit={regenerate}><fieldset disabled={locked || !regenerationAllowed}>
        <label htmlFor="recovery-refresh-password">当前密码（更新恢复码）</label><input id="recovery-refresh-password" type="password" autoComplete="current-password" required maxLength={1024} value={refreshPassword} onChange={e => setRefreshPassword(e.target.value)} />
        <ProofFields prefix="recovery-refresh" proof={refreshProof} change={setRefreshProof} />
        <p>重新生成后，所有旧恢复码将全部失效，其它登录会话和所有 API 令牌将撤销；当前会话保留，TOTP 验证器绑定不变。</p>
        <label className="account-security-check"><input type="checkbox" required checked={refreshConfirmed} onChange={e => setRefreshConfirmed(e.target.checked)} />我已知晓旧恢复码将全部失效</label>
        <button className="btn" type="submit" disabled={!refreshConfirmed}>{command.pending?.action === "account.mfa.recovery.regenerate" && command.canRetryOriginal ? "使用原键提交恢复码更新" : "重新生成恢复码"}</button>
      </fieldset></form>}
      {recoveryCodes.length > 0 && <div className="account-token-secret"><h3>保存一次性恢复码</h3><p>恢复码只显示这一次。请保存在密码管理器或离线安全位置，与账户密码分开。</p>{showCodes ? <ol className="account-security-recovery-codes">{recoveryCodes.map(code => <li key={code}>{code}</li>)}</ol> : <p>已生成 {recoveryCodes.length} 个恢复码，当前隐藏。</p>}<div className="account-token-actions"><button className="btn" onClick={() => setShowCodes(!showCodes)}>{showCodes ? "隐藏恢复码" : "显示恢复码"}</button><button className="btn" onClick={async () => { try { await navigator.clipboard.writeText(recoveryCodes.join("\n")); command.setNotice("恢复码已复制，请保存在安全位置。"); } catch { command.setError("无法使用剪贴板，请显示恢复码并手工保存。"); } }}>复制恢复码</button><button className="btn" onClick={() => { setRecoveryCodes([]); setShowCodes(false); }}>已安全保存，清除恢复码</button></div></div>}
      {status?.enabled && (!disableConfirm ? <button className="btn" disabled={locked || !allowed("account.mfa.disable")} onClick={() => { setDisableConfirm(true); setRecoveryCodes([]); }}>停用双因素…</button> : <form onSubmit={disable}><fieldset disabled={locked || !allowed("account.mfa.disable")}><p>停用后只需密码即可登录。必须同时验证当前密码和验证码或一个未使用的恢复码。</p><label htmlFor="mfa-disable-password">当前密码（停用验证）</label><input id="mfa-disable-password" type="password" autoComplete="current-password" required maxLength={1024} value={mfaPassword} onChange={e => setMFAPassword(e.target.value)} /><ProofFields prefix="disable" proof={disableProof} change={setDisableProof} /><div className="account-token-actions"><button className="btn" type="submit">确认停用双因素</button><button className="btn" type="button" onClick={() => { setDisableConfirm(false); setMFAPassword(""); setDisableProof(emptyProof); }}>取消</button></div></fieldset></form>)}
    </section>
    <section aria-labelledby="account-sessions-title"><h3 id="account-sessions-title">我的登录会话</h3>
      <p>撤销其它会话会保留当前会话和全部 API 令牌。</p>
      {!otherSessionsOpen && !otherSessionsPending ? <button className="btn" disabled={locked || !otherSessionsAllowed} onClick={() => { clearSecrets(); setOtherSessionsOpen(true); }}>撤销其它会话…</button> : <form className="account-token-form" aria-label="撤销其它会话" onSubmit={revokeOthers}><fieldset disabled={locked || !otherSessionsAllowed}>
        <label htmlFor="other-sessions-password">当前密码（撤销其它会话）</label><input id="other-sessions-password" type="password" autoComplete="current-password" required maxLength={1024} value={otherSessionsPassword} onChange={e => setOtherSessionsPassword(e.target.value)} />
        {status?.enabled && <ProofFields prefix="other-sessions" proof={otherSessionsProof} change={setOtherSessionsProof} />}
        <p>仅撤销这次操作执行时的其它登录会话；原键重试不会撤销之后新登录的会话。</p>
        <label className="account-security-check"><input id="other-sessions-confirm" type="checkbox" required checked={otherSessionsConfirmed} onChange={e => setOtherSessionsConfirmed(e.target.checked)} />我确认撤销其它会话，保留当前会话和全部 API 令牌</label>
        <button className="btn" type="submit" disabled={!otherSessionsConfirmed}>{otherSessionsPending ? "使用原键撤销其它会话" : "确认撤销其它会话"}</button>
      </fieldset><button className="btn" type="button" disabled={command.busy} onClick={() => { clearSecrets(); setOtherSessionsOpen(false); }}>取消撤销其它会话</button></form>}
      {loading ? <p role="status">正在读取会话…</p> : status ? <ul className="account-token-list">{sessions.map(session => <li key={session.id}><div className="account-token-record"><h3>{session.current ? "当前会话" : "其它会话"}</h3><dl><div><dt>创建时间</dt><dd>{new Date(session.created_at).toLocaleString("zh-CN")}</dd></div><div><dt>最后使用</dt><dd>{new Date(session.last_seen_at).toLocaleString("zh-CN")}</dd></div><div><dt>到期时间</dt><dd>{new Date(session.expires_at).toLocaleString("zh-CN")}</dd></div></dl></div><div>{revokeID === session.id ? <><p>{session.current ? "撤销当前会话后需要重新登录，确认继续？" : "确认撤销这个会话？"}</p><div className="account-token-actions"><button className="btn" disabled={locked || !allowed(`account.session.revoke:${session.id}`)} onClick={() => void revoke(session.id)}>确认撤销会话</button><button className="btn" disabled={command.busy} onClick={() => setRevokeID(null)}>取消</button></div></> : <button className="btn" disabled={locked || !allowed(`account.session.revoke:${session.id}`)} onClick={() => setRevokeID(session.id)}>{session.current ? "撤销当前会话" : "撤销会话"}</button>}</div></li>)}</ul> : <p>会话状态未知，请重新读取。</p>}</section>
  </section>;
}

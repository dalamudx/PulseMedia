import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "./AuthContext";

type Pending = { key: string; action: string };
type Reply<T> = { data?: T; error?: unknown; response: Response };
const keyPattern = /^[0-9a-f-]{36}$/i;
const requiresReceiptFirst = (action: string) => action === "account.mfa.recovery.regenerate" || action === "account.sessions.revoke_others";
function read(key: string): Pending | null {
  const raw = localStorage.getItem(key); if (!raw) return null;
  const p: unknown = JSON.parse(raw);
  if (!p || typeof p !== "object") throw new Error("invalid command metadata");
  const v = p as Record<string, unknown>;
  if (Object.keys(v).sort().join() !== "action,key" || typeof v.key !== "string" || !keyPattern.test(v.key) || typeof v.action !== "string" || !v.action || v.action.length > 100) throw new Error("invalid command metadata");
  return { key: v.key, action: v.action };
}
// Only original keys and action names survive navigation. Credentials stay in form memory.
export function useAccountSecurityCommand(scope: string) {
  const { user } = useAuth();
  const storageKey = `pulse.account-security.v1:${user?.id}:${scope}`;
  const [pending, setPending] = useState<Pending | null>(null);
  const [canRetryOriginal, setCanRetryOriginal] = useState(false);
  const retryOriginal = useRef<string | null>(null);
  const journalVersion = useRef(0);
  function resetRetry() { retryOriginal.current = null; setCanRetryOriginal(false); }
  const [busy, setBusy] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const epoch = useRef(0);
  const writing = useRef(false);
  useEffect(() => {
    resetRetry(); ++epoch.current; writing.current = false; setBusy(false); setPending(null); setBlocked(false); setError(""); setNotice("");
    const restore = () => { resetRetry(); ++journalVersion.current; try { setPending(read(storageKey)); } catch { setBlocked(true); setError("无法读取原命令记录。请保留记录并核对账户状态，暂不能发起新操作。"); } };
    restore(); const changed = (e: StorageEvent) => { if (e.key === storageKey || e.key === null) restore(); };
    window.addEventListener("storage", changed);
    return () => { ++epoch.current; window.removeEventListener("storage", changed); };
  }, [storageKey]);
  function clear(p: Pending) { resetRetry(); if (read(storageKey)?.key === p.key) localStorage.removeItem(storageKey); setPending(null); }
  async function run<T>(action: string, send: (key: string) => Promise<Reply<T>>, reusePendingConfirmationKey = false): Promise<T | undefined> {
    if (writing.current || blocked || !user) return;
    const version = epoch.current;
    let submitted = false;
    writing.current = true; setBusy(true); setError(""); setNotice("");
    try {
      const original = read(storageKey);
      const retry = original && requiresReceiptFirst(original.action) && action === original.action && retryOriginal.current === `${storageKey}:${original.key}`;
      resetRetry();
      if (original && requiresReceiptFirst(original.action) && !retry) { setPending(original); setError(original.action === "account.sessions.revoke_others" ? "撤销其它会话结果待确认，请只读取原安全命令回执。" : "恢复码更新结果待确认，请只读取原安全命令回执。"); return; }
      const replacesConfirmation = reusePendingConfirmationKey && action === "account.mfa.setup" && original?.action.startsWith("account.mfa.confirm:");
      if (original && original.action !== action && !replacesConfirmation) { setPending(original); setError("请先读取原命令回执；结果未确认前不能进行其它操作。"); return; }
      const p = { key: original?.key ?? crypto.randomUUID(), action };
      localStorage.setItem(storageKey, JSON.stringify(p)); setPending(p);
      submitted = true;
      const result = await send(p.key);
      if (version !== epoch.current) return;
      const current = read(storageKey);
      if (current?.key !== p.key || current.action !== p.action) { resetRetry(); setPending(current); return; }
      if (result.error || !result.data) {
        const code = result.error && typeof result.error === "object" && "code" in result.error ? result.error.code : undefined;
        if ([400, 401, 403, 404].includes(result.response.status) || code === "LAST_ACTIVE_ADMIN" || (!original && requiresReceiptFirst(action) && result.response.status === 429 && code === "AUTH_RATE_LIMITED")) clear(p);
        setError(code === "LAST_ACTIVE_ADMIN" ? "必须保留至少一位活动管理员。请先启用或提升另一位管理员。" : code === "REAUTHENTICATION_FAILED" ? "当前密码或第二因素不正确。验证码不可重复使用，请核对后重试。" : action === "account.sessions.revoke_others" ? "撤销其它会话未确认。若原命令仍待确认，请只读取原安全命令回执。" : action === "account.mfa.recovery.regenerate" ? "恢复码更新未确认。若原命令仍待确认，请只读取原安全命令回执。" : "操作未完成。若原命令仍待确认，请读取原回执；也可重新填写原输入，显式使用原键提交。");
        return;
      }
      clear(p); setNotice("操作已确认。"); return result.data;
    } catch { if (version === epoch.current) { if (!submitted) { setBlocked(true); setError("无法保存原命令记录，操作尚未发送。请检查本地存储后重新打开此页面。"); } else setError("连接中断，结果尚未确认。原命令键已保留；请读取原回执，不要另建重复请求。"); } }
    finally { if (version === epoch.current) { writing.current = false; setBusy(false); } }
  }
  async function recover(): Promise<boolean> {
    if (writing.current || blocked) return false;
    resetRetry(); writing.current = true; setBusy(true); const version = epoch.current, journal = journalVersion.current; setError("");
    try {
      const p = read(storageKey); if (!p) { setPending(null); return true; }
      const result = await api.GET("/api/v1/auth/security/commands/{command_key}", { params: { path: { command_key: p.key } }, cache: "no-store" });
      if (version !== epoch.current) return false;
      const current = read(storageKey);
      if (journal !== journalVersion.current || current?.key !== p.key || current.action !== p.action) { setPending(current); return false; }
      if (requiresReceiptFirst(p.action) && result.response.status === 404) {
        retryOriginal.current = `${storageKey}:${p.key}`; setCanRetryOriginal(true);
        setError(p.action === "account.sessions.revoke_others" ? "尚无原撤销其它会话回执。请重新填写当前密码及已启用的第二因素并确认后，显式使用原键提交；也可继续读取原回执。" : "尚无原恢复码更新回执。请重新填写当前密码、有效第二因素并确认后，显式使用原键提交；也可继续读取原回执。"); return false;
      }
      if (result.error || !result.data) { setError(p.action === "account.sessions.revoke_others" ? "尚未读取到撤销其它会话回执。原命令键仍保留，请继续读取；结果确认前不能重新提交。" : p.action === "account.mfa.recovery.regenerate" ? "尚未读取到恢复码更新回执。原命令键仍保留，请稍后继续读取；结果确认前不能重新提交。" : "尚未读取到原回执。保留了原命令键；请稍后读取，或用原输入显式提交原命令。"); return false; }
      if (p.action === "account.sessions.revoke_others" && result.data.action !== "account.sessions.others_revoked") { setError("回执与原撤销其它会话命令不匹配，命令键仍保留。请继续读取原回执。"); return false; }
      if (p.action === "account.mfa.recovery.regenerate" && result.data.action !== "account.mfa.recovery.regenerated") { setError("回执与原恢复码更新命令不匹配，命令键仍保留。请继续读取原回执。"); return false; }
      clear(p); setNotice(p.action === "account.sessions.revoke_others" ? "原撤销其它会话操作已确认，保留当前会话和全部 API 令牌。此回执不表示之后新登录的会话已被撤销；请查看重新读取的会话列表。" : p.action === "account.mfa.recovery.regenerate" ? "恢复码更新已确认。回执不包含新恢复码；若首次响应丢失，请使用当前密码和有效的验证器验证码或未使用的新恢复码重新生成。旧恢复码已全部失效，其它会话和 API 令牌已撤销，当前会话保留，TOTP 验证器绑定不变。" : "原命令已确认。秘密不会再次显示；若丢失绑定密钥，请重新绑定；若丢失恢复码，请使用当前密码和有效的验证器验证码或未使用的恢复码重新生成。"); return true;
    } catch { if (version === epoch.current) setError("原回执连接失败，命令键仍保留。请重新读取。"); return false; }
    finally { if (version === epoch.current) { writing.current = false; setBusy(false); } }
  }
  return { pending, canRetryOriginal, busy, blocked, error, notice, run, recover, setError, setNotice };
}

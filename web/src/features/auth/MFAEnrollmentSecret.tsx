import { useEffect, useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import type { components } from "@/api/schema";
import "./MFAEnrollmentSecret.css";

type Setup = components["schemas"]["OwnMFASetupResult"];

function expiryTime(value: string) {
  if (typeof value !== "string") return NaN;
  const date = value.slice(0, 10);
  if (!/^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/.test(value)) return NaN;
  const day = new Date(`${date}T00:00:00Z`);
  if (!Number.isFinite(day.getTime()) || day.toISOString().slice(0, 10) !== date) return NaN;
  return Date.parse(value);
}

function validURI(setup: Setup) {
  if (setup.replayed || !setup.otpauth_url || !setup.secret || !/^[A-Z2-7]+$/.test(setup.secret)) return false;
  try {
    const raw = setup.otpauth_url;
    if (!raw.startsWith("otpauth://totp/") || /[\s#]/.test(raw) || raw.length > 2048) return false;
    const uri = new URL(raw);
    if (uri.protocol !== "otpauth:" || uri.host !== "totp" || uri.username || uri.password) return false;
    const label = decodeURIComponent(uri.pathname.slice(1));
    if (!label.startsWith("PulseMedia:") || !label.slice("PulseMedia:".length) || /[\u0000-\u001f\u007f]/.test(label)) return false;
    const expected: Record<string, string> = { algorithm: "SHA1", digits: "6", issuer: "PulseMedia", period: "30", secret: setup.secret };
    const params = uri.searchParams;
    return [...params].length === 5 && Object.entries(expected).every(([key, value]) => params.getAll(key).length === 1 && params.get(key) === value);
  } catch {
    return false;
  }
}

export function MFAEnrollmentSecret({ setup }: { setup: Setup }) {
  const [view, setView] = useState({ setup, manual: false, qr: false, expired: false });
  // Reset during render so a new response never paints with the previous reveal state.
  const current = view.setup === setup ? view : { setup, manual: false, qr: false, expired: false };
  if (view.setup !== setup) setView(current);
  const expires = expiryTime(setup.expires_at);
  const expired = current.expired || !Number.isFinite(expires) || expires <= Date.now();
  const qrAvailable = validURI(setup);

  useEffect(() => {
    if (!Number.isFinite(expires) || expires <= Date.now()) return;
    let timer: ReturnType<typeof setTimeout>;
    const schedule = () => {
      const remaining = expires - Date.now();
      if (remaining <= 0) {
        setView(previous => previous.setup === setup ? { ...previous, manual: false, qr: false, expired: true } : previous);
      } else {
        timer = setTimeout(schedule, Math.min(remaining, 2_147_483_647));
      }
    };
    schedule();
    return () => clearTimeout(timer);
  }, [setup, expires]);

  function toggle(field: "manual" | "qr") {
    if (!Number.isFinite(expires) || expires <= Date.now()) {
      setView({ setup, manual: false, qr: false, expired: true });
      return;
    }
    setView(previous => previous.setup === setup ? { ...previous, [field]: !previous[field] } : previous);
  }

  if (expired) return <p role="status">此绑定已过期或有效期不可用，请重新开始绑定。</p>;
  if (!setup.secret || setup.replayed) return <p role="status">绑定密钥仅在首次准备后提供，请重新开始绑定。</p>;

  return <div className="mfa-enrollment-secret">
    <p>在验证器中扫描二维码或手工输入密钥。此绑定在 {new Date(expires).toLocaleTimeString("zh-CN")} 到期；确认前不会启用。</p>
    {qrAvailable ? <>
      <button className="btn" type="button" aria-expanded={current.qr} onClick={() => toggle("qr")}>{current.qr ? "隐藏绑定二维码" : "显示绑定二维码"}</button>
      {current.qr && <QRCodeSVG className="mfa-enrollment-qr" value={setup.otpauth_url!} size={256} marginSize={4} bgColor="#FFFFFF" fgColor="#000000" role="img" aria-label="双因素绑定二维码" />}
    </> : <p>二维码不可用，请在验证器中手工输入绑定密钥。</p>}
    {current.manual && <><label htmlFor="mfa-setup-secret">绑定密钥</label><input id="mfa-setup-secret" type="text" readOnly value={setup.secret} autoComplete="off" spellCheck={false} /></>}
    <button className="btn" type="button" aria-expanded={current.manual} onClick={() => toggle("manual")}>{current.manual ? "隐藏绑定密钥" : "显示绑定密钥"}</button>
  </div>;
}

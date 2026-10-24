import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { useAuth } from "@/features/auth/AuthContext";
import { automationInputHash } from "./automationSettingsCommand";
import { csrfHeaders } from "./useAutomationSettings";
import { browserPushAvailable, clearBrowserPush, currentBrowserPush, enableBrowserPush } from "./browserPush";
import { parsePushCommand, pushStorageKey, serializePushCommand, type PushCommand } from "./webPushCommand";
import "./automation-settings.css";

type Config = components["schemas"]["WebPushConfiguration"];
type Device = components["schemas"]["WebPushDevice"];
type Registration = { id: string; vapid_version: string; subscription: { endpoint: string; p256dh: string; auth: string } };
export function WebPushPanel() {
  const { user, checking } = useAuth();
  if (checking || !user) return null;
  return <BrowserPushSettings key={`${user.id}:${user.role}`} owner={user.id} />;
}
function BrowserPushSettings({ owner }: { owner: string }) {
  const [config, setConfig] = useState<Config | null>(null), [devices, setDevices] = useState<Device[]>([]);
  const [ready, setReady] = useState(false), [busy, setBusy] = useState(false), [storageError, setStorageError] = useState(false);
  const [message, setMessage] = useState(""), [pending, setPending] = useState<PushCommand | null>(null), [absent, setAbsent] = useState(false), [rejected, setRejected] = useState(false);
  const alive = useRef(false), gate = useRef(false), controller = useRef<AbortController | null>(null), current = useRef<PushCommand | null>(null);
  const original = useRef<Registration | { expected_updated_at: string } | null>(null);
  const storage = pushStorageKey(owner), localDeviceKey = `pulse.webpush.device.v1:${owner}`;
  const remember = (value: PushCommand | null) => { current.current = value; setPending(value); };
  function hydrate() {
    try { const raw = localStorage.getItem(storage), parsed = raw === null ? null : parsePushCommand(raw, owner); if (raw !== null && !parsed) throw Error(); remember(parsed); setStorageError(false); setAbsent(false); if (parsed) setMessage("上次通知设置结果待确认，请查询原请求回执。"); }
    catch { setStorageError(true); setMessage("恢复记录不可读，已停止提交。请修复浏览器存储后重新读取。"); }
  }
  async function refresh() {
    if (gate.current) return;
    gate.current = true; setBusy(true); setReady(false); setConfig(null); setDevices([]);
    const ctl = new AbortController(); controller.current = ctl;
    try {
      const [settings, list] = await Promise.all([api.GET("/api/v1/web-push/config", { signal: ctl.signal, cache: "no-store" }), api.GET("/api/v1/web-push/devices", { signal: ctl.signal, cache: "no-store" })]);
      if (!alive.current || ctl.signal.aborted) return;
      if (!settings.response.ok || !settings.data || !list.response.ok || !Array.isArray(list.data)) throw Error();
      setConfig(settings.data); setDevices(list.data); setReady(true);
      setMessage(current.current ? "已读取当前状态；原请求仍须查询回执。" : "已读取当前通知设置。");
    } catch { if (alive.current && !ctl.signal.aborted) setMessage("通知设置读取失败，当前状态未知。请重新读取后再操作。"); }
    finally { if (alive.current) { gate.current = false; setBusy(false); } }
  }
  useEffect(() => {
    alive.current = true; hydrate(); void refresh();
    const changed = (event: StorageEvent) => { if (event.key === storage || event.key === null) { setStorageError(true); setMessage("恢复记录在其他标签页变化，请重新读取并核对原回执。"); } };
    window.addEventListener("storage", changed);
    return () => { alive.current = false; controller.current?.abort(); original.current = null; window.removeEventListener("storage", changed); };
  }, [owner]);
  function finish(command: PushCommand, device: Device) {
    if (device.id !== command.id) throw Error();
    if (localStorage.getItem(storage) !== serializePushCommand(command)) throw Error();
    if (command.kind === "register") localStorage.setItem(localDeviceKey, device.id);
    localStorage.removeItem(storage); remember(null); original.current = null; setAbsent(false); setRejected(false);
  }
  async function send(command: PushCommand, body: Registration | { expected_updated_at: string }, signal: AbortSignal) {
    const params = { header: { "Idempotency-Key": command.key } };
    const response = command.kind === "register"
      ? await api.POST("/api/v1/web-push/devices", { params, body: body as Registration, signal, headers: csrfHeaders() })
      : await api.PUT("/api/v1/web-push/devices/{id}/revocation", { params: { ...params, path: { id: command.id } }, body: body as { expected_updated_at: string }, signal, headers: csrfHeaders() });
    if (!alive.current || signal.aborted) return false;
    if ([400, 403, 404, 409].includes(response.response.status)) { setRejected(true); setReady(false); setMessage("操作被拒绝或设备版本已变化。请查询原回执，再重新读取当前状态。"); return false; }
    if (!response.response.ok || !response.data) throw Error();
    if (command.kind === "revoke" && localStorage.getItem(localDeviceKey) === command.id) { await clearBrowserPush(); if (!alive.current || signal.aborted) return false; localStorage.removeItem(localDeviceKey); }
    finish(command, response.data); setMessage(command.kind === "register" ? "设备通知订阅已保存。推送服务接受请求和浏览器实际显示仍是不同状态。" : "设备已停用；正在发送的通知可能完成。");
    return true;
  }
  async function save(kind: "register" | "revoke", device?: Device) {
    if (gate.current || !ready || storageError || current.current && !absent) return;
    gate.current = true; setBusy(true); const ctl = new AbortController(); controller.current = ctl; let reload = false, storageFailed = false;
    try {
      const old = current.current;
      let command: PushCommand, body: Registration | { expected_updated_at: string };
      if (old) {
        if (old.kind !== kind) { setMessage("请先恢复原请求回执。"); return; }
        command = old;
        if (original.current) body = original.current;
        else if (old.kind === "revoke" && old.expectedUpdatedAt) body = { expected_updated_at: old.expectedUpdatedAt };
        else {
          const registration = await currentBrowserPush();
          const subscription = (await registration?.pushManager.getSubscription())?.toJSON();
          if (!subscription?.endpoint || !subscription.keys?.p256dh || !subscription.keys.auth || !old.vapidVersion) throw Error();
          body = { id: old.id, vapid_version: old.vapidVersion, subscription: { endpoint: subscription.endpoint, p256dh: subscription.keys.p256dh, auth: subscription.keys.auth } };
        }
      } else if (kind === "register") {
        if (!config?.enabled) return;
        setMessage("正在向浏览器推送服务申请订阅，请稍候；此步骤不会保存设备配置。");
        const subscription = await enableBrowserPush(config.public_key, () => alive.current && !ctl.signal.aborted);
        if (!alive.current || ctl.signal.aborted) return;
        const id = crypto.randomUUID(); body = { id, vapid_version: config.version, subscription };
        command = { owner, id, key: crypto.randomUUID(), kind, vapidVersion: config.version, hash: await automationInputHash(body) };
      } else {
        if (!device) return;
        body = { expected_updated_at: device.updated_at };
        command = { owner, id: device.id, key: crypto.randomUUID(), kind, expectedUpdatedAt: device.updated_at, hash: await automationInputHash(body) };
      }
      if (!alive.current || ctl.signal.aborted) return;
      const bodyHash = await automationInputHash(body);
      if (!alive.current || ctl.signal.aborted) return;
      if (bodyHash !== command.hash) throw Error();
      try { localStorage.setItem(storage, serializePushCommand(command)); } catch { storageFailed = true; setStorageError(true); throw Error(); }
      remember(command); original.current = body; setAbsent(false);
      reload = await send(command, body, ctl.signal);
    } catch (error) { if (alive.current && !ctl.signal.aborted) setMessage(current.current ? "操作结果未确认。原请求已保留，请先查询回执；不会自动重试。" : storageFailed ? "恢复记录保存失败，尚未提交设备配置。请修复浏览器存储后重新读取。" : error instanceof Error && error.name === "BrowserPushRegistrationError" ? error.message : "浏览器通知订阅尚未就绪，尚未提交设备配置。请检查通知权限后重试。"); }
    finally { if (alive.current) { gate.current = false; setBusy(false); } }
    if (reload && alive.current) await refresh();
  }
  async function recover() {
    const command = current.current; if (!command || gate.current) return;
    gate.current = true; setBusy(true); const ctl = new AbortController(); controller.current = ctl; let reload = false;
    try {
      const r = await api.GET("/api/v1/web-push/receipts/{key}", { params: { path: { key: command.key } }, signal: ctl.signal, cache: "no-store" });
      if (!alive.current || ctl.signal.aborted) return;
      if (r.response.status === 404) { setAbsent(true); setMessage("原请求暂无回执。仅可明确重试同一请求，或在拒绝后重新读取并结束冲突。"); return; }
      if (!r.response.ok || !r.data) throw Error();
      if (command.kind === "revoke" && localStorage.getItem(localDeviceKey) === command.id) { await clearBrowserPush(); if (!alive.current || ctl.signal.aborted) return; localStorage.removeItem(localDeviceKey); }
      finish(command, r.data); setMessage("原请求回执已恢复。历史回执不代表当前设备仍可接收通知。"); reload = true;
    } catch { if (alive.current && !ctl.signal.aborted) setMessage("原回执读取或恢复记录清理失败，原请求已保留。请重试查询。"); }
    finally { if (alive.current) { gate.current = false; setBusy(false); } }
    if (reload && alive.current) await refresh();
  }
  const locked = busy || !ready || storageError || !!pending;
  return <section className="card automation-settings" aria-label="浏览器通知" aria-busy={busy}>
    <div className="card-header"><h3>浏览器通知</h3><button type="button" className="btn-secondary" disabled={busy} onClick={() => void refresh()}>重新读取通知设置</button></div>
    <p className="card-desc">在此浏览器接收入库提醒。只有点击启用后才请求通知权限；通知不展示账号或媒体详情，点击后打开本站查看。</p>
    <p role="status">{!browserPushAvailable() ? "此浏览器不支持安全推送，请使用HTTPS或本机地址。" : !ready ? "通知设置状态未知，暂不能修改" : config?.enabled ? "服务器已启用浏览器推送" : "服务器尚未启用浏览器推送"}</p>
    {message && <p className="automation-message" role="status">{message}</p>}
    {storageError && <button type="button" className="btn-secondary" disabled={busy} onClick={hydrate}>重新读取恢复记录</button>}
    {pending && <div className="automation-recovery"><p>有一项设备操作结果待核对。</p><div className="automation-actions"><button type="button" className="btn-secondary" disabled={busy} onClick={() => void recover()}>查询原设备操作回执</button>{absent && <button type="button" className="btn-secondary" disabled={busy || !ready || storageError} onClick={() => void save(pending.kind)}>使用原键重试设备操作</button>}{absent && rejected && ready && <button type="button" className="btn-secondary" disabled={busy || storageError} onClick={() => { try { if (localStorage.getItem(storage) !== serializePushCommand(pending)) throw Error(); localStorage.removeItem(storage); remember(null); original.current = null; setRejected(false); setMessage("已结束被拒绝的请求，未再次提交操作。"); } catch { setStorageError(true); } }}>结束已核对的拒绝请求</button>}</div></div>}
    <button type="button" className="btn-primary" disabled={locked || !config?.enabled || !browserPushAvailable()} onClick={() => void save("register")}>在此浏览器启用通知</button>
    {ready && (devices.length === 0 ? <p className="empty-hint">当前账号没有浏览器通知设备。</p> : <ul className="integration-list">{devices.map(device => <li className="integration-row" key={device.id}><div><strong>{device.status === "active" ? "已登记设备" : device.status === "expired" ? "订阅已过期" : "已停用设备"}</strong><p className="item-meta">登记于 {new Date(device.created_at).toLocaleString()}</p></div>{device.status === "active" && <button type="button" className="btn-secondary" disabled={locked} onClick={() => void save("revoke", device)}>停用此设备</button>}</li>)}</ul>)}
  </section>;
}

export function browserPushAvailable() {
  return window.isSecureContext && "Notification" in window && "serviceWorker" in navigator && "PushManager" in window;
}
function publicKey(value: string): Uint8Array<ArrayBuffer> {
  const bytes = atob(value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - value.length % 4) % 4));
  return Uint8Array.from(bytes, c => c.charCodeAt(0));
}
export async function currentBrowserPush() {
  if (!browserPushAvailable()) return null;
  const registration = await navigator.serviceWorker.getRegistration("/");
  if (registration?.active?.scriptURL !== new URL("/pulse-push-sw.js", window.location.origin).href) return null;
  return registration;
}
export async function enableBrowserPush(key: string, current: () => boolean) {
  if (!browserPushAvailable() || !current()) throw Error("浏览器不支持安全通知，请使用HTTPS或本机地址。");
  // This function is called only from the user's enable button.
  const permission = await Notification.requestPermission();
  if (!current()) throw Error("账户已变化，请重新读取通知设置。");
  if (permission !== "granted") throw Error("通知权限未授予。请在浏览器站点设置中允许通知后重试。");
  await navigator.serviceWorker.register("/pulse-push-sw.js", { scope: "/", updateViaCache: "none" });
  let timer: ReturnType<typeof setTimeout> | undefined;
  const registration = await Promise.race([navigator.serviceWorker.ready, new Promise<never>((_, reject) => { timer = setTimeout(() => reject(Error("通知后台等待超时，请重试。")), 15000); })]).finally(() => clearTimeout(timer));
  if (!current()) throw Error("账户已变化，请重新读取通知设置。");
  if (registration.active?.scriptURL !== new URL("/pulse-push-sw.js", window.location.origin).href) throw Error("通知后台尚未就绪，请重新尝试。");
  let subscription = await registration.pushManager.getSubscription();
  const expected = publicKey(key);
  if (subscription) {
    const previous = subscription.options.applicationServerKey;
    if (!previous || new Uint8Array(previous).some((v, i) => v !== expected[i]) || previous.byteLength !== expected.byteLength) {
      if (!await subscription.unsubscribe()) throw Error("旧通知订阅未能移除，请重试。");
      subscription = null;
    }
  }
  if (!current()) throw Error("账户已变化，请重新读取通知设置。");
  try { subscription ??= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: expected }); }
  catch (error) {
    const category = error instanceof DOMException && ["AbortError", "NotAllowedError", "NotSupportedError", "InvalidStateError", "InvalidAccessError"].includes(error.name) ? error.name : "Unavailable";
    const safe = new Error(`浏览器推送订阅失败（${category}），尚未提交设备配置。请稍后重试。`);
    safe.name = "BrowserPushRegistrationError"; throw safe;
  }
  if (!current()) { await subscription.unsubscribe(); throw Error("账户已变化，请重新启用通知。"); }
  const value = subscription.toJSON();
  if (!value.endpoint || !value.keys?.p256dh || !value.keys.auth) throw Error("浏览器没有返回完整通知订阅。");
  return { endpoint: value.endpoint, p256dh: value.keys.p256dh, auth: value.keys.auth };
}
export async function clearBrowserPush() {
  const registration = await currentBrowserPush();
  if (!registration) return;
  registration.active?.postMessage({ type: "pulsemedia:clear-notifications" });
  const subscription = await registration.pushManager.getSubscription();
  if (subscription && !await subscription.unsubscribe()) throw Error("浏览器订阅尚未移除。");
  for (const notification of await registration.getNotifications()) notification.close();
}

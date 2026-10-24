/* Dedicated notification worker: no fetch interception or account data cache. */
self.addEventListener("install", event => { event.waitUntil(self.skipWaiting()); });
self.addEventListener("activate", event => { event.waitUntil(self.clients.claim()); });

self.addEventListener("push", event => {
  let payload;
  try { payload = event.data?.json(); } catch { return; }
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return;
  if (typeof payload.title !== "string" || !payload.title.trim() || payload.title.length > 160) return;
  if (typeof payload.body !== "string" || payload.body.length > 1800) return;
  if (typeof payload.tag !== "string" || !/^pulsemedia:[0-9a-f-]{36}$/.test(payload.tag)) return;
  if (payload.url !== "/" || Object.keys(payload).some(key => !["title", "body", "tag", "url", "icon"].includes(key))) return;
  // A notification can outlive a login. Keep its visible text free of account or media details.
  event.waitUntil(self.registration.showNotification("媒体已入库", {
    body: "媒体库有新内容，请登录 PulseMedia 查看。",
    tag: payload.tag,
    data: { path: "/" },
  }));
});

self.addEventListener("notificationclick", event => {
  event.notification.close();
  event.waitUntil((async () => {
    const destination = new URL("/", self.location.origin).href;
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    for (const client of windows) {
      if (new URL(client.url).origin !== self.location.origin) continue;
      await client.navigate(destination);
      await client.focus();
      return;
    }
    await self.clients.openWindow(destination);
  })());
});

self.addEventListener("message", event => {
  if (event.data?.type !== "pulsemedia:clear-notifications" || !event.source?.url) return;
  try { if (new URL(event.source.url).origin !== self.location.origin) return; } catch { return; }
  event.waitUntil((async () => {
    for (const notification of await self.registration.getNotifications()) notification.close();
  })());
});

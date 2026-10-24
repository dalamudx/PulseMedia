import { useEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";

export type EndpointFamily = "download_client" | "media_server";
export type EndpointItem = { id: string; name: string; type: string; url: string; updated_at: string; username: string };
export type EndpointDraft = { name: string; type: string; url: string; username: string; password: string; api_key: string };
type Editing = { id: string; version: string; url: string };
type Gate = "none" | "conflict" | "unknown" | "saved" | "denied";
export type EndpointProbe = { kind: "online" | "offline" | "error"; message: string };
type View = {
  items: EndpointItem[]; draft: EndpointDraft; editing: Editing | null;
  listStatus: "loading" | "ready" | "error"; busy: "read" | "save" | "delete" | "probe" | null;
  gate: Gate; message: string; feedbackKind: "status" | "error";
  probes: Record<string, EndpointProbe>;
};
type Owner = { identity: string; active: boolean; request: AbortController | null; view: View };
export type EndpointConfiguration = View & {
  isAdmin: boolean; blocked: boolean;
  refresh: () => Promise<boolean>; reread: () => Promise<boolean>;
  startEdit: (id: string) => void; cancelEdit: () => void;
  change: (field: keyof EndpointDraft, value: string) => void;
  save: () => Promise<boolean>; remove: (id: string) => Promise<boolean>; probe: (id: string) => Promise<boolean>;
};
const blankDraft = (family: EndpointFamily): EndpointDraft => ({ name: "", type: family === "download_client" ? "qbittorrent" : "jellyfin", url: "", username: "", password: "", api_key: "" });
const initial = (family: EndpointFamily): View => ({ items: [], draft: blankDraft(family), editing: null, listStatus: "loading", busy: null, gate: "none", message: "", feedbackKind: "status", probes: {} });
const draftOf = (item: EndpointItem): EndpointDraft => ({ name: item.name, type: item.type, url: item.url, username: item.username, password: "", api_key: "" });
const downloadType = (value: string): value is "qbittorrent" | "transmission" | "rtorrent" => value === "qbittorrent" || value === "transmission" || value === "rtorrent";
const mediaType = (value: string): value is "jellyfin" | "emby" | "plex" => value === "jellyfin" || value === "emby" || value === "plex";
const failureMessage = (status: number) => status === 403 ? "权限不足，请确认当前管理员账户后重新读取。" : status === 401 ? "登录已失效，请重新登录。" : status === 404 ? "配置已不存在，请重新读取。" : status === 503 ? "配置服务暂时不可用，请重新读取。" : "请求失败，请稍后重新读取。";

function readItems(data: unknown, family: EndpointFamily): EndpointItem[] {
  if (!data || typeof data !== "object" || !("items" in data) || !Array.isArray(data.items)) throw Error("invalid list");
  const ids = new Set<string>();
  return data.items.map((value: unknown) => {
    if (!value || typeof value !== "object") throw Error("invalid item");
    const row = value as Record<string, unknown>, type = row[family === "download_client" ? "client_type" : "server_type"];
    if (typeof row.id !== "string" || !row.id || ids.has(row.id) || typeof row.name !== "string" || typeof type !== "string" || !type || typeof row.url !== "string" || typeof row.updated_at !== "string" || !row.updated_at || !Number.isFinite(Date.parse(row.updated_at))) throw Error("invalid item");
    if (family === "download_client" && typeof row.username !== "string") throw Error("invalid username");
    ids.add(row.id);
    // Only the safe read projection enters state; response credentials are never copied.
    return { id: row.id, name: row.name, type, url: row.url, updated_at: row.updated_at, username: family === "download_client" ? row.username as string : "" };
  });
}

export function useEndpointConfiguration(family: EndpointFamily): EndpointConfiguration {
  const { user } = useAuth();
  const identity = JSON.stringify([user?.id ?? "", user?.role ?? "", family]);
  const isAdmin = !!user?.id && user.role === "admin";
  const owner = useRef<Owner | null>(null);
  const [, render] = useState(0);
  // Fence during render so an account/role ABA cannot expose even one old frame.
  if (owner.current?.identity !== identity) {
    if (owner.current) { owner.current.active = false; owner.current.request?.abort(); }
    owner.current = { identity, active: false, request: null, view: initial(family) };
  }
  const scope = owner.current;
  const current = () => owner.current === scope && scope.active && isAdmin;
  const publish = (patch: Partial<View>) => { if (current()) { scope.view = { ...scope.view, ...patch }; render(n => n + 1); } };
  const writable = () => current() && !scope.request && scope.view.listStatus === "ready" && scope.view.gate === "none";
  const active = (request: AbortController) => current() && scope.request === request && !request.signal.aborted;

  async function read(request: AbortController, discard: boolean): Promise<boolean> {
    const before = scope.view;
    publish({ listStatus: "loading" });
    try {
      const result = family === "download_client"
        ? await api.GET("/api/v1/download-clients", { signal: request.signal, cache: "no-store" })
        : await api.GET("/api/v1/media-servers", { signal: request.signal, cache: "no-store" });
      if (!active(request)) return false;
      if (result.response.status !== 200 || result.error) {
        publish({ listStatus: "error", message: `${before.gate === "saved" ? "已保存，但列表刷新失败。" : "配置列表读取失败。"}${failureMessage(result.response.status)}`, feedbackKind: "error" });
        return false;
      }
      const items = readItems(result.data, family);
      const probes = Object.fromEntries(Object.entries(before.probes).filter(([id]) => {
        const old = before.items.find(item => item.id === id), fresh = items.find(item => item.id === id);
        return old && fresh && old.updated_at === fresh.updated_at;
      }));
      let editing = before.editing, draft = before.draft, gate = before.gate;
      let message = "配置列表已更新。";
      if (discard || gate === "unknown" || gate === "saved") {
        const item = discard && editing ? items.find(item => item.id === editing?.id) : undefined;
        editing = item ? { id: item.id, version: item.updated_at, url: item.url } : null;
        draft = item ? draftOf(item) : blankDraft(family);
        message = gate === "unknown" ? "最新列表已读取，请核对配置；上次操作结果仍未确认。" : gate === "saved" ? "已保存，配置列表已更新。" : "已重新读取，原草稿已放弃。";
        gate = "none";
      } else if (editing && items.find(item => item.id === editing?.id)?.updated_at !== editing.version) {
        gate = "conflict";
      } else if (gate === "denied") gate = "none";
      if (gate === "conflict") message = "配置已变化，草稿已保留。请重新读取并放弃草稿后再编辑。";
      publish({ items, probes, editing, draft, gate, listStatus: "ready", message, feedbackKind: gate === "conflict" ? "error" : "status" });
      return true;
    } catch {
      if (active(request)) publish({ listStatus: "error", message: before.gate === "saved" ? "已保存，但列表刷新失败。请重新读取，不要再次保存。" : "配置列表读取失败，请重新读取后继续。", feedbackKind: "error" });
      return false;
    }
  }

  async function refresh(discard = false): Promise<boolean> {
    if (!current() || scope.request) return false;
    const request = new AbortController(); scope.request = request;
    publish({ busy: "read" });
    try { return await read(request, discard); }
    finally { if (active(request)) { scope.request = null; publish({ busy: null }); } }
  }

  async function command(kind: "save" | "delete" | "probe", id?: string): Promise<boolean> {
    if (!writable()) return false;
    const before = scope.view, draft = { ...before.draft }, editing = before.editing;
    if (kind !== "save" && !before.items.some(item => item.id === id)) return false;
    if (kind === "save") {
      let validUrl = false;
      try { const url = new URL(draft.url); validUrl = ["http:", "https:"].includes(url.protocol) && !!url.hostname; } catch { /* Keep the entered value for correction. */ }
      const unchanged = !!editing && draft.url === editing.url;
      const typeValid = family === "download_client" ? downloadType(draft.type) : mediaType(draft.type) && (draft.type !== "plex" || before.items.some(item => item.id === editing?.id && item.type === "plex"));
      if (!draft.name.trim() || !validUrl || (!unchanged && /\*\*\*|%2a%2a%2a/i.test(draft.url)) || !typeValid || (editing && !editing.version)) {
        publish({ message: "请填写名称、有效的 HTTP(S) 地址和支持的类型。隐藏地址只能原样保留；修改时请填写完整地址。", feedbackKind: "error" });
        return false;
      }
    }
    const request = new AbortController(); scope.request = request;
    publish({ busy: kind, message: "", feedbackKind: "status" });
    try {
      const options = { signal: request.signal, params: { path: { id: id ?? editing?.id ?? "" } } };
      let result;
      if (kind === "probe") {
        result = family === "download_client" ? await api.POST("/api/v1/download-clients/{id}/test", options) : await api.POST("/api/v1/media-servers/{id}/test", options);
      } else if (kind === "delete") {
        result = family === "download_client" ? await api.DELETE("/api/v1/download-clients/{id}", options) : await api.DELETE("/api/v1/media-servers/{id}", options);
      } else {
        const common = { name: draft.name, url: draft.url };
        if (family === "download_client" && downloadType(draft.type)) {
          const body = { ...common, client_type: draft.type, username: draft.username, password: draft.password };
          result = editing ? await api.PUT("/api/v1/download-clients/{id}", { ...options, body: { ...body, expected_updated_at: editing.version } }) : await api.POST("/api/v1/download-clients", { signal: request.signal, body });
        } else if (family === "media_server" && mediaType(draft.type)) {
          const body = { ...common, server_type: draft.type, api_key: draft.api_key };
          result = editing ? await api.PUT("/api/v1/media-servers/{id}", { ...options, body: { ...body, expected_updated_at: editing.version } }) : await api.POST("/api/v1/media-servers", { signal: request.signal, body });
        } else return false;
      }
      if (!active(request)) return false;
      const status = result.response.status;
      if (kind === "probe") {
        const data = result.data;
        const ok = status === 200 && !result.error && data && "alive" in data && typeof data.alive === "boolean";
        const probe: EndpointProbe = ok ? { kind: data.alive ? "online" : "offline", message: data.alive ? "连通正常" : "服务未连通" } : { kind: "error", message: `测试失败。${failureMessage(status)}` };
        publish({ probes: { ...scope.view.probes, [id!]: probe } });
        return !!ok;
      }
      const expectedStatus = kind === "delete" ? 204 : editing ? 200 : 201;
      if (status !== expectedStatus || result.error) {
        if (status >= 500 || status < 400) throw Error("unconfirmed mutation");
        const conflict = status === 409;
        publish({ gate: conflict ? "conflict" : [401, 403, 404].includes(status) ? "denied" : "none", message: conflict ? "配置已变化，草稿已保留。请重新读取并放弃草稿后再编辑。" : `操作未完成。${status === 400 ? "请检查配置后再保存。" : failureMessage(status)}`, feedbackKind: "error" });
        return false;
      }
      publish({ gate: "saved", draft: blankDraft(family), editing: null, probes: {}, message: kind === "delete" ? "已删除，正在刷新列表。" : "已保存，正在刷新列表。" });
      await read(request, false);
      return active(request);
    } catch {
      if (active(request)) {
        if (kind === "probe") publish({ probes: { ...scope.view.probes, [id!]: { kind: "error", message: "测试失败，请检查连接后重试。" } } });
        else publish({ gate: "unknown", message: "操作结果未知，请手动重新读取列表并核对配置后继续，不要重复提交。", feedbackKind: "error" });
      }
      return false;
    } finally {
      if (active(request)) { scope.request = null; publish({ busy: null }); }
    }
  }

  useEffect(() => {
    scope.active = true;
    if (isAdmin) void refresh();
    return () => { scope.active = false; scope.request?.abort(); scope.request = null; scope.view = initial(family); };
  }, [scope]);

  const view = scope.view;
  return {
    ...view, items: isAdmin && view.listStatus === "ready" ? view.items : [], isAdmin,
    blocked: !isAdmin || view.busy !== null || view.listStatus !== "ready" || view.gate !== "none",
    refresh: () => refresh(), reread: () => refresh(true),
    change(field, value) { if (writable()) publish({ draft: { ...scope.view.draft, [field]: value } }); },
    startEdit(id) { if (writable()) { const item = scope.view.items.find(item => item.id === id); if (item) publish({ editing: { id, version: item.updated_at, url: item.url }, draft: draftOf(item), message: "" }); } },
    cancelEdit() { if (writable()) publish({ editing: null, draft: blankDraft(family), message: "" }); },
    save: () => command("save"), remove: id => command("delete", id), probe: id => command("probe", id),
  };
}

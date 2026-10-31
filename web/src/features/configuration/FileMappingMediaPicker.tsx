import { useEffect, useId, useRef, useState } from "react";
import { api, type paths } from "@/api/client";
import { useAuth } from "@/features/auth/AuthContext";
import "./file-mapping-media-picker.css";

type Page = paths["/api/v1/library/mapping-options"]["get"]["responses"]["200"]["content"]["application/json"];
type Choice = Page["media"][number];
type Filters = { query: string; type: "" | "movie" | "tv"; tmdb: string };
type Props = { value: string; onChange: (id: string) => void; disabled: boolean; initialPage: Page };
const emptyFilters: Filters = { query: "", type: "", tmdb: "" };

export function FileMappingMediaPicker(props: Props) {
  const { user } = useAuth();
  return user?.role === "admin" ? <Picker key={`${user.id}:${user.role}`} {...props} /> : null;
}
function Picker({ value, onChange, disabled, initialPage }: Props) {
  const id = useId();
  const [draft, setDraft] = useState<Filters>(emptyFilters);
  const [filter, setFilter] = useState<Filters>(emptyFilters);
  const [items, setItems] = useState(initialPage.media);
  const [next, setNext] = useState(initialPage.media_next_cursor ?? "");
  const [hasMore, setHasMore] = useState(initialPage.media_has_more ?? false);
  const [selected, setSelected] = useState<Choice | undefined>(initialPage.selected_media ?? initialPage.media.find(m => m.id === value));
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const owner = useRef<AbortController | null>(null);
  useEffect(() => {
    if (value && !selected) void read("", emptyFilters);
    return () => { const c = owner.current; owner.current = null; c?.abort(); };
  }, []);
  async function read(cursor: string, filters: Filters) {
    if (owner.current) return;
    const title = filters.query.trim(), tmdb = filters.tmdb.trim();
    if (Array.from(title).length > 120 || (tmdb && (!/^[1-9][0-9]*$/.test(tmdb) || !Number.isSafeInteger(Number(tmdb))))) {
      setError("请输入不超过 120 个字符的标题或有效 TMDB ID。"); return;
    }
    const c = new AbortController(); owner.current = c; setBusy(true); setError("");
    const timer = window.setTimeout(() => c.abort(), 15000);
    try {
      const r = await api.GET("/api/v1/library/mapping-options", {
        signal: c.signal, cache: "no-store", params: { query: {
          ...(title ? { media_query: title } : {}), ...(filters.type ? { media_type: filters.type } : {}),
          ...(tmdb ? { tmdb_id: Number(tmdb) } : {}), ...(cursor ? { media_cursor: cursor } : {}),
          ...(value ? { media_id: value } : {}),
        } },
      });
      if (c.signal.aborted) return;
      if (r.error || !r.data) { setError("媒体选项读取失败，已有结果和选择已保留，请重试。"); return; }
      const page = r.data;
      setItems(previous => cursor ? [...new Map([...previous, ...page.media].map(m => [m.id, m])).values()] : page.media);
      setSelected(page.selected_media ?? page.media.find(m => m.id === value));
      setNext(page.media_next_cursor ?? ""); setHasMore(page.media_has_more ?? false);
      setFilter({ ...filters, query: title, tmdb });
    } catch {
      if (owner.current === c) setError(c.signal.aborted ? "媒体选项读取超时，已有选择已保留，请重试。" : "媒体选项连接中断，已有选择已保留，请重试。");
    } finally {
      window.clearTimeout(timer);
      if (owner.current === c) { owner.current = null; setBusy(false); }
    }
  }
  const pinned = value && !items.some(m => m.id === value);
  return <div className="mapping-media-picker" role="group" aria-label="已有媒体检索">
    <form onSubmit={e => { e.preventDefault(); void read("", draft); }}>
      <fieldset className="config-fieldset" disabled={disabled || busy}>
        <legend>查找已有媒体</legend>
        <label className="config-field" htmlFor={`${id}-query`}>标题<input id={`${id}-query`} value={draft.query} maxLength={120} onChange={e => setDraft(v => ({ ...v, query: e.target.value }))} /></label>
        <label className="config-field" htmlFor={`${id}-type`}>媒体类型<select aria-label="媒体类型" id={`${id}-type`} value={draft.type} onChange={e => setDraft(v => ({ ...v, type: e.target.value as Filters["type"] }))}><option value="">全部类型</option><option value="movie">电影</option><option value="tv">剧集</option></select></label>
        <label className="config-field" htmlFor={`${id}-tmdb`}>TMDB ID<input id={`${id}-tmdb`} inputMode="numeric" value={draft.tmdb} onChange={e => setDraft(v => ({ ...v, tmdb: e.target.value }))} /></label>
        <button type="submit" className="btn-secondary">查找媒体</button>
      </fieldset>
    </form>
    {error && <p role="alert" className="config-error">{error}</p>}
    <label className="config-field" htmlFor={`${id}-media`}>持久媒体<select aria-label="持久媒体" id={`${id}-media`} value={value} disabled={disabled || busy} onChange={e => { setSelected(items.find(m => m.id === e.target.value)); onChange(e.target.value); }}>
      <option value="">选择持久媒体</option>
      {pinned && <option value={value} disabled={!selected}>{selected ? `${selected.title}（当前选择）` : "原选择暂不可读取"}</option>}
      {items.map(m => <option key={m.id} value={m.id}>{m.title} · {m.media_type === "movie" ? "电影" : "剧集"}</option>)}
    </select></label>
    {!busy && !items.length && <p role="status">当前筛选没有已有媒体，可修改条件重新查找。</p>}
    {hasMore && <button type="button" className="btn-secondary" disabled={disabled || busy} onClick={() => void read(next, filter)}>读取下一页媒体</button>}
    {busy && <p role="status">正在读取媒体选项…</p>}
  </div>;
}

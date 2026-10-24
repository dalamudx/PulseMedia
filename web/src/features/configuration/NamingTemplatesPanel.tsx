import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/api/client";
import type { components } from "@/api/schema";
import { useAuth } from "@/features/auth/AuthContext";
import { useAutomationSettings, csrfHeaders } from "./useAutomationSettings";
import { AutomationSettingsFeedback } from "./AutomationSettingsFeedback";
import "./configuration.css";
import "./naming-editor.css";

type Templates = components["schemas"]["NamingTemplates"];
type Sample = components["schemas"]["NamingPreviewRequest"];
type Preview = components["schemas"]["NamingPreviewResult"];
type Config = Templates & { version: number };
type Input = { expected_version: number; value: Templates };
const key = "organization.naming_templates";
const defaults: Templates = { movie_template: "", episode_template: "" };
function templates(value: unknown): value is Templates {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>;
  return Object.keys(v).length === 2 && typeof v.movie_template === "string" && typeof v.episode_template === "string" && [v.movie_template, v.episode_template].every(text => new TextEncoder().encode(text).length <= 4096);
}
function config(value: components["schemas"]["SettingItem"] | undefined): Config {
  if (!value || value.key !== key || !Number.isSafeInteger(value.version) || value.version < 1 || !templates(value.value)) throw Error("Invalid naming setting");
  return { movie_template: value.value.movie_template, episode_template: value.value.episode_template, version: value.version };
}
export function NamingTemplatesPanel() {
  const { user } = useAuth();
  const scope = useMemo(() => ({ id: user?.id, role: user?.role }), [user?.id, user?.role]);
  const live = useRef(scope); live.current = scope;
  const isCurrent = useCallback(() => live.current === scope, [scope]);
  return user?.role === "admin" ? <NamingWorkspace key={`${user.id}:${user.role}`} owner={user.id} isCurrent={isCurrent} /> : null;
}
function NamingWorkspace({ owner, isCurrent }: { owner: string; isCurrent: () => boolean }) {
  const adapter = useMemo(() => ({
    read: async (signal: AbortSignal) => {
      const result = await api.GET("/api/v1/settings", { signal, cache: "no-store" });
      if (result.response.status !== 200) return { status: result.response.status, config: null };
      if (!result.data || !Array.isArray(result.data.items)) throw Error("Invalid settings");
      const rows = result.data.items.filter(item => item.key === key);
      if (rows.length > 1) throw Error("Duplicate naming setting");
      return { status: 200, config: rows.length ? config(rows[0]) : null };
    },
    save: async (input: Input, command: string, signal: AbortSignal) => {
      const result = await api.PUT("/api/v1/settings/{key}", { signal, params: { path: { key } }, headers: { ...csrfHeaders(), "Idempotency-Key": command }, body: { expected_version: input.expected_version, value: { ...input.value } } });
      return { status: result.response.status, key: command, config: result.response.status === 200 ? config(result.data) : null };
    },
    receipt: async (command: string, signal: AbortSignal) => {
      const result = await api.GET("/api/v1/settings/{key}/commands/{request_key}", { signal, cache: "no-store", params: { path: { key, request_key: command } } });
      return { status: result.response.status, key: command, config: result.response.status === 200 ? config(result.data) : null };
    },
  }), []);
  const model = useAutomationSettings<Config, Input>(owner, "naming-templates", adapter, { isCurrent, savedMessage: "命名模板已保存。新整理计划使用新模板，已有计划保留原路径。", requireReceiptBeforeRetry: true });
  const [draft, setDraft] = useState<Templates>({ ...defaults });
  const [sample, setSample] = useState({ media_type: "movie" as "movie" | "tv", title: "示例影片", extension: "mkv", year: "2020", season: "1", episode: "7", tmdb_id: "", imdb_id: "", release_title: "Example.2020.1080p.WEB-DL.x264" });
  const [preview, setPreview] = useState<Preview | null>(null), [previewError, setPreviewError] = useState(""), [previewBusy, setPreviewBusy] = useState(false);
  const previewOwner = useRef<AbortController | null>(null);
  const invalidate = () => { previewOwner.current?.abort(); previewOwner.current = null; setPreviewBusy(false); setPreview(null); setPreviewError(""); };
  useEffect(() => { invalidate(); if (model.ready) setDraft(model.config ? { movie_template: model.config.movie_template, episode_template: model.config.episode_template } : { ...defaults }); }, [model.config, model.ready]);
  useEffect(() => () => { previewOwner.current?.abort(); }, []);
  const locked = model.busy || !model.ready || model.storageError || (!!model.pending && model.phase !== "absent");
  const change = (value: Templates) => { invalidate(); setDraft(value); };
  const runPreview = async (saved: boolean) => {
    if (previewOwner.current || model.busy || !model.ready || !isCurrent()) return;
    setPreview(null); setPreviewError("");
    const body: Sample = { media_type: sample.media_type, title: sample.title, extension: sample.extension, ...(saved ? {} : { templates: { ...draft } }) };
    for (const field of ["year", "tmdb_id", ...(sample.media_type === "tv" ? ["season", "episode"] as const : [])] as const) {
      const raw = sample[field]; if (raw === "") continue;
      if (!/^(0|[1-9][0-9]*)$/.test(raw) || !Number.isSafeInteger(Number(raw))) { setPreviewError("样例数值须为有效整数，未知值请留空。"); return; }
      body[field] = Number(raw);
    }
    if (sample.imdb_id) body.imdb_id = sample.imdb_id;
    if (sample.release_title) body.release_title = sample.release_title;
    const ctl = new AbortController(); previewOwner.current = ctl; setPreviewBusy(true);
    try {
      const result = await api.POST("/api/v1/organization/naming/preview", { signal: ctl.signal, headers: csrfHeaders(), body });
      if (ctl.signal.aborted || previewOwner.current !== ctl || !isCurrent()) return;
      if (result.error || !result.data || result.data.execution_allowed !== false || !result.data.relative_path || result.data.draft !== !saved || !Number.isSafeInteger(result.data.configuration_version)) { setPreviewError("预览失败，请检查模板和所需的样例资料。未保存配置。"); return; }
      setPreview(result.data);
    } catch { if (!ctl.signal.aborted && isCurrent()) setPreviewError("预览读取失败，请重试。未保存配置。"); }
    finally { if (previewOwner.current === ctl) { previewOwner.current = null; if (isCurrent()) setPreviewBusy(false); } }
  };
  return <section className="card naming-templates-panel" aria-label="文件与目录命名模板"><div className="card-header"><h3>文件与目录命名模板</h3></div><div className="card-body">
    <p>电影和单集模板按版本保存。留空使用原规范命名；只影响新整理计划，已有计划及人工确认目标保持原路径。</p>
    <AutomationSettingsFeedback model={{ ...model, canRetry: model.canRetry && model.phase === "absent" }} />
    <div className="config-actions"><button type="button" className="btn-secondary" disabled={model.busy || previewBusy} onClick={() => void model.refresh()}>重新读取模板（放弃草稿）</button></div>
    {!model.ready && <p role="status">当前模板尚未读取成功，保存已锁定。</p>}
    <form aria-label="保存命名模板" onSubmit={event => { event.preventDefault(); if (!locked && !previewBusy && templates(draft)) void model.save({ expected_version: model.expectedVersion, value: { ...draft } }); }}><fieldset disabled={locked || previewBusy}><legend>命名草稿 · 基于版本 {model.ready || model.pending ? model.expectedVersion : "未知"}</legend>
      <label>电影模板<textarea rows={3} maxLength={4096} value={draft.movie_template} onChange={e => change({ ...draft, movie_template: e.target.value })} placeholder="Films/{title} ({year})/{title}.{extension}" /></label>
      <label>剧集单集模板<textarea rows={3} maxLength={4096} value={draft.episode_template} onChange={e => change({ ...draft, episode_template: e.target.value })} placeholder="Series/{title}/Season {season}/{episode_key}.{extension}" /></label>
      <p>常用字段：{"{title}、{year}、{tmdb_id}、{imdb_id}、{extension}、{resolution}、{source}、{codec}、{season}、{episode}、{episode_key}"}。字段缺少资料会拒绝预览；字段值不会增加目录层级。</p>
      <button className="btn-primary" type="submit" disabled={!templates(draft)}>{model.pending ? "使用原键保存相同模板" : "保存模板"}</button>
    </fieldset></form>
    <section className="config-editor" aria-label="命名样例预览"><h4>样例预览</h4><p>以下为可修改的示例资料。预览不读取视频、不保存模板，也不授予整理权限。</p><fieldset disabled={previewBusy || model.busy || !model.ready}><legend>样例资料（未知值留空）</legend><div className="naming-sample-grid">
      <label>媒体类型<select value={sample.media_type} onChange={e => { invalidate(); setSample({ ...sample, media_type: e.target.value as "movie" | "tv" }); }}><option value="movie">电影</option><option value="tv">剧集</option></select></label>
      {([ ["title", "标题"], ["year", "年份"], ["extension", "原扩展名"], ["tmdb_id", "TMDB ID"], ["imdb_id", "IMDb ID"], ["release_title", "资源标题（品质样例）"], ...(sample.media_type === "tv" ? [["season", "季编号"], ["episode", "集编号"]] : []) ] as [Exclude<keyof typeof sample,"media_type">,string][]).map(([field,label]) => <label key={field}>{label}<input value={sample[field]} maxLength={1000} onChange={e => { invalidate(); setSample({ ...sample, [field]: e.target.value }); }} /></label>)}
    </div><div className="config-actions"><button type="button" className="btn-secondary" onClick={() => void runPreview(false)}>预览草稿模板</button><button type="button" className="btn-secondary" onClick={() => void runPreview(true)}>预览已存模板</button></div></fieldset>
      {previewError && <p role="alert" className="config-error">{previewError}</p>}{preview && model.ready && <div role="status" className="naming-preview-result"><p>{preview.draft ? "草稿模板" : "已存模板"} · 配置版本 {preview.configuration_version}</p><code>{preview.relative_path}</code><p>仅为样例相对路径，尚未执行整理。</p></div>}
    </section>
  </div></section>;
}

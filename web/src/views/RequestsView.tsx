import React, { useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { useAuth } from "@/features/auth/AuthContext";
import { useMediaRequests, type ApprovalOptions } from "@/features/media-requests/useMediaRequests";
import { discoveryPrefill, intentFromInputs, sameIntent } from "@/features/media-requests/commandJournal";
import { requestStateLabel, type MediaRequest } from "@/features/media-requests/types";
import "@/features/media-requests/media-requests.css";

type Requests = ReturnType<typeof useMediaRequests>;
const time = (value: string) => new Date(value).toLocaleString();

function ApprovalForm({ row, options, requests }: { row: MediaRequest; options: ApprovalOptions; requests: Requests }) {
  const [root, setRoot] = useState(""), [policy, setPolicy] = useState(""), [downloader, setDownloader] = useState("");
  const [indexers, setIndexers] = useState<string[]>([]);
  const roots = options.roots.filter(r => r.media_type === row.intent.media_type);
  const ready = roots.some(r => r.id === root) && options.policies.some(p => p.id === policy) && options.downloaders.some(d => d.id === downloader) && indexers.length > 0 && indexers.every(id => options.indexers.some(i => i.id === id));
  return <form aria-label={`批准请求 ${row.id}`} onSubmit={event => { event.preventDefault(); if (ready) void requests.approve(row, { expected_version: row.version, root_id: root, quality_policy_id: policy, indexer_ids: indexers, downloader_id: downloader }); }}>
    <fieldset disabled={!requests.canWrite}><legend>批准并创建订阅</legend>
      <div className="requests-fields">
        <label>在线目标根<select required value={root} onChange={e => setRoot(e.target.value)}><option value="">请选择目标根</option>{roots.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}</select></label>
        <label>质量策略<select required value={policy} onChange={e => setPolicy(e.target.value)}><option value="">请选择质量策略</option>{options.policies.map(p => <option key={p.id} value={p.id}>{p.name} · 版本 {p.revision}</option>)}</select></label>
        <label>下载器<select required value={downloader} onChange={e => setDownloader(e.target.value)}><option value="">请选择下载器</option>{options.downloaders.map(d => <option key={d.id} value={d.id}>{d.name}</option>)}</select></label>
      </div>
      {roots.length === 0 && <p>没有类型匹配、在线且完成基线扫描的目标根。</p>}
      <fieldset><legend>检索索引器（至少选择一个）</legend><div className="requests-checks">{options.indexers.map(i => <label key={i.id}><input type="checkbox" checked={indexers.includes(i.id)} onChange={e => setIndexers(old => e.target.checked ? [...old, i.id] : old.filter(id => id !== i.id))} />{i.name} · {i.health === "online" ? "在线" : "离线或未知"}</label>)}</div>{options.indexers.length === 0 && <p>尚未配置索引器，请先在站点页面配置。</p>}</fieldset>
      <p>批准会创建正式订阅，并使其可由后台调度；下载和入库结果请查看订阅状态。</p>
      <button type="submit" disabled={!ready || !requests.canWrite}>批准并创建订阅</button>
    </fieldset>
  </form>;
}
function RequestRow({ row, requests, userId, admin }: { row: MediaRequest; requests: Requests; userId: string; admin: boolean }) {
  const [reason, setReason] = useState("");
  const own = row.owner_id === userId;
  return <li className="requests-row" data-request-id={row.id}>
    <div className="requests-row-heading"><h3>{row.intent.media_type === "movie" ? "电影" : "剧集"} · TMDB {row.intent.tmdb_id}</h3><strong>{requestStateLabel[row.state]}</strong></div>
    <dl><div><dt>请求 ID</dt><dd>{row.id}</dd></div>{admin && <div><dt>申请人 ID</dt><dd>{row.owner_id}{own ? "（本人）" : ""}</dd></div>}<div><dt>提交时间</dt><dd><time dateTime={row.created_at}>{time(row.created_at)}</time></dd></div><div><dt>更新时间</dt><dd><time dateTime={row.updated_at}>{time(row.updated_at)}</time></dd></div>{row.decided_at && <div><dt>决定时间</dt><dd><time dateTime={row.decided_at}>{time(row.decided_at)}</time></dd></div>}<div><dt>季范围</dt><dd>{row.intent.media_type === "movie" ? "不适用" : row.intent.seasons.length ? row.intent.seasons.join("、") : "全部常规季"}{row.intent.include_specials ? "，包含特别篇" : ""}</dd></div><div><dt>原因</dt><dd>{row.reason || "未填写"}</dd></div><div><dt>版本</dt><dd>{row.version}</dd></div></dl>
    {row.subscription_id && <p>订阅 ID：{row.subscription_id}{own && <> · <Link to={`/subscriptions?subscription=${encodeURIComponent(row.subscription_id)}`}>查看本人订阅</Link></>}</p>}
    {row.state === "approved" && <p>已批准并创建订阅，可由后台调度。此状态不表示下载成功。</p>}
    {row.state === "pending" && (admin || own) && <details><summary>处理此请求</summary>
      {admin && (requests.optionsLoading ? <p role="status">正在读取审批选项…</p> : requests.optionsError ? <p role="alert">{requests.optionsError}</p> : requests.options ? <ApprovalForm row={row} options={requests.options} requests={requests} /> : <p>审批选项尚未读取。</p>)}
      <form aria-label={`关闭请求 ${row.id}`} onSubmit={e => e.preventDefault()}><fieldset disabled={!requests.canWrite}><legend>{admin ? "拒绝或撤回" : "撤回本人请求"}</legend><label>原因{admin ? "（拒绝时必填）" : "（可选）"}<textarea maxLength={2000} rows={2} value={reason} onChange={e => setReason(e.target.value)} /></label><div className="requests-actions">{admin && <button type="button" disabled={!requests.canWrite || !reason.trim()} onClick={() => void requests.close(row, { expected_version: row.version, action: "reject", reason })}>拒绝请求</button>}{own && <button type="button" disabled={!requests.canWrite} onClick={() => void requests.close(row, { expected_version: row.version, action: "withdraw", reason })}>撤回本人请求</button>}</div></fieldset></form>
    </details>}
  </li>;
}
function RequestsWorkspace({ userId, role, search }: { userId: string; role: string; search: string }) {
  const pendingOnly = role === "admin" && new URLSearchParams(search).get("state") === "pending";
  const requests = useMediaRequests({ userId, role, pendingOnly }), prefill = discoveryPrefill(search);
  const [mediaType, setMediaType] = useState(prefill.mediaType), [tmdb, setTmdb] = useState(prefill.tmdbId), [seasons, setSeasons] = useState(""), [specials, setSpecials] = useState(false), [formError, setFormError] = useState("");
  const admin = role === "admin";
  return <section className="requests-view" aria-labelledby="requests-title">
    <header className="requests-heading"><h1 id="requests-title">媒体请求</h1><Link to="/discover">去发现页面选择作品</Link></header>
    <section aria-labelledby="requests-create-title"><h2 id="requests-create-title">提交媒体请求</h2>
      <p>使用 TMDB 作品编号提交，由管理员审核后创建正式订阅。</p>
      {prefill.title && <p className="requests-handoff">发现页显示名称：{prefill.title}。请核对下方类型与 TMDB 编号；名称仅供参考。</p>}
      <form aria-label="提交媒体请求" onSubmit={e => { e.preventDefault(); const body = intentFromInputs(mediaType, tmdb, seasons, specials); if (!body) { setFormError("请选择电影或剧集，并输入正的安全整数 TMDB 编号。季列表使用逗号分隔且不可重复；第 0 季需要勾选特别篇。"); return; } setFormError(""); void requests.create(body); }}>
        <fieldset disabled={!requests.canWrite}><legend>作品与范围</legend><div className="requests-fields"><label>媒体类型<select required value={mediaType} onChange={e => { setMediaType(e.target.value); setSeasons(""); setSpecials(false); }}><option value="">请选择媒体类型</option><option value="movie">电影</option><option value="tv">剧集</option></select></label><label>TMDB 编号<input required inputMode="numeric" pattern="[1-9][0-9]*" maxLength={16} value={tmdb} onChange={e => setTmdb(e.target.value)} /></label>{mediaType === "tv" && <label>季列表（可选）<input inputMode="text" maxLength={600} placeholder="例如 1,3" value={seasons} onChange={e => setSeasons(e.target.value)} aria-describedby="requests-season-help" /></label>}</div>
          {mediaType === "tv" && <><p id="requests-season-help">留空选择全部常规季。第 0 季仅在包含特别篇时有效。</p><label className="requests-check"><input type="checkbox" checked={specials} onChange={e => setSpecials(e.target.checked)} />包含特别篇</label></>}
          <button type="submit" disabled={!requests.canWrite}>提交请求</button>
        </fieldset>
        {formError && <p role="alert">{formError}</p>}
      </form>
    </section>
    <section aria-labelledby="requests-command-title" className="requests-command" aria-busy={requests.busy}><h2 id="requests-command-title">命令确认</h2>
      {requests.busy && <p role="status">正在处理，请等待结果确认…</p>}
      {requests.message && <p role={requests.phase === "blocked" || requests.phase === "protected" ? "alert" : "status"}>{requests.message}</p>}
      {requests.pending ? <><p>原命令：{requests.pending.kind === "create" ? "提交" : requests.pending.kind === "approve" ? "批准" : "关闭"} · {requests.pending.key}</p><p>TMDB {requests.pending.intent.tmdb_id} · {requests.pending.intent.media_type === "movie" ? "电影" : "剧集"}。确认前暂不能提交其他写操作。</p><div className="requests-actions"><button type="button" disabled={requests.busy} onClick={() => void requests.readReceipt()}>读取原回执</button>{requests.phase === "missing" && <button type="button" disabled={requests.busy} onClick={() => void requests.retryOriginal()}>以原键和原内容重试</button>}{["conflict", "verified-conflict"].includes(requests.phase) && <button type="button" disabled={requests.busy} onClick={() => void requests.readCurrent()}>读取当前请求</button>}{requests.phase === "conflict" && requests.pending.kind === "create" && requests.items.filter(row => row.owner_id === requests.pending?.ownerId && !!requests.pending && sameIntent(row.intent, requests.pending.intent)).map(row => <button key={row.id} type="button" disabled={requests.busy} onClick={() => void requests.readCurrent(row.id)}>读取匹配请求 {row.id}</button>)}{requests.phase === "verified-conflict" && <button type="button" disabled={requests.busy} onClick={() => void requests.finishConflict()}>结束冲突（仅本地）</button>}</div>{requests.current && <p>当前状态：{requestStateLabel[requests.current.state]} · 版本 {requests.current.version} · {time(requests.current.updated_at)}</p>}</> : requests.phase === "idle" ? <p>当前没有待确认命令。</p> : null}
    </section>
    <section aria-labelledby="requests-list-title" aria-busy={requests.loading}><div className="requests-heading"><h2 id="requests-list-title">{admin ? pendingOnly ? "待审批请求" : "全部成员请求" : "我的请求"}</h2><div className="requests-actions"><button type="button" disabled={requests.loading} onClick={() => void requests.refresh(requests.cursor)}>刷新当前页</button>{admin && <button type="button" disabled={requests.optionsLoading} onClick={() => void requests.loadOptions()}>重新读取审批选项</button>}</div></div>
      <p>每页最多 25 条。分页仅表示此次读取的记录，不代表媒体库完整性。</p>
      {requests.loading ? <p role="status">正在读取请求…</p> : requests.listError ? <p role="alert">{requests.listError}</p> : requests.loaded && requests.items.length === 0 ? <p role="status">此页没有可见请求。</p> : <ul className="requests-list">{requests.items.map(row => <RequestRow key={`${row.id}:${row.version}`} row={row} requests={requests} userId={userId} admin={admin} />)}</ul>}
      <nav className="requests-actions" aria-label="请求分页">{requests.cursor && <button type="button" disabled={requests.loading} onClick={() => void requests.refresh()}>返回第一页</button>}{requests.next && <button type="button" disabled={requests.loading} onClick={() => void requests.refresh(requests.next)}>下一页</button>}</nav>
    </section>
  </section>;
}
export function RequestsView(): React.ReactElement {
  const { user } = useAuth(), location = useLocation();
  if (!user) return <section className="requests-view"><h1>媒体请求</h1><p role="status">请登录后查看和提交请求。</p></section>;
  return <RequestsWorkspace key={`${user.id}:${user.role}:${location.search}`} userId={user.id} role={user.role} search={location.search} />;
}
export default RequestsView;

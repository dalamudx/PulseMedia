import { useEffect, useRef, useState } from 'react';
import { useAuth } from '@/features/auth/AuthContext';
import { readJournal, request, runCommand, versionOK, type Journal } from './libraryMaintenanceCommand';
import './library-maintenance.css';

export type UpgradeRecyclePanelProps = { accountID: string };
type Item = { recycle_id: string; kind: 'movie' | 'tv'; old_file_id: string; replacement_file_id: string; root_id: string; original_relative_path: string; retained_until: string; version: string; status: 'retained' | 'restored' | 'cleaned' | 'unknown_requires_review'; size_bytes: number; allocated_bytes: number | null; link_count: number | null; usage_observed_at: string | null };
type Page = { items: Item[]; next_cursor: string };
type Preview = Item & { can_confirm: boolean; execution_allowed: false };
const statuses = { retained: '保留中', restored: '已恢复', cleaned: '已清理', unknown_requires_review: '结果不明，需人工复核' };
const bytes = (value: number) => `${value.toLocaleString('zh-CN')} B`;
function validItem(i: Item) {
  return i && ['movie', 'tv'].includes(i.kind) && i.recycle_id.startsWith(`${i.kind}:`) && versionOK(i.recycle_id) && versionOK(i.old_file_id) && versionOK(i.replacement_file_id) && versionOK(i.root_id) && versionOK(i.version) && typeof i.original_relative_path === 'string' && Object.hasOwn(statuses, i.status) && typeof i.retained_until === 'string' && Number.isSafeInteger(i.size_bytes) && i.size_bytes >= 0 && (i.allocated_bytes === null || Number.isSafeInteger(i.allocated_bytes) && i.allocated_bytes >= 0) && (i.link_count === null || Number.isSafeInteger(i.link_count) && i.link_count > 0) && (i.usage_observed_at === null || typeof i.usage_observed_at === 'string' && Number.isFinite(Date.parse(i.usage_observed_at)));
}
export function UpgradeRecyclePanel(props: UpgradeRecyclePanelProps) {
  const { user, checking, statusError } = useAuth();
  if (!user || user.id !== props.accountID || user.role !== 'admin' || checking || statusError) return null;
  return <RecycleList key={props.accountID} {...props} />;
}
function RecycleList({ accountID }: UpgradeRecyclePanelProps) {
  const [page, setPage] = useState<Page | null>(null); const [cursor, setCursor] = useState('');
  const [draftKind, setDraftKind] = useState(''); const [draftQuery, setDraftQuery] = useState('');
  const [filter, setFilter] = useState({ kind: '', query: '' });
  const [revision, setRevision] = useState(0); const [busy, setBusy] = useState(true); const [error, setError] = useState('');
  useEffect(() => {
    const ctl = new AbortController(); setBusy(true); setError(''); setPage(null);
    const params = new URLSearchParams({ cursor, limit: '20' });
    if (filter.kind) params.set('kind', filter.kind);
    if (filter.query) params.set('q', filter.query);
    void request('GET', `/recycle?${params}`, { signal: ctl.signal }).then(value => {
      const data = value as Page;
      if (!Array.isArray(data.items) || data.items.length > 20 || !data.items.every(validItem) || new Set(data.items.map(i => i.recycle_id)).size !== data.items.length || typeof data.next_cursor !== 'string' || data.next_cursor.length > 2048 || data.next_cursor !== '' && data.next_cursor === cursor || filter.kind && data.items.some(item => item.kind !== filter.kind)) throw Error('Invalid recycle page');
      if (!ctl.signal.aborted) setPage(data);
    }).catch(() => { if (!ctl.signal.aborted) setError('回收清单读取失败，请重新读取。'); }).finally(() => { if (!ctl.signal.aborted) setBusy(false); });
    return () => ctl.abort();
  }, [accountID, cursor, filter, revision]);
  return <section className="config-editor library-maintenance" aria-label="升级回收区" aria-busy={busy}>
    <div className="config-heading"><h3>升级回收区</h3><button className="btn-secondary" disabled={busy} onClick={() => setRevision(v => v + 1)}>重新读取清单</button></div>
    <p className="config-description">先检查原回收文件与实际占用，再单独确认恢复或到期清理。当前新版本文件会保留，恢复不覆盖已有路径。</p>
    <p className="config-description">磁盘分配大小是 inode 的缓存观测，不代表独占占用或可释放空间。存在多个硬链接时，删除此路径也不保证释放空间。</p>
    <form className="maintenance-filters" aria-label="筛选回收记录" onSubmit={event => { event.preventDefault(); setCursor(''); setFilter({ kind: draftKind, query: draftQuery.trim() }); setRevision(v => v + 1); }}>
      <label className="config-field">媒体类型<select aria-label="媒体类型" value={draftKind} onChange={event => setDraftKind(event.target.value)}><option value="">全部类型</option><option value="movie">电影</option><option value="tv">剧集</option></select></label>
      <label className="config-field">原路径关键词<input type="search" value={draftQuery} maxLength={128} onChange={event => setDraftQuery(event.target.value)} /></label>
      <button className="btn-secondary" type="submit">筛选</button>
      <button className="btn-secondary" type="button" onClick={() => { setDraftKind(''); setDraftQuery(''); setCursor(''); setFilter({ kind: '', query: '' }); setRevision(v => v + 1); }}>清除筛选</button>
    </form>
    {error ? <p className="config-error" role="alert">{error}</p> : null}
    {busy ? <p role="status">正在读取回收清单…</p> : null}
    {page?.items.length === 0 ? <p role="status">{filter.kind || filter.query ? '暂无符合筛选条件的回收文件。' : '暂无回收文件。'}</p> : null}
    {page?.items.map(item => <RecycleItem key={`${item.recycle_id}:${item.version}`} item={item} accountID={accountID} />)}
    {page ? <nav className="maintenance-actions" aria-label="回收清单分页">{cursor ? <button className="btn-secondary" disabled={busy} onClick={() => setCursor('')}>返回首页</button> : null}<button className="btn-secondary" disabled={busy || !page.next_cursor} onClick={() => setCursor(page.next_cursor)}>下一页</button><span>本页 {page.items.length} 项，每页最多 20 项</span></nav> : null}
  </section>;
}
function RecycleItem({ item, accountID }: { item: Item; accountID: string }) {
  const resource = `recycle:${item.recycle_id}`; const [journal, setJournal] = useState<Journal | null>(null);
  const [action, setAction] = useState<'restore' | 'cleanup' | null>(null); const [confirmed, setConfirmed] = useState(false);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [ready, setReady] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  const live = useRef(false); const inFlight = useRef(false); const ctl = useRef<AbortController | null>(null);
  useEffect(() => { live.current = true; try { setJournal(readJournal(localStorage, accountID, resource)); setReady(true); } catch { setError('本地命令记录不可读取，请恢复浏览器存储后重新打开。'); } return () => { live.current = false; ctl.current?.abort(); }; }, [accountID, resource]);
  const observed = preview ?? item;
  const retained = observed.status === 'retained'; const cleanupAllowed = retained && Number.isFinite(Date.parse(observed.retained_until)) && Date.parse(observed.retained_until) <= Date.now();
  const blocked = busy || !ready || !navigator.locks?.request || !retained;
  async function inspect(nextAction: 'restore' | 'cleanup') {
    if (inFlight.current || blocked || journal || nextAction === 'cleanup' && !cleanupAllowed) return;
    inFlight.current = true; setBusy(true); setError(''); setAction(null); setPreview(null); setConfirmed(false);
    const c = new AbortController(); ctl.current = c;
    try {
      const p = await request('POST', '/recycle-previews', { signal: c.signal, body: { recycle_id: item.recycle_id, action: nextAction } }) as Preview;
      if (!validItem(p) || p.recycle_id !== item.recycle_id || p.kind !== item.kind || p.old_file_id !== item.old_file_id || p.replacement_file_id !== item.replacement_file_id || p.root_id !== item.root_id || p.original_relative_path !== item.original_relative_path || typeof p.can_confirm !== 'boolean' || p.execution_allowed !== false) throw Error('Invalid recycle preview');
      if (live.current) { setPreview(p); setAction(nextAction); }
    } catch { if (live.current) setError('原回收文件检查失败，请重新检查后再确认。'); }
    finally { inFlight.current = false; if (live.current) setBusy(false); }
  }
  async function command(sending: boolean) {
    if (inFlight.current || !ready || sending && (!confirmed || !action || !preview?.can_confirm || !retained || action === 'cleanup' && !cleanupAllowed)) return;
    inFlight.current = true; setBusy(true); setError(''); const c = new AbortController(); ctl.current = c;
    try {
      await runCommand({ storage: localStorage, locks: navigator.locks, account: accountID, resource, fileID: item.old_file_id, live: () => live.current, saved: setJournal,
        body: sending && action && preview ? { action, recycle_id: preview.recycle_id, expected_version: preview.version, confirmed: true } : undefined,
        transport: (entry, send) => send ? request('POST', '/commands', { signal: c.signal, params: { header: { 'Idempotency-Key': entry.key } }, body: entry.body }) : request('GET', `/commands/${encodeURIComponent(entry.key)}`, { signal: c.signal }) });
    } catch { if (live.current) { setError('命令结果尚未确认。请读取原回执；即使未找到回执，也不会重新发送。'); try { setJournal(readJournal(localStorage, accountID, resource)); } catch { setReady(false); } } }
    finally { inFlight.current = false; if (live.current) setBusy(false); }
  }
  const measured = observed.usage_observed_at !== null;
  return <article aria-label={item.original_relative_path} aria-busy={busy}>
    <h4>{item.kind === 'movie' ? '电影' : '剧集'} · {item.original_relative_path}</h4>
    <dl><dt>状态</dt><dd>{statuses[observed.status]}</dd><dt>保留至</dt><dd>{Number.isFinite(Date.parse(observed.retained_until)) ? new Date(observed.retained_until).toLocaleString('zh-CN') : '未知，禁止清理'}</dd><dt>文件大小</dt><dd>{bytes(observed.size_bytes)}</dd><dt>磁盘分配大小</dt><dd>{measured && observed.allocated_bytes !== null ? bytes(observed.allocated_bytes) : '未知'}</dd><dt>硬链接数</dt><dd>{measured && observed.link_count !== null ? observed.link_count : '未知'}</dd><dt>占用观测时间</dt><dd>{measured ? new Date(observed.usage_observed_at!).toLocaleString('zh-CN') : '未知'}</dd></dl>
    {!navigator.locks?.request ? <p role="alert">浏览器不支持跨标签锁，命令已停用。</p> : null}
    {error ? <p role="alert" className="config-error">{error}</p> : null}
    {journal ? <><p role="status">{journal.receipt?.status === 'completed' ? `${journal.body.action === 'restore' ? '恢复' : '清理'}已完成，请重新读取清单。` : '原命令结果待核对，请保留原记录并转人工复核。'}</p>{journal.receipt?.status !== 'completed' ? <button className="btn-secondary" disabled={busy || !ready || !navigator.locks?.request} onClick={() => void command(false)}>读取原回执</button> : null}</> : <>
      <div className="maintenance-actions"><button className="btn-secondary" disabled={blocked} onClick={() => void inspect('restore')}>恢复原文件</button><button className="btn-secondary" disabled={blocked || !cleanupAllowed} onClick={() => void inspect('cleanup')}>清理回收文件</button></div>
      {retained && !cleanupAllowed ? <p>保留期尚未结束或时间未知，暂不能清理。</p> : null}
      {action && preview ? <div className="maintenance-review"><p role="status">{preview.can_confirm ? '已检查原回收文件，请核对本次观测后确认。' : '当前检查不允许确认，请重新读取清单或转人工复核。'}</p><p>{action === 'restore' ? '恢复到原路径，保留当前新版本；路径冲突时停止。' : '永久移除此回收路径，保留当前新版本；不保证释放磁盘空间。'}</p><label className="config-check"><input type="checkbox" checked={confirmed} disabled={blocked || !preview.can_confirm || action === 'cleanup' && !cleanupAllowed} onChange={e => setConfirmed(e.target.checked)} />我确认{action === 'restore' ? '恢复原回收文件' : '永久清理原回收文件'}</label><div className="maintenance-actions"><button className="btn-primary" disabled={blocked || !confirmed || !preview.can_confirm || action === 'cleanup' && !cleanupAllowed} onClick={() => void command(true)}>{action === 'restore' ? '确认恢复' : '确认永久清理'}</button><button className="btn-secondary" disabled={busy} onClick={() => { setAction(null); setPreview(null); setConfirmed(false); }}>取消</button></div></div> : null}
    </>}
  </article>;
}

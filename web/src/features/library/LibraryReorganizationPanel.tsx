import { useEffect, useRef, useState } from 'react';
import { useAuth } from '@/features/auth/AuthContext';
import { readJournal, relativePathOK, request, runCommand, versionOK, type Journal } from './libraryMaintenanceCommand';
import './library-maintenance.css';

export type LibraryReorganizationPanelProps = { fileID: string; accountID: string; onCompleted?: () => void };
type Preview = { file_id: string; root_id: string; source_relative_path: string; target_relative_path: string; version: string; can_confirm: true; execution_allowed: false };
export function LibraryReorganizationPanel(props: LibraryReorganizationPanelProps) {
  const { user, checking, statusError } = useAuth();
  if (!user || user.id !== props.accountID || user.role !== 'admin' || checking || statusError) return null;
  return <Reorganization key={`${props.accountID}:${props.fileID}`} {...props} />;
}
function Reorganization({ fileID, accountID, onCompleted }: LibraryReorganizationPanelProps) {
  const resource = `file:${fileID}`;
  const [target, setTarget] = useState(''); const [preview, setPreview] = useState<Preview | null>(null);
  const [confirmed, setConfirmed] = useState(false); const [journal, setJournal] = useState<Journal | null>(null);
  const [ready, setReady] = useState(false); const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  const live = useRef(false); const inFlight = useRef(false); const ctl = useRef<AbortController | null>(null);
  useEffect(() => { live.current = true; try { setJournal(readJournal(localStorage, accountID, resource)); setReady(true); } catch { setError('本地命令记录不可读取，请恢复浏览器存储后重新打开。'); } return () => { live.current = false; ctl.current?.abort(); }; }, [accountID, resource]);
  async function getPreview() {
    if (inFlight.current || journal || !relativePathOK(target)) return;
    inFlight.current = true; setBusy(true); setError(''); setPreview(null); setConfirmed(false);
    const c = new AbortController(); ctl.current = c;
    try {
      const p = await request('POST', '/reorganization-previews', { signal: c.signal, body: { file_id: fileID, target_relative_path: target } }) as Preview;
      if (p.file_id !== fileID || p.target_relative_path !== target || !relativePathOK(p.source_relative_path) || !versionOK(p.root_id) || !versionOK(p.version) || p.can_confirm !== true || p.execution_allowed !== false) throw Error('Invalid preview');
      if (live.current) setPreview(p);
    } catch { if (live.current) setError('无法读取当前文件与目标预览，请核对目标后重试。'); }
    finally { inFlight.current = false; if (live.current) setBusy(false); }
  }
  async function command(sending: boolean) {
    if (inFlight.current || !ready || (sending && (!preview || !confirmed))) return;
    inFlight.current = true; setBusy(true); setError('');
    const c = new AbortController(); ctl.current = c;
    try {
      const receipt = await runCommand({ storage: localStorage, locks: navigator.locks, account: accountID, resource, fileID, live: () => live.current, saved: setJournal,
        body: sending && preview ? { action: 'reorganize', file_id: fileID, target_relative_path: preview.target_relative_path, expected_version: preview.version, confirmed: true } : undefined,
        transport: (entry, send) => send ? request('POST', '/commands', { signal: c.signal, params: { header: { 'Idempotency-Key': entry.key } }, body: entry.body }) : request('GET', `/commands/${encodeURIComponent(entry.key)}`, { signal: c.signal }) });
      if (live.current && receipt?.status === 'completed') onCompleted?.();
    } catch { if (live.current) { setError('命令结果尚未确认。请读取原回执；即使未找到回执，也不会重新发送。'); try { setJournal(readJournal(localStorage, accountID, resource)); } catch { setReady(false); } } }
    finally { inFlight.current = false; if (live.current) setBusy(false); }
  }
  return <section className="config-editor library-maintenance" aria-label="已有文件重整理" aria-busy={busy}>
    <h3>已有文件重整理</h3><p className="config-description">将选中文件整理到同一库根下的新路径。保留映射历史，不覆盖现有文件。</p>
    {!navigator.locks?.request ? <p role="alert">浏览器不支持跨标签锁，命令已停用。</p> : null}
    {error ? <p className="config-error" role="alert">{error}</p> : null}
    {journal ? <><p role="status">{journal.receipt?.status === 'completed' ? '重整理已完成。' : '原命令结果待核对，请保留原记录并转人工复核。'}</p>
      {journal.receipt?.status !== 'completed' ? <button className="btn-secondary" disabled={busy || !ready || !navigator.locks?.request} onClick={() => void command(false)}>读取原回执</button> : null}</> : <>
      <label className="config-field">目标相对路径<input value={target} disabled={busy || !ready} placeholder="电影/片名 (年份)/片名.mkv" onChange={e => { setTarget(e.target.value); setPreview(null); setConfirmed(false); }} /></label>
      {target && !relativePathOK(target) ? <p role="status">请使用库根内的相对路径，不含空路径段、上级目录或反斜杠。</p> : null}
      <button className="btn-secondary" disabled={busy || !ready || !relativePathOK(target)} onClick={() => void getPreview()}>预览重整理</button>
      {preview ? <div className="maintenance-review"><dl><dt>原路径</dt><dd>{preview.source_relative_path}</dd><dt>目标路径</dt><dd>{preview.target_relative_path}</dd></dl>
        <label className="config-check"><input type="checkbox" checked={confirmed} disabled={busy} onChange={e => setConfirmed(e.target.checked)} />我已核对原路径与目标路径，确认重整理</label>
        <button className="btn-primary" disabled={busy || !confirmed || !navigator.locks?.request} onClick={() => void command(true)}>确认重整理</button></div> : null}
    </>}
    {busy ? <p role="status">正在处理…</p> : null}
  </section>;
}

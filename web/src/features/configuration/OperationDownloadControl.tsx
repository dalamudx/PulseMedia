import { useEffect, useRef, useState } from 'react';
import { api } from '@/api/client';
import { useAuth } from '@/features/auth/AuthContext';

type Receipt = {command_id: string; operation_id: string; action: 'pause' | 'resume'; state: 'confirmed' | 'unknown'};
type Journal = {version: 1; account: string; operation: string; key: string; action: 'pause' | 'resume'; shared: boolean; receipt?: Receipt};
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
function validReceipt(value: unknown, entry: Journal): value is Receipt {
  if (!value || typeof value !== 'object') return false;
  const r = value as Receipt;
  return uuid.test(r.command_id) && r.operation_id === entry.operation && r.action === entry.action && (r.state === 'confirmed' || r.state === 'unknown');
}
function readJournal(storageKey: string, account: string, operation: string): Journal | null {
  const raw = localStorage.getItem(storageKey);
  if (!raw) return null;
  const item = JSON.parse(raw) as Journal;
  if (item.version !== 1 || item.account !== account || item.operation !== operation || !uuid.test(item.key) || (item.action !== 'pause' && item.action !== 'resume') || typeof item.shared !== 'boolean' || (item.receipt && !validReceipt(item.receipt, item))) throw new Error('invalid journal');
  return item;
}
export function OperationDownloadControl({operationId, contextReady = true}: {operationId: string; contextReady?: boolean}) {
  const {user} = useAuth();
  if (!user) return null;
  return <DownloadControl key={`${user.id}:${user.role}:${operationId}`} account={user.id} isAdmin={user.role === 'admin'} operationId={operationId} contextReady={contextReady} />;
}
function DownloadControl({account, isAdmin, operationId, contextReady}: {account: string; isAdmin: boolean; operationId: string; contextReady: boolean}) {
  const storageKey = `pulse.download-control.v1:${account}:${operationId}`;
  const [journal, setJournal] = useState<Journal | null>(null);
  const [action, setAction] = useState<'pause' | 'resume'>('pause');
  const [shared, setShared] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState('');
  const current = useRef(true);
  const sending = useRef(false);
  const controller = useRef<AbortController | null>(null);
  const lockAvailable = Boolean(navigator.locks?.request);
  useEffect(() => {
    current.current = true;
    try { setJournal(readJournal(storageKey, account, operationId)); setReady(true); }
    catch { setError('本地命令记录不可读取。请恢复浏览器存储后重新打开详情。'); }
    return () => { current.current = false; controller.current?.abort(); };
  }, [storageKey, account, operationId]);
  async function run(mode: 'send' | 'receipt' | 'observe' | 'next') {
    if (sending.current || !ready || !lockAvailable) return;
    sending.current = true; setBusy(true); setError('');
    const ctl = new AbortController(); controller.current = ctl;
    try {
      await navigator.locks.request(storageKey, {signal: ctl.signal}, async () => {
        let entry = readJournal(storageKey, account, operationId);
        if (!current.current) return;
        if (mode === 'next') {
          if (entry?.receipt?.state !== 'confirmed') return;
          localStorage.removeItem(storageKey); setJournal(null); setConfirmed(false); return;
        }
        if (mode === 'send') {
          // Every POST is preceded by durable identity; recovery never resends it.
          if (entry) { setJournal(entry); return; }
          if (!contextReady || !confirmed) return;
          entry = {version: 1, account, operation: operationId, key: crypto.randomUUID(), action, shared};
          localStorage.setItem(storageKey, JSON.stringify(entry)); setJournal(entry); setConfirmed(false);
        }
        if (!entry) return;
        const params = {path: {id: operationId}, header: {'Idempotency-Key': entry.key}};
        const result = mode === 'send'
          ? await api.POST('/api/v1/operations/{id}/download-control', {signal: ctl.signal, params, body: {action: entry.action, confirmed: true, affect_shared: entry.shared}})
          : mode === 'observe'
            ? await api.POST('/api/v1/operations/{id}/download-control/observations', {signal: ctl.signal, params})
            : await api.GET('/api/v1/operations/{id}/download-control', {signal: ctl.signal, cache: 'no-store', params});
        if (!current.current || ctl.signal.aborted) return;
        if (result.error || !validReceipt(result.data, entry)) {
          setError('原命令结果尚未确认。请读取原回执；若仍不明，可明确发起状态核对，勿创建替代命令。'); return;
        }
        const saved = {...entry, receipt: result.data};
        setJournal(saved); localStorage.setItem(storageKey, JSON.stringify(saved));
      });
    } catch {
      if (current.current) setError('连接或本地记录不可用。原命令已保留，请读取原回执，不会自动重发。');
    } finally {
      sending.current = false;
      if (current.current) { setBusy(false); controller.current = null; }
    }
  }
  const pending = journal !== null && journal.receipt?.state !== 'confirmed';
  return <section className="config-editor" aria-label="原生下载暂停与恢复">
    <h4>下载暂停与恢复</h4>
    <p className="config-description">控制原下载任务。自动剧集、整季包和电影升级仅支持尚未完成的下载任务。暂停仍保留下载需求和占位；共享任务需管理员明确确认。</p>
    {!lockAvailable ? <p role="alert">当前浏览器不支持跨标签锁，下载控制已停用。</p> : null}
    {error ? <p role="alert" className="config-error">{error}</p> : null}
    {busy ? <p role="status">正在处理原命令…</p> : null}
    {journal ? <>
      <p role="status">原命令：{journal.action === 'pause' ? '暂停' : '恢复'} · {pending ? '结果尚未确认' : '已确认'}</p>
      {pending ? <div className="config-actions">
        <button type="button" className="btn-secondary" disabled={busy || !ready || !lockAvailable} onClick={() => void run('receipt')}>读取原回执</button>
        <button type="button" className="btn-secondary" disabled={busy || !ready || !lockAvailable} onClick={() => void run('observe')}>核对下载器当前状态</button>
      </div> : <button type="button" className="btn-secondary" disabled={busy || !ready || !lockAvailable} onClick={() => void run('next')}>准备下一条控制命令</button>}
    </> : <>
      <label className="config-field">操作<select value={action} disabled={busy} onChange={e => { setAction(e.target.value as 'pause' | 'resume'); setConfirmed(false); }}><option value="pause">暂停下载</option><option value="resume">恢复下载</option></select></label>
      {isAdmin ? <label className="config-check"><input type="checkbox" checked={shared} disabled={busy} onChange={e => { setShared(e.target.checked); setConfirmed(false); }} />允许影响此任务的其他共享需求</label> : null}
      <label className="config-check"><input type="checkbox" checked={confirmed} disabled={busy || !contextReady} onChange={e => setConfirmed(e.target.checked)} />确认{action === 'pause' ? '暂停' : '恢复'}原下载任务{shared ? '及其共享需求' : ''}</label>
      <button type="button" className="btn-primary" disabled={busy || !ready || !confirmed || !contextReady || !lockAvailable} onClick={() => void run('send')}>确认{action === 'pause' ? '暂停' : '恢复'}</button>
      {!contextReady ? <p>操作详情尚未刷新，请先读取当前状态。</p> : null}
    </>}
  </section>;
}

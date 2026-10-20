import { useEffect, useId, useState } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { api } from '@/api/client';
import { useAuth } from '@/features/auth/AuthContext';
import { accountStatsView, statsReadError, type StatsObservation, type StatsSource } from './accountStatsView';
import './site-account-stats.css';

type ReadState = { phase: 'idle' | 'loading' | 'ready' | 'error'; observation: StatsObservation | null; error: string };
const emptyRead: ReadState = { phase: 'idle', observation: null, error: '' };

export function SiteAccountStatsPanel(props: StatsSource) {
  const { user, checking } = useAuth();
  if (checking || !user || user.role !== 'admin') return null;
  return <AccountStatsSection key={JSON.stringify([user.id, user.role, props.sourceID, props.sourceVersion])} {...props} />;
}

function AccountStatsSection({ sourceID, sourceVersion }: StatsSource) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [forbidden, setForbidden] = useState(false);
  const [read, setRead] = useState<ReadState>(emptyRead);
  useEffect(() => {
    const hide = () => {
      if (document.hidden) { setOpen(false); setRead(emptyRead); }
    };
    document.addEventListener('visibilitychange', hide);
    return () => document.removeEventListener('visibilitychange', hide);
  }, []);
  useEffect(() => {
    if (!open || forbidden || document.hidden) return;
    const controller = new AbortController();
    let active = true;
    setRead({ phase: 'loading', observation: null, error: '' });
    void (async () => {
      try {
        const { data, response } = await api.GET('/api/v1/indexers/{indexer_id}/account-stats', {
          params: { path: { indexer_id: sourceID } }, signal: controller.signal, cache: 'no-store',
        });
        if (!active || controller.signal.aborted) return;
        if (!response.ok || !data) {
          if (response.status === 403) setForbidden(true);
          setRead({ phase: 'error', observation: null, error: statsReadError(response.status) });
          return;
        }
        setRead({ phase: 'ready', observation: data.observation, error: '' });
      } catch {
        if (active && !controller.signal.aborted) setRead({ phase: 'error', observation: null, error: statsReadError(0) });
      }
    })();
    return () => { active = false; controller.abort(); };
  }, [open, refresh, sourceID, sourceVersion, forbidden]);
  const view = read.phase === 'ready' ? accountStatsView(read.observation, { sourceID, sourceVersion }) : null;
  return <section className="site-account-stats" aria-label="账号统计">
    <button type="button" className="btn-sm btn-secondary" aria-expanded={open} aria-controls={id} disabled={forbidden}
      onClick={() => { setRead(emptyRead); setOpen(value => !value); }}>
      {open ? <ChevronDown size={16} aria-hidden="true" /> : <ChevronRight size={16} aria-hidden="true" />}账号统计
    </button>
    {forbidden && <p role="alert">{statsReadError(403)}</p>}
    {open && <div id={id} aria-busy={read.phase === 'loading'}>
      <div className="site-account-stats-toolbar">
        <p role="status">{read.phase === 'loading' ? '正在读取已保存统计…' : view?.status}</p>
        <button type="button" className="btn-sm btn-secondary" disabled={forbidden || read.phase === 'loading'}
          onClick={() => { setRead({ phase: 'loading', observation: null, error: '' }); setRefresh(value => value + 1); }}>刷新</button>
      </div>
      {read.phase === 'error' && !forbidden && <p role="alert">{read.error}</p>}
      {view && <>
        <dl>{['上传量', '下载量', '站点分享率', '魔力值'].map((label, index) => <div key={label}><dt>{label}</dt><dd>{view.values[index]}</dd></div>)}</dl>
        <p>采集时间：{view.observedAt ? <time dateTime={view.observedAt}>{new Date(view.observedAt).toLocaleString('zh-CN')}</time> : '未知'}</p>
      </>}
    </div>}
  </section>;
}

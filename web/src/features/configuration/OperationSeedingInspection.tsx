import { useEffect, useRef, useState } from 'react';
import { api } from '@/api/client';
import { useAuth } from '@/features/auth/AuthContext';

type Inspection = {
  observed_at: string;
  complete: boolean;
  share_ratio: number | null;
  infinite_share_ratio: boolean;
  seeding_seconds: string | null;
};

function validInspection(value: unknown): value is Inspection {
  if (!value || typeof value !== 'object') return false;
  const result = value as Inspection;
  return typeof result.observed_at === 'string' && Number.isFinite(Date.parse(result.observed_at))
    && typeof result.complete === 'boolean' && typeof result.infinite_share_ratio === 'boolean'
    && (result.share_ratio === null || (typeof result.share_ratio === 'number' && Number.isFinite(result.share_ratio) && result.share_ratio >= 0 && !result.infinite_share_ratio))
    && (result.seeding_seconds === null || (typeof result.seeding_seconds === 'string' && /^(0|[1-9][0-9]*)$/.test(result.seeding_seconds)));
}

export function OperationSeedingInspection({ operationId, contextReady = true }: { operationId: string; contextReady?: boolean }) {
  const { user } = useAuth();
  if (!user || (user.role !== 'admin' && user.role !== 'member')) return null;
  return <SeedingInspection key={`${user.id}:${user.role}:${operationId}`} operationId={operationId} contextReady={contextReady} />;
}

function SeedingInspection({ operationId, contextReady }: { operationId: string; contextReady: boolean }) {
  const [inspection, setInspection] = useState<Inspection | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const mounted = useRef(false);
  const ready = useRef(contextReady);
  const controller = useRef<AbortController | null>(null);
  ready.current = contextReady;

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; controller.current?.abort(); };
  }, []);
  useEffect(() => {
    if (!contextReady) {
      controller.current?.abort(); controller.current = null;
      setInspection(null); setError(''); setBusy(false);
    }
  }, [contextReady]);

  async function inspect() {
    if (!ready.current || controller.current) return;
    const request = new AbortController();
    controller.current = request;
    setInspection(null); setError(''); setBusy(true);
    try {
      const result = await api.POST('/api/v1/operations/{id}/seeding-observations', {
        params: { path: { id: operationId } }, body: {}, signal: request.signal, cache: 'no-store',
      });
      if (!mounted.current || !ready.current || request.signal.aborted || controller.current !== request) return;
      if (result.error || !validInspection(result.data)) {
        setError('做种信息暂不可用。请刷新操作详情后再次读取。');
      } else {
        setInspection(result.data);
      }
    } catch {
      if (mounted.current && ready.current && !request.signal.aborted && controller.current === request) {
        setError('做种信息读取失败。请检查连接后再次读取。');
      }
    } finally {
      if (mounted.current && controller.current === request) { controller.current = null; setBusy(false); }
    }
  }

  return <fieldset className="config-fieldset" disabled={!contextReady || busy} aria-label="做种信息">
    <legend>做种信息</legend>
    <p className="config-description">手动读取原下载任务的当前信息。未知数据保持未知。</p>
    <button type="button" className="btn-secondary" onClick={() => void inspect()}>{busy ? '正在读取做种信息…' : '读取做种信息'}</button>
    {!contextReady ? <p>请先刷新操作详情。</p> : null}
    {busy ? <p role="status">正在读取下载器…</p> : null}
    {contextReady && error ? <p role="alert" className="config-error">{error}</p> : null}
    {contextReady && inspection ? <dl aria-live="polite">
      <dt>读取时间</dt><dd><time dateTime={inspection.observed_at}>{new Date(inspection.observed_at).toLocaleString()}</time></dd>
      <dt>下载完成</dt><dd>{inspection.complete ? '已完成' : '未完成'}</dd>
      <dt>分享率</dt><dd>{inspection.infinite_share_ratio ? '无限' : inspection.share_ratio === null ? '未知' : String(inspection.share_ratio)}</dd>
      <dt>原生做种时长</dt><dd>{inspection.seeding_seconds === null ? '未知' : `${inspection.seeding_seconds} 秒`}</dd>
    </dl> : null}
  </fieldset>;
}

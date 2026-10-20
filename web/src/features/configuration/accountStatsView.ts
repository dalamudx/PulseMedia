import type { components } from '../../api/schema';

export type StatsObservation = components['schemas']['SiteAccountStatsObservation'];
type Amount = components['schemas']['SiteAccountStatsAmount'];
type Ratio = components['schemas']['SiteAccountStatsRatio'];
export type StatsSource = { sourceID: string; sourceVersion: number };

export function statsAmount(value: Amount | null | undefined): string {
  return value ? `${value.approximate ? '约 ' : ''}${value.value} ${value.unit}` : '未知';
}
export function statsRatio(value: Ratio | null | undefined): string {
  if (!value) return '未知';
  switch (value.state) {
    case 'finite': return value.value ?? '未知';
    case 'infinite': return '∞';
    case 'undefined': return '未定义';
    case 'unknown': return '未知';
  }
}
const failureText: Record<NonNullable<StatsObservation['failure_code']>, string> = {
  auth_required: '认证失效', blocked: '站点拒绝访问', rate_limited: '采集频率受限',
  bad_response: '站点响应无法识别', unavailable: '采集服务暂不可用', interrupted: '采集中断',
};
export function statsReadError(status: number): string {
  switch (status) {
    case 401: return '会话已失效，请重新登录。';
    case 403: return '当前账户无权读取账号统计。';
    case 404: return '站点不存在，请刷新站点列表。';
    default: return '账号统计读取失败，请重试刷新。';
  }
}
export function accountStatsView(observation: StatsObservation | null, source: StatsSource) {
  if (!observation) return { status: '尚未配置统计采集', stale: false, observedAt: null, values: ['未知', '未知', '未知', '未知'] };
  const { config, stats } = observation;
  // A response for another source or PT account must never supply displayed metrics.
  if (config.indexer_id !== source.sourceID || (stats && stats.account_id !== config.expected_account_id)) {
    return { status: '统计身份不匹配，请刷新站点列表。', stale: true, observedAt: null, values: ['未知', '未知', '未知', '未知'] };
  }
  const stale = observation.stale || config.source_config_version !== source.sourceVersion ||
    (!!stats && (observation.sample_source_version !== source.sourceVersion || observation.sample_config_version !== config.version)) ||
    observation.collection_state === 'failed' || observation.collection_state === 'review_required';
  const stateText = !config.enabled || observation.collection_state === 'disabled' ? '采集已停用'
    : observation.failure_code ? failureText[observation.failure_code]
    : observation.collection_state === 'running' ? '正在采集'
    : observation.collection_state === 'queued' ? '等待采集'
    : stats ? '已保存统计' : '尚无有效采集记录';
  return {
    status: `${stateText}${stale ? ' · 陈旧' : ''}${stats && stale ? ' · 上次有效记录' : ''}`,
    stale, observedAt: stats ? observation.observed_at : null,
    values: [statsAmount(stats?.uploaded), statsAmount(stats?.downloaded), statsRatio(stats?.ratio), stats?.bonus ?? '未知'],
  };
}

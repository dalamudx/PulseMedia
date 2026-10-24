import React, { useId, useRef, useState } from "react";
import { X, Rss, Search, Star } from "lucide-react";
import type { MediaItem } from "@/components/cards/MediaCard";
import { useAuth } from "@/features/auth/AuthContext";
import { useDialogFocus } from "@/features/configuration/useDialogFocus";
import { useMediaDetail } from "@/features/configuration/useMediaDetail";
import { episodeKey, safeTMDBImage } from "@/features/configuration/mediaDetailSelection";
import type { MovieDetail } from "@/features/configuration/mediaDetailSelection";
import { EndpointBindingFields } from "@/features/configuration/EndpointBindingFields";
import { PolicySelector } from "@/features/configuration/PolicySelector";
import { RootSelector } from "@/features/configuration/RootSelector";
import "@/features/configuration/media-detail.css";

interface MediaDetailModalProps {
  media: MediaItem | null;
  subscribing?: boolean;
  onClose: () => void;
  onRequestApproval?: (media: MediaItem) => void;
  onSubscribe: (
    m: MediaItem,
    scope: "all" | "selected",
    seasons: number[],
    episodes: string[],
    includeSpecials: boolean,
    downloaderId?: string,
    indexerIds?: string[],
    policyId?: string,
    rootId?: string
  ) => void;
  onSearchReleases: (m: MediaItem) => void;
}

function DetailImage({ src, alt, className, missing, lazy = false }: { src?: string; alt: string; className: string; missing: string; lazy?: boolean }) {
  const safe = safeTMDBImage(src);
  const [failed, setFailed] = useState<string>();
  return safe && failed !== safe
    ? <img className={className} src={safe} alt={alt} loading={lazy ? "lazy" : "eager"} decoding="async" referrerPolicy="no-referrer" onError={() => setFailed(safe)} />
    : <div className={`${className} media-detail-image-missing`} role="img" aria-label={missing}>{missing}</div>;
}

export const MediaDetailModal: React.FC<MediaDetailModalProps> = props => {
  const { user } = useAuth();
  if (!props.media) return null;
  // Reset local configuration before a different account, role or media can see it.
  const scope = JSON.stringify([user?.id, user?.role, props.media.media_type ?? "movie", props.media.tmdb_id]);
  return <MediaDetailDialog key={scope} {...props} media={props.media} />;
};

function MediaDetailDialog({ media, subscribing = false, onClose, onSubscribe, onSearchReleases, onRequestApproval }: MediaDetailModalProps & { media: MediaItem }) {
  const dialog = useRef<HTMLDivElement>(null);
  useDialogFocus(dialog, onClose);
  const titleId = useId(), seasonId = useId(), helpId = useId();
  const model = useMediaDetail(media, subscribing);
  const { detail, credits, seasons, selection, tv } = model;
  const data = detail.status === "ready" ? detail.data : null;
  const isTV = media.media_type === "tv";
  const sn = selection.activeSeason;
  const season = sn === null ? undefined : seasons[sn];
  const seasonSummary = tv?.seasons.find(s => s.season_number === sn);
  const selected = sn === null ? [] : selection.episodes[sn] ?? [];
  const custom = selection.mode === "selected";
  const chosenSeason = sn !== null && selection.selectedSeasons.includes(sn);
  const [indexerIds, setIndexerIds] = useState<string[]>([]);
  const [downloaderId, setDownloaderId] = useState("");
  const [bindingsReady, setBindingsReady] = useState(false);
  const [policyId, setPolicyId] = useState("default");
  const [policyReady, setPolicyReady] = useState(false);
  const [rootId, setRootId] = useState("");
  const [rootReady, setRootReady] = useState(false);
  const ready = model.canSubscribe && bindingsReady && policyReady && rootReady;
  const submit = () => {
    const intent = model.getSubscription();
    if (!ready || !intent) return;
    onSubscribe(media, intent.scope, intent.seasons, intent.episodes, intent.includeSpecials, downloaderId || undefined, [...indexerIds], policyId, rootId);
  };
  const episodeEditing = custom && chosenSeason && season?.status === "ready";
  const totalSelected = selection.selectedSeasons.reduce((n, s) => n + (selection.episodes[s]?.length ?? 0), 0);

  return <div className="modal-overlay media-detail-overlay" onClick={onClose}>
    <div ref={dialog} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} className="modal-card media-detail-dialog" onClick={event => event.stopPropagation()}>
      <header className="media-detail-header">
        <h2 id={titleId}>{data?.title ?? "影视详情"}</h2>
        <button type="button" className="btn-secondary media-detail-close" onClick={onClose} aria-label="关闭影视详情"><X size={20} aria-hidden="true" /></button>
      </header>
      <div className="media-detail-scroll">
        {(detail.status === "loading" || detail.status === "idle") && <div className="media-detail-status" role="status" aria-live="polite">正在读取影视详情…</div>}
        {detail.status === "error" && <div className="media-detail-status" role="alert"><p>{detail.error}</p><button type="button" className="btn-secondary" disabled={subscribing} onClick={model.retryDetail}>重试影视详情</button></div>}
        {data && <>
          <DetailImage src={data.backdrop_url ?? data.poster_url} alt={`${data.title}画报`} className="media-detail-artwork" missing="暂无影视画报" />
          <section className="media-detail-section" aria-label="影视资料">
            <p className="media-detail-meta">
              <span>{isTV ? "电视剧" : "电影"}</span>
              <span>{data.year ? `${data.year} 年` : "年份未知"}</span>
              {data.rating !== undefined && data.rating > 0 && <span><Star size={15} aria-hidden="true" /> {data.rating.toFixed(1)}</span>}
              {!isTV && (data as MovieDetail).runtime ? <span>{(data as MovieDetail).runtime} 分钟</span> : null}
              {tv && <span>{tv.seasons_count} 季 · {tv.episodes_count} 集（普通季）</span>}
            </p>
            {data.original_title && data.original_title !== data.title && <p className="media-detail-original">{data.original_title}</p>}
            {!!data.genres?.length && <p className="media-detail-muted">{data.genres.join(" / ")}</p>}
            <h3>剧情简介</h3><p className="media-detail-overview">{data.overview?.trim() || "暂无剧情简介。"}</p>
          </section>
        </>}

        <section className="media-detail-section" aria-label="演员">
          <div className="media-detail-section-heading"><h3>演员</h3>{credits.status === "ready" && <button type="button" className="btn-secondary" disabled={subscribing} onClick={model.retryCredits}>刷新演员</button>}</div>
          {(credits.status === "idle" || credits.status === "loading") && <p role="status">正在读取演员信息…</p>}
          {credits.status === "error" && <div role="alert"><p>{credits.error} 演员列表未确认；有效影视资料仍可用于订阅。</p><button type="button" className="btn-secondary" disabled={subscribing} onClick={model.retryCredits}>重试演员信息</button></div>}
          {credits.status === "ready" && (credits.data.cast.length ? <ul className="media-detail-cast">
            {credits.data.cast.map((actor, index) => <li className="media-detail-actor" key={`${actor.tmdb_id}-${index}`}>
              <DetailImage src={actor.profile_url} alt={actor.name} className="media-detail-portrait" missing="暂无照片" lazy />
              <strong>{actor.name}</strong><span className="media-detail-muted">{actor.character?.trim() || "角色未知"}</span>
            </li>)}
          </ul> : <p className="media-detail-muted">元数据源未提供演员。</p>)}
        </section>

        {tv && <section className="media-detail-section" aria-label="选季选集">
          <h3>订阅范围</h3>
          <fieldset className="media-detail-fieldset" disabled={subscribing}>
            <legend className="media-detail-sr-only">订阅范围设置</legend>
            <div className="media-detail-modes">
              <label><input type="radio" name={`${seasonId}-mode`} checked={!custom} onChange={() => model.setMode("all")} />全剧追更</label>
              <label><input type="radio" name={`${seasonId}-mode`} checked={custom} onChange={() => model.setMode("selected")} />指定季与集</label>
            </div>
            <p className="media-detail-muted">{custom ? "选择要订阅的季，并在每季完整列表中勾选剧集。" : "包含普通季与未来连载季；保存时由服务器准备权威季集资料。"}</p>
            <label className="media-detail-check"><input type="checkbox" checked={selection.includeSpecials} onChange={e => model.setIncludeSpecials(e.target.checked)} />包含特别篇（S00）</label>
            <div className="media-detail-season-picker"><label htmlFor={seasonId}>查看季</label>
              <select id={seasonId} value={sn ?? ""} onChange={e => model.selectSeason(Number(e.target.value))} disabled={subscribing || !tv.seasons.some(s => s.season_number > 0 || selection.includeSpecials)}>
                {sn === null && <option value="">暂无可查看的季</option>}
                {tv.seasons.filter(s => s.season_number > 0 || selection.includeSpecials).map(s => <option key={s.season_number} value={s.season_number}>S{String(s.season_number).padStart(2, "0")} · {s.name.trim() || "季名未知"} · 元数据摘要 {s.episode_count} 集</option>)}
              </select>
            </div>
            {custom && <div className="media-detail-seasons" aria-label="订阅的季">
              {tv.seasons.filter(s => s.season_number > 0 || selection.includeSpecials).map(s => <label className="media-detail-check" key={s.season_number}>
                <input type="checkbox" checked={selection.selectedSeasons.includes(s.season_number)} onChange={() => model.toggleSeason(s.season_number)} />
                S{String(s.season_number).padStart(2, "0")} · {s.name.trim() || "季名未知"}
                {selection.selectedSeasons.includes(s.season_number) && <span className="media-detail-muted">{seasons[s.season_number]?.status === "ready" ? `已选 ${selection.episodes[s.season_number]?.length ?? 0} 集` : seasons[s.season_number]?.status === "error" ? "列表失败，请切换到本季重试" : "请查看本季完整列表"}</span>}
              </label>)}
            </div>}
            {sn !== null && <div className="media-detail-episodes">
              <div className="media-detail-section-heading"><h4>{seasonSummary?.name.trim() || "季名未知"} · S{String(sn).padStart(2, "0")}</h4>
                {season?.status === "ready" && <button type="button" className="btn-secondary" onClick={() => model.retrySeason(sn)}>刷新本季列表</button>}
              </div>
              {(!season || season.status === "idle" || season.status === "loading") && <p role="status">正在读取本季完整剧集列表…</p>}
              {season?.status === "error" && <div role="alert"><p>{season.error}</p><button type="button" className="btn-secondary" onClick={() => model.retrySeason(sn)}>重试本季列表</button></div>}
              {season?.status === "ready" && <>
                <p className="media-detail-muted">实际返回 {season.data.episodes.length} 集 · {seasonSummary?.air_date || "本季播出日期未知"}</p>
                {custom && <>
                  {!chosenSeason && <p className="media-detail-muted">先勾选订阅本季，再选择剧集。</p>}
                  <div className="media-detail-batch">
                    <button type="button" className="btn-secondary" disabled={!episodeEditing} onClick={() => model.selectEpisodes("all")}>全选本季</button>
                    <button type="button" className="btn-secondary" disabled={!episodeEditing} onClick={() => model.selectEpisodes("invert")}>反选</button>
                    <button type="button" className="btn-secondary" disabled={!episodeEditing} onClick={() => model.selectEpisodes("clear")}>清空</button>
                    <button type="button" className="btn-secondary" disabled={!episodeEditing || selection.anchor?.season !== sn} onClick={() => model.selectEpisodes("forward", selection.anchor?.episode)}>从上次点击集向后选</button>
                  </div>
                  <p id={helpId} className="media-detail-muted">按住 Shift 点击可选择或取消两个集号之间的实际剧集。</p>
                </>}
                {season.data.episodes.length === 0 ? <p className="media-detail-muted">本季完整列表为空，没有可选择的剧集。</p> : <ul className="media-detail-episode-list" aria-describedby={custom ? helpId : undefined}>
                  {[...season.data.episodes].sort((a, b) => a.episode_number - b.episode_number).map(episode => <li className="media-detail-episode" key={episode.tmdb_id}>
                    {custom && <button type="button" className="btn-secondary media-detail-episode-toggle" aria-label={`选择 ${episodeKey(sn, episode.episode_number)}`} aria-pressed={selected.includes(episode.episode_number)} disabled={!episodeEditing} onClick={e => model.selectEpisodes("toggle", episode.episode_number, e.shiftKey)}>{selected.includes(episode.episode_number) ? "已选" : "选择"}</button>}
                    <div className="media-detail-episode-copy"><strong>{episodeKey(sn, episode.episode_number)} · {episode.name?.trim() || "集名未知"}</strong><span className="media-detail-muted">{episode.air_date || "播出日期未知"}</span>{episode.overview && <p>{episode.overview}</p>}</div>
                    {custom && <button type="button" className="btn-secondary media-detail-forward" disabled={!episodeEditing} onClick={() => model.selectEpisodes("forward", episode.episode_number)} aria-label={`从 ${episodeKey(sn, episode.episode_number)} 向后选`}>向后选</button>}
                  </li>)}
                </ul>}
              </>}
            </div>}
          </fieldset>
          {custom && <p role="status" className="media-detail-muted">已选 {selection.selectedSeasons.length} 季 · {totalSelected} 集。{!model.episodeKeys && "每个选中季都需读取完整列表，并选择至少一集后才能保存。"}</p>}
        </section>}

        {data && <section className="media-detail-section" aria-label="订阅配置">
          <h3>订阅配置</h3><p className="config-description">新订阅使用所选策略的当前版本；未选索引器时仅保存订阅。</p>
          <fieldset className="media-detail-fieldset" disabled={subscribing}>
            <legend className="media-detail-sr-only">目标根、策略和端点</legend>
            <RootSelector mediaType={isTV ? "tv" : "movie"} value={rootId} onChange={setRootId} onReady={setRootReady} disabled={subscribing} />
            <PolicySelector value={policyId} onChange={setPolicyId} onReady={setPolicyReady} disabled={subscribing} />
            <EndpointBindingFields indexerIds={indexerIds} downloaderId={downloaderId} disabled={subscribing} onReady={setBindingsReady} onIndexers={setIndexerIds} onDownloader={setDownloaderId} />
          </fieldset>
        </section>}
      </div>
      <footer className="media-detail-actions">
        <button type="button" className="btn-primary" disabled={!ready} onClick={submit}><Rss size={17} aria-hidden="true" />{subscribing ? "正在发起订阅…" : isTV && custom ? `保存选集订阅（${totalSelected} 集）` : isTV ? "保存剧集订阅" : "保存电影订阅"}</button>
        <button type="button" className="btn-secondary" disabled={subscribing || !data} onClick={() => onSearchReleases(media)}><Search size={17} aria-hidden="true" />检索资源</button>
        {onRequestApproval && data && Number.isSafeInteger(media.tmdb_id) && media.tmdb_id > 0 && (media.media_type === "movie" || media.media_type === "tv") && <button type="button" className="btn-secondary" disabled={subscribing} onClick={() => onRequestApproval(media)}>请求审核</button>}
        {data && <button type="button" className="btn-secondary" disabled={subscribing} onClick={model.retryDetail}>刷新影视详情</button>}
      </footer>
    </div>
  </div>;
}

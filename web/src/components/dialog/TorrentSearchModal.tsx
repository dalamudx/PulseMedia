import React, { useState, useEffect, useRef, useId } from "react";
import {useAuth} from "@/features/auth/AuthContext";
import type {useInteractiveReleaseSearch} from "@/features/configuration/useInteractiveReleaseSearch";
import { X, ArrowUp, HardDrive, Download, Loader2 } from "lucide-react";
import type { paths } from "@/api/client";
import {MovieAcquisitionPanel} from "@/features/configuration/MovieAcquisitionPanel";
import {useDialogFocus} from "@/features/configuration/useDialogFocus";
import {DiscoveryTVReviewTarget} from "@/features/configuration/DiscoveryTVReviewTarget";
import "./torrent-search-modal.css";

type ReleaseItem = paths["/api/v1/releases/search"]["get"]["responses"]["200"]["content"]["application/json"]["results"][number];

interface TorrentSearchModalProps {
  mediaTitle: string;
  movieId?: number;
  mediaType?: string;
  releases: ReleaseItem[];
  search: ReturnType<typeof useInteractiveReleaseSearch>;
  loading: boolean;
  onClose: () => void;
}

export const TorrentSearchModal: React.FC<TorrentSearchModalProps> = ({
  mediaTitle,
  movieId, mediaType,
  releases, search,
  loading,
  onClose,
}) => {
  const {user}=useAuth();
  const [selection,setChosen]=useState<{userId:string;searchId:string;item:ReleaseItem}|null>(null);
  const chosen=selection?.userId===user?.id&&selection?.searchId===search.recovery?.searchId?selection?.item:null;
  useEffect(()=>{setChosen(null)},[user?.id,search.recovery?.searchId]);
  const [pending,setPending]=useState(false);
  useEffect(()=>{setPending(false)},[user?.id]);
  const dialog=useRef<HTMLDivElement>(null),titleId=useId();
  const close=()=>{if(!pending)onClose()};useDialogFocus(dialog,close);
  const formatSize = (bytes: number) => {
    if (bytes < 1024 * 1024 * 1024) {
      return (bytes / (1024 * 1024)).toFixed(1) + " MB";
    }
    return (bytes / (1024 * 1024 * 1024)).toFixed(2) + " GB";
  };

  return (
    <div className="modal-overlay" onClick={close}>
      <div ref={dialog} role="dialog" aria-modal="true" aria-labelledby={titleId} className="modal-card torrent-search-card" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <h3 id={titleId}>{mediaTitle?`「${mediaTitle}」站点资源检索`: "恢复站点资源检索"}</h3>
          <button aria-label="关闭资源检索" disabled={pending} className="close-btn" onClick={close}>
            <X size={18} />
          </button>
        </div>
        <div className="torrent-search-content">

        {mediaType === "tv" && <p className="config-description">选择本剧的本人订阅，再分别确认单集或完整覆盖审核与获取。</p>}{!movieId && <p className="config-description">恢复的搜索可继续观察。请重新选择原影视发起搜索，再确认获取目标。</p>}
        {mediaType === "tv" && user?.role !== "admin" && <p role="status" className="config-description">手工剧集审核与获取需要管理员会话。成员可创建本人订阅并查看自动获取状态。</p>}
        {user&&chosen&&movieId&&mediaType==="movie"&&search.phase!=="auth"&&<fieldset className="config-fieldset" disabled={!search.canAcquire&&!pending}><MovieAcquisitionPanel key={user.id} movieId={movieId} candidateId={chosen.id} candidateTitle={chosen.title} onPending={setPending}/></fieldset>}
        {user && chosen && movieId && mediaType === "tv" && search.recovery?.searchId && <DiscoveryTVReviewTarget tmdbId={movieId} candidateId={chosen.id} searchId={search.recovery.searchId} disabled={!search.canAcquire} onPending={setPending}/>}
        {search.message&&<p role={search.phase==="stale"||search.phase==="auth"?"alert":"status"} className="config-description">{search.message}</p>}
        {search.sourceSummary&&<p className="config-description">{search.sourceSummary}</p>}
        {search.policySummary&&<p className="config-description">{search.policySummary}</p>}
        <div className="config-actions"><button type="button" className="btn-secondary" disabled={search.busy||pending} onClick={search.refresh}>刷新搜索观察</button>{search.recovery&&<button type="button" className="btn-secondary" disabled={search.busy||pending} onClick={()=>void search.cancel()}>取消原搜索需求</button>}{search.canRetryOriginal&&<button type="button" className="btn-secondary" disabled={search.busy||pending} onClick={()=>void search.retryOriginal()}>明确重试原键命令</button>}</div>
        {loading&&<div className="modal-loading" role="status"><Loader2 className="spinner-icon" size={24}/><p>正在等待其余站点资源…</p></div>}
        {releases.length === 0 ? (
          <div className="modal-empty"><HardDrive size={36} color="#64748b"/><p>{search.message||"请选择搜索命令或恢复原搜索。"}</p></div>
        ) : (
          <div className="release-list">
            {releases.map((rel) => (
              <div key={rel.id} className="release-item">
                <div className="rel-main">
                  <strong className="rel-title">{rel.title}</strong>
                  <div className="rel-tags">
                    <span className="tag tag-site">{rel.indexer_name}</span>
                    <span className="tag tag-size">{formatSize(rel.size_bytes)}</span>
                    <span className="tag tag-seeders">
                      <ArrowUp size={11} /> {rel.seeders} 做种
                    </span>
                    {rel.resolution && <span className="tag tag-res">{rel.resolution}</span>}
                    {rel.source && <span className="tag tag-src">{rel.source}</span>}
                    {rel.score !== undefined && (
                      <span className="tag tag-score">评分: {rel.score}</span>
                    )}
                  </div>
                </div>
                <div className="rel-action">
                  <button
                    className="btn-sm btn-primary"
                    disabled={pending || !search.canAcquire || !movieId || !(mediaType === "movie" || mediaType === "tv" && user?.role === "admin")}
                    onClick={() => user&&search.recovery?.searchId&&setChosen({userId:user.id,searchId:search.recovery.searchId,item:rel})}
                  >
                    <Download size={13} /><span>选择获取目标</span>
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
        </div>
      </div>
    </div>
  );
};

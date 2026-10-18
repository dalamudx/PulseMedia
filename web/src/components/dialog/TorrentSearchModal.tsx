import React, { useState, useEffect, useRef, useId } from "react";
import { X, ArrowUp, HardDrive, Download, Loader2, Sliders } from "lucide-react";
import { api } from "@/api/client";
import type { paths } from "@/api/client";
import {MovieAcquisitionPanel} from "@/features/configuration/MovieAcquisitionPanel";
import {useDialogFocus} from "@/features/configuration/useDialogFocus";

type ReleaseItem = paths["/api/v1/releases/search"]["get"]["responses"]["200"]["content"]["application/json"]["results"][number];

interface TorrentSearchModalProps {
  mediaTitle: string;
  movieId?: number;
  mediaType?: string;
  releases: ReleaseItem[];
  error?: string;
  partial?: boolean;
  loading: boolean;
  downloadingId: string | null;
  onClose: () => void;
  onDownload: (rel: ReleaseItem, clientId?: string) => void;
}

export const TorrentSearchModal: React.FC<TorrentSearchModalProps> = ({
  mediaTitle,
  movieId, mediaType,
  releases, error, partial,
  loading,
  downloadingId,
  onClose,
  onDownload,
}) => {
  const [chosen,setChosen]=useState<ReleaseItem|null>(null);
  const [pending,setPending]=useState(false);
  const dialog=useRef<HTMLDivElement>(null),titleId=useId();
  const close=()=>{if(!pending)onClose()};useDialogFocus(dialog,close);
  const [downloadClients, setDownloadClients] = useState<any[]>([]);
  const [selectedClientId, setSelectedClientId] = useState<string>("");

  useEffect(() => {
    api.GET("/api/v1/download-clients").then(({ data }) => {
      if (data?.items && data.items.length > 0) {
        setDownloadClients(data.items);
        setSelectedClientId(data.items[0].id);
      }
    });
  }, []);

  const formatSize = (bytes: number) => {
    if (bytes < 1024 * 1024 * 1024) {
      return (bytes / (1024 * 1024)).toFixed(1) + " MB";
    }
    return (bytes / (1024 * 1024 * 1024)).toFixed(2) + " GB";
  };

  return (
    <div className="modal-overlay" onClick={close}>
      <div ref={dialog} role="dialog" aria-modal="true" aria-labelledby={titleId} className="modal-card" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <h3 id={titleId}>「{mediaTitle}」站点资源检索</h3>
          <button aria-label="关闭资源检索" disabled={pending} className="close-btn" onClick={close}>
            <X size={18} />
          </button>
        </div>

        {/* Downloader Target Selector Bar */}
        {!movieId && downloadClients.length > 0 && (
          <div className="modal-downloader-select-bar">
            <span className="dl-select-lbl">
              <Sliders size={13} />
              <span>目标下载器:</span>
            </span>
            <select
              value={selectedClientId}
              onChange={(e) => setSelectedClientId(e.target.value)}
              className="dl-select-input"
            >
              {downloadClients.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} ({c.client_type}) · {c.url}
                </option>
              ))}
            </select>
          </div>
        )}

        {movieId&&mediaType!=="movie"&&<p className="config-description">剧集与整包获取尚未放行；检索结果不构成覆盖授权。</p>}
        {chosen&&movieId&&mediaType==="movie"&&<MovieAcquisitionPanel movieId={movieId} candidateId={chosen.id} candidateTitle={chosen.title} onPending={setPending}/>}
        {partial&&!error&&<p role="status" className="config-description">当前结果不完整；只显示已返回资源，不能据此判断没有其它候选。</p>}
        {error ? <p role="alert" className="config-error">{error}</p> : loading ? (
          <div className="modal-loading">
            <Loader2 className="spinner-icon" size={32} />
            <p>正在向所有已配置站点检索种子候选...</p>
          </div>
        ) : releases.length === 0 ? (
          <div className="modal-empty">
            <HardDrive size={36} color="#64748b" />
            <p>{partial ? '当前结果不完整，尚不能判断是否有匹配资源。' : '未在已配置站点找到匹配的种子资源。'}</p>
            <span className="modal-tip">{partial ? '可稍后重新检索，或检查站点状态。' : '可在「站点管理」中添加更多 PT/Torznab 站点以扩充检索源。'}</span>
          </div>
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
                    disabled={pending || downloadingId === rel.id || (movieId!==undefined && mediaType!=="movie")}
                    onClick={() => movieId ? setChosen(rel) : onDownload(rel, selectedClientId)}
                  >
                    {downloadingId === rel.id ? (
                      <>
                        <Loader2 className="spinner-icon" size={13} />
                        <span>提交中...</span>
                      </>
                    ) : (
                      <>
                        <Download size={13} />
                        <span>{movieId?"选择获取目标":"选择下载"}</span>
                      </>
                    )}
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

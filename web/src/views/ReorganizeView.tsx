import React from "react";
import { useAuth } from "@/features/auth/AuthContext";
import { FileMappingPanel } from "@/features/configuration/FileMappingPanel";
import { FolderSearch } from "lucide-react";

/** A safe preview/commit flow must verify configured roots and persist a transfer plan. */
export const ReorganizeView: React.FC = () => {
const {user}=useAuth();
return (
  <div className="reorganize-container">
    <div className="page-header">
      <div>
        <h2>本地文件整理</h2>
        <p className="subtitle">手动整理暂不可用。当前尚未接入受控目录扫描、路径校验和入库事实写入。</p>
      </div>
    </div>
    {user?.role==="admin"&&<FileMappingPanel/>}
    <div className="card scan-card" role="status">
      <div className="card-header"><h3>尚未开放</h3></div>
      <p className="card-desc"><FolderSearch size={16} aria-hidden="true" /> 为避免误操作，不会扫描任意目录或声明文件已入库。请在媒体库页面查看已经确认的文件。</p>
    </div>
  </div>
);
};

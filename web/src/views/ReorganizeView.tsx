import React from "react";
import { useAuth } from "@/features/auth/AuthContext";
import { FileMappingPanel } from "@/features/configuration/FileMappingPanel";
import { ExternalFileReviewPanel } from "@/features/configuration/ExternalFileReviewPanel";
import { ExternalBatchImportPanel } from "@/features/configuration/ExternalBatchImportPanel";

/** A safe preview/commit flow must verify configured roots and persist a transfer plan. */
export const ReorganizeView: React.FC = () => {
const {user}=useAuth();
return (
  <div className="reorganize-container">
    <div className="page-header">
      <div>
        <h2>本地文件整理</h2>
        <p className="subtitle">查看受控目录中的外部文件，确认作品和季集后整理入库。</p>
      </div>
    </div>
    {user?.role === "admin" ? <><ExternalFileReviewPanel /><ExternalBatchImportPanel /><FileMappingPanel /></> : <p role="status">请联系管理员整理外部文件。</p>}
  </div>
);
};

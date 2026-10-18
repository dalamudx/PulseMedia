package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// CompleteTVSubmissionService prepares only. It has no downloader or Add capability.
type CompleteTVSubmissionService struct {
	repo      ports.CompleteTVSubmissionRepository
	inspector ports.TorrentMetainfoInspector
}

func NewCompleteTVSubmissionService(repo ports.CompleteTVSubmissionRepository, inspector ports.TorrentMetainfoInspector) *CompleteTVSubmissionService {
	return &CompleteTVSubmissionService{repo: repo, inspector: inspector}
}
func (s *CompleteTVSubmissionService) Prepare(ctx context.Context, lease ports.JobLease, task, operation uuid.UUID, torrent []byte) (*ports.CompleteTVSubmissionReceipt, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	proof, e := ports.InspectCompleteTVMetainfo(s.inspector, torrent)
	if e != nil {
		return nil, e
	}
	return s.repo.PrepareCompleteTVSubmission(ctx, ports.PrepareCompleteTVSubmissionCommand{Lease: lease, TaskID: task, OperationID: operation, Metainfo: proof})
}

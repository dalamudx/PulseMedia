package mediasidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"path"
)

type managedGeneration struct {
	ID          uuid.UUID          `json:"id"`
	FileID      uuid.UUID          `json:"file_id"`
	UnitID      uuid.UUID          `json:"unit_id"`
	SourceJobID uuid.UUID          `json:"source_job_id"`
	RootID      uuid.UUID          `json:"root_id"`
	Directory   string             `json:"directory"`
	Name        string             `json:"name"`
	Identity    ports.FileIdentity `json:"identity"`
	ContentHex  string             `json:"content_hex"`
	ContentHash string             `json:"content_hash"`
}

func generationRecord(s ports.MediaSidecarSnapshot, job uuid.UUID, c ports.ManagedSidecarContent) managedGeneration {
	return managedGeneration{ID: uuid.NewSHA1(job, []byte(c.Name+":"+c.Hash)), FileID: s.Source.FileID, UnitID: s.Source.UnitID, SourceJobID: job, RootID: s.RootID, Directory: c.Directory, Name: c.Name, Identity: c.Identity, ContentHex: hex.EncodeToString(c.Content), ContentHash: c.Hash}
}
func recordManagedGeneration(ctx context.Context, q *db.Queries, lease ports.JobLease, s ports.MediaSidecarSnapshot, result ports.MediaSidecarCompletion) error {
	records := []managedGeneration{}
	for _, a := range result.Artifacts {
		if a.Name != "movie.nfo" && a.Name != "tvshow.nfo" && a.Name != "poster.jpg" {
			continue
		}
		if a.Result.Disposition != ports.SidecarCreated || !a.Result.Published || len(a.Content) == 0 {
			continue
		}
		if len(a.Content) > ports.MaxSidecarBytes || int64(len(a.Content)) != a.Result.Identity.Size || !safeRelative(a.Directory) || path.Dir(s.FileRelative) != a.Directory && (s.MediaType != "tv" || path.Dir(path.Dir(s.FileRelative)) != a.Directory) {
			return ports.ErrMediaSidecarResult
		}
		h := sha256.Sum256(a.Content)
		c := ports.ManagedSidecarContent{Name: a.Name, Directory: a.Directory, Identity: a.Result.Identity, Content: a.Content, Hash: hex.EncodeToString(h[:])}
		records = append(records, generationRecord(s, lease.ID, c))
	}
	if len(records) == 0 {
		return nil
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return q.RecordManagedSidecarGeneration(ctx, raw)
}

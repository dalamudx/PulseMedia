package search

import (
	"encoding/json"
	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

func (s *Service) prepareCandidates(releases []*ports.ReleaseItem, indexerID uuid.UUID, expected int64) (ports.SearchQueryCommit, error) {
	prepared := ports.SearchQueryCommit{Candidates: make([]ports.SearchCandidate, 0, len(releases))}
	for _, rel := range releases {
		if rel == nil || strings.TrimSpace(rel.Title) == "" || len(rel.Title) > 4096 || rel.DownloadURL == "" || len(rel.DownloadURL) > 8192 || len(rel.GUID) > 8192 || rel.SizeBytes < 0 || rel.Seeders < 0 || rel.Leechers < 0 {
			return ports.SearchQueryCommit{}, ports.ErrUnavailable
		}
		encrypted, e := s.secrets.EncryptSecret([]byte(rel.DownloadURL))
		if e != nil {
			return ports.SearchQueryCommit{}, ports.ErrUnavailable
		}
		rec := domainMedia.Recognize(rel.Title)
		qualityBytes, _ := json.Marshal(rec.Quality)
		var hash *string
		if rel.InfoHash != "" {
			value := rel.InfoHash
			hash = &value
		}
		prepared.Candidates = append(prepared.Candidates, ports.SearchCandidate{ID: s.idgen.NewUUID(), IndexerID: indexerID, Title: rel.Title, Guid: releaseGUID(rel.GUID, rel.DownloadURL), LinkEncrypted: encrypted, InfoHash: hash, SizeBytes: rel.SizeBytes, Seeders: rel.Seeders, Leechers: rel.Leechers, PublishDate: rel.PublishDate, QualityProfile: qualityBytes, SourceConfigVersion: expected})
	}
	return prepared, nil
}

package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Providers do not supply a stable cross-page snapshot. Retrieved pages remain
// partial even when their advertised total has been traversed.
const aggregatePageLimit = 4
const aggregateContinuationTimeout = 8 * time.Second

type pageChain struct {
	total int64
	seen  map[string]bool
}

func releaseGUID(guid, downloadURL string) string {
	guid = strings.TrimSpace(guid)
	if guid == "" || strings.Contains(guid, "://") || strings.ContainsAny(guid, "?&=") || strings.HasPrefix(guid, "magnet:") {
		value := guid
		if value == "" {
			value = downloadURL
		}
		sum := sha256.Sum256([]byte(value))
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	return guid
}
func (c *pageChain) accepts(p *ports.IndexerPage) bool {
	if p == nil || !p.OffsetVerified || p.Total == nil || *p.Total != c.total || len(p.Items) == 0 {
		return false
	}
	seen := make(map[string]bool, len(p.Items))
	for _, item := range p.Items {
		if item == nil {
			return false
		}
		guid := releaseGUID(item.GUID, item.DownloadURL)
		if c.seen[guid] || seen[guid] {
			return false
		}
		seen[guid] = true
	}
	return true
}
func (s *Service) aggregateEndpointPages(ctx context.Context, indexer uuid.UUID, version int64, keyword string) []uuid.UUID {
	result, e := s.execute(ctx, indexer, keyword, 0, uuid.Nil, version, nil, false, nil)
	if e != nil || result == nil || result.State != "succeeded" {
		return nil
	}
	ids := []uuid.UUID{result.QueryID}
	callCtx, cancel := context.WithTimeout(ctx, aggregateContinuationTimeout)
	defer cancel()
	chain := pageChain{seen: make(map[string]bool)}
	for page := int32(1); page < aggregatePageLimit; page++ {
		proof, e := s.repo.ReadPage(callCtx, result.QueryID)
		if e != nil || !proof.OffsetVerified || proof.Total == nil || proof.ItemCount != proof.PageLimit || proof.PageLimit != 50 || proof.PageOffset != int64(page-1)*50 || int64(proof.PageOffset)+int64(proof.ItemCount) >= *proof.Total {
			break
		}
		if page == 1 {
			chain.total = *proof.Total
		} else if *proof.Total != chain.total {
			break
		}
		rows, e := s.repo.ReadQueryCandidates(callCtx, result.QueryID)
		if e != nil || len(rows) != int(proof.ItemCount) {
			break
		}
		duplicate := false
		for _, row := range rows {
			if chain.seen[row.Guid] {
				duplicate = true
			}
			chain.seen[row.Guid] = true
		}
		if duplicate {
			break
		}
		delay, e := s.repo.ContinuationDelay(callCtx, indexer, version)
		if e != nil || delay > aggregateContinuationTimeout {
			break
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-callCtx.Done():
				timer.Stop()
				return ids
			case <-timer.C:
			}
		}
		next, e := s.execute(callCtx, indexer, keyword, page, uuid.Nil, version, nil, false, &chain)
		if e != nil || next == nil || next.State != "succeeded" {
			break
		}
		// A shared query can have been written outside this chain. Check its immutable
		// candidate associations before admitting it to this browsing response.
		nextProof, e := s.repo.ReadPage(callCtx, next.QueryID)
		if e != nil || !nextProof.OffsetVerified || nextProof.Total == nil || *nextProof.Total != chain.total {
			break
		}
		nextRows, e := s.repo.ReadQueryCandidates(callCtx, next.QueryID)
		if e != nil || len(nextRows) != int(nextProof.ItemCount) {
			break
		}
		duplicate = false
		for _, row := range nextRows {
			if chain.seen[row.Guid] {
				duplicate = true
			}
		}
		if duplicate {
			break
		}
		ids = append(ids, next.QueryID)
		result = next
	}
	return ids
}

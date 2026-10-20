package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func doubanSyncCloneBindings(b *ports.DoubanSyncBindings) *ports.DoubanSyncBindings {
	if b == nil {
		return nil
	}
	c := *b
	if b.IndexerIDs != nil {
		c.IndexerIDs = append([]uuid.UUID{}, b.IndexerIDs...)
	}
	c.DownloaderID = doubanSyncCopyID(b.DownloaderID)
	return &c
}

func doubanSyncValidBindings(b *ports.DoubanSyncBindings) bool {
	if b == nil {
		return true
	}
	if b.IndexerIDs == nil || len(b.IndexerIDs) > 64 || b.DownloaderID != nil && *b.DownloaderID == uuid.Nil || len(b.IndexerIDs) > 0 && b.DownloaderID == nil {
		return false
	}
	if len(b.IndexerIDs) == 0 {
		return b.DownloaderID == nil
	}
	seen := make(map[uuid.UUID]bool, len(b.IndexerIDs))
	for _, id := range b.IndexerIDs {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func doubanSyncBindingsEqual(a, b *ports.DoubanSyncBindings) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return (a.IndexerIDs == nil) == (b.IndexerIDs == nil) && slices.Equal(a.IndexerIDs, b.IndexerIDs) && doubanSyncSameID(a.DownloaderID, b.DownloaderID)
}

func doubanSyncSortBindings(b *ports.DoubanSyncBindings) {
	if b != nil {
		slices.SortFunc(b.IndexerIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	}
}

func doubanSyncLockBindings(ctx context.Context, q *db.Queries, b *ports.DoubanSyncBindings, enabled bool) error {
	if !doubanSyncValidBindings(b) {
		return ports.ErrRejected
	}
	if b == nil {
		return nil
	}
	ids := slices.Clone(b.IndexerIDs)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	for _, id := range ids {
		_, err := q.LockSearchIndexer(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.ErrNotFound
		}
		if err != nil {
			return ports.ErrUnavailable
		}
		if enabled {
			available, err := IndexerAvailableForExecution(ctx, q, id)
			if err != nil {
				return err
			}
			if !available {
				return ports.ErrRejected
			}
		}
	}
	if b.DownloaderID != nil {
		_, err := q.LockDoubanSyncDownloadClient(ctx, *b.DownloaderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.ErrNotFound
		}
		if err != nil {
			return ports.ErrUnavailable
		}
	}
	return nil
}

func doubanSyncDecode(raw []byte, v any) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v) == nil && d.Decode(new(any)) == io.EOF
}

// Validate each immutable request before trusting its effective snapshot. Omitted
// bindings inherit only from a verified predecessor, without a recursion limit.
func doubanSyncVerifiedReceipt(ctx context.Context, q *db.Queries, row db.DoubanSyncConfigReceipt, current db.DoubanSyncConfig) (ports.DoubanSyncConfig, error) {
	var result ports.DoubanSyncConfig
	var child *ports.DoubanSyncConfig
	for {
		var in ports.DoubanSyncInput
		var c ports.DoubanSyncConfig
		key := strings.TrimPrefix(row.CommandKey, doubanSyncNamespace)
		if !doubanSyncDecode(row.Input, &in) || !doubanSyncDecode(row.Snapshot, &c) || !doubanSyncValid(in) || !doubanSyncValidBindings(c.Bindings) ||
			!strings.HasPrefix(row.CommandKey, doubanSyncNamespace) || !doubanSyncASCII(key, 160, false) || row.OwnerID != current.OwnerID || row.RequestHash != doubanSyncHash(in) ||
			c.ID == uuid.Nil || c.ID != row.ConfigID || c.ID != current.ID || c.OwnerID != row.OwnerID || c.Version != row.Version || c.Version != in.ExpectedVersion+1 ||
			c.DoubanUID != in.DoubanUID || c.Enabled != in.Enabled || !doubanSyncSameID(c.MovieRootID, in.MovieRootID) || !doubanSyncSameID(c.TVRootID, in.TVRootID) ||
			c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) || !current.CreatedAt.Time.Equal(c.CreatedAt) || current.Version < c.Version || current.UpdatedAt.Time.Before(c.UpdatedAt) {
			return result, ports.ErrSubscriptionBindingsConflict
		}
		canonical := doubanSyncCloneBindings(in.Bindings)
		doubanSyncSortBindings(canonical)
		snapshotCanonical := doubanSyncCloneBindings(c.Bindings)
		doubanSyncSortBindings(snapshotCanonical)
		if !doubanSyncBindingsEqual(in.Bindings, canonical) || !doubanSyncBindingsEqual(c.Bindings, snapshotCanonical) {
			return result, ports.ErrSubscriptionBindingsConflict
		}
		if child != nil && (child.Version != c.Version+1 || child.UpdatedAt.Before(c.UpdatedAt) || !doubanSyncBindingsEqual(child.Bindings, c.Bindings)) {
			return result, ports.ErrSubscriptionBindingsConflict
		}
		if child == nil {
			result = c
		}

		if in.Bindings != nil {
			if !doubanSyncBindingsEqual(in.Bindings, c.Bindings) {
				return result, ports.ErrSubscriptionBindingsConflict
			}
			break
		}
		if c.Version == 1 {
			if c.Bindings != nil {
				return result, ports.ErrSubscriptionBindingsConflict
			}
			break
		}
		child = &c
		var err error
		row, err = q.GetDoubanSyncReceiptByVersion(ctx, db.GetDoubanSyncReceiptByVersionParams{ConfigID: c.ID, Version: c.Version - 1})
		if errors.Is(err, pgx.ErrNoRows) {
			return result, ports.ErrSubscriptionBindingsConflict
		}
		if err != nil {
			return result, ports.ErrUnavailable
		}
	}
	if current.Version == result.Version {
		base := doubanSyncConfig(current)
		if !base.CreatedAt.Equal(result.CreatedAt) || !base.UpdatedAt.Equal(result.UpdatedAt) {
			return result, ports.ErrSubscriptionBindingsConflict
		}
		base.CreatedAt, base.UpdatedAt = result.CreatedAt, result.UpdatedAt
		withoutBindings := result
		withoutBindings.Bindings = nil
		original, _ := json.Marshal(withoutBindings)
		actual, _ := json.Marshal(base)
		if !bytes.Equal(original, actual) {
			return result, ports.ErrSubscriptionBindingsConflict
		}
	}
	return result, nil
}

func doubanSyncConfigSnapshot(ctx context.Context, q *db.Queries, row db.DoubanSyncConfig) (ports.DoubanSyncConfig, error) {
	receipt, err := q.GetDoubanSyncReceiptByVersion(ctx, db.GetDoubanSyncReceiptByVersionParams{ConfigID: row.ID, Version: row.Version})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.DoubanSyncConfig{}, ports.ErrSubscriptionBindingsConflict
	}
	if err != nil {
		return ports.DoubanSyncConfig{}, ports.ErrUnavailable
	}
	return doubanSyncVerifiedReceipt(ctx, q, receipt, row)
}

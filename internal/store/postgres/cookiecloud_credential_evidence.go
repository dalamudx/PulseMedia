package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func cookieCloudCredentialSchema(ctx context.Context, q *db.Queries) (bool, error) {
	status, err := q.CookieCloudCredentialSchemaStatus(ctx)
	if err != nil || status.Required && !status.Available {
		return false, ports.ErrUnavailable
	}
	return status.Available, nil
}
func recordCookieCloudCredentialEvidence(ctx context.Context, q *db.Queries, job, id uuid.UUID, version int64, until *time.Time) error {
	available, err := cookieCloudCredentialSchema(ctx, q)
	if err != nil {
		return err
	}
	if !available {
		return nil
	}
	deadline := pgtype.Timestamptz{}
	if until != nil {
		deadline = pgtype.Timestamptz{Time: until.UTC().Truncate(time.Microsecond), Valid: true}
	}
	n, err := q.RecordCookieCloudCredentialEvidence(ctx, db.RecordCookieCloudCredentialEvidenceParams{JobID: job, IndexerID: id, SourceVersion: version, ValidUntil: deadline})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrCookieCloudConfigConflict
	}
	return nil
}

// Business execution rejects expired synchronized credentials; refresh itself uses
// the lifecycle-scoped original configuration to replace an expired cookie.
func cookieCloudCredentialUsable(ctx context.Context, q *db.Queries, row db.Indexer) error {
	available, err := cookieCloudCredentialSchema(ctx, q)
	if err != nil {
		return err
	}
	if !available {
		return nil
	}
	evidence, err := q.GetCookieCloudCredentialEvidence(ctx, db.GetCookieCloudCredentialEvidenceParams{IndexerID: row.ID, SourceVersion: row.ConfigVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return ports.ErrUnavailable
	}
	digest := sha256.Sum256(row.ApiKeyEncrypted)
	if !bytes.Equal(digest[:], evidence.CredentialDigest) {
		return ports.ErrUnavailable
	}
	if evidence.ValidUntil.Valid {
		now, err := q.GetSearchDatabaseTime(ctx)
		if err != nil || !now.Valid {
			return ports.ErrUnavailable
		}
		if !now.Time.Before(evidence.ValidUntil.Time) {
			return ports.ErrAuthRequired
		}
	}
	return nil
}

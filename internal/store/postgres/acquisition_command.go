package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"strings"
)

func acquisitionCommandActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) {
		return ports.ErrForbidden
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	return nil
}
func lockAcquisitionUser(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	if e := acquisitionCommandActor(a); e != nil {
		return e
	}
	user, e := q.LockAcquisitionCommandUser(ctx, uuid.MustParse(a.ID))
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !user.IsActive || user.Role != a.Role {
		return ports.ErrForbidden
	}
	return nil
}
func acquisitionCommandHash(c ports.AcquireMovieCommand) string {
	raw, _ := json.Marshal([]any{"movie.acquire.command.v1", c.UnitIDs, c.RootID, c.CandidateID, c.DownloaderID, c.SubscriptionID, c.IntentVersion, c.PolicyID, c.PolicyRevision, c.UnitMetadataVersion, c.MediaMetadataVersion})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func prepareAcquisitionCommand(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.AcquireMovieCommand) (*ports.AcquireResult, error) {
	if e := acquisitionCommandActor(a); e != nil {
		return nil, e
	}
	if c.IdempotencyKey == "" || strings.TrimSpace(c.IdempotencyKey) != c.IdempotencyKey || len(c.IdempotencyKey) > 200 {
		return nil, ports.ErrAcquisitionCommandConflict
	}
	if e := lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	material, _ := json.Marshal([]string{string(a.Type), a.ID, c.IdempotencyKey})
	if e := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "movie.acquire.command", AggregateID: string(material)}); e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.IdempotencyKey})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out ports.AcquireResult
	if old.RequestHash != acquisitionCommandHash(c) || old.ResponseCode != 202 || json.Unmarshal(old.ResponseBody, &out) != nil || out.OperationID == uuid.Nil || out.JobID == uuid.Nil || out.SourceCandidateID == uuid.Nil || !old.ResourceID.Valid || old.ResourceID.String != out.OperationID.String() {
		return nil, ports.ErrAcquisitionCommandConflict
	}
	return &out, nil
}
func (s *AcquisitionStore) saveAcquisitionCommand(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.AcquireMovieCommand, r *ports.AcquireResult) error {
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: c.IdempotencyKey, RequestHash: acquisitionCommandHash(c), ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: r.OperationID.String(), Valid: true}})
	return e
}

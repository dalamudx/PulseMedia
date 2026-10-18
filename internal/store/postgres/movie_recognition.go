package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MovieRecognitionStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewMovieRecognitionStore(pool *pgxpool.Pool, ids ports.IDGen) *MovieRecognitionStore {
	return &MovieRecognitionStore{pool: pool, ids: ids}
}

var _ ports.MovieRecognitionScheduler = (*MovieRecognitionStore)(nil)
var _ ports.MovieRecognitionRepository = (*MovieLinkStore)(nil)

func (s *MovieRecognitionStore) QueueDue(ctx context.Context, limit int32) (int, error) {
	if limit <= 0 || limit > 200 {
		return 0, ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	roots, err := q.LockMovieRecognitionRoots(ctx, limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, root := range roots {
		files, e := q.ListMovieRecognitionFiles(ctx, root)
		if e != nil {
			return 0, e
		}
		for _, file := range files {
			if e = q.InsertMovieRecognitionJob(ctx, db.InsertMovieRecognitionJobParams{JobID: s.ids.NewUUID(), RootID: root, FileID: file.ID, Generation: file.Generation}); e != nil {
				return 0, e
			}
			n++
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}
func (s *MovieRecognitionStore) Work(ctx context.Context, lease ports.JobLease) (ports.MovieRecognitionWork, error) {
	q := db.New(s.pool)
	r, err := q.GetMovieRecognitionWork(ctx, lease.ID)
	if err != nil {
		return ports.MovieRecognitionWork{}, recognitionLeaseError(err)
	}
	w := ports.MovieRecognitionWork{Lease: lease, RootID: r.RootID, FileID: r.FileID, Generation: r.RootGeneration, RelPath: r.RelPath}
	probe := NewLeasedMovieLinkStore(s.pool, s.ids, w)
	if err = probe.guardRecognition(ctx, q, w.RootID, w.FileID); err != nil {
		return ports.MovieRecognitionWork{}, err
	}
	return w, nil
}
func recognitionLeaseError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrMovieRecognitionLease
	}
	return err
}
func NewLeasedMovieLinkStore(pool *pgxpool.Pool, ids ports.IDGen, work ports.MovieRecognitionWork) *MovieLinkStore {
	return &MovieLinkStore{pool: pool, ids: ids, work: &work}
}
func (s *MovieLinkStore) guardRecognition(ctx context.Context, q *db.Queries, root, file uuid.UUID) error {
	if s.work == nil {
		return nil
	}
	w := s.work
	if w.RootID != root || w.FileID != file {
		return ports.ErrMovieRecognitionLease
	}
	_, err := q.GuardOwnedMovieRecognitionWork(ctx, db.GuardOwnedMovieRecognitionWorkParams{JobID: w.Lease.ID, RootID: root, FileID: file, Generation: w.Generation, Owner: w.Lease.Owner, Token: w.Lease.Token})
	return recognitionLeaseError(err)
}
func (s *MovieLinkStore) finishRecognition(ctx context.Context, q *db.Queries, result ports.MovieLinkResult, reason string) error {
	if s.work == nil {
		return nil
	}
	w := s.work
	if err := s.guardRecognition(ctx, q, w.RootID, w.FileID); err != nil {
		return err
	}
	outcome := "linked"
	unit := pgtype.UUID{Bytes: result.Unit.ID, Valid: true}
	review := pgtype.Text{}
	if reason != "" {
		outcome = "review"
		unit = pgtype.UUID{}
		review = pgtype.Text{String: reason, Valid: true}
	}
	body, err := json.Marshal(struct {
		RootID  uuid.UUID  `json:"root_id"`
		FileID  uuid.UUID  `json:"file_id"`
		Outcome string     `json:"outcome"`
		Reason  string     `json:"reason,omitempty"`
		UnitID  *uuid.UUID `json:"unit_id,omitempty"`
	}{w.RootID, w.FileID, outcome, reason, func() *uuid.UUID {
		if reason != "" {
			return nil
		}
		return &result.Unit.ID
	}()})
	if err != nil {
		return err
	}
	n, err := q.FinishMovieRecognitionWork(ctx, db.FinishMovieRecognitionWorkParams{JobID: w.Lease.ID, Outcome: outcome, Reason: review, UnitID: unit})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrMovieRecognitionLease
	}
	if err = q.InsertMovieRecognitionAudit(ctx, db.InsertMovieRecognitionAuditParams{ID: s.ids.NewUUID(), TargetID: w.FileID.String(), Payload: body}); err != nil {
		return err
	}
	if _, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "movie_recognition", AggregateID: w.Lease.ID.String(), EventType: "media.file_identification." + outcome, Payload: body, Seq: 1}); err != nil {
		return err
	}
	_, err = q.FinishJob(ctx, db.FinishJobParams{Column1: w.Lease.ID, Column2: w.Lease.Owner, Column3: w.Lease.Token, Column4: body})
	return recognitionLeaseError(err)
}
func (s *MovieLinkStore) Review(ctx context.Context, reason string) error {
	if s.work == nil {
		return ports.ErrRejected
	}
	switch reason {
	case "low_confidence", "identity_unconfirmed", "invalid_response", "source_changed", "mapping_conflict", "source_unavailable":
	default:
		return ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = s.finishRecognition(ctx, db.New(tx), ports.MovieLinkResult{}, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func NewCheckedMovieRecognitionStore(pool *pgxpool.Pool, ids ports.IDGen, work ports.MovieRecognitionWork) *MovieLinkStore {
	return &MovieLinkStore{pool: pool, ids: ids, work: &work, requirePhysical: true}
}

var _ ports.CheckedMovieRecognitionRepository = (*MovieLinkStore)(nil)

func (s *MovieLinkStore) BeginFileCheck(ctx context.Context, source ports.MovieFileSnapshot) (ports.MovieFileCheck, error) {
	if err := s.guardRecognition(ctx, db.New(s.pool), source.Root.ID, source.FileID); err != nil {
		return ports.MovieFileCheck{}, err
	}
	row, err := db.New(s.pool).BeginMovieFileCheck(ctx, db.BeginMovieFileCheckParams{RootID: source.Root.ID, Generation: source.Root.Generation})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.MovieFileCheck{}, ports.ErrMoviePhysicalCheck
	}
	if err != nil {
		return ports.MovieFileCheck{}, err
	}
	return ports.MovieFileCheck{StartedAt: row.CheckedAt.Time, ScanID: row.ScanID, RootIdentity: ports.FileIdentity{Device: row.RootDevice, Inode: row.RootInode}}, nil
}
func (s *MovieLinkStore) guardFileCheck(ctx context.Context, q *db.Queries, in ports.MovieLinkInput) error {
	if !s.requirePhysical {
		return nil
	}
	p := in.PhysicalCheck
	if p == nil || p.ScanID == uuid.Nil || p.StartedAt.IsZero() || p.RootIdentity.Inode <= 0 {
		return ports.ErrMoviePhysicalCheck
	}
	ok, err := q.GuardMovieFileCheck(ctx, db.GuardMovieFileCheckParams{RootID: in.Source.Root.ID, Generation: in.Source.Root.Generation, ScanID: p.ScanID, RootDevice: p.RootIdentity.Device, RootInode: p.RootIdentity.Inode, CheckedAt: pgtype.Timestamptz{Time: p.StartedAt, Valid: true}})
	if err != nil {
		return err
	}
	if !ok {
		return ports.ErrMoviePhysicalCheck
	}
	return nil
}

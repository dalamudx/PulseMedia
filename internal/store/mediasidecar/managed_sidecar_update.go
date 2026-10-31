package mediasidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"time"
)

type ManagedUpdateRepository struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewManagedUpdateRepository(pool *pgxpool.Pool, ids ports.IDGen) *ManagedUpdateRepository {
	return &ManagedUpdateRepository{pool: pool, ids: ids}
}

var _ ports.ManagedSidecarUpdateRepository = (*ManagedUpdateRepository)(nil)

func (r *ManagedUpdateRepository) transact(ctx context.Context, a *ports.Actor, fn func(*db.Queries) error) (err error) {
	defer func() {
		var p *pgconn.PgError
		if errors.As(err, &p) && p.Code == "23505" {
			err = ports.ErrMediaRequestConflict
		}
		err = candidateError(ctx, err)
	}()
	if r.pool == nil || r.ids == nil {
		return ports.ErrUnavailable
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if e = q.SetMediaSidecarUTC(ctx); e != nil {
		return e
	}
	if e = candidateActor(ctx, q, a); e != nil {
		return e
	}
	isolated, e := q.LockMediaSidecarRestoreState(ctx)
	if e != nil {
		return e
	}
	if isolated {
		return ports.ErrMediaSidecarStale
	}
	if e = q.LockManagedSidecarConfiguration(ctx); e != nil {
		return e
	}
	if e = fn(q); e != nil {
		return e
	}
	if e = candidateActor(ctx, q, a); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func managedSnapshot(ctx context.Context, q *db.Queries, t ports.ManagedSidecarUpdateTicket) (ports.MediaSidecarSnapshot, error) {
	return snapshot(ctx, q, ports.JobLease{ID: t.SourceJobID}, t.Snapshot.Source, ports.MediaSidecarJobKind)
}
func managedCurrent(ctx context.Context, q *db.Queries, t ports.ManagedSidecarUpdateTicket) error {
	s, e := managedSnapshot(ctx, q, t)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(s, t.Snapshot) {
		return ports.ErrMediaSidecarStale
	}
	cfg, e := q.GetManagedSidecarConfiguration(ctx)
	if e != nil {
		return e
	}
	if cfg != t.ConfigBinding {
		return ports.ErrMediaSidecarStale
	}
	now, e := q.GetManagedSidecarClock(ctx)
	if e != nil {
		return e
	}
	if !now.Time.Before(t.ExpiresAt) {
		return ports.ErrMediaSidecarStale
	}
	return nil
}
func (r *ManagedUpdateRepository) LoadManaged(ctx context.Context, a *ports.Actor, file, unit uuid.UUID) (out ports.ManagedSidecarUpdateTicket, err error) {
	err = r.transact(ctx, a, func(q *db.Queries) error {
		if file == uuid.Nil || unit == uuid.Nil {
			return ports.ErrRejected
		}
		source, e := q.FindManagedSidecarSource(ctx, db.FindManagedSidecarSourceParams{FileID: file, UnitID: unit})
		if errors.Is(e, pgx.ErrNoRows) {
			return ports.ErrMediaSidecarUnavailable
		}
		if e != nil {
			return e
		}
		p, e := parsePayload(source.Payload)
		if e != nil {
			return e
		}
		out.ID = r.ids.NewUUID()
		out.SourceJobID = source.ID
		out.Snapshot.Source = p
		out.Snapshot, e = managedSnapshot(ctx, q, out)
		if e != nil {
			return e
		}
		out.ConfigBinding, e = q.GetManagedSidecarConfiguration(ctx)
		if e != nil {
			return e
		}
		now, e := q.GetManagedSidecarClock(ctx)
		if e != nil {
			return e
		}
		out.ExpiresAt = now.Time.Add(10 * time.Minute)
		rows, e := q.ListManagedSidecarContents(ctx, db.ListManagedSidecarContentsParams{FileID: file, UnitID: unit})
		if e != nil {
			return e
		}
		out.Managed = []ports.ManagedSidecarContent{}
		for _, v := range rows {
			c := ports.ManagedSidecarContent{Name: v.Name, Directory: v.Directory, Content: v.Content, Hash: v.ContentHash}
			if json.Unmarshal(v.Identity, &c.Identity) != nil {
				return ports.ErrUnavailable
			}
			out.Managed = append(out.Managed, c)
		}
		return nil
	})
	return
}
func (r *ManagedUpdateRepository) SaveManagedPreview(ctx context.Context, a *ports.Actor, t ports.ManagedSidecarUpdateTicket, out ports.ManagedSidecarUpdateResult) (ports.ManagedSidecarUpdateResult, error) {
	err := r.transact(ctx, a, func(q *db.Queries) error {
		if e := managedCurrent(ctx, q, t); e != nil {
			return e
		}
		ticket, e := json.Marshal(t)
		if e != nil {
			return e
		}
		result, e := json.Marshal(out)
		if e != nil {
			return e
		}
		return q.InsertManagedSidecarPreview(ctx, db.InsertManagedSidecarPreviewParams{ID: t.ID, OwnerID: uuid.MustParse(a.ID), FileID: t.Snapshot.Source.FileID, Ticket: ticket, Result: result, State: out.State, ExpiresAt: pgtype.Timestamptz{Time: t.ExpiresAt, Valid: true}})
	})
	return out, err
}
func managedDecode(raw []byte) (out ports.ManagedSidecarUpdateResult, e error) {
	e = json.Unmarshal(raw, &out)
	return
}
func (r *ManagedUpdateRepository) AuthorizeManagedUpdate(ctx context.Context, a *ports.Actor, file, id uuid.UUID, key string) (ticket ports.ManagedSidecarUpdateTicket, out ports.ManagedSidecarUpdateResult, err error) {
	err = r.transact(ctx, a, func(q *db.Queries) error {
		if !candidateKey.MatchString(key) || file == uuid.Nil || id == uuid.Nil {
			return ports.ErrRejected
		}
		if e := q.LockManagedSidecarCommand(ctx, "managed-sidecar:"+a.ID+":"+key); e != nil {
			return e
		}
		old, e := q.ReadManagedSidecarCommand(ctx, db.ReadManagedSidecarCommandParams{OwnerID: uuid.MustParse(a.ID), CommandKey: key})
		if e == nil {
			if old.ID != id || old.FileID != file {
				return ports.ErrMediaRequestConflict
			}
			out, e = managedDecode(old.Result)
			if out.State == "executing" {
				out.State = "review_required"
			}
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		row, e := q.LockManagedSidecarUpdate(ctx, id)
		if e != nil {
			return e
		}
		if row.OwnerID.String() != a.ID || row.FileID != file || row.State != "preview" || row.Permit.Valid {
			return ports.ErrMediaRequestConflict
		}
		if e = json.Unmarshal(row.Ticket, &ticket); e != nil {
			return e
		}
		if e = managedCurrent(ctx, q, ticket); e != nil {
			return e
		}
		out, e = managedDecode(row.Result)
		if e != nil {
			return e
		}
		ticket.Permit = r.ids.NewUUID()
		out.State = "executing"
		out.CommandKey = key
		raw, e := json.Marshal(out)
		if e != nil {
			return e
		}
		n, e := q.PermitManagedSidecarUpdate(ctx, db.PermitManagedSidecarUpdateParams{ID: id, Permit: ticket.Permit, CommandKey: key, Result: raw})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrMediaSidecarStale
		}
		if e = q.FlushMediaRequestConstraints(ctx); e != nil {
			return e
		}
		// The permit's expiry is checked again after all business locks.
		_, e = q.GuardManagedSidecarUpdate(ctx, db.GuardManagedSidecarUpdateParams{ID: id, Permit: ticket.Permit})
		return staleOrUnavailable(e)
	})
	return
}
func (r *ManagedUpdateRepository) ValidateManagedUpdate(ctx context.Context, a *ports.Actor, t ports.ManagedSidecarUpdateTicket) error {
	return r.transact(ctx, a, func(q *db.Queries) error {
		row, e := q.LockManagedSidecarUpdate(ctx, t.ID)
		if e != nil {
			return e
		}
		if row.OwnerID.String() != a.ID {
			return ports.ErrForbidden
		}
		if e = managedFrozen(row.Ticket, t); e != nil {
			return e
		}
		if e = managedCurrent(ctx, q, t); e != nil {
			return e
		}
		_, e = q.GuardManagedSidecarUpdate(ctx, db.GuardManagedSidecarUpdateParams{ID: t.ID, Permit: t.Permit})
		return staleOrUnavailable(e)
	})
}
func (r *ManagedUpdateRepository) CompleteManagedUpdate(ctx context.Context, a *ports.Actor, t ports.ManagedSidecarUpdateTicket, out ports.ManagedSidecarUpdateResult, created []ports.ManagedSidecarContent) (ports.ManagedSidecarUpdateResult, error) {
	err := r.transact(ctx, a, func(q *db.Queries) error {
		row, e := q.LockManagedSidecarUpdate(ctx, t.ID)
		if e != nil {
			return e
		}
		if row.OwnerID.String() != a.ID {
			return ports.ErrForbidden
		}
		if e = managedFrozen(row.Ticket, t); e != nil {
			return e
		}
		if e = managedCurrent(ctx, q, t); e != nil {
			return e
		}
		records := []managedGeneration{}
		if out.State != "completed" && out.State != "review_required" {
			return ports.ErrMediaSidecarResult
		}
		seen := map[string]bool{}
		for _, c := range created {
			valid := false
			for _, p := range t.Proposed {
				if p.Name == c.Name && p.Directory == c.Directory && bytes.Equal(p.Content, c.Content) && p.Hash == c.Hash {
					valid = true
				}
			}
			sum := sha256.Sum256(c.Content)
			if !valid || seen[c.Name] || hex.EncodeToString(sum[:]) != c.Hash || c.Identity.Device != t.Snapshot.RootIdentity.Device || c.Identity.Inode <= 0 || c.Identity.Size != int64(len(c.Content)) || c.Identity.Inode == t.Snapshot.FileIdentity.Inode {
				return ports.ErrMediaSidecarResult
			}
			seen[c.Name] = true
			record := generationRecord(t.Snapshot, t.SourceJobID, c)
			record.ID = uuid.NewSHA1(t.Permit, []byte(c.Name))
			records = append(records, record)
		}
		if len(records) > 0 {
			raw, e := json.Marshal(records)
			if e != nil {
				return e
			}
			if e = q.RecordManagedSidecarGeneration(ctx, raw); e != nil {
				return e
			}
		}
		raw, e := json.Marshal(out)
		if e != nil {
			return e
		}
		if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: r.ids.NewUUID(), ActorType: "user", ActorID: a.ID, Action: "media.sidecar.updated", TargetType: "file", TargetID: out.FileID.String(), Payload: raw}); e != nil {
			return e
		}
		n, e := q.FinishManagedSidecarUpdate(ctx, db.FinishManagedSidecarUpdateParams{ID: t.ID, Permit: t.Permit, State: out.State, Result: raw})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrMediaSidecarStale
		}
		if e = q.FlushMediaRequestConstraints(ctx); e != nil {
			return e
		}
		current, e := q.LockManagedSidecarUpdate(ctx, t.ID)
		if e != nil {
			return e
		}
		if !current.ValidLease || !current.ValidPreview {
			return ports.ErrMediaSidecarStale
		}
		return nil
	})
	return out, err
}
func (r *ManagedUpdateRepository) ReadManagedUpdate(ctx context.Context, a *ports.Actor, file uuid.UUID, key string) (out ports.ManagedSidecarUpdateResult, err error) {
	err = r.transact(ctx, a, func(q *db.Queries) error {
		if !candidateKey.MatchString(key) {
			return ports.ErrRejected
		}
		row, e := q.ReadManagedSidecarCommand(ctx, db.ReadManagedSidecarCommandParams{OwnerID: uuid.MustParse(a.ID), CommandKey: key})
		if errors.Is(e, pgx.ErrNoRows) {
			return ports.ErrNotFound
		}
		if e != nil {
			return e
		}
		if row.FileID != file {
			return ports.ErrNotFound
		}
		out, e = managedDecode(row.Result)
		if out.State == "executing" {
			out.State = "review_required"
		}
		return e
	})
	return
}

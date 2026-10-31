package mediasidecar

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"regexp"
)

type CandidateRepository struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	secrets ports.SecretCipher
}

func NewCandidateRepository(pool *pgxpool.Pool, ids ports.IDGen, secrets ports.SecretCipher) *CandidateRepository {
	return &CandidateRepository{pool: pool, ids: ids, secrets: secrets}
}

var _ ports.SubtitleCandidateRepository = (*CandidateRepository)(nil)
var candidateKey = regexp.MustCompile(`^[A-Za-z0-9:_.-]{1,80}$`)

func candidateError(ctx context.Context, e error) error {
	if e == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, a := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrMediaSidecarStale, ports.ErrMediaSidecarUnavailable, ports.ErrMediaSidecarResult, ports.ErrMediaRequestConflict} {
		if errors.Is(e, a) {
			return a
		}
	}
	return ports.ErrUnavailable
}
func candidateActor(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Scopes != nil || a.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	u, e := q.LockAcquisitionCommandUser(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !u.IsActive || u.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}
func candidateAuthority(ctx context.Context, q *db.Queries, id uuid.UUID) error {
	return candidateActor(ctx, q, &ports.Actor{Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleAdmin})
}
func candidateEmpty(file uuid.UUID, revision int64) ports.SubtitleCandidateState {
	return ports.SubtitleCandidateState{FileID: file, MappingRevision: revision, Outcome: "not_queried", Candidates: []ports.SubtitleCandidate{}}
}
func (r *CandidateRepository) candidateState(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) (out ports.SubtitleCandidateState, e error) {
	q := db.New(tx)
	row, e := q.GetSubtitleCandidateState(ctx, id)
	if e != nil {
		return out, e
	}
	if row.MappingRevision != revision {
		return candidateEmpty(row.FileID, revision), nil
	}
	out = ports.SubtitleCandidateState{FileID: row.FileID, MappingRevision: row.MappingRevision, QueryID: &id, Outcome: row.Outcome, Candidates: []ports.SubtitleCandidate{}}
	if (out.Outcome == "querying" || out.Outcome == "confirming") && (!row.ValidLease || row.JobState != "running") {
		out.Outcome = "review_required"
	}
	candidates, e := q.ListSubtitleCandidateContents(ctx, id)
	if e != nil {
		return out, e
	}
	for _, candidate := range candidates {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		if r.secrets == nil {
			return out, ports.ErrUnavailable
		}
		body, decryptErr := r.secrets.DecryptSecret(candidate.ContentEncrypted)
		if decryptErr != nil {
			clear(body)
			return out, ports.ErrUnavailable
		}
		summary, summarizeErr := media.SummarizeChineseSRT(body)
		clear(body)
		if summarizeErr != nil {
			return out, ports.ErrMediaSidecarResult
		}
		out.Candidates = append(out.Candidates, ports.SubtitleCandidate{ID: candidate.ID, Provider: "shooter", LanguageHint: "Han", Format: "srt", Summary: &ports.SubtitleCandidateSummary{CueCount: summary.CueCount, StartMS: summary.StartMS, EndMS: summary.EndMS}})
	}
	return out, nil
}
func (r *CandidateRepository) ReadCandidates(ctx context.Context, a *ports.Actor, file uuid.UUID) (out ports.SubtitleCandidateState, err error) {
	defer func() { err = candidateError(ctx, err) }()
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if e = candidateActor(ctx, db.New(tx), a); e != nil {
		return out, e
	}
	revision, e := db.New(tx).GetSubtitleCandidateFileRevision(ctx, file)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ports.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	out = candidateEmpty(file, revision)
	id, e := db.New(tx).GetLatestSubtitleCandidateQuery(ctx, db.GetLatestSubtitleCandidateQueryParams{FileID: file, OwnerID: uuid.MustParse(a.ID)})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	return r.candidateState(ctx, tx, id, revision)
}
func (r *CandidateRepository) BeginCandidateQuery(ctx context.Context, a *ports.Actor, file uuid.UUID, revision int64, key string) (ticket ports.SubtitleCandidateTicket, out ports.SubtitleCandidateState, err error) {
	defer func() { err = candidateError(ctx, err) }()
	if file == uuid.Nil || revision < 1 || !candidateKey.MatchString(key) {
		return ticket, out, ports.ErrRejected
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return ticket, out, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if e = candidateActor(ctx, q, a); e != nil {
		return ticket, out, e
	}
	e = q.LockSubtitleCandidateCommand(ctx, "subtitle-candidate:"+a.ID+":"+key)
	if e != nil {
		return ticket, out, e
	}
	var id uuid.UUID
	old, e := q.GetSubtitleCandidateCommand(ctx, db.GetSubtitleCandidateCommandParams{OwnerID: uuid.MustParse(a.ID), CommandKey: key})
	if e == nil {
		if file != old.FileID || revision != old.MappingRevision {
			return ticket, out, ports.ErrMediaRequestConflict
		}
		out, e = r.candidateState(ctx, tx, old.ID, revision)
		return ticket, out, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return ticket, out, e
	}
	var p ports.MediaSidecarPayload
	p.Version = 1
	p.FileID = file
	source, e := q.FindSubtitleCandidateSource(ctx, db.FindSubtitleCandidateSourceParams{FileID: file, MappingRevision: revision})
	p.SourceEventID, p.UnitID = source.EventID, source.UnitID
	if errors.Is(e, pgx.ErrNoRows) {
		return ticket, out, ports.ErrMediaSidecarStale
	}
	if e != nil {
		return ticket, out, e
	}
	if r.ids == nil {
		return ticket, out, ports.ErrUnavailable
	}
	id = r.ids.NewUUID()
	owner := "subtitle-candidate:" + id.String()
	payload, _ := json.Marshal(p)
	e = q.CreateSubtitleCandidateJob(ctx, db.CreateSubtitleCandidateJobParams{JobID: id, Owner: owner, Payload: payload})
	if e != nil {
		return ticket, out, e
	}
	ticket = ports.SubtitleCandidateTicket{QueryID: id, ActorID: uuid.MustParse(a.ID), Lease: ports.JobLease{ID: id, Owner: owner, Token: 1}}
	if e = q.SetMediaSidecarUTC(ctx); e != nil {
		return ticket, out, e
	}
	isolated, e := q.LockMediaSidecarRestoreState(ctx)
	if e != nil {
		return ticket, out, e
	}
	if isolated {
		return ticket, out, ports.ErrMediaSidecarStale
	}
	ticket.Snapshot, e = snapshot(ctx, q, ticket.Lease, p, ports.SubtitleCandidateJobKind)
	if e != nil {
		return ticket, out, e
	}
	if ticket.Snapshot.FileMappingRevision != revision {
		return ticket, out, ports.ErrMediaSidecarStale
	}
	raw, _ := json.Marshal(ticket.Snapshot)
	e = q.CreateSubtitleCandidateQuery(ctx, db.CreateSubtitleCandidateQueryParams{QueryID: id, OwnerID: ticket.ActorID, FileID: file, MappingRevision: revision, CommandKey: key, Snapshot: raw})
	if e != nil {
		return ticket, out, e
	}
	if e = guard(ctx, q, ticket.Lease, ports.SubtitleCandidateJobKind); e != nil {
		return ticket, out, e
	}
	out, e = r.candidateState(ctx, tx, id, revision)
	if e != nil {
		return ticket, out, e
	}
	return ticket, out, tx.Commit(ctx)
}

// Every physical checkpoint uses the original frozen imported snapshot, current
// account, original lease and current database clock, in one short transaction.
func (r *CandidateRepository) transact(ctx context.Context, t ports.SubtitleCandidateTicket, work func(pgx.Tx, *db.Queries) error) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return candidateError(ctx, e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if _, e = q.LockMediaSidecarJob(ctx, t.Lease.ID); e != nil {
		return candidateError(ctx, e)
	}
	if e = candidateAuthority(ctx, q, t.ActorID); e != nil {
		return candidateError(ctx, e)
	}
	if e = q.SetMediaSidecarUTC(ctx); e != nil {
		return candidateError(ctx, e)
	}
	if e = guard(ctx, q, t.Lease, ports.SubtitleCandidateJobKind); e != nil {
		return candidateError(ctx, e)
	}
	isolated, e := q.LockMediaSidecarRestoreState(ctx)
	if e != nil {
		return candidateError(ctx, e)
	}
	if isolated {
		return ports.ErrMediaSidecarStale
	}
	row, e := q.LockSubtitleCandidateQuery(ctx, t.QueryID)
	if e != nil {
		return candidateError(ctx, e)
	}
	var saved ports.MediaSidecarSnapshot
	if json.Unmarshal(row.Snapshot, &saved) != nil || row.OwnerID != t.ActorID || !reflect.DeepEqual(saved, t.Snapshot) || t.Lease.ID != t.QueryID {
		return ports.ErrMediaSidecarStale
	}
	if e = compare(ctx, q, t.Lease, t.Snapshot.Source, t.Snapshot, ports.SubtitleCandidateJobKind); e != nil {
		return candidateError(ctx, e)
	}
	if e = work(tx, q); e != nil {
		return candidateError(ctx, e)
	}
	current, e := q.LockMediaSidecarJob(ctx, t.Lease.ID)
	if e != nil {
		return candidateError(ctx, e)
	}
	if current.State == "running" {
		if e = guard(ctx, q, t.Lease, ports.SubtitleCandidateJobKind); e != nil {
			return candidateError(ctx, e)
		}
	}
	return candidateError(ctx, tx.Commit(ctx))
}
func (r *CandidateRepository) ValidateCandidate(ctx context.Context, t ports.SubtitleCandidateTicket) error {
	return r.transact(ctx, t, func(pgx.Tx, *db.Queries) error { return nil })
}
func (r *CandidateRepository) FinishCandidateQuery(ctx context.Context, t ports.SubtitleCandidateTicket, outcome string, contents []ports.SubtitleCandidateContent) (out ports.SubtitleCandidateState, err error) {
	if len(contents) > 8 || outcome == "candidates" && len(contents) == 0 || outcome != "candidates" && len(contents) != 0 {
		return out, ports.ErrRejected
	}
	switch outcome {
	case "candidates", "not_found", "already_present", "too_small", "provider_error":
	default:
		return out, ports.ErrRejected
	}
	encrypted := make([][]byte, len(contents))
	for i, c := range contents {
		normalized, e := media.NormalizeChineseSRT(c.Content)
		if e != nil || r.secrets == nil {
			return out, ports.ErrMediaSidecarResult
		}
		encrypted[i], e = r.secrets.EncryptSecret(normalized)
		if e != nil {
			return out, ports.ErrUnavailable
		}
	}
	err = r.transact(ctx, t, func(tx pgx.Tx, q *db.Queries) error {
		for i, c := range encrypted {
			if e := q.InsertSubtitleCandidate(ctx, db.InsertSubtitleCandidateParams{CandidateID: r.ids.NewUUID(), QueryID: t.QueryID, Ordinal: int32(i), ContentEncrypted: c}); e != nil {
				return e
			}
		}
		tag, e := q.FinishSubtitleCandidateQuery(ctx, db.FinishSubtitleCandidateQueryParams{QueryID: t.QueryID, Outcome: outcome})
		if e != nil {
			return e
		}
		if tag != 1 {
			return ports.ErrMediaSidecarStale
		}
		if e = r.finish(ctx, q, t, outcome, nil); e != nil {
			return e
		}
		out, e = r.candidateState(ctx, tx, t.QueryID, t.Snapshot.FileMappingRevision)
		return e
	})
	return
}
func (r *CandidateRepository) finish(ctx context.Context, q *db.Queries, t ports.SubtitleCandidateTicket, outcome string, completion *ports.MediaSubtitleCompletion) error {
	raw, _ := json.Marshal(struct {
		Outcome     string                         `json:"outcome"`
		Publication *ports.MediaSubtitleCompletion `json:"publication,omitempty"`
	}{outcome, completion})
	_, e := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: r.ids.NewUUID(), ActorType: "user", ActorID: t.ActorID.String(), Action: "media.subtitle.candidates." + outcome, TargetType: "library_file", TargetID: t.Snapshot.Source.FileID.String(), Payload: raw})
	if e != nil {
		return e
	}
	payload, _ := json.Marshal(t.Snapshot.Source)
	_, e = q.CompleteMediaSidecarJob(ctx, db.CompleteMediaSidecarJobParams{JobID: t.Lease.ID, Owner: t.Lease.Owner, Token: t.Lease.Token, Kind: ports.SubtitleCandidateJobKind, ExpectedPayload: payload, Result: raw, ReviewRequired: outcome == "review_required"})
	return staleOrUnavailable(e)
}
func (r *CandidateRepository) BeginCandidateConfirmation(ctx context.Context, a *ports.Actor, file, candidate uuid.UUID, revision int64) (ticket ports.SubtitleCandidateTicket, out ports.SubtitleCandidateState, body []byte, err error) {
	defer func() { err = candidateError(ctx, err) }()
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return ticket, out, nil, e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if e = candidateActor(ctx, q, a); e != nil {
		return ticket, out, nil, e
	}
	var id, owner uuid.UUID
	var raw, cipher []byte
	var outcome string
	var permit bool
	var savedRevision int64
	confirmation, e := q.LockSubtitleCandidateConfirmation(ctx, db.LockSubtitleCandidateConfirmationParams{CandidateID: candidate, FileID: file})
	id, owner, savedRevision, raw, outcome, permit, cipher = confirmation.ID, confirmation.OwnerID, confirmation.MappingRevision, confirmation.Snapshot, confirmation.Outcome, confirmation.ConfirmPermit, confirmation.ContentEncrypted
	if errors.Is(e, pgx.ErrNoRows) {
		return ticket, out, nil, ports.ErrNotFound
	}
	if e != nil {
		return ticket, out, nil, e
	}
	if owner.String() != a.ID {
		return ticket, out, nil, ports.ErrForbidden
	}
	if revision < 1 || savedRevision != revision {
		return ticket, out, nil, ports.ErrMediaSidecarStale
	}
	out, e = r.candidateState(ctx, tx, id, revision)
	if e != nil {
		return ticket, out, nil, e
	}
	if permit || outcome != "candidates" {
		return ticket, out, nil, nil
	}
	if r.secrets == nil {
		return ticket, out, nil, ports.ErrUnavailable
	}
	body, e = r.secrets.DecryptSecret(cipher)
	if e != nil || len(body) > ports.MaxSubtitleBytes {
		return ticket, out, nil, ports.ErrUnavailable
	}
	body, e = media.NormalizeChineseSRT(body)
	if e != nil {
		return ticket, out, nil, ports.ErrMediaSidecarResult
	}
	ticket.QueryID = id
	ticket.ActorID = owner
	if json.Unmarshal(raw, &ticket.Snapshot) != nil {
		return ticket, out, nil, ports.ErrUnavailable
	}
	ticket.Lease.ID = id
	ticket.Lease.Token, e = q.BeginSubtitleCandidateJobConfirmation(ctx, db.BeginSubtitleCandidateJobConfirmationParams{JobID: id, Owner: "subtitle-confirm:" + id.String()})
	if e != nil {
		return ticket, out, nil, ports.ErrMediaSidecarStale
	}
	ticket.Lease.Owner = "subtitle-confirm:" + id.String()
	if e = q.SetMediaSidecarUTC(ctx); e != nil {
		return ticket, out, nil, e
	}
	isolated, e := q.LockMediaSidecarRestoreState(ctx)
	if e != nil {
		return ticket, out, nil, e
	}
	if isolated {
		return ticket, out, nil, ports.ErrMediaSidecarStale
	}
	if e = compare(ctx, q, ticket.Lease, ticket.Snapshot.Source, ticket.Snapshot, ports.SubtitleCandidateJobKind); e != nil {
		return ticket, out, nil, e
	}
	e = q.PermitSubtitleCandidateConfirmation(ctx, db.PermitSubtitleCandidateConfirmationParams{QueryID: id, CandidateID: candidate})
	if e != nil {
		return ticket, out, nil, e
	}
	if e = guard(ctx, q, ticket.Lease, ports.SubtitleCandidateJobKind); e != nil {
		return ticket, out, nil, e
	}
	out.Outcome = "confirming"
	return ticket, out, body, tx.Commit(ctx)
}
func (r *CandidateRepository) FinishCandidateConfirmation(ctx context.Context, t ports.SubtitleCandidateTicket, c ports.MediaSubtitleCompletion) (out ports.SubtitleCandidateState, err error) {
	review, e := validateSubtitleResult(t.Snapshot, c)
	if e != nil {
		return out, e
	}
	outcome := "confirmed"
	if review {
		outcome = "review_required"
	} else if c.Publication.Disposition == ports.SidecarPreservedRegular {
		outcome = "already_present"
	}
	err = r.transact(ctx, t, func(tx pgx.Tx, q *db.Queries) error {
		tag, e := q.FinishSubtitleCandidateConfirmation(ctx, db.FinishSubtitleCandidateConfirmationParams{QueryID: t.QueryID, Outcome: outcome})
		if e != nil {
			return e
		}
		if tag != 1 {
			return ports.ErrMediaSidecarStale
		}
		if e = r.finish(ctx, q, t, outcome, &c); e != nil {
			return e
		}
		out, e = r.candidateState(ctx, tx, t.QueryID, t.Snapshot.FileMappingRevision)
		return e
	})
	return
}

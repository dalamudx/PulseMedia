package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ ports.LibraryReuseRepository = (*Repository)(nil)

type reuseFrozen struct {
	Command            ports.LibraryReuseCommand
	Source             db.ListLibraryReuseFilesRow
	TargetRoot         reuseRootInput
	Units              []db.MediaUnit
	Quality            quality.Profile
	MediaVersion       int64
	IntentVersion      int64
	SourceIdentity     ports.FileIdentity
	SourceRootIdentity ports.FileIdentity
	TargetRootIdentity ports.FileIdentity
}

func reuseError(err error) error {
	if err == nil {
		return nil
	}
	for _, allowed := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrInvalidLibraryReuse, ports.ErrLibraryReuseChanged, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && (pe.Code == "23505" || pe.Code == "40001") {
		return ports.ErrLibraryReuseChanged
	}
	return ports.ErrUnavailable
}

func reuseActor(id uuid.UUID) *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleAdmin}
}
func sameReuseUnits(a, b []uuid.UUID) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.SortFunc(a, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })
	slices.SortFunc(b, func(x, y uuid.UUID) int { return strings.Compare(x.String(), y.String()) })
	return slices.Equal(a, b)
}
func reuseSourceStamp(f db.ListLibraryReuseFilesRow) string {
	// Transient health changes pause execution. They do not replace the frozen
	// incarnation, generation, mapping, complete imported facts or content.
	f.Health = ""
	f.HealthRevision = 0
	return recoveryHash(f)
}
func reuseRootMatches(a, b reuseRootInput) bool {
	a.Health = ""
	b.Health = ""
	a.HealthRevision = 0
	b.HealthRevision = 0
	return a == b
}
func loadReuseFrozen(ctx context.Context, q *db.Queries, who uuid.UUID, c ports.LibraryReuseCommand, checkStamp bool, ignore ...uuid.UUID) (reuseFrozen, error) {
	out := reuseFrozen{Command: c}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return out, e
	}
	preview, e := loadLibraryReusePreview(ctx, q, who, c.Target, now, 0, ignore...)
	if e != nil {
		return out, e
	}
	if checkStamp && preview.InputStamp != c.InputStamp {
		return out, ports.ErrLibraryReuseChanged
	}
	if !preview.CanConfirm {
		return out, ports.ErrLibraryReuseChanged
	}
	var selected *ports.LibraryReuseCandidate
	for i := range preview.Candidates {
		if preview.Candidates[i].FileID == c.SourceFileID {
			selected = &preview.Candidates[i]
			break
		}
	}
	if selected == nil || !selected.Qualified || !selected.SourceAvailable || !sameReuseUnits(selected.UnitIDs, c.ConfirmedUnitIDs) {
		return out, ports.ErrLibraryReuseChanged
	}
	root, e := q.GetLibraryRootByID(ctx, c.Target.RootID)
	if e != nil {
		return out, e
	}
	out.TargetRoot = reuseRootSnapshot(root)
	rows, e := q.ListLibraryReuseFiles(ctx, db.ListLibraryReuseFilesParams{UnitID: c.Target.UnitID, TargetRootID: c.Target.RootID, ObservedAt: now})
	if e != nil {
		return out, e
	}
	for _, f := range rows {
		if f.FileID == c.SourceFileID {
			out.Source = f
			break
		}
	}
	out.MediaVersion = 0
	out.Quality = selected.Quality
	for _, id := range selected.UnitIDs {
		target := c.Target
		target.UnitID = id
		p, e := loadLibraryReusePreview(ctx, q, who, target, now, 0, ignore...)
		if e != nil {
			return out, e
		}
		if !p.CanConfirm {
			return out, ports.ErrLibraryReuseChanged
		}
		u, e := q.GetLibraryReuseUnit(ctx, id)
		if e != nil {
			return out, e
		}
		out.Units = append(out.Units, u)
		m, e := q.GetMediaByID(ctx, u.MediaID)
		if e != nil {
			return out, e
		}
		out.MediaVersion = m.MetadataVersion
	}
	if c.Target.SubscriptionID != uuid.Nil {
		sub, e := q.GetSubscriptionByID(ctx, c.Target.SubscriptionID)
		if e != nil {
			return out, e
		}
		out.IntentVersion = sub.IntentVersion
	}
	return out, nil
}
func lockReuseInputs(ctx context.Context, q *db.Queries, who uuid.UUID, f reuseFrozen) error {
	if _, e := recoveryUser(ctx, q, reuseActor(who)); e != nil {
		return e
	}
	if _, e := q.LockLibraryReusePolicy(ctx, f.Command.Target.PolicyID); e != nil {
		return e
	}
	if f.Command.Target.SubscriptionID != uuid.Nil {
		if _, e := q.LockLibraryReuseSubscription(ctx, f.Command.Target.SubscriptionID); e != nil {
			return e
		}
	}
	roots := []uuid.UUID{f.Source.RootID, f.Command.Target.RootID}
	slices.SortFunc(roots, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range roots {
		if _, e := q.LockTransferRoot(ctx, id); e != nil {
			return e
		}
	}
	ids := slices.Clone(f.Command.ConfirmedUnitIDs)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range ids {
		if _, e := q.LockTransferUnit(ctx, id); e != nil {
			return e
		}
	}
	if len(f.Units) == 0 {
		return ports.ErrLibraryReuseChanged
	}
	if _, e := q.LockSubmissionMedia(ctx, f.Units[0].MediaID); e != nil {
		return e
	}
	_, e := q.LockTransferOldFile(ctx, f.Source.FileID)
	return e
}
func reuseCached(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.LibraryReuseCommand, key string) (*ports.LibraryReuseReceipt, error) {
	record, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "library.reuse:" + key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out ports.LibraryReuseReceipt
	if record.RequestHash != recoveryHash(c) || json.Unmarshal(record.ResponseBody, &out) != nil {
		return nil, ports.ErrLibraryReuseChanged
	}
	return &out, nil
}
func (s *Service) reuseMediaEligible(f reuseFrozen) bool {
	source, _ := naming.IsEligibleMediaFile(f.Source.RelPath, f.Source.SizeBytes, s.minimumMediaBytes)
	target, _ := naming.IsEligibleMediaFile(f.Command.TargetRel, f.Source.SizeBytes, s.minimumMediaBytes)
	return source && target
}

func (r *Repository) ConfirmLibraryReuse(ctx context.Context, a *ports.Actor, c ports.LibraryReuseCommand, key string) (receipt *ports.LibraryReuseReceipt, err error) {
	defer func() { err = reuseError(err) }()
	if !c.Confirmed || key == "" || len(key) > 140 || len(c.InputStamp) != 64 || len(c.ConfirmedUnitIDs) == 0 || len(c.ConfirmedUnitIDs) > 20 || !fs.ValidPath(c.TargetRel) || strings.ContainsAny(c.TargetRel, "\\\r\n") || c.SourceFileID == uuid.Nil || (c.Action != "hardlink" && c.Action != "copy" && c.Action != "reflink") {
		return nil, ports.ErrInvalidLibraryReuse
	}
	s := r.execution
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	who, e := recoveryUser(ctx, q, a)
	if e != nil {
		return nil, e
	}
	cached, e := reuseCached(ctx, q, a, c, key)
	if e != nil {
		return nil, e
	}
	if cached != nil {
		return cached, tx.Commit(ctx)
	}
	f, e := loadReuseFrozen(ctx, q, who, c, true)
	if e != nil {
		// A competing confirmation may commit its receipt after our first lookup.
		if errors.Is(e, ports.ErrLibraryReuseChanged) {
			cached, receiptErr := reuseCached(ctx, q, a, c, key)
			if receiptErr != nil {
				return nil, receiptErr
			}
			if cached != nil {
				return cached, tx.Commit(ctx)
			}
		}
		return nil, e
	}
	if !s.reuseMediaEligible(f) {
		return nil, ports.ErrLibraryReuseChanged
	}
	if !strings.EqualFold(path.Ext(c.TargetRel), path.Ext(f.Source.RelPath)) {
		return nil, ports.ErrInvalidLibraryReuse
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	// No database transaction spans root acquisition, marker checks or file I/O.
	roots, release, e := s.files.LockRoots(ctx, f.Source.RootPath, f.TargetRoot.Path)
	if e != nil {
		return nil, e
	}
	defer release()
	// A concurrent same-key confirmation may have committed while this call
	// waited for physical roots. Recover its receipt before inspecting side effects.
	tx, e = s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q = s.queries.WithTx(tx)
	if _, e = recoveryUser(ctx, q, a); e != nil {
		return nil, e
	}
	cached, e = reuseCached(ctx, q, a, c, key)
	if e != nil {
		return nil, e
	}
	if cached != nil {
		return cached, tx.Commit(ctx)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	f.SourceRootIdentity, e = roots[f.Source.RootPath].Identity(ctx)
	if e != nil {
		return nil, e
	}
	f.TargetRootIdentity, e = roots[f.TargetRoot.Path].Identity(ctx)
	if e != nil {
		return nil, e
	}
	if e = inspectReuseMarkers(ctx, roots, f); e != nil {
		return nil, e
	}
	f.SourceIdentity, e = roots[f.Source.RootPath].Inspect(ctx, f.Source.RelPath)
	if e != nil {
		return nil, e
	}
	if !reuseIndexedIdentity(f.Source, f.SourceIdentity) {
		return nil, ports.ErrLibraryReuseChanged
	}
	if _, e = roots[f.TargetRoot.Path].Inspect(ctx, c.TargetRel); !errors.Is(e, fs.ErrNotExist) {
		return nil, ports.ErrLibraryReuseChanged
	}
	if c.Action == "hardlink" && f.SourceIdentity.Device != f.TargetRootIdentity.Device {
		return nil, ports.ErrInvalidLibraryReuse
	}
	if c.Action == "reflink" {
		cap, ok := s.files.(ports.VerifiedTransferFilesystem)
		if !ok {
			return nil, ports.ErrInvalidLibraryReuse
		}
		ok, e = cap.ProbeReflink(ctx, roots[f.Source.RootPath], f.Source.RelPath, roots[f.TargetRoot.Path], f.SourceIdentity, s.idgen.NewUUID())
		if e != nil || !ok {
			return nil, ports.ErrInvalidLibraryReuse
		}
	}
	observed, e := s.queries.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	tx, e = s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q = s.queries.WithTx(tx)
	if e = lockReuseInputs(ctx, q, who, f); e != nil {
		return nil, e
	}
	cached, e = reuseCached(ctx, q, a, c, key)
	if e != nil {
		return nil, e
	}
	if cached != nil {
		return cached, tx.Commit(ctx)
	}
	current, e := loadReuseFrozen(ctx, q, who, c, true)
	if e != nil {
		return nil, e
	}
	if !s.reuseMediaEligible(current) {
		return nil, ports.ErrLibraryReuseChanged
	}
	if reuseSourceStamp(current.Source) != reuseSourceStamp(f.Source) || !reuseRootMatches(current.TargetRoot, f.TargetRoot) {
		return nil, ports.ErrLibraryReuseChanged
	}
	id, jobID := s.idgen.NewUUID(), s.idgen.NewUUID()
	body, _ := json.Marshal(map[string]any{"plan_id": id})
	if _, e = q.EnqueueJob(ctx, db.EnqueueJobParams{ID: jobID, Kind: "library_reuse", DedupeKey: text(id.String()), Payload: body, MaxAttempts: 100, NotBefore: observed}); e != nil {
		return nil, e
	}
	frozen, _ := json.Marshal(f)
	if _, e = q.CreateLibraryReusePlan(ctx, db.CreateLibraryReusePlanParams{ID: id, ActorID: who, RootID: c.Target.RootID, SourceFileID: c.SourceFileID, JobID: jobID, Action: c.Action, TargetRel: c.TargetRel, Frozen: frozen}); e != nil {
		return nil, e
	}
	for _, u := range f.Units {
		if e = q.ReserveLibraryReuseUnit(ctx, db.ReserveLibraryReuseUnitParams{PlanID: id, RootID: c.Target.RootID, UnitID: u.ID}); e != nil {
			return nil, e
		}
	}
	receipt = &ports.LibraryReuseReceipt{PlanID: id, JobID: jobID, State: "queued"}
	body, _ = json.Marshal(receipt)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "library.reuse.confirmed", TargetType: "library_reuse_plan", TargetID: id.String(), Payload: body}); e != nil {
		return nil, e
	}
	if _, e = s.outbox.Publish(ctx, q, "library_reuse", id.String(), "library.reuse.queued", json.RawMessage(body), nil); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "library.reuse:" + key, RequestHash: recoveryHash(c), ResponseCode: 202, ResponseBody: body, ResourceID: text(id.String())}); e != nil {
		return nil, e
	}
	fresh, e := q.IsTransferObservationFresh(ctx, observed)
	if e != nil {
		return nil, e
	}
	if !fresh.Valid || !fresh.Bool {
		return nil, ports.ErrLibraryReuseChanged
	}
	return receipt, tx.Commit(ctx)
}
func reuseIndexedIdentity(f db.ListLibraryReuseFilesRow, v ports.FileIdentity) bool {
	return f.SizeBytes == v.Size && f.MtimeNs == v.Mtime && f.FileFingerprint.Valid && f.FileFingerprint.String == fmt.Sprintf("stat.v1:%d:%d:%d", v.Inode, v.Size, v.Mtime)
}
func inspectReuseMarkers(ctx context.Context, roots map[string]ports.TransferRoot, f reuseFrozen) error {
	for _, entry := range []struct{ path, marker string }{{f.Source.RootPath, f.Source.MarkerFilename}, {f.TargetRoot.Path, f.TargetRoot.Marker}} {
		marker := entry.marker
		if marker == "" {
			marker = ".pulsemedia_root"
		}
		if _, e := roots[entry.path].Inspect(ctx, marker); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) ListLibraryReuse(ctx context.Context, a *ports.Actor) (out []ports.LibraryReuseStatus, err error) {
	defer func() { err = reuseError(err) }()
	tx, e := r.execution.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := r.execution.queries.WithTx(tx)
	who, e := recoveryUser(ctx, q, a)
	if e != nil {
		return nil, e
	}
	rows, e := q.ListLibraryReusePlans(ctx, who)
	if e != nil {
		return nil, e
	}
	out = make([]ports.LibraryReuseStatus, 0, len(rows))
	for _, p := range rows {
		var f reuseFrozen
		if json.Unmarshal(p.Frozen, &f) != nil {
			return nil, ports.ErrUnavailable
		}
		v := ports.LibraryReuseStatus{LibraryReuseReceipt: ports.LibraryReuseReceipt{PlanID: p.ID, JobID: p.JobID, State: p.State}, RootID: p.RootID, SourceFileID: p.SourceFileID, TargetRel: p.TargetRel, Action: p.Action, UnitIDs: f.Command.ConfirmedUnitIDs, Reason: p.Reason}
		if p.TargetFileID.Valid {
			id := uuid.UUID(p.TargetFileID.Bytes)
			v.TargetFileID = &id
		}
		out = append(out, v)
	}
	return out, tx.Commit(ctx)
}

package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var errReuseWaiting = errors.New("reuse source or target unavailable")

func guardReuseLease(ctx context.Context, q *db.Queries, l ports.JobLease) error {
	if _, e := q.GuardLibraryReuseJob(ctx, db.GuardLibraryReuseJobParams{ID: l.ID, Owner: l.Owner, Token: l.Token}); e != nil {
		return ErrExecutionRejected
	}
	return nil
}
func validateReuseFrozen(ctx context.Context, q *db.Queries, p db.LibraryReusePlan, f reuseFrozen) error {
	if e := lockReuseInputs(ctx, q, p.ActorID, f); e != nil {
		return e
	}
	for _, id := range []uuid.UUID{p.RootID, f.Source.RootID} {
		root, e := q.GetLibraryRootByID(ctx, id)
		if e != nil {
			return e
		}
		if root.Health != "online" || !root.BaselineCompleted {
			return errReuseWaiting
		}
	}
	current, e := loadReuseFrozen(ctx, q, p.ActorID, f.Command, false, p.ID)
	if e != nil {
		return e
	}
	if reuseSourceStamp(current.Source) != reuseSourceStamp(f.Source) || !reuseRootMatches(current.TargetRoot, f.TargetRoot) || current.MediaVersion != f.MediaVersion || current.IntentVersion != f.IntentVersion || len(current.Units) != len(f.Units) {
		return ports.ErrLibraryReuseChanged
	}
	for i, u := range f.Units {
		v := current.Units[i]
		if u.ID != v.ID || u.MetadataVersion != v.MetadataVersion || u.MediaID != v.MediaID || u.UnitKey != v.UnitKey || v.Retired {
			return ports.ErrLibraryReuseChanged
		}
	}
	units, e := q.ListLibraryReuseUnits(ctx, p.ID)
	if e != nil {
		return e
	}
	if len(units) != len(f.Units) {
		return ports.ErrLibraryReuseChanged
	}
	for _, u := range units {
		if !u.Active || u.RootID != p.RootID {
			return ports.ErrLibraryReuseChanged
		}
	}
	return nil
}
func (s *Service) reuseBoundary(ctx context.Context, l ports.JobLease, p db.LibraryReusePlan, f reuseFrozen, content *ports.TransferContent) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if e = validateReuseFrozen(ctx, q, p, f); e != nil {
		return e
	}
	if e = guardReuseLease(ctx, q, l); e != nil {
		return e
	}
	if content != nil {
		raw, _ := json.Marshal(content)
		if e = q.FreezeLibraryReuseContent(ctx, db.FreezeLibraryReuseContentParams{PlanID: p.ID, Content: raw}); e != nil {
			return e
		}
	}
	if e = q.SetLibraryReuseState(ctx, db.SetLibraryReuseStateParams{ID: p.ID, State: "running"}); e != nil {
		return e
	}
	if e = guardReuseLease(ctx, q, l); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (r *Repository) ExecuteLibraryReuse(ctx context.Context, l ports.JobLease) error {
	s := r.execution
	p, e := s.queries.GetLibraryReusePlanForJob(ctx, l.ID)
	if e != nil {
		return e
	}
	var f reuseFrozen
	if json.Unmarshal(p.Frozen, &f) != nil {
		return ErrReviewRequired
	}
	if p.State == "completed" || p.State == "review_required" {
		return ErrExecutionRejected
	}
	// Check logical authority before waiting for physical roots. Repeat after the
	// wait: neither this check nor the lease can displace a live syscall owner.
	e = s.reuseBoundary(ctx, l, p, f, nil)
	if e != nil {
		return s.settleReuseError(ctx, l, p, e)
	}
	e = withTransferLease(ctx, func(c context.Context) error {
		_, err := s.queries.RenewJobLease(c, db.RenewJobLeaseParams{Column1: l.ID, Column2: l.Owner, Column3: l.Token, Column4: 300000})
		return err
	}, func(c context.Context) error {
		roots, release, err := s.files.LockRoots(c, f.Source.RootPath, f.TargetRoot.Path)
		if err != nil {
			return errReuseWaiting
		}
		defer release()
		if err = s.reuseBoundary(c, l, p, f, nil); err != nil {
			return err
		}
		if err = checkReuseRoots(c, roots, f); err != nil {
			return err
		}
		source := roots[f.Source.RootPath]
		target := roots[f.TargetRoot.Path]
		sourceID, err := source.Inspect(c, f.Source.RelPath)
		if err != nil {
			return err
		}
		if sourceID != f.SourceIdentity {
			return ports.ErrLibraryReuseChanged
		}
		var result ports.FileIdentity
		var proof *ports.TransferContent
		if p.Action == "hardlink" {
			if err = s.files.Hardlink(c, source, f.Source.RelPath, target, p.TargetRel, f.SourceIdentity); err != nil {
				return err
			}
			result = f.SourceIdentity
		} else {
			cap, ok := s.files.(ports.VerifiedTransferFilesystem)
			if !ok {
				return ports.ErrFileUnsupported
			}
			raw, readErr := s.queries.GetLibraryReuseContent(c, p.ID)
			value := ports.TransferContent{}
			if errors.Is(readErr, pgx.ErrNoRows) {
				value, err = cap.InspectContent(c, source, f.Source.RelPath, f.SourceIdentity)
				if err != nil {
					return err
				}
				if err = s.reuseBoundary(c, l, p, f, &value); err != nil {
					return err
				}
			} else if readErr != nil {
				return readErr
			} else if json.Unmarshal(raw, &value) != nil || value.File != f.SourceIdentity {
				return ErrReviewRequired
			}
			proof = &value
			got, err := cap.TransferVerified(c, source, f.Source.RelPath, target, p.TargetRel, value, p.Action, p.ID)
			if err != nil {
				return err
			}
			result = got.Target.File
		}
		// Reopen configured paths after publication. Old FDs alone do not prove
		// that either directory still occupies its frozen namespace.
		freshSource, err := s.files.OpenRoot(c, f.Source.RootPath)
		if err != nil {
			return err
		}
		defer freshSource.Close()
		freshTarget, err := s.files.OpenRoot(c, f.TargetRoot.Path)
		if err != nil {
			return err
		}
		defer freshTarget.Close()
		fresh := map[string]ports.TransferRoot{f.Source.RootPath: freshSource, f.TargetRoot.Path: freshTarget}
		if err = checkReuseRoots(c, fresh, f); err != nil {
			return err
		}
		if proof != nil {
			cap := s.files.(ports.VerifiedTransferFilesystem)
			got, err := cap.ObserveTransfer(c, freshSource, f.Source.RelPath, freshTarget, p.TargetRel, *proof, p.Action, p.ID)
			if err != nil {
				return err
			}
			if got.Target.File != result {
				return ports.ErrFileIdentity
			}
		} else {
			src, err := freshSource.Inspect(c, f.Source.RelPath)
			if err != nil {
				return err
			}
			dst, err := freshTarget.Inspect(c, p.TargetRel)
			if err != nil {
				return err
			}
			if src != f.SourceIdentity || dst != f.SourceIdentity {
				return ports.ErrFileIdentity
			}
		}
		observed, err := s.queries.GetSearchDatabaseTime(c)
		if err != nil {
			return err
		}
		return s.commitReuse(c, l, p, f, result, observed)
	}, 20*time.Second)
	if e != nil {
		return s.settleReuseError(ctx, l, p, e)
	}
	return nil
}
func checkReuseRoots(ctx context.Context, roots map[string]ports.TransferRoot, f reuseFrozen) error {
	for _, item := range []struct {
		path string
		id   ports.FileIdentity
	}{{f.Source.RootPath, f.SourceRootIdentity}, {f.TargetRoot.Path, f.TargetRootIdentity}} {
		actual, e := roots[item.path].Identity(ctx)
		if e != nil {
			return e
		}
		if actual.Device != item.id.Device || actual.Inode != item.id.Inode {
			return ports.ErrFileIdentity
		}
	}
	return inspectReuseMarkers(ctx, roots, f)
}
func (s *Service) settleReuseError(ctx context.Context, l ports.JobLease, p db.LibraryReusePlan, cause error) error {
	waiting := errors.Is(cause, errReuseWaiting) || errors.Is(cause, ports.ErrFileNoSpace)
	review := errors.Is(cause, ports.ErrInvalidLibraryReuse) || errors.Is(cause, ports.ErrLibraryReuseChanged) || errors.Is(cause, ports.ErrForbidden) || errors.Is(cause, ports.ErrFileUnsupported) || errors.Is(cause, fs.ErrNotExist) || reviewable(cause)
	if !waiting && !review {
		return cause
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if e = guardReuseLease(ctx, q, l); e != nil {
		return e
	}
	state := "review_required"
	if waiting {
		state = "waiting"
	}
	reason := "file_evidence_changed"
	switch {
	case errors.Is(cause, errReuseWaiting):
		reason = "root_unavailable"
	case errors.Is(cause, ports.ErrFileNoSpace):
		reason = "no_space"
	case errors.Is(cause, ports.ErrForbidden):
		reason = "authorization_changed"
	case errors.Is(cause, ports.ErrLibraryReuseChanged), errors.Is(cause, ports.ErrInvalidLibraryReuse):
		reason = "inputs_changed"
	case errors.Is(cause, ports.ErrFileUnsupported):
		reason = "action_unsupported"
	}
	if e = q.SetLibraryReuseState(ctx, db.SetLibraryReuseStateParams{ID: p.ID, State: state, Reason: reason}); e != nil {
		return e
	}
	if waiting {
		e = q.WaitLibraryReuseJob(ctx, l.ID)
	} else {
		e = q.ReviewLibraryReuseJob(ctx, l.ID)
	}
	if e != nil {
		return e
	}
	body, _ := json.Marshal(map[string]any{"plan_id": p.ID, "state": state, "reason": reason})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: "system", ActorID: l.Owner, Action: "library.reuse." + state, TargetType: "library_reuse_plan", TargetID: p.ID.String(), Payload: body}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) commitReuse(ctx context.Context, l ports.JobLease, p db.LibraryReusePlan, f reuseFrozen, target ports.FileIdentity, observed pgtype.Timestamptz) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if e = validateReuseFrozen(ctx, q, p, f); e != nil {
		return e
	}
	if e = guardReuseLease(ctx, q, l); e != nil {
		return e
	}
	root, e := q.GetLibraryRootByID(ctx, p.RootID)
	if e != nil {
		return e
	}
	// Reuse the scan-record adoption rule, including exact target incarnation and
	// an empty current mapping; this is not a downloaded-file identity.
	step := db.TransferStep{Action: p.Action, TargetRel: text(p.TargetRel)}
	file, e := s.targetFile(ctx, q, root, step, target)
	if e != nil {
		return e
	}
	profile, _ := json.Marshal(f.Quality)
	for _, u := range f.Units {
		if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: u.ID}); e != nil {
			return e
		}
		if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: s.idgen.NewUUID(), UnitID: u.ID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, RootID: pgtype.UUID{Bytes: p.RootID, Valid: true}, QualityProfile: profile}); e != nil {
			return e
		}
		payload := map[string]any{"plan_id": p.ID, "file_id": file.ID, "unit_id": u.ID, "target_path": p.TargetRel}
		if _, e = s.outbox.PublishImported(ctx, q, u.ID.String(), payload); e != nil {
			return e
		}
	}
	if _, e = q.IncrementRootMutationSeq(ctx, p.RootID); e != nil {
		return e
	}
	if _, e = q.CompleteLibraryReuse(ctx, db.CompleteLibraryReuseParams{ID: p.ID, TargetFileID: pgtype.UUID{Bytes: file.ID, Valid: true}}); e != nil {
		return e
	}
	if e = q.ConsumeLibraryReuseUnits(ctx, p.ID); e != nil {
		return e
	}
	body, _ := json.Marshal(map[string]any{"plan_id": p.ID, "file_id": file.ID, "state": "completed"})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: "system", ActorID: l.Owner, Action: "library.reuse.completed", TargetType: "library_reuse_plan", TargetID: p.ID.String(), Payload: body}); e != nil {
		return e
	}
	fresh, e := q.IsTransferObservationFresh(ctx, observed)
	if e != nil {
		return e
	}
	if !fresh.Valid || !fresh.Bool {
		return ErrReviewRequired
	}
	if e = guardReuseLease(ctx, q, l); e != nil {
		return e
	}
	if _, e = q.FinishJob(ctx, db.FinishJobParams{Column1: l.ID, Column2: l.Owner, Column3: l.Token, Column4: body}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

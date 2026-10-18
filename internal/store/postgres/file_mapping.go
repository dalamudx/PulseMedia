package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"path"
	"sort"
	"strings"
)

type FileMappingStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewFileMappingStore(pool *pgxpool.Pool, ids ports.IDGen) *FileMappingStore {
	return &FileMappingStore{pool, ids}
}

func (s *FileMappingStore) ReadFileMappingOptions(ctx context.Context, a *ports.Actor) (*ports.FileMappingOptions, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	files, e := q.ListFileMappingOptions(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	media, e := q.ListFileMappingMediaOptions(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.FileMappingOptions{Files: []ports.FileMappingOption{}, Media: []ports.FileMappingMediaOption{}, Limit: 100}
	for _, f := range files {
		out.Files = append(out.Files, ports.FileMappingOption{ID: f.ID, RootID: f.RootID, RelPath: f.RelPath, RootName: f.RootName})
	}
	for _, m := range media {
		out.Media = append(out.Media, ports.FileMappingMediaOption{ID: m.ID, Title: m.Title, MediaType: m.MediaType})
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}

type mappingSnapshot struct {
	Preview      ports.FileMappingPreview
	File         db.LibraryFile
	Root         db.LibraryRoot
	Media        db.Medium
	History      []db.ListFileMappingHistoryRow
	Units        []db.MediaUnit
	Evidence     []db.UnitFact
	Quality      []byte
	SourceExpiry pgtype.Timestamptz
}

func mappingUnit(u db.MediaUnit) ports.FileMappingUnit {
	return ports.FileMappingUnit{ID: u.ID, MediaID: u.MediaID, UnitKey: u.UnitKey, MetadataVersion: u.MetadataVersion, Retired: u.Retired}
}
func loadMapping(ctx context.Context, q *db.Queries, file, media uuid.UUID) (*mappingSnapshot, error) {
	f, e := q.GetFileByID(ctx, file)
	if e != nil {
		return nil, e
	}
	root, e := q.GetLibraryRootByID(ctx, f.RootID)
	if e != nil {
		return nil, e
	}
	m, e := q.GetMediaByID(ctx, media)
	if e != nil {
		return nil, e
	}
	hist, e := q.ListFileMappingHistory(ctx, file)
	if e != nil {
		return nil, e
	}
	units, e := q.ListMediaUnits(ctx, media)
	if e != nil {
		return nil, e
	}
	if len(hist) > 1000 || len(units) > 1000 {
		return nil, ports.ErrUnavailable
	}
	facts, e := q.ListFileMappingImportedEvidence(ctx, pgtype.UUID{Bytes: file, Valid: true})
	if e != nil {
		return nil, e
	}
	s := &mappingSnapshot{File: f, Root: root, Media: m, History: hist, Units: units, Evidence: facts}
	p := &s.Preview
	p.FileID, p.RootID, p.RelPath, p.MediaID, p.MediaTitle, p.MappingRevision = file, f.RootID, f.RelPath, media, m.Title, f.MappingRevision
	p.AvailableUnits = []ports.FileMappingUnit{}
	p.Associations = []ports.FileMappingAssociation{}
	for _, u := range units {
		p.AvailableUnits = append(p.AvailableUnits, mappingUnit(u))
	}
	for _, h := range hist {
		p.Associations = append(p.Associations, ports.FileMappingAssociation{Unit: ports.FileMappingUnit{ID: h.ID, MediaID: h.MediaID, UnitKey: h.UnitKey, MetadataVersion: h.MetadataVersion, Retired: h.Retired}, Active: !h.MappingRetiredAt.Valid})
	}
	p.CanCorrect = true
	eligible, _ := naming.IsEligibleMediaFile(f.RelPath, f.SizeBytes, 1)
	switch {
	case !eligible || f.SizeBytes <= 0 || path.IsAbs(f.RelPath) || strings.Contains(f.RelPath, "\\") || strings.HasPrefix(path.Clean(f.RelPath), "../") || path.Clean(f.RelPath) == "..":
		p.HoldReason = "unsupported_file_identity"
	case f.RetiredAt.Valid:
		p.HoldReason = "file_retired"
	case root.Health != "online" || !root.BaselineCompleted:
		p.HoldReason = "root_unavailable_or_baseline_pending"
	case root.MediaType != m.MediaType:
		p.HoldReason = "media_type_mismatch"
	case len(facts) == 0:
		p.HoldReason = "no_current_imported_file_evidence"
	case len(units) == 0:
		p.HoldReason = "no_active_target_units"
	}
	for _, fact := range facts {
		if fact.ExpiresAt.Valid && (!s.SourceExpiry.Valid || fact.ExpiresAt.Time.Before(s.SourceExpiry.Time)) {
			s.SourceExpiry = fact.ExpiresAt
		}
		if !fact.RootID.Valid || uuid.UUID(fact.RootID.Bytes) != f.RootID {
			p.HoldReason = "inconsistent_file_evidence"
			break
		}
		var profile quality.Profile
		if json.Unmarshal(fact.QualityProfile, &profile) != nil {
			p.HoldReason = "unknown_source_quality"
			break
		}
		canonical, _ := json.Marshal(profile)
		if s.Quality == nil {
			s.Quality = canonical
		} else if !bytes.Equal(s.Quality, canonical) {
			p.HoldReason = "conflicting_source_quality"
			break
		}
	}
	p.CanCorrect = p.HoldReason == ""
	raw, _ := json.Marshal([]any{f, root, m, hist, units, facts})
	hash := sha256.Sum256(raw)
	p.InputStamp = hex.EncodeToString(hash[:])
	return s, nil
}
func (s *FileMappingStore) ReadFileMappingPreview(ctx context.Context, a *ports.Actor, file, media uuid.UUID) (out *ports.FileMappingPreview, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	snapshot, e := loadMapping(ctx, q, file, media)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return &snapshot.Preview, nil
}
func (s *FileMappingStore) CorrectFileMapping(ctx context.Context, a *ports.Actor, file uuid.UUID, c ports.FileMappingCommand, key, hash string) (out *ports.FileMappingReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if file == uuid.Nil || c.MediaID == uuid.Nil || !c.ConfirmedCoverage || len(c.UnitIDs) < 1 || len(c.UnitIDs) > 20 || len(c.ExpectedStamp) != 64 || key == "" || len(key) > 160 || len(hash) != 64 {
		return nil, ports.ErrInvalidFileMapping
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrNotFound) && !errors.Is(err, ports.ErrFileMappingConflict) && !errors.Is(err, ports.ErrInvalidFileMapping) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "file.mapping.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	f, e := q.GetFileByID(ctx, file)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if _, e = q.LockAcquisitionRoot(ctx, f.RootID); e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if e == nil {
		var result ports.FileMappingReceipt
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &result) != nil || result.FileID != file {
			return nil, ports.ErrFileMappingConflict
		}
		return &result, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	before, e := loadMapping(ctx, q, file, c.MediaID)
	if e != nil {
		return nil, e
	}
	ids := map[uuid.UUID]bool{}
	for _, h := range before.History {
		if !h.MappingRetiredAt.Valid {
			ids[h.ID] = true
		}
	}
	for _, id := range c.UnitIDs {
		if id == uuid.Nil {
			return nil, ports.ErrInvalidFileMapping
		}
		ids[id] = true
	}
	ordered := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].String() < ordered[j].String() })
	for _, id := range ordered {
		if _, e = q.LockAcquisitionUnit(ctx, id); e != nil {
			return nil, e
		}
	}
	if _, e = q.LockMappingMedia(ctx, c.MediaID); e != nil {
		return nil, e
	}
	if _, e = q.LockMappingFile(ctx, file); e != nil {
		return nil, e
	}
	current, e := loadMapping(ctx, q, file, c.MediaID)
	if e != nil {
		return nil, e
	}
	if current.File.RootID != f.RootID || !current.Preview.CanCorrect || current.Preview.InputStamp != c.ExpectedStamp {
		return nil, ports.ErrFileMappingConflict
	}
	allowed := map[uuid.UUID]bool{}
	for _, u := range current.Units {
		allowed[u.ID] = true
	}
	chosen := map[uuid.UUID]bool{}
	for _, id := range c.UnitIDs {
		if !allowed[id] || chosen[id] {
			return nil, ports.ErrInvalidFileMapping
		}
		chosen[id] = true
	}
	if current.Media.MediaType == "movie" && (len(c.UnitIDs) != 1 || len(current.Units) != 1) {
		return nil, ports.ErrInvalidFileMapping
	}
	active := map[uuid.UUID]bool{}
	for _, h := range current.History {
		if !h.MappingRetiredAt.Valid {
			active[h.ID] = true
		}
	}
	changed := len(active) != len(chosen)
	for id := range chosen {
		if !active[id] {
			changed = true
		}
	}
	newFactIDs := []uuid.UUID{}
	if changed {
		if e = q.RetireFileMappingsExcept(ctx, db.RetireFileMappingsExceptParams{FileID: file, Column2: c.UnitIDs}); e != nil {
			return nil, e
		}
		for _, id := range c.UnitIDs {
			if active[id] {
				continue
			}
			if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: id}); e != nil {
				return nil, e
			}
			newFactID := s.ids.NewUUID()
			newFactIDs = append(newFactIDs, newFactID)
			if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: newFactID, UnitID: id, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: f.RootID, Valid: true}, QualityProfile: current.Quality, ExpiresAt: current.SourceExpiry}); e != nil {
				return nil, e
			}
		}
		root, e := q.IncrementRootMutationSeq(ctx, f.RootID)
		if e != nil {
			return nil, e
		}
		if e = q.SetMappingFileMutationSeq(ctx, db.SetMappingFileMutationSeqParams{ID: file, LastMutationSeq: root}); e != nil {
			return nil, e
		}
	}
	after, e := q.GetFileByID(ctx, file)
	if e != nil {
		return nil, e
	}
	out = &ports.FileMappingReceipt{FileID: file, MediaID: c.MediaID, MappingRevision: after.MappingRevision, UnitIDs: c.UnitIDs}
	raw, _ := json.Marshal(out)
	sourceFactIDs := []uuid.UUID{}
	for _, fact := range current.Evidence {
		sourceFactIDs = append(sourceFactIDs, fact.ID)
	}
	oldUnitIDs := []uuid.UUID{}
	for _, h := range current.History {
		if !h.MappingRetiredAt.Valid {
			oldUnitIDs = append(oldUnitIDs, h.ID)
		}
	}
	auditPayload, _ := json.Marshal(map[string]any{"receipt": out, "previous_mapping_revision": current.File.MappingRevision, "previous_unit_ids": oldUnitIDs, "source_imported_fact_ids": sourceFactIDs, "new_imported_fact_ids": newFactIDs, "input_stamp": c.ExpectedStamp, "confirmed_coverage": true})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "file.mapping_corrected", TargetType: "library_file", TargetID: file.String(), Payload: auditPayload}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "library_file", AggregateID: file.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "library_file", AggregateID: file.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "library_file", AggregateID: file.String(), EventType: "file.mapping_corrected", Seq: int64(seq), Payload: raw}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 200, ResponseBody: raw, ResourceID: pgtype.Text{String: file.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if current.SourceExpiry.Valid {
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return nil, e
		}
		if !now.Valid || !current.SourceExpiry.Time.After(now.Time) {
			return nil, ports.ErrFileMappingConflict
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

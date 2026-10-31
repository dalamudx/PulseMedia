package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CompleteTVSubmissionStore prepares physical protection, never sends Add.
// It is deliberately independent of the generic submission job whitelist.
type CompleteTVSubmissionStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewCompleteTVSubmissionStore(pool *pgxpool.Pool, ids ports.IDGen) *CompleteTVSubmissionStore {
	return &CompleteTVSubmissionStore{pool, ids}
}

var _ ports.CompleteTVSubmissionRepository = (*CompleteTVSubmissionStore)(nil)

type completeTVPreparedProof struct {
	Kind          string                    `json:"kind"`
	MaterialStamp string                    `json:"material_stamp"`
	InfoHash      string                    `json:"info_hash"`
	SavePath      string                    `json:"save_path"`
	TorrentSHA256 [32]byte                  `json:"torrent_sha256"`
	TotalBytes    int64                     `json:"total_bytes"`
	Files         []media.TVTorrentUnitFile `json:"files"`
	// OriginalFiles is inspected from the exact original send bytes, including
	// attachments. Only Transmission needs this persisted non-export proof.
	OriginalFiles []media.TVTorrentFile `json:"original_files,omitempty"`
}
type completeTVPreparationPayload struct {
	OperationID      uuid.UUID   `json:"operation_id"`
	Authority        uuid.UUID   `json:"complete_tv_authority"`
	CandidateID      uuid.UUID   `json:"candidate_id"`
	RootID           uuid.UUID   `json:"root_id"`
	RootGeneration   int64       `json:"root_generation"`
	ClientID         uuid.UUID   `json:"client_id"`
	UnitIDs          []uuid.UUID `json:"unit_ids"`
	MaterialStamp    string      `json:"material_stamp"`
	ObservationStamp string      `json:"observation_stamp"`
}

func (s *CompleteTVSubmissionStore) PrepareCompleteTVSubmission(ctx context.Context, c ports.PrepareCompleteTVSubmissionCommand) (out *ports.CompleteTVSubmissionReceipt, err error) {
	review := ports.ErrCompleteTVSubmissionReview
	defer func() {
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if !errors.Is(err, review) {
				err = ports.ErrUnavailable
			}
		}
	}()
	if c.Lease.ID == uuid.Nil || c.Lease.Owner == "" || c.TaskID == uuid.Nil || c.OperationID == uuid.Nil || !c.Metainfo.ValidateBinding() {
		return nil, review
	}
	// Read immutable actor before any policy/subscription/root locks.
	record, e := db.New(s.pool).GetCompleteTVExecutionAuthority(ctx, c.OperationID)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, review
		}
		return nil, e
	}
	var frozen ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(record.Snapshot, &frozen) != nil || frozen.Kind != "reviewed_complete_tv_coverage_v1" || frozen.ActorID != record.ActorID || frozen.ReviewID != record.ReviewID || frozen.SubscriptionID != record.SubscriptionID || frozen.RootID != record.RootID || frozen.CandidateID != record.CandidateID || frozen.ClientID != record.ClientID || len(frozen.Units) < 2 || len(frozen.Units) > 1000 {
		return nil, review
	}
	actor := &ports.Actor{ID: frozen.ActorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, actor); e != nil {
		if errors.Is(e, ports.ErrForbidden) || errors.Is(e, ports.ErrUnauthorized) {
			return nil, review
		}
		return nil, e
	}
	policy, e := q.LockSearchPolicy(ctx, frozen.PolicyID)
	if e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, frozen.SubscriptionID)
	if e != nil {
		return nil, e
	}
	root, e := q.LockAcquisitionRoot(ctx, frozen.RootID)
	if e != nil {
		return nil, e
	}
	// Full-media UUID order also protects against changed current season membership.
	if _, e = q.LockScopeMediaUnits(ctx, frozen.MediaID); e != nil {
		return nil, e
	}
	if e = q.LockDownloadAcquisition(ctx, c.OperationID); e != nil {
		return nil, e
	}
	if _, e = q.LockSubmissionMedia(ctx, frozen.MediaID); e != nil {
		return nil, e
	}
	if _, e = q.LockJobForWrite(ctx, c.Lease.ID); e != nil {
		return nil, e
	}
	auth := db.GetAuthorizedCompleteTVPreparationJobParams{ID: c.Lease.ID, Owner: c.Lease.Owner, Token: c.Lease.Token}
	job, e := q.GetAuthorizedCompleteTVPreparationJob(ctx, auth)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, review
		}
		return nil, e
	}
	var payload completeTVPreparationPayload
	if json.Unmarshal(job.Payload, &payload) != nil || payload.OperationID != c.OperationID || payload.Authority != c.OperationID || payload.CandidateID != frozen.CandidateID || payload.RootID != frozen.RootID || payload.RootGeneration != frozen.RootGeneration || payload.ClientID != frozen.ClientID || payload.MaterialStamp != frozen.MaterialStamp || payload.ObservationStamp != frozen.ObservationStamp {
		return nil, review
	}
	rows := append([]media.NumberingRow(nil), frozen.Units...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].UnitID.String() < rows[j].UnitID.String() })
	if len(payload.UnitIDs) != len(rows) {
		return nil, review
	}
	declared := append([]uuid.UUID(nil), payload.UnitIDs...)
	sort.Slice(declared, func(i, j int) bool { return declared[i].String() < declared[j].String() })
	own := map[uuid.UUID]bool{}
	for i, row := range rows {
		if row.UnitID == uuid.Nil || own[row.UnitID] || declared[i] != row.UnitID {
			return nil, review
		}
		own[row.UnitID] = true
	}
	hint, e := q.GetCandidateByID(ctx, frozen.CandidateID)
	if e != nil {
		return nil, e
	}
	if _, e = q.LockSubmissionIndexer(ctx, hint.IndexerID); e != nil {
		return nil, e
	}
	candidate, e := q.LockSubmissionCandidate(ctx, hint.ID)
	if e != nil {
		return nil, e
	}
	client, e := q.LockEpisodeSubmissionClient(ctx, frozen.ClientID)
	if e != nil {
		return nil, e
	}
	if candidate.InfoHash.Valid && candidate.InfoHash.String != "" && strings.ToLower(candidate.InfoHash.String) != c.Metainfo.InfoHash() {
		return nil, review
	}
	if c.Metainfo.TotalBytes() != frozen.CandidateSize || candidate.SizeBytes != frozen.CandidateSize {
		return nil, review
	}
	if policy.CurrentRevision != frozen.PolicyRevision || sub.UserID != frozen.ActorID || sub.State != "active" || sub.MediaID != frozen.MediaID || sub.RootID != frozen.RootID || sub.IntentVersion != frozen.IntentVersion || sub.QualityPolicyID != frozen.PolicyID || sub.QualityPolicyRevision != frozen.PolicyRevision || !sub.DownloaderID.Valid || uuid.UUID(sub.DownloaderID.Bytes) != frozen.ClientID || root.Generation != frozen.RootGeneration || root.Health != "online" || !root.BaselineCompleted || root.MediaType != "tv" || episodeClientStamp(client) != frozen.ClientStamp {
		return nil, review
	}
	// An existing physical task is never released or silently replaced.
	existing, e := q.GetDownloadTaskByOpID(ctx, c.OperationID)
	replay := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	extraBumps := int64(0)
	if replay {
		saved, e := q.GetCompleteTVPreparation(ctx, c.OperationID)
		if e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return nil, review
			}
			return nil, e
		}
		digest := c.Metainfo.SHA256()
		if existing.ID != c.TaskID || existing.State != "submitting" || existing.ClientID != frozen.ClientID || existing.CandidateID != frozen.CandidateID || existing.InfoHash != c.Metainfo.InfoHash() || existing.SavePath != root.Path || saved.TaskID != existing.ID || saved.JobID != c.Lease.ID || saved.LeaseOwner != c.Lease.Owner || saved.LeaseToken != c.Lease.Token || saved.InfoHash != c.Metainfo.InfoHash() || !bytes.Equal(saved.TorrentSha256, digest[:]) {
			return nil, review
		}
		extraBumps = 3
	}
	var proof completeTVPreparedProof
	// All checks run after waits and again after the final writes. The only ignored
	// fact bumps are the three writes owned here plus schema59's original two.
	validate := func() error {
		if !c.Metainfo.ValidateBinding() {
			return review
		}
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return e
		}
		v, e := readEpisodeCandidate(ctx, q, actor, sub.ID, candidate.ID)
		if e != nil {
			return e
		}
		current, e := readTVCoverageCandidate(ctx, q, actor, sub.ID, candidate.ID)
		if e != nil {
			return e
		}
		source, e := readEpisodeSourceContext(ctx, q, actor, sub.ID)
		if e != nil {
			return e
		}
		sort.Slice(current.Rows, func(i, j int) bool { return current.Rows[i].UnitID.String() < current.Rows[j].UnitID.String() })
		if !current.CanReview || current.MaterialStamp != frozen.MaterialStamp || completeTVJSONDigest(current.Material) != completeTVJSONDigest(frozen.Material) || coverageDigest(current.Rows) != coverageDigest(frozen.Units) || coverageDigest(source.CompleteSeasons) != coverageDigest(frozen.CompleteSeasons) || !completeTVEligible(v, rows, own, now.Time) || (v.Mapping.ValidUntil != nil && !v.Mapping.ValidUntil.After(now.Time)) {
			return review
		}
		for i := range v.Target.Units {
			if own[v.Target.Units[i].UnitID] {
				v.Target.Units[i].FactsVersion -= extraBumps
			}
		}
		if completeTVJSONDigest(completeTVFacts(v.Target.Units, own)) != completeTVJSONDigest(frozen.Facts) {
			return review
		}
		canonical := make([]media.NumberingUnit, 0, len(source.Units))
		for _, u := range source.Units {
			canonical = append(canonical, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Version: u.Version, Key: u.Key, Season: u.Season, Episode: u.Episode, Retired: u.Retired})
		}
		files, e := media.ValidateCompleteTVTorrentFiles(c.Metainfo.Files(), rows, canonical)
		if e != nil {
			return review
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Unit.UnitID.String() < files[j].Unit.UnitID.String() })
		proof = completeTVPreparedProof{Kind: "complete_tv_prepared_v2", MaterialStamp: frozen.MaterialStamp, SavePath: root.Path, InfoHash: c.Metainfo.InfoHash(), TorrentSHA256: c.Metainfo.SHA256(), TotalBytes: c.Metainfo.TotalBytes(), Files: files}
		if client.ClientType == "transmission" {
			proof.OriginalFiles = c.Metainfo.Files()
		}
		demands, e := q.ListRequirementsForOperation(ctx, c.OperationID)
		if e != nil {
			return e
		}
		if len(demands) != len(rows) {
			return review
		}
		seen := map[uuid.UUID]bool{}
		for _, d := range demands {
			if !own[d.UnitID] || seen[d.UnitID] || d.State != "pending" || d.RootID != root.ID || d.ActorID != actor.ID || d.ActorType != string(actor.Type) {
				return review
			}
			seen[d.UnitID] = true
			var row media.NumberingRow
			for _, r := range rows {
				if r.UnitID == d.UnitID {
					row = r
				}
			}
			if completeTVJSONDigest(d.SubmissionFence) != completeTVJSONDigest(completeTVFence(actor, sub, root.Generation, candidate, row, frozen.SourceStamp, frozen.MediaVersion)) {
				return review
			}
		}
		lanes, e := q.ListCompleteTVPreparationLanes(ctx, c.OperationID)
		if e != nil {
			return e
		}
		reservations, e := q.ListCompleteTVPreparationReservations(ctx, c.OperationID)
		if e != nil {
			return e
		}
		if len(lanes) != len(rows) || len(reservations) != len(rows) {
			return review
		}
		// Row locks can wait past reservation expiry even while the job/source
		// remain valid. Re-read database time after both protection sets are held.
		now, e = q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return e
		}
		for i, row := range rows {
			l, r := lanes[i], reservations[i]
			if l.UnitID != row.UnitID || l.AcquisitionLane != "default" || l.LegacyConflict || l.SourceRootID != root.ID || l.SourceGeneration != root.Generation || l.SourceStamp != frozen.SourceStamp || l.CandidateID != candidate.ID || l.TaskID.Valid != (extraBumps > 0) || (l.TaskID.Valid && uuid.UUID(l.TaskID.Bytes) != c.TaskID) || r.UnitID != row.UnitID || r.RootID != root.ID || r.CandidateID != candidate.ID || r.Purpose != "manual_acquisition" || !r.ExpiresAt.Valid || !r.ExpiresAt.Time.After(now.Time) || (extraBumps == 0 && r.State != "reserved") || (extraBumps > 0 && r.State != "dispatched") {
				return review
			}
		}
		// Recheck strict source expiry after the above row-lock waits, not just before them.
		tail, e := readTVCoverageCandidate(ctx, q, actor, sub.ID, candidate.ID)
		if e != nil {
			return e
		}
		if !tail.CanReview || tail.MaterialStamp != frozen.MaterialStamp {
			return review
		}
		if _, e = q.GetAuthorizedCompleteTVPreparationJob(ctx, auth); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return review
			}
			return e
		}
		return nil
	}
	if e = validate(); e != nil {
		return nil, e
	}
	if replay {
		saved, e := q.GetCompleteTVPreparation(ctx, c.OperationID)
		if e != nil {
			return nil, e
		}
		raw, _ := json.Marshal(proof)
		units, e := q.ListCompleteTVPreparedUnits(ctx, existing.ID)
		if e != nil {
			return nil, e
		}
		if completeTVJSONDigest(saved.Proof) != completeTVJSONDigest(raw) || len(units) != len(proof.Files) {
			return nil, review
		}
		for i, u := range units {
			f := proof.Files[i]
			if u.UnitID != f.Unit.UnitID || u.UnitVersion != f.Unit.UnitVersion || u.ProviderEpisodeID != f.Unit.EpisodeID || u.UnitKey != f.Unit.UnitKey || u.VideoPath != f.Path || u.VideoSize != f.Size {
				return nil, review
			}
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &ports.CompleteTVSubmissionReceipt{TaskID: existing.ID, State: "prepared_not_sent", AlreadyExisted: true}, nil
	}
	if _, e = q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: c.TaskID, OperationID: c.OperationID, ClientID: frozen.ClientID, CandidateID: candidate.ID, InfoHash: c.Metainfo.InfoHash(), State: "submitting", SavePath: root.Path}); e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(proof)
	digest := c.Metainfo.SHA256()
	if e = q.CreateCompleteTVPreparation(ctx, db.CreateCompleteTVPreparationParams{OperationID: c.OperationID, TaskID: c.TaskID, JobID: c.Lease.ID, LeaseOwner: c.Lease.Owner, LeaseToken: c.Lease.Token, InfoHash: c.Metainfo.InfoHash(), TorrentSha256: digest[:], Proof: raw}); e != nil {
		return nil, e
	}
	for _, f := range proof.Files {
		if e = q.CreateCompleteTVPreparedUnit(ctx, db.CreateCompleteTVPreparedUnitParams{TaskID: c.TaskID, UnitID: f.Unit.UnitID, UnitVersion: f.Unit.UnitVersion, ProviderEpisodeID: f.Unit.EpisodeID, UnitKey: f.Unit.UnitKey, VideoPath: f.Path, VideoSize: f.Size}); e != nil {
			return nil, e
		}
	}
	if e = q.BindPhysicalAcquisitionTask(ctx, db.BindPhysicalAcquisitionTaskParams{OperationID: c.OperationID, TaskID: pgtype.UUID{Bytes: c.TaskID, Valid: true}}); e != nil {
		return nil, e
	}
	n, e := q.DispatchCompleteTVReservations(ctx, c.OperationID)
	if e != nil {
		return nil, e
	}
	if n != int64(len(rows)) {
		return nil, review
	}
	extraBumps = 3
	out = &ports.CompleteTVSubmissionReceipt{TaskID: c.TaskID, State: "prepared_not_sent"}
	safe, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: c.Lease.Owner, Action: "complete_tv.submission_prepared", TargetType: "download_task", TargetID: c.TaskID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if _, e = (&AcquisitionStore{idgen: s.ids}).publishAcquisitionNotification(ctx, q, "acquisition", c.OperationID.String(), "complete_tv.submission_prepared", map[string]any{"operation_id": c.OperationID, "task_id": c.TaskID, "state": "prepared_not_sent"}); e != nil {
		return nil, e
	}
	if e = validate(); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

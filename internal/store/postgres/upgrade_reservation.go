package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

type UpgradeReservationStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewUpgradeReservationStore(p *pgxpool.Pool, ids ports.IDGen) *UpgradeReservationStore {
	return &UpgradeReservationStore{p, ids}
}

var _ ports.UpgradeReservationRepository = (*UpgradeReservationStore)(nil)

func upgradeKey(key string) bool {
	return key != "" && strings.TrimSpace(key) == key && len(key) <= 130
}
func upgradeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrUpgradeReservationConflict) || errors.Is(err, ports.ErrAcquisitionUnitAlreadyReserved) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ports.ErrUnavailable
}
func upgradeRollback(ctx context.Context, tx pgx.Tx) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(c)
}

func upgradeReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, key, hash string, out any) (bool, error) {
	if e := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "upgrade.command", AggregateID: a.ID + ":" + key}); e != nil {
		return false, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if old.RequestHash != hash || old.ResponseCode != 202 || json.Unmarshal(old.ResponseBody, out) != nil {
		return false, ports.ErrUpgradeReservationConflict
	}
	return true, nil
}

// ownedUpgradeRows verifies exact rows, not just counts. A late trigger changing
// a fence, lease, state, audit payload or receipt cannot hide behind ownership.
type ownedUpgradeRows map[string]map[uuid.UUID]string

func (o ownedUpgradeRows) remember(ctx context.Context, tx pgx.Tx, table string, id uuid.UUID) error {
	if e := upgradeValidateOwned(ctx, tx, table, id); e != nil {
		return e
	}
	var raw json.RawMessage
	if e := tx.QueryRow(ctx, "SELECT to_jsonb(x) FROM "+table+" x WHERE "+upgradePK(table)+"=$1", id).Scan(&raw); e != nil {
		return e
	}
	if o[table] == nil {
		o[table] = map[uuid.UUID]string{}
	}
	o[table][id] = completeTVJSONDigest(raw)
	return nil
}
func upgradePK(table string) string {
	if table == "movie_upgrade_authority" {
		return "operation_id"
	}
	return "id"
}
func (o ownedUpgradeRows) verify(ctx context.Context, tx pgx.Tx) error {
	for table, rows := range o {
		for id, want := range rows {
			var raw json.RawMessage
			if e := tx.QueryRow(ctx, "SELECT to_jsonb(x) FROM "+table+" x WHERE "+upgradePK(table)+"=$1", id).Scan(&raw); e != nil {
				return e
			}
			if completeTVJSONDigest(raw) != want {
				return ports.ErrUpgradeReservationConflict
			}
		}
	}
	return nil
}

// success writes only durable audit, an outbox event and a stable receipt. The
// event is persistence evidence, not notification delivery or external execution.
func (s *UpgradeReservationStore) upgradeSuccess(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub uuid.UUID, key, hash, action string, resource uuid.UUID, result any, own ownedUpgradeRows, check func() error) error {
	q := db.New(tx)
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	audit := s.ids.NewUUID()
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: audit, ActorType: string(a.Type), ActorID: a.ID, Action: action, TargetType: "subscription", TargetID: sub.String(), Payload: raw}); e != nil {
		return e
	}
	if e = upgradeValidateSuccess(ctx, tx, "audit_log", audit, a, sub, key, hash, action, resource, raw); e != nil {
		return e
	}
	if e = own.remember(ctx, tx, "audit_log", audit); e != nil {
		return e
	}
	if e = check(); e != nil {
		return e
	}
	event := s.ids.NewUUID()
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: event, AggregateType: "upgrade.command", AggregateID: resource.String(), EventType: action, Payload: raw, Seq: 1}); e != nil {
		return e
	}
	if e = upgradeValidateSuccess(ctx, tx, "outbox_event", event, a, sub, key, hash, action, resource, raw); e != nil {
		return e
	}
	if e = own.remember(ctx, tx, "outbox_event", event); e != nil {
		return e
	}
	if e = check(); e != nil {
		return e
	}
	receipt := s.ids.NewUUID()
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: receipt, ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: resource.String(), Valid: true}}); e != nil {
		return e
	}
	if e = upgradeValidateSuccess(ctx, tx, "idempotency_record", receipt, a, sub, key, hash, action, resource, raw); e != nil {
		return e
	}
	if e = own.remember(ctx, tx, "idempotency_record", receipt); e != nil {
		return e
	}
	if e = check(); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
		return e
	}
	return check()
}

func (s *UpgradeReservationStore) ReserveUpgrade(ctx context.Context, a *ports.Actor, c ports.ReserveUpgradeCommand, key string) (out *ports.UpgradeReservationResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	conflict := ports.ErrUpgradeReservationConflict
	if !upgradeKey(key) || !c.Confirmed || c.SubscriptionID == uuid.Nil || c.CurrentFileID == uuid.Nil || c.CandidateID == uuid.Nil || !completeTVStamp(c.Expected.InputStamp) || !completeTVStamp(c.ExpectedClientDigest) {
		return nil, conflict
	}
	defer func() { err = upgradeError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	q, sub, e := lockUpgradeCommand(ctx, tx, a, c.SubscriptionID)
	if e != nil {
		return nil, e
	}
	key = "upgrade.reserve:" + key
	hash := coverageDigest([]any{"upgrade.reserve.v1", c})
	var old ports.UpgradeReservationResult
	if found, e := upgradeReceipt(ctx, q, a, key, hash, &old); e != nil {
		return nil, e
	} else if found {
		if old.Status != "reserved_not_executable" || old.ExecutionAllowed || old.OperationID == uuid.Nil || old.ReservationID == uuid.Nil {
			return nil, conflict
		}
		var actor, subscription uuid.UUID
		if e = tx.QueryRow(ctx, "SELECT actor_id,subscription_id FROM movie_upgrade_authority WHERE operation_id=$1", old.OperationID).Scan(&actor, &subscription); e != nil {
			return nil, e
		}
		if actor != uuid.MustParse(a.ID) || subscription != c.SubscriptionID {
			return nil, conflict
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &old, nil
	}
	var mode ports.UpgradeMode
	if e = tx.QueryRow(ctx, "SELECT upgrade_mode FROM subscription WHERE id=$1", sub.ID).Scan(&mode); e != nil {
		return nil, e
	}
	if !mode.Enabled() || sub.State != "active" || !sub.DownloaderID.Valid {
		return nil, conflict
	}
	root, e := q.LockAcquisitionRoot(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	if _, e = q.LockScopeMediaUnits(ctx, sub.MediaID); e != nil {
		return nil, e
	}
	// File/fact locks precede operation/source/config/candidate locks.
	if _, e = q.ListUpgradeFiles(ctx, db.ListUpgradeFilesParams{RootID: sub.RootID, UnitID: c.Expected.UnitID}); e != nil {
		return nil, e
	}
	if _, e = q.ListUpgradeFacts(ctx, db.ListUpgradeFactsParams{SourceFileID: pgtype.UUID{Bytes: c.CurrentFileID, Valid: true}, UnitID: c.Expected.UnitID, RootID: pgtype.UUID{Bytes: sub.RootID, Valid: true}}); e != nil {
		return nil, e
	}
	op := s.ids.NewUUID()
	if e = q.LockDownloadAcquisition(ctx, op); e != nil {
		return nil, e
	}
	cand, e := q.GetCandidateByID(ctx, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM indexer WHERE id=$1 FOR SHARE", cand.IndexerID); e != nil {
		return nil, e
	}
	clientID := uuid.UUID(sub.DownloaderID.Bytes)
	if _, e = tx.Exec(ctx, "SELECT id FROM download_client WHERE id=$1 FOR SHARE", clientID); e != nil {
		return nil, e
	}
	client, e := q.GetDownloadClientByID(ctx, clientID)
	if e != nil {
		return nil, e
	}
	if episodeClientStamp(client) != c.ExpectedClientDigest {
		return nil, conflict
	}
	if client.ClientType != "qbittorrent" || strings.TrimSpace(client.Url) == "" || len(client.PasswordEncrypted) == 0 {
		return nil, conflict
	}
	v, e := readUpgradePreview(ctx, q, a, ports.UpgradePreviewRequest{SubscriptionID: sub.ID, CurrentFileID: c.CurrentFileID, CandidateID: c.CandidateID})
	if e != nil {
		return nil, e
	}
	if v.Version != c.Expected || v.HoldReason != "" || (mode == ports.UpgradeStopAtCutoff && v.Candidate.Rules.CutoffScore <= 0) || root.MediaType != "movie" {
		return nil, conflict
	}
	if media.ExplainMovieCandidate(v.Candidate).Decision != "accept" || !quality.EvaluateUpgradeForMode(v.Candidate.Rules, v.Current, v.Candidate.Profile, mode).ShouldUpgrade {
		return nil, conflict
	}
	// Never reuse acquire authority, unknown/submitting work, or any live lane.
	var occupied bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM acquisition_reservation WHERE unit_id=$1 AND released_at IS NULL) OR EXISTS(SELECT 1 FROM unit_reservation WHERE unit_id=$1 AND state IN ('reserved','dispatched'))`, v.Version.UnitID).Scan(&occupied)
	if e != nil {
		return nil, e
	}
	if occupied {
		return nil, ports.ErrAcquisitionUnitAlreadyReserved
	}
	baseline, now, until, e := upgradeEvidence(ctx, tx, a, v, op, 0)
	if e != nil {
		return nil, e
	}
	own := ownedUpgradeRows{}
	bumps := 0
	check := func() error {
		if e := own.verify(ctx, tx); e != nil {
			return e
		}
		var physical, lanes, reservations, requirements int
		if e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM download_task WHERE operation_id=$1)+(SELECT count(*) FROM job WHERE payload->>'operation_id'=$1::text),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1),(SELECT count(*) FROM unit_reservation WHERE operation_id=$1),(SELECT count(*) FROM download_requirement WHERE operation_id=$1)`, op).Scan(&physical, &lanes, &reservations, &requirements); e != nil {
			return e
		}
		if physical != 0 || lanes != len(own["acquisition_reservation"]) || reservations != len(own["unit_reservation"]) || requirements != len(own["download_requirement"]) {
			return conflict
		}
		fresh, _, _, e := upgradeEvidence(ctx, tx, a, v, op, bumps)
		if e != nil {
			return e
		}
		if fresh != baseline {
			return conflict
		}
		return nil
	}
	identities, e := upgradeFrozenIdentities(ctx, tx, v)
	if e != nil {
		return nil, e
	}
	snap := upgradeFrozenSnapshot{FrozenIdentities: identities, Kind: "frozen_movie_upgrade_v1", Goal: "upgrade", Confirmed: true, Mode: mode, Observation: v.Version, CurrentFileID: v.CurrentFileID, CurrentFactID: v.CurrentFactID, CandidateID: v.CandidateID, SourceID: v.SourceID, ClientID: clientID, ClientDigest: episodeClientStamp(client), ProtectedDigest: baseline, SourceStamp: quality.AcquisitionSourceStamp(cand.Title, cand.InfoHash.String, cand.LinkEncrypted, cand.QualityProfile), ParserVersion: media.EpisodeParserVersion, EvaluatorVersion: media.CandidateDecisionVersion, RootGeneration: root.Generation, UnitVersion: v.Candidate.UnitVersion, Target: *v, ObservedAt: now, ValidUntil: until}
	raw, _ := json.Marshal(snap)
	_, e = tx.Exec(ctx, `INSERT INTO movie_upgrade_authority(operation_id,actor_id,subscription_id,intent_version,unit_id,root_id,root_generation,old_file_id,old_fact_id,candidate_id,client_id,policy_id,policy_revision,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, op, uuid.MustParse(a.ID), sub.ID, sub.IntentVersion, v.Version.UnitID, root.ID, root.Generation, v.CurrentFileID, v.CurrentFactID, cand.ID, clientID, v.Version.PolicyID, v.Version.PolicyRevision, raw)
	if e != nil {
		return nil, e
	}
	validAuthority, e := q.MatchesMovieUpgradeAuthority(ctx, db.MatchesMovieUpgradeAuthorityParams{
		OperationID: op, ActorID: uuid.MustParse(a.ID), SubscriptionID: sub.ID,
		IntentVersion: sub.IntentVersion, UnitID: v.Version.UnitID,
		RootID: root.ID, RootGeneration: root.Generation,
		OldFileID: v.CurrentFileID, OldFactID: v.CurrentFactID,
		CandidateID: cand.ID, ClientID: clientID,
		PolicyID: v.Version.PolicyID, PolicyRevision: v.Version.PolicyRevision, Snapshot: raw,
	})
	if e != nil {
		return nil, e
	}
	if !validAuthority.Valid || !validAuthority.Bool {
		return nil, conflict
	}
	if e = own.remember(ctx, tx, "movie_upgrade_authority", op); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	lane := s.ids.NewUUID()
	if _, e = q.CreateAcquisitionLane(ctx, db.CreateAcquisitionLaneParams{ID: lane, UnitID: v.Version.UnitID, OperationID: op, CandidateID: cand.ID, SourceRootID: root.ID, SourceGeneration: root.Generation, SourceStamp: snap.SourceStamp}); e != nil {
		return nil, e
	}
	bumps = 1
	if e = own.remember(ctx, tx, "acquisition_reservation", lane); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	reservation := s.ids.NewUUID()
	if _, e = q.CreateUnitReservation(ctx, db.CreateUnitReservationParams{ID: reservation, UnitID: v.Version.UnitID, RootID: root.ID, Purpose: "upgrade", CandidateID: cand.ID, OperationID: op}); e != nil {
		return nil, e
	}
	bumps = 2
	if e = own.remember(ctx, tx, "unit_reservation", reservation); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	requirement := s.ids.NewUUID()
	// Deliberately distinct from DownloadDemandFence's acquire contract.
	fence, _ := json.Marshal(map[string]any{"goal": "upgrade", "status": "reserved_not_executable", "authority": op, "protected_digest": baseline, "observation": v.Version})
	if _, e = q.CreateDownloadRequirement(ctx, db.CreateDownloadRequirementParams{ID: requirement, OperationID: op, UnitID: v.Version.UnitID, RootID: root.ID, ActorType: string(a.Type), ActorID: a.ID, SubmissionFence: fence}); e != nil {
		return nil, e
	}
	if e = own.remember(ctx, tx, "download_requirement", requirement); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	out = &ports.UpgradeReservationResult{OperationID: op, ReservationID: reservation, Status: "reserved_not_executable"}
	if e = s.upgradeSuccess(ctx, tx, a, sub.ID, key, hash, "upgrade.reserve", op, out, own, check); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

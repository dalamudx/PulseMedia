package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type tvUpgradeRetirementPlan struct {
	Ingestion     tvUpgradeIngestion `json:"ingestion"`
	RecycleRel    string             `json:"recycle_rel"`
	RetentionDays int                `json:"retention_days"`
}
type tvUpgradeRetirementReturn struct {
	Plan          tvUpgradeRetirementPlan `json:"plan"`
	RetainedUntil time.Time               `json:"retained_until"`
}

func (x *tvUpgradeSession) retirement(ctx context.Context) (*ports.TVEpisodeUpgradeResult, error) {
	if x.store.recycler == nil {
		return nil, ports.ErrUnavailable
	}
	var plan tvUpgradeRetirementPlan
	if x.command.Action == "retire" {
		if e := x.transaction(ctx, false, func(tx pgx.Tx) error { return x.evidence(ctx, tx, "retirement-confirm", &plan) }); e != nil {
			return nil, e
		}
	} else {
		if e := x.transaction(ctx, false, func(tx pgx.Tx) error {
			if e := x.evidence(ctx, tx, "organize", &plan.Ingestion); e != nil {
				return e
			}
			plan.RetentionDays = 7
			plan.RecycleRel = filepath.Join(".pulsemedia_recycle", "tv-upgrade", x.authority.OperationID.String(), filepath.Base(plan.Ingestion.Plan.Physical.OldRel))
			return x.tvUpgradeRetirementBasis(ctx, tx, plan)
		}); e != nil {
			return nil, e
		}
	}
	if plan.RetentionDays != 7 || plan.RecycleRel != filepath.Join(".pulsemedia_recycle", "tv-upgrade", x.authority.OperationID.String(), filepath.Base(plan.Ingestion.Plan.Physical.OldRel)) {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	roots, release, e := x.store.recycler.LockRoots(ctx, plan.Ingestion.Plan.Physical.RootPath)
	if e != nil {
		return nil, e
	}
	defer release()
	root := roots[plan.Ingestion.Plan.Physical.RootPath]
	if root == nil {
		return nil, ports.ErrFileIdentity
	}
	if e = x.tvUpgradeRetirementInspect(ctx, root, plan, false); e != nil {
		return nil, e
	}
	out := x.result("retirement_preview_not_executable")
	out.NewFileID = plan.Ingestion.FileID
	out.Version = coverageDigest(plan)
	if x.command.Action != "retire" {
		e = x.transaction(ctx, false, func(tx pgx.Tx) error {
			if e := x.tvUpgradeRetirementBasis(ctx, tx, plan); e != nil {
				return e
			}
			if x.command.Action == "retirement-preview" {
				return nil
			}
			if x.command.ExpectedVersion != out.Version {
				return ports.ErrTVEpisodeUpgradeConflict
			}
			out.Status = "retirement_confirmed_not_executable"
			if e := x.phase(ctx, tx, "retirement-confirm", out, plan); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
				return e
			}
			return x.tvUpgradeRetirementBasis(ctx, tx, plan)
		})
		return out, e
	}
	if x.command.ExpectedVersion != out.Version {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if e = x.transaction(ctx, false, func(tx pgx.Tx) error { return x.tvUpgradeRetirementBasis(ctx, tx, plan) }); e != nil {
		return nil, e
	}
	permit, e := x.permit(ctx, "retire", false)
	if e != nil {
		return nil, e
	}
	if e = x.transaction(ctx, false, func(tx pgx.Tx) error {
		if e := x.tvUpgradeRetirementBasis(ctx, tx, plan); e != nil {
			return e
		}
		return x.validPermit(ctx, tx, permit)
	}); e != nil {
		return nil, e
	}
	if e = x.tvUpgradeRetirementInspect(ctx, root, plan, false); e != nil {
		return nil, e
	}
	physical := plan.Ingestion.Plan.Physical
	callErr := x.store.recycler.Recycle(ctx, root, physical.OldRel, plan.RecycleRel, physical.Old)
	trusted := callErr == nil && ctx.Err() == nil
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var observed time.Time
	if e = x.conn.QueryRow(record, "SELECT clock_timestamp()").Scan(&observed); e != nil {
		return nil, e
	}
	if trusted {
		trusted = x.tvUpgradeRetirementInspect(record, root, plan, true) == nil
	}
	out.Status = "retirement_unknown_requires_observation"
	if !trusted {
		e = x.recordReturn(record, permit, "unknown", out, plan, false, nil)
		return out, e
	}
	returned := tvUpgradeRetirementReturn{Plan: plan}
	out.Status = "retired"
	out.NewFileID = plan.Ingestion.FileID
	e = x.transaction(record, false, func(tx pgx.Tx) error {
		if e := x.tvUpgradeRetirementBasis(record, tx, plan); e != nil {
			return e
		}
		if e := x.validPermit(record, tx, permit); e != nil {
			return e
		}
		var at time.Time
		if e := tx.QueryRow(record, "SELECT clock_timestamp()").Scan(&at); e != nil {
			return e
		}
		returned.RetainedUntil = at.Add(7 * 24 * time.Hour)
		expected, e := x.tvUpgradeRetirementGraph(record, tx, plan, &at)
		if e != nil {
			return e
		}
		q := db.New(tx)
		retiredAt := pgtype.Timestamptz{Time: at, Valid: true}
		rows, e := q.RevokeTVEpisodeUpgradeOldFact(record, db.RevokeTVEpisodeUpgradeOldFactParams{OperationID: x.authority.OperationID, RetiredAt: retiredAt})
		if e != nil {
			return e
		}
		if rows != 1 {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		rows, e = q.RetireTVEpisodeUpgradeOldMapping(record, db.RetireTVEpisodeUpgradeOldMappingParams{OperationID: x.authority.OperationID, RetiredAt: retiredAt})
		if e != nil {
			return e
		}
		if rows != 1 {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		rows, e = q.RetireTVEpisodeUpgradeOldFile(record, db.RetireTVEpisodeUpgradeOldFileParams{OperationID: x.authority.OperationID, RetiredAt: retiredAt})
		if e != nil {
			return e
		}
		if rows != 1 {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		for _, statement := range []string{
			`UPDATE acquisition_reservation SET released_at=$2 WHERE operation_id=$1 AND released_at IS NULL`,
			`UPDATE unit_reservation SET state='consumed',updated_at=$2 WHERE operation_id=$1 AND state IN ('reserved','dispatched')`,
			`UPDATE download_requirement SET state='imported' WHERE operation_id=$1 AND state='pending' AND $2::timestamptz IS NOT NULL`,
		} {
			tag, e := tx.Exec(record, statement, x.authority.OperationID, at)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ports.ErrTVEpisodeUpgradeConflict
			}
		}
		rows, e = q.AdvanceTVEpisodeUpgradeRetiredRoot(record, db.AdvanceTVEpisodeUpgradeRetiredRootParams{OperationID: x.authority.OperationID, RetiredAt: retiredAt})
		if e != nil {
			return e
		}
		if rows != 1 {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		ev, e := json.Marshal(returned)
		if e != nil {
			return e
		}
		if e = db.New(tx).InsertTVEpisodeUpgradePhysicalReturn(record, db.InsertTVEpisodeUpgradePhysicalReturnParams{OperationID: x.authority.OperationID, Action: "retire", Owner: permit.Owner, Token: permit.Token, Status: "confirmed", Evidence: ev}); e != nil {
			return e
		}
		if e = x.phase(record, tx, "retire", out, returned); e != nil {
			return e
		}
		if _, e = tx.Exec(record, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
			return e
		}
		actual, e := x.tvUpgradeRetirementGraph(record, tx, plan, nil)
		if e != nil {
			return e
		}
		if actual != expected {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		if e = x.tvUpgradeImportedCurrent(record, tx, plan.Ingestion); e != nil {
			return e
		}
		if e = x.tvUpgradeObservedFresh(record, tx, observed); e != nil {
			return e
		}
		return x.validPermit(record, tx, permit)
	})
	return out, e
}

// The ordinary preview deliberately requires one active file. Retirement has two
// verified active files, and compares original material after subtracting only
// this operation's exact ingestion, never relaxing the old-file checks.
func (x *tvUpgradeSession) tvUpgradeRetirementBasis(ctx context.Context, tx pgx.Tx, p tvUpgradeRetirementPlan) error {
	in := p.Ingestion
	if in.FileID == uuid.Nil || in.FactID == uuid.Nil || in.FileID == x.authority.Request.CurrentFileID {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	if e := x.tvUpgradeLockMaterial(ctx, tx, in.Plan); e != nil {
		return e
	}
	var persisted tvUpgradeIngestion
	if e := x.evidence(ctx, tx, "organize", &persisted); e != nil {
		return e
	}
	if coverageDigest(persisted) != coverageDigest(in) {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	confirmed, e := db.New(tx).CheckTVEpisodeUpgradeRetirementBasis(ctx, db.CheckTVEpisodeUpgradeRetirementBasisParams{OperationID: x.authority.OperationID, Evidence: mustTVUpgradeJSON(in)})
	if e != nil {
		return e
	}
	if !confirmed.Valid || !confirmed.Bool {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	if e = x.tvUpgradePlanCurrent(ctx, tx, in.Plan, in.FileID, in.FactID); e != nil {
		return e
	}
	if e = x.tvUpgradeImportedCurrent(ctx, tx, in); e != nil {
		return e
	}
	var old quality.Profile
	var rules quality.PolicyRules
	qualityRow, e := db.New(tx).ReadTVEpisodeUpgradeRetirementQuality(ctx, x.authority.OperationID)
	oldRaw, rulesRaw := qualityRow.QualityProfile, qualityRow.Rules
	if e != nil {
		return e
	}
	if json.Unmarshal(oldRaw, &old) != nil || json.Unmarshal(rulesRaw, &rules) != nil {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	var mode string
	if e = tx.QueryRow(ctx, "SELECT upgrade_mode FROM subscription WHERE id=$1", x.authority.Request.SubscriptionID).Scan(&mode); e != nil {
		return e
	}
	if !quality.EvaluateUpgradeForMode(rules, old, in.Plan.Profile, quality.UpgradeMode(mode)).ShouldUpgrade {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	return nil
}

// Project only the authorized retirement deltas from the locked pre-write graph.
// Capture before UPDATEs so a trigger on an UPDATE cannot redefine success.
func (x *tvUpgradeSession) tvUpgradeRetirementGraph(ctx context.Context, tx pgx.Tx, p tvUpgradeRetirementPlan, at *time.Time) (string, error) {
	in := p.Ingestion
	plan := in.Plan
	raw, e := db.New(tx).ReadTVEpisodeUpgradeOrganizationState(ctx, db.ReadTVEpisodeUpgradeOrganizationStateParams{ActorID: uuid.MustParse(x.actor.ID), SubscriptionID: x.authority.Request.SubscriptionID, PolicyID: plan.PolicyID, PolicyRevision: plan.PolicyRevision, RootID: plan.RootID, MediaID: plan.MediaID, UnitID: x.authority.Request.UnitID, FileID: x.authority.Request.CurrentFileID, CandidateID: x.authority.Request.CandidateID})
	if e != nil {
		return "", e
	}
	var state map[string]json.RawMessage
	if e := json.Unmarshal(raw, &state); e != nil {
		return "", e
	}
	if at == nil {
		encoded, e := json.Marshal(state)
		if e != nil {
			return "", e
		}
		return completeTVJSONDigest(encoded), nil
	}
	var timestamp json.RawMessage
	if e := tx.QueryRow(ctx, "SELECT to_jsonb($1::timestamptz)", *at).Scan(&timestamp); e != nil {
		return "", e
	}
	sequence, _ := json.Marshal(plan.RootMutationSeq + 2)
	var root map[string]json.RawMessage
	if e := json.Unmarshal(state["root"], &root); e != nil {
		return "", e
	}
	root["mutation_seq"], root["updated_at"] = sequence, timestamp
	state["root"], _ = json.Marshal(root)
	changes := []struct {
		field, key string
		id         uuid.UUID
		values     map[string]json.RawMessage
	}{
		{"files", "id", x.authority.Request.CurrentFileID, map[string]json.RawMessage{"retired_at": timestamp, "last_mutation_seq": sequence}},
		{"mappings", "file_id", x.authority.Request.CurrentFileID, map[string]json.RawMessage{"retired_at": timestamp}},
		{"facts", "id", uuid.Nil, map[string]json.RawMessage{"revoked_at": timestamp, "revocation_reason": json.RawMessage(`"quality_upgrade"`)}},
		{"lanes", "operation_id", x.authority.OperationID, map[string]json.RawMessage{"released_at": timestamp}},
		{"reservations", "operation_id", x.authority.OperationID, map[string]json.RawMessage{"state": json.RawMessage(`"consumed"`), "updated_at": timestamp}},
		{"requirements", "operation_id", x.authority.OperationID, map[string]json.RawMessage{"state": json.RawMessage(`"imported"`)}},
	}
	oldFactID, e := db.New(tx).ReadTVEpisodeUpgradeOldFactID(ctx, x.authority.OperationID)
	if e != nil {
		return "", e
	}
	changes[2].id = oldFactID
	for _, change := range changes {
		var rows []map[string]json.RawMessage
		if e := json.Unmarshal(state[change.field], &rows); e != nil {
			return "", e
		}
		count := 0
		for _, row := range rows {
			var id uuid.UUID
			if e := json.Unmarshal(row[change.key], &id); e != nil {
				return "", e
			}
			if id != change.id {
				continue
			}
			count++
			if change.field == "files" {
				var revision int64
				if e := json.Unmarshal(row["mapping_revision"], &revision); e != nil {
					return "", e
				}
				row["mapping_revision"], _ = json.Marshal(revision + 1)
			}
			for key, value := range change.values {
				row[key] = value
			}
		}
		if count != 1 {
			return "", ports.ErrTVEpisodeUpgradeConflict
		}
		state[change.field], _ = json.Marshal(rows)
	}
	encoded, e := json.Marshal(state)
	if e != nil {
		return "", e
	}
	return completeTVJSONDigest(encoded), nil
}
func mustTVUpgradeJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
func (x *tvUpgradeSession) tvUpgradeObservedFresh(ctx context.Context, tx pgx.Tx, observed time.Time) error {
	var valid bool
	if e := tx.QueryRow(ctx, "SELECT $1::timestamptz<=clock_timestamp() AND clock_timestamp()<$1::timestamptz+interval '5 seconds'", observed).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return ports.ErrTVEpisodeUpgradeObservation
	}
	return nil
}

func (x *tvUpgradeSession) tvUpgradeLockMaterial(ctx context.Context, tx pgx.Tx, p tvUpgradeOrganizationPlan) error {
	// FOR UPDATE on parents also prevents a concurrent new FK mapping/fact from
	// passing the one-file/one-unit check while a retirement is being authorized.
	for _, query := range []string{
		`SELECT id FROM library_root WHERE id=$1 FOR UPDATE`,
		`SELECT id FROM media_unit WHERE id=$2 FOR UPDATE`,
		`SELECT id FROM library_file WHERE root_id=$1 AND (id=$3 OR id IN(SELECT file_id FROM library_file_unit WHERE unit_id=$2)) ORDER BY id FOR UPDATE`,
		`SELECT file_id FROM library_file_unit WHERE unit_id=$2 OR file_id=$3 ORDER BY file_id,unit_id FOR UPDATE`,
		`SELECT id FROM unit_fact WHERE unit_id=$2 OR source_file_id=$3 ORDER BY id FOR UPDATE`,
	} {
		if _, e := tx.Exec(ctx, "WITH args AS (SELECT $1::uuid,$2::uuid,$3::uuid) "+query, p.RootID, x.authority.Request.UnitID, x.authority.Request.CurrentFileID); e != nil {
			return e
		}
	}
	return nil
}
func (x *tvUpgradeSession) tvUpgradeRetirementInspect(ctx context.Context, root ports.TransferRoot, p tvUpgradeRetirementPlan, moved bool) error {
	frozen := p.Ingestion.Plan
	current, e := x.store.recycler.OpenRoot(ctx, frozen.Physical.RootPath)
	if e != nil {
		return e
	}
	defer current.Close()
	for _, r := range []ports.TransferRoot{root, current} {
		id, e := r.Identity(ctx)
		if e != nil || !tvUpgradeSameRoot(id, frozen.Physical.Root) {
			return ports.ErrFileIdentity
		}
		if _, e = r.Inspect(ctx, frozen.Marker); e != nil {
			return e
		}
		id, e = r.Inspect(ctx, frozen.Physical.TargetRel)
		if e != nil || id != frozen.Physical.Source {
			return ports.ErrFileIdentity
		}
		oldPath, absent := frozen.Physical.OldRel, p.RecycleRel
		if moved {
			oldPath, absent = p.RecycleRel, frozen.Physical.OldRel
		}
		id, e = r.Inspect(ctx, oldPath)
		if e != nil || id != frozen.Physical.Old {
			return ports.ErrFileIdentity
		}
		if _, e = r.Inspect(ctx, absent); !errors.Is(e, fs.ErrNotExist) {
			if e != nil {
				return e
			}
			return ports.ErrFileConflict
		}
	}
	return nil
}

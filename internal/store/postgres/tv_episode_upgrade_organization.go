package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type tvUpgradeOrganizationPlan struct {
	Physical           upgradeOrganizationPhysical `json:"physical"`
	SourceRootIdentity ports.FileIdentity          `json:"source_root_identity"`
	Marker             string                      `json:"marker"`
	RootID, MediaID    uuid.UUID
	PolicyID           string
	PolicyRevision     int64
	RootMutationSeq    int64
	Prepared           tvUpgradePrepared `json:"prepared"`
	Profile            quality.Profile   `json:"profile"`
	Basis              string            `json:"basis"`
	OldFingerprint     string            `json:"old_fingerprint"`
}
type tvUpgradeIngestion struct {
	Plan                tvUpgradeOrganizationPlan `json:"plan"`
	FileID, FactID      uuid.UUID
	File, Mapping, Fact json.RawMessage
}

func (x *tvUpgradeSession) organization(ctx context.Context) (*ports.TVEpisodeUpgradeResult, error) {
	if x.store.files == nil {
		return nil, ports.ErrUnavailable
	}
	if x.command.Action == "organize" {
		return x.organize(ctx)
	}
	m, e := x.material(ctx)
	if e != nil {
		return nil, e
	}
	var plan tvUpgradeOrganizationPlan
	e = x.transaction(ctx, true, func(tx pgx.Tx) error {
		prepared, e := x.completedTVUpgrade(ctx, tx)
		if e != nil {
			return e
		}
		if coverageDigest(prepared.Target) != coverageDigest(m.Target) {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		plan = tvUpgradeOrganizationPlan{RootID: m.Root.ID, MediaID: m.Sub.MediaID, PolicyID: m.Sub.QualityPolicyID, PolicyRevision: m.Sub.QualityPolicyRevision, RootMutationSeq: m.Root.MutationSeq, Prepared: prepared, Marker: m.Root.MarkerFilename}
		if plan.Marker == "" {
			plan.Marker = ".pulsemedia_root"
		}
		if json.Unmarshal(m.Candidate.QualityProfile, &plan.Profile) != nil {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		rules, version, e := lockOrganizationNaming(ctx, db.New(tx))
		if e != nil {
			return e
		}
		target, e := rules.EpisodeRelative(naming.MediaTemplateInput{Title: m.Target.Title, Year: int(m.Target.Year), Season: m.Target.Unit.Season, Episode: m.Target.Unit.Episode, Extension: filepath.Ext(prepared.Video.Path), Quality: plan.Profile, TMDBID: m.Target.TMDBID})
		if e != nil {
			return e
		}
		sourceRoot, sourceRel, e := tvUpgradeSource(m.Client.RemotePathMap, prepared)
		if e != nil {
			return e
		}
		plan.Physical = upgradeOrganizationPhysical{RootPath: m.Root.Path, SourceRoot: sourceRoot, SourceRel: sourceRel, TargetRel: target, OldRel: m.OldRel, NamingVersion: version}
		if target == m.OldRel || filepath.Join(sourceRoot, sourceRel) == filepath.Join(m.Root.Path, target) {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		if e = tx.QueryRow(ctx, "SELECT COALESCE(file_fingerprint,'') FROM library_file WHERE id=$1", x.authority.Request.CurrentFileID).Scan(&plan.OldFingerprint); e != nil {
			return e
		}
		plan.Basis, e = x.tvUpgradeState(ctx, tx, plan, uuid.Nil, uuid.Nil)
		return e
	})
	if e != nil {
		return nil, e
	}
	roots, release, e := x.store.files.LockRoots(ctx, plan.Physical.RootPath, plan.Physical.SourceRoot)
	if e != nil {
		return nil, e
	}
	defer release()
	root, source := roots[plan.Physical.RootPath], roots[plan.Physical.SourceRoot]
	if root == nil || source == nil {
		return nil, ports.ErrFileIdentity
	}
	if plan.Physical.Root, e = root.Identity(ctx); e != nil {
		return nil, e
	}
	if plan.SourceRootIdentity, e = source.Identity(ctx); e != nil {
		return nil, e
	}
	if plan.Physical.Old, e = root.Inspect(ctx, plan.Physical.OldRel); e != nil {
		return nil, e
	}
	if plan.Physical.Source, e = source.Inspect(ctx, plan.Physical.SourceRel); e != nil {
		return nil, e
	}
	if plan.Physical.Old.Size != m.OldSize || plan.Physical.Old.Mtime != m.OldMtime || tvUpgradeFingerprint(plan.Physical.Old) != plan.OldFingerprint || plan.Physical.Source.Size != plan.Prepared.Video.Size || plan.Physical.Source.Size < 1 || plan.Physical.Source.Device != plan.Physical.Root.Device || plan.Physical.Old.Device != plan.Physical.Root.Device || plan.Physical.Source.Inode == plan.Physical.Old.Inode {
		return nil, ports.ErrFileIdentity
	}
	if e = x.tvUpgradeInspect(ctx, plan, root, source, false); e != nil {
		return nil, e
	}
	out := x.result("organization_preview_not_executable")
	out.Version = coverageDigest(plan)
	e = x.transaction(ctx, true, func(tx pgx.Tx) error {
		if e = x.tvUpgradePlanCurrent(ctx, tx, plan, uuid.Nil, uuid.Nil); e != nil {
			return e
		}
		if x.command.Action == "organization-preview" {
			return nil
		}
		if x.command.ExpectedVersion != out.Version {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		out.Status = "organization_confirmed_not_executable"
		return x.phase(ctx, tx, "organization-confirm", out, plan)
	})
	return out, e
}

func (x *tvUpgradeSession) completedTVUpgrade(ctx context.Context, tx pgx.Tx) (tvUpgradePrepared, error) {
	var prepared tvUpgradePrepared
	if e := x.evidence(ctx, tx, "prepare", &prepared); e != nil {
		return prepared, e
	}
	// The latest observation alone is authoritative; an older complete result cannot
	// hide a later partial or unknown observation.
	raw, e := db.New(tx).ReadTVEpisodeUpgradeLatestObservation(ctx, x.authority.OperationID)
	if e != nil {
		return prepared, e
	}
	var obs tvUpgradeObservation
	if json.Unmarshal(raw, &obs) != nil || !obs.Complete || coverageDigest(obs.Prepared) != coverageDigest(prepared) {
		return prepared, ports.ErrTVEpisodeUpgradeObservation
	}
	valid, e := db.New(tx).CheckTVEpisodeUpgradeCompletedReturn(ctx, db.CheckTVEpisodeUpgradeCompletedReturnParams{OperationID: x.authority.OperationID, Evidence: raw})
	if e == nil && (!valid.Valid || !valid.Bool) {
		e = ports.ErrTVEpisodeUpgradeObservation
	}
	return prepared, e
}

func tvUpgradeSource(raw []byte, p tvUpgradePrepared) (string, string, error) {
	var mapping map[string]string
	if json.Unmarshal(raw, &mapping) != nil || len(mapping) == 0 {
		return "", "", ports.ErrTVEpisodeUpgradeConflict
	}
	clean, e := naming.CleanRelPath(p.Video.Path)
	if e != nil || clean != p.Video.Path {
		return "", "", ports.ErrTVEpisodeUpgradeConflict
	}
	remote := filepath.Join(p.SavePath, clean)
	local := naming.TranslateRemotePath(mapping, remote)
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		prefix := strings.TrimRight(key, "/")
		if key != "/" && remote != prefix && !strings.HasPrefix(remote, prefix+"/") {
			continue
		}
		root := mapping[key]
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || local == "" {
			break
		}
		rel, e := filepath.Rel(root, local)
		if e != nil {
			break
		}
		safe, e := naming.CleanRelPath(rel)
		if e == nil && safe == rel {
			return root, rel, nil
		}
		break
	}
	return "", "", ports.ErrTVEpisodeUpgradeConflict
}
func tvUpgradeFingerprint(id ports.FileIdentity) string {
	return fmt.Sprintf("stat.v1:%d:%d:%d", id.Inode, id.Size, id.Mtime)
}
func tvUpgradeSameRoot(a, b ports.FileIdentity) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.Inode > 0
}
func (x *tvUpgradeSession) tvUpgradeInspect(ctx context.Context, p tvUpgradeOrganizationPlan, root, source ports.TransferRoot, linked bool) error {
	current, e := x.store.files.OpenRoot(ctx, p.Physical.RootPath)
	if e != nil {
		return e
	}
	defer current.Close()
	id, e := current.Identity(ctx)
	if e != nil || !tvUpgradeSameRoot(id, p.Physical.Root) {
		return ports.ErrFileIdentity
	}
	if _, e = current.Inspect(ctx, p.Marker); e != nil {
		return e
	}
	id, e = root.Identity(ctx)
	if e != nil || !tvUpgradeSameRoot(id, p.Physical.Root) {
		return ports.ErrFileIdentity
	}
	reopened, e := x.store.files.OpenRoot(ctx, p.Physical.SourceRoot)
	if e != nil {
		return e
	}
	defer reopened.Close()
	id, e = reopened.Identity(ctx)
	if e != nil || !tvUpgradeSameRoot(id, p.SourceRootIdentity) {
		return ports.ErrFileIdentity
	}
	for _, r := range []ports.TransferRoot{root, current} {
		id, e = r.Inspect(ctx, p.Physical.OldRel)
		if e != nil || id != p.Physical.Old {
			return ports.ErrFileIdentity
		}
	}
	for _, r := range []ports.TransferRoot{source, reopened} {
		id, e = r.Inspect(ctx, p.Physical.SourceRel)
		if e != nil || id != p.Physical.Source {
			return ports.ErrFileIdentity
		}
	}
	for _, r := range []ports.TransferRoot{root, current} {
		id, e = r.Inspect(ctx, p.Physical.TargetRel)
		if linked {
			if e != nil || id != p.Physical.Source {
				return ports.ErrFileIdentity
			}
		} else if !errors.Is(e, fs.ErrNotExist) {
			if e != nil {
				return e
			}
			return ports.ErrFileConflict
		}
	}
	return nil
}

// Ignore only our new ingestion rows and the root's mutation watermark. All
// original file/fact/mapping rows, policy, intent and configuration stay bound.
func (x *tvUpgradeSession) tvUpgradeState(ctx context.Context, tx pgx.Tx, p tvUpgradeOrganizationPlan, file, fact uuid.UUID) (string, error) {
	var raw json.RawMessage
	raw, e := db.New(tx).ReadTVEpisodeUpgradeOrganizationState(ctx, db.ReadTVEpisodeUpgradeOrganizationStateParams{ActorID: uuid.MustParse(x.actor.ID), SubscriptionID: x.authority.Request.SubscriptionID, PolicyID: p.PolicyID, PolicyRevision: p.PolicyRevision, RootID: p.RootID, MediaID: p.MediaID, UnitID: x.authority.Request.UnitID, FileID: x.authority.Request.CurrentFileID, CandidateID: x.authority.Request.CandidateID})
	if e != nil {
		return "", e
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(raw, &state) != nil {
		return "", ports.ErrTVEpisodeUpgradeConflict
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(state["root"], &root) != nil {
		return "", ports.ErrTVEpisodeUpgradeConflict
	}
	delete(root, "mutation_seq")
	delete(root, "updated_at")
	state["root"], _ = json.Marshal(root)
	for _, field := range []string{"files", "mappings", "facts"} {
		if string(state[field]) == "null" {
			continue
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(state[field], &rows) != nil {
			return "", ports.ErrTVEpisodeUpgradeConflict
		}
		kept := rows[:0]
		for _, row := range rows {
			var id uuid.UUID
			key := "id"
			if field == "mappings" {
				key = "file_id"
			}
			_ = json.Unmarshal(row[key], &id)
			if file != uuid.Nil && ((field != "facts" && id == file) || (field == "facts" && id == fact)) {
				continue
			}
			kept = append(kept, row)
		}
		state[field], _ = json.Marshal(kept)
	}
	encoded, e := json.Marshal(state)
	if e != nil {
		return "", e
	}
	return completeTVJSONDigest(encoded), nil
}
func (x *tvUpgradeSession) tvUpgradePlanCurrent(ctx context.Context, tx pgx.Tx, p tvUpgradeOrganizationPlan, file, fact uuid.UUID) error {
	if e := x.tvUpgradeLockMaterial(ctx, tx, p); e != nil {
		return e
	}
	if e := tvUpgradeProtection(ctx, tx, x.authority.OperationID, x.authority.Request.UnitID, p.RootID, x.actor.ID); e != nil {
		return e
	}
	prepared, e := x.completedTVUpgrade(ctx, tx)
	if e != nil {
		return e
	}
	if coverageDigest(prepared) != coverageDigest(p.Prepared) {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	expectedSeq := p.RootMutationSeq
	if file != uuid.Nil {
		expectedSeq++
	}
	var seq int64
	if e = tx.QueryRow(ctx, "SELECT mutation_seq FROM library_root WHERE id=$1", p.RootID).Scan(&seq); e != nil {
		return e
	}
	if seq != expectedSeq {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	var device, inode int64
	e = tx.QueryRow(ctx, `SELECT sf.root_device,sf.root_inode FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id JOIN library_root r ON r.id=sf.root_id WHERE sf.root_id=$1 AND sr.generation=r.generation AND sr.state='succeeded' AND sf.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1`, p.RootID).Scan(&device, &inode)
	if e != nil {
		return e
	}
	if device != p.Physical.Root.Device || inode != p.Physical.Root.Inode {
		return ports.ErrFileIdentity
	}
	digest, e := x.tvUpgradeState(ctx, tx, p, file, fact)
	if e != nil {
		return e
	}
	if digest != p.Basis {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	return nil
}

func (x *tvUpgradeSession) organize(ctx context.Context) (*ports.TVEpisodeUpgradeResult, error) {
	if x.store.publisher == nil {
		return nil, ports.ErrUnavailable
	}
	var plan tvUpgradeOrganizationPlan
	if e := x.transaction(ctx, true, func(tx pgx.Tx) error { return x.evidence(ctx, tx, "organization-confirm", &plan) }); e != nil {
		return nil, e
	}
	if x.command.ExpectedVersion != coverageDigest(plan) {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	roots, release, e := x.store.files.LockRoots(ctx, plan.Physical.RootPath, plan.Physical.SourceRoot)
	if e != nil {
		return nil, e
	}
	defer release()
	root, source := roots[plan.Physical.RootPath], roots[plan.Physical.SourceRoot]
	if root == nil || source == nil {
		return nil, ports.ErrFileIdentity
	}
	if e = x.tvUpgradeInspect(ctx, plan, root, source, false); e != nil {
		return nil, e
	}
	if e = x.transaction(ctx, true, func(tx pgx.Tx) error { return x.tvUpgradePlanCurrent(ctx, tx, plan, uuid.Nil, uuid.Nil) }); e != nil {
		return nil, e
	}
	permit, e := x.permit(ctx, "organize", true)
	if e != nil {
		return nil, e
	}
	if e = x.transaction(ctx, true, func(tx pgx.Tx) error {
		if e := x.tvUpgradePlanCurrent(ctx, tx, plan, uuid.Nil, uuid.Nil); e != nil {
			return e
		}
		return x.validPermit(ctx, tx, permit)
	}); e != nil {
		return nil, e
	}
	if e = x.tvUpgradeInspect(ctx, plan, root, source, false); e != nil {
		return nil, e
	}
	callErr := x.store.files.Hardlink(ctx, source, plan.Physical.SourceRel, root, plan.Physical.TargetRel, plan.Physical.Source)
	trusted := callErr == nil && ctx.Err() == nil
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var observed time.Time
	if e = x.conn.QueryRow(record, "SELECT clock_timestamp()").Scan(&observed); e != nil {
		return nil, e
	}
	if trusted {
		trusted = x.tvUpgradeInspect(record, plan, root, source, true) == nil
	}
	out := x.result("organization_unknown_requires_observation")
	out.Version = coverageDigest(plan)
	if !trusted {
		e = x.recordReturn(record, permit, "unknown", out, plan, false, nil)
		return out, e
	}
	in := tvUpgradeIngestion{Plan: plan, FileID: x.store.ids.NewUUID(), FactID: x.store.ids.NewUUID()}
	out.Status = "imported_pending_retirement"
	out.NewFileID = in.FileID
	e = x.transaction(record, false, func(tx pgx.Tx) error {
		// readPreview is valid before inserting the second active file, never after.
		if e := x.authorize(record, tx, true); e != nil {
			return e
		}
		if e := x.validPermit(record, tx, permit); e != nil {
			return e
		}
		if e := x.tvUpgradePlanCurrent(record, tx, plan, uuid.Nil, uuid.Nil); e != nil {
			return e
		}
		if e := x.tvUpgradeInsertImported(record, tx, &in); e != nil {
			return e
		}
		ev, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if err = db.New(tx).InsertTVEpisodeUpgradePhysicalReturn(record, db.InsertTVEpisodeUpgradePhysicalReturnParams{OperationID: x.authority.OperationID, Action: "organize", Owner: permit.Owner, Token: permit.Token, Status: "confirmed", Evidence: ev}); err != nil {
			return err
		}
		if err = x.phase(record, tx, "organize", out, in); err != nil {
			return err
		}
		if _, err = tx.Exec(record, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return err
		}
		if err = x.tvUpgradePlanCurrent(record, tx, plan, in.FileID, in.FactID); err != nil {
			return err
		}
		if err = x.tvUpgradeImportedCurrent(record, tx, in); err != nil {
			return err
		}
		if err = x.tvUpgradeObservedFresh(record, tx, observed); err != nil {
			return err
		}
		return x.validPermit(record, tx, permit)
	})
	return out, e
}
func (x *tvUpgradeSession) tvUpgradeInsertImported(ctx context.Context, tx pgx.Tx, in *tvUpgradeIngestion) error {
	p := in.Plan
	q := db.New(tx)
	var seq int64
	if e := tx.QueryRow(ctx, `UPDATE library_root SET mutation_seq=mutation_seq+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING mutation_seq`, p.RootID).Scan(&seq); e != nil {
		return e
	}
	file, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: in.FileID, RootID: p.RootID, RelPath: p.Physical.TargetRel, SizeBytes: p.Physical.Source.Size, MtimeNs: p.Physical.Source.Mtime, FileFingerprint: pgtype.Text{String: tvUpgradeFingerprint(p.Physical.Source), Valid: true}, LastMutationSeq: seq})
	if e != nil {
		return e
	}
	if file.ID != in.FileID || file.RootID != p.RootID || file.RelPath != p.Physical.TargetRel || file.SizeBytes != p.Physical.Source.Size || file.MtimeNs != p.Physical.Source.Mtime || !file.FileFingerprint.Valid || file.FileFingerprint.String != tvUpgradeFingerprint(p.Physical.Source) || file.LastMutationSeq != seq || file.RetiredAt.Valid {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	if e := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: in.FileID, UnitID: x.authority.Request.UnitID}); e != nil {
		return e
	}
	profile, e := json.Marshal(p.Profile)
	if e != nil {
		return e
	}
	fact, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: in.FactID, UnitID: x.authority.Request.UnitID, RootID: pgtype.UUID{Bytes: p.RootID, Valid: true}, SourceFileID: pgtype.UUID{Bytes: in.FileID, Valid: true}, Stage: "imported", QualityProfile: profile})
	if e != nil {
		return e
	}
	if fact.ID != in.FactID || fact.UnitID != x.authority.Request.UnitID || !fact.RootID.Valid || uuid.UUID(fact.RootID.Bytes) != p.RootID || !fact.SourceFileID.Valid || uuid.UUID(fact.SourceFileID.Bytes) != in.FileID || fact.SourceTaskID.Valid || fact.Stage != "imported" || fact.RevokedAt.Valid || fact.ExpiresAt.Valid || !preparationSameJSON(fact.QualityProfile, profile) {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	if e = tx.QueryRow(ctx, `SELECT (SELECT to_jsonb(f) FROM library_file f WHERE id=$1),(SELECT to_jsonb(m) FROM library_file_unit m WHERE file_id=$1 AND unit_id=$2),(SELECT to_jsonb(f) FROM unit_fact f WHERE id=$3)`, in.FileID, x.authority.Request.UnitID, in.FactID).Scan(&in.File, &in.Mapping, &in.Fact); e != nil {
		return e
	}
	_, e = x.store.publisher.PublishImported(ctx, q, x.authority.Request.UnitID.String(), map[string]any{"operation_id": x.authority.OperationID, "file_id": in.FileID, "unit_id": x.authority.Request.UnitID})
	return e
}
func (x *tvUpgradeSession) tvUpgradeImportedCurrent(ctx context.Context, tx pgx.Tx, in tvUpgradeIngestion) error {
	var valid bool
	e := tx.QueryRow(ctx, `SELECT (SELECT to_jsonb(f)=$3::jsonb FROM library_file f WHERE id=$1) AND (SELECT count(*) FROM library_file_unit WHERE file_id=$1)=1 AND (SELECT to_jsonb(m)=$4::jsonb FROM library_file_unit m WHERE file_id=$1 AND unit_id=$2) AND (SELECT to_jsonb(f)=$6::jsonb FROM unit_fact f WHERE id=$5) AND (SELECT count(*) FROM unit_fact WHERE source_file_id=$1)=1`, in.FileID, x.authority.Request.UnitID, in.File, in.Mapping, in.FactID, in.Fact).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	return nil
}

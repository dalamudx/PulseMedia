package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type upgradeOrganizationPhysical struct {
	RootPath      string             `json:"root_path"`
	SourceRoot    string             `json:"source_root"`
	SourceRel     string             `json:"source_rel"`
	TargetRel     string             `json:"target_rel"`
	NamingVersion int64              `json:"naming_version,omitempty"`
	Root          ports.FileIdentity `json:"root"`
	Source        ports.FileIdentity `json:"source"`
	OldRel        string             `json:"old_rel"`
	Old           ports.FileIdentity `json:"old"`
}
type upgradeOrganizationPlanInputs struct {
	authorization  json.RawMessage
	basis          *upgradeOrganizationBasis
	physical       upgradeOrganizationPhysical
	marker         string
	profile        quality.Profile
	generation     int64
	oldSize        int64
	oldMtime       int64
	oldFingerprint string
}

func (r *movieUpgradeOrganizationPreparationRepository) inputs(ctx context.Context, tx pgx.Tx, a *ports.Actor, c ports.PrepareMovieUpgradeOrganization) (*upgradeOrganizationPlanInputs, error) {
	return r.readInputs(ctx, tx, a, c, nil)
}

// A prepared plan has already fixed its target. Revalidate its current business
// authority without consulting a later naming configuration.
func (r *movieUpgradeOrganizationPreparationRepository) readInputs(ctx context.Context, tx pgx.Tx, a *ports.Actor, c ports.PrepareMovieUpgradeOrganization, prepared *upgradeOrganizationPhysical) (*upgradeOrganizationPlanInputs, error) {
	var raw json.RawMessage
	var key string
	var obs uuid.UUID
	if e := tx.QueryRow(ctx, `SELECT to_jsonb(x),request_key,observation_id FROM movie_upgrade_organization_authorization x WHERE id=$1 AND operation_id=$2 AND actor_id=$3 FOR SHARE`, c.AuthorizationID, c.OperationID, a.ID).Scan(&raw, &key, &obs); e != nil {
		return nil, e
	}
	org := movieUpgradeOrganizationRepository{store: r.store}
	_, sub, e := r.store.authorizePreparation(ctx, tx, a, c.OperationID)
	if e != nil {
		return nil, e
	}
	recovered, e := org.recoverOrganization(ctx, tx, a, sub, c.OperationID, key, nil)
	if e != nil {
		return nil, e
	}
	if recovered == nil || recovered.ID != c.AuthorizationID {
		return nil, preparationConflict()
	}
	b, e := r.store.organizationBasis(ctx, tx, a, c.OperationID, obs)
	if e != nil {
		return nil, e
	}
	var auth struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(raw, &auth) != nil || !preparationSameJSON(auth.Snapshot, b.raw) {
		return nil, preparationConflict()
	}
	var f struct {
		RootPath string          `json:"root_path"`
		SavePath string          `json:"save_path"`
		Profile  quality.Profile `json:"candidate_profile"`
		Proof    struct {
			Target media.MovieUpgradeTarget `json:"target"`
		} `json:"proof"`
	}
	if json.Unmarshal(b.raw, &f) != nil {
		return nil, preparationConflict()
	}
	var mapping map[string]string
	var mappingJSON json.RawMessage
	out := &upgradeOrganizationPlanInputs{authorization: raw, basis: b, profile: f.Profile}
	if e = tx.QueryRow(ctx, `SELECT root.generation,root.marker_filename,c.remote_path_map FROM movie_upgrade_authority a JOIN library_root root ON root.id=a.root_id JOIN download_client c ON c.id=a.client_id WHERE a.operation_id=$1`, c.OperationID).Scan(&out.generation, &out.marker, &mappingJSON); e != nil {
		return nil, e
	}
	if json.Unmarshal(mappingJSON, &mapping) != nil || len(mapping) == 0 {
		return nil, preparationConflict()
	}
	if out.marker == "" {
		out.marker = ".pulsemedia_root"
	}
	clean, e := naming.CleanRelPath(b.review.SourceRelativePath)
	if e != nil || clean != b.review.SourceRelativePath {
		return nil, preparationConflict()
	}
	remote := filepath.Join(f.SavePath, clean)
	local := naming.TranslateRemotePath(mapping, remote)
	if local == "" || !filepath.IsAbs(local) || !filepath.IsAbs(f.RootPath) || filepath.Clean(f.RootPath) != f.RootPath {
		return nil, preparationConflict()
	}
	// Require an explicit complete prefix, including for identity mappings.
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		prefix := strings.TrimRight(key, "/")
		if key == "/" {
			prefix = "/"
		}
		if remote != prefix && !strings.HasPrefix(remote, prefix+"/") && prefix != "/" {
			continue
		}
		root := mapping[key]
		rel, e := filepath.Rel(root, local)
		if e != nil {
			return nil, e
		}
		safe, e := naming.CleanRelPath(rel)
		if e != nil || safe != rel {
			return nil, preparationConflict()
		}
		out.physical.SourceRoot, out.physical.SourceRel = root, rel
		break
	}
	if out.physical.SourceRoot == "" {
		return nil, preparationConflict()
	}
	var target string
	if prepared != nil {
		target, e = naming.CleanRelPath(prepared.TargetRel)
		if e != nil || target != prepared.TargetRel || prepared.NamingVersion < 0 || !strings.EqualFold(filepath.Ext(target), filepath.Ext(clean)) {
			return nil, preparationConflict()
		}
		out.physical.NamingVersion = prepared.NamingVersion
	} else {
		namingRules, namingVersion, err := lockOrganizationNaming(ctx, db.New(tx))
		if err != nil {
			if errors.Is(err, ports.ErrTransferReviewRequired) {
				return nil, preparationConflict()
			}
			return nil, err
		}
		out.physical.NamingVersion = namingVersion
		target, e = naming.CleanRelPath(filepath.Join(naming.FormatMovieDir(f.Proof.Target.OriginalTitle, f.Proof.Target.Year), naming.FormatMovieFile(f.Proof.Target.OriginalTitle, f.Proof.Target.Year, f.Profile, filepath.Ext(clean))))
		if namingRules.MovieTemplate != "" {
			input, err := upgradeNamingInput(f.Proof.Target, f.Profile, filepath.Ext(clean), namingRules.MovieTemplate)
			if err != nil {
				return nil, err
			}
			target, e = namingRules.MovieRelative(input)
		}
	}
	if e != nil || target == "" {
		return nil, preparationConflict()
	}
	out.physical.RootPath, out.physical.TargetRel = f.RootPath, target
	if filepath.Join(f.RootPath, target) == local {
		return nil, preparationConflict()
	}
	if e = tx.QueryRow(ctx, `SELECT rel_path,size_bytes,mtime_ns,COALESCE(file_fingerprint,'') FROM library_file WHERE id=$1`, b.review.CurrentFileID).Scan(&out.physical.OldRel, &out.oldSize, &out.oldMtime, &out.oldFingerprint); e != nil {
		return nil, e
	}
	oldRel, e := naming.CleanRelPath(out.physical.OldRel)
	if e != nil || oldRel != out.physical.OldRel || oldRel == target || out.oldFingerprint == "" {
		return nil, preparationConflict()
	}
	return out, nil
}
func (r *movieUpgradeOrganizationPreparationRepository) observe(ctx context.Context, in *upgradeOrganizationPlanInputs) (time.Time, func(), error) {
	roots, release, e := r.files.LockRoots(ctx, in.physical.RootPath, in.physical.SourceRoot)
	if e != nil {
		return time.Time{}, nil, e
	}
	fail := func(e error) (time.Time, func(), error) { release(); return time.Time{}, nil, e }
	var observed time.Time
	if e = r.store.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observed); e != nil {
		return fail(e)
	}
	root := roots[in.physical.RootPath]
	source := roots[in.physical.SourceRoot]
	if root == nil || source == nil {
		return fail(preparationConflict())
	}
	if in.physical.Root, e = root.Identity(ctx); e != nil {
		return fail(e)
	}
	if _, e = root.Inspect(ctx, in.marker); e != nil {
		return fail(e)
	}
	if in.physical.Old, e = root.Inspect(ctx, in.physical.OldRel); e != nil {
		return fail(e)
	}
	old := in.physical.Old
	if old.Size != in.oldSize || old.Mtime != in.oldMtime || old.Device != in.physical.Root.Device || fmt.Sprintf("stat.v1:%d:%d:%d", old.Inode, old.Size, old.Mtime) != in.oldFingerprint {
		return fail(preparationConflict())
	}
	if _, e = root.Inspect(ctx, in.physical.TargetRel); e == nil {
		return fail(ports.ErrFileConflict)
	} else if !errors.Is(e, fs.ErrNotExist) {
		return fail(e)
	}
	if in.physical.Source, e = source.Inspect(ctx, in.physical.SourceRel); e != nil {
		return fail(e)
	}
	if in.physical.Source.Inode == in.physical.Old.Inode || in.physical.Source.Size != in.basis.review.SourceSize || in.physical.Source.Size <= 0 || in.physical.Source.Inode <= 0 || in.physical.Root.Inode <= 0 || in.physical.Source.Device != in.physical.Root.Device {
		return fail(preparationConflict())
	}
	return observed, release, nil
}

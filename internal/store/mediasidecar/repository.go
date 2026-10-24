package mediasidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository holds no physical capability; every operation ends its DB transaction.
type Repository struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
	kind string
}

func NewRepository(pool *pgxpool.Pool, ids ports.IDGen) *Repository {
	return &Repository{pool: pool, ids: ids, kind: ports.MediaSidecarJobKind}
}

var _ ports.MediaSidecarRepository = (*Repository)(nil)

func (r *Repository) Load(ctx context.Context, lease ports.JobLease) (ports.MediaSidecarSnapshot, error) {
	var out ports.MediaSidecarSnapshot
	err := r.transact(ctx, lease, func(q *db.Queries, payload ports.MediaSidecarPayload) error {
		var err error
		out, err = snapshot(ctx, q, lease, payload, r.kind)
		return err
	})
	if err != nil {
		return ports.MediaSidecarSnapshot{}, err
	}
	return out, nil
}

func (r *Repository) Validate(ctx context.Context, lease ports.JobLease, expected ports.MediaSidecarSnapshot) error {
	return r.transact(ctx, lease, func(q *db.Queries, payload ports.MediaSidecarPayload) error {
		return compare(ctx, q, lease, payload, expected, r.kind)
	})
}

func (r *Repository) Complete(ctx context.Context, lease ports.JobLease, expected ports.MediaSidecarSnapshot, result ports.MediaSidecarCompletion) error {
	return r.transact(ctx, lease, func(q *db.Queries, payload ports.MediaSidecarPayload) error {
		if err := compare(ctx, q, lease, payload, expected, r.kind); err != nil {
			return err
		}
		review, err := validateResult(expected, result)
		if err != nil {
			return err
		}
		if r.ids == nil {
			return ports.ErrUnavailable
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return ports.ErrMediaSidecarResult
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return ports.ErrMediaSidecarResult
		}
		// Audit first: a tail lock wait must not bypass the final DB-clock lease check.
		if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: r.ids.NewUUID(), ActorType: "system", ActorID: "media_sidecar", Action: "media.sidecar.completed", TargetType: "job", TargetID: lease.ID.String(), Payload: encoded}); err != nil {
			return err
		}
		_, err = q.CompleteMediaSidecarJob(ctx, db.CompleteMediaSidecarJobParams{ReviewRequired: review, Result: encoded, JobID: lease.ID, Owner: lease.Owner, Token: lease.Token, ExpectedPayload: body, Kind: r.kind})
		return staleOrUnavailable(err)
	})
}

func (r *Repository) transact(ctx context.Context, lease ports.JobLease, work func(*db.Queries, ports.MediaSidecarPayload) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.pool == nil {
		return ports.ErrUnavailable
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token <= 0 {
		return ports.ErrMediaSidecarStale
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return safeError(ctx, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := db.New(tx)
	if err = q.SetMediaSidecarUTC(ctx); err != nil {
		return safeError(ctx, err)
	}
	job, err := q.LockMediaSidecarJob(ctx, lease.ID)
	if err != nil {
		return safeError(ctx, staleOrUnavailable(err))
	}
	if err = guard(ctx, q, lease, r.kind); err != nil {
		return safeError(ctx, err)
	}
	isolated, err := q.LockMediaSidecarRestoreState(ctx)
	if err != nil {
		return safeError(ctx, err)
	}
	if isolated {
		return ports.ErrMediaSidecarStale
	}
	payload, err := parsePayload(job.Payload)
	if err != nil {
		return err
	}
	if err = work(q, payload); err != nil {
		return safeError(ctx, err)
	}
	// Complete clears the lease, so only read operations need a final clock check.
	current, err := q.LockMediaSidecarJob(ctx, lease.ID)
	if err != nil {
		return safeError(ctx, err)
	}
	if current.State == "running" {
		if err = guard(ctx, q, lease, r.kind); err != nil {
			return safeError(ctx, err)
		}
	}
	return safeError(ctx, tx.Commit(ctx))
}

func guard(ctx context.Context, q *db.Queries, lease ports.JobLease, kind string) error {
	_, err := q.GuardMediaSidecarLease(ctx, db.GuardMediaSidecarLeaseParams{JobID: lease.ID, Owner: lease.Owner, Token: lease.Token, Kind: kind})
	return staleOrUnavailable(err)
}
func staleOrUnavailable(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrMediaSidecarStale
	}
	return err
}
func safeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, allowed := range []error{ports.ErrMediaSidecarStale, ports.ErrMediaSidecarUnavailable, ports.ErrMediaSidecarResult, ports.ErrUnavailable, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	return ports.ErrUnavailable
}
func parsePayload(raw []byte) (ports.MediaSidecarPayload, error) {
	var p ports.MediaSidecarPayload
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Version != 1 || p.SourceEventID == uuid.Nil || p.FileID == uuid.Nil || p.UnitID == uuid.Nil {
		return p, ports.ErrMediaSidecarStale
	}
	return p, nil
}
func compare(ctx context.Context, q *db.Queries, lease ports.JobLease, p ports.MediaSidecarPayload, want ports.MediaSidecarSnapshot, kind string) error {
	got, err := snapshot(ctx, q, lease, p, kind)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return ports.ErrMediaSidecarStale
	}
	return nil
}

// These JSON projections mirror the generated snapshot, not Go's SQL nullable wrappers.
type unitEvidence struct {
	ID              uuid.UUID `json:"id"`
	MediaID         uuid.UUID `json:"media_id"`
	UnitKey         string    `json:"unit_key"`
	Season          *int32    `json:"season_number"`
	Episode         *int32    `json:"episode_number"`
	MetadataVersion int64     `json:"metadata_version"`
}
type snapshotEvidence struct {
	RootIdentity *struct {
		Device int64 `json:"device"`
		Inode  int64 `json:"inode"`
	} `json:"root_identity"`
	Mappings []struct {
		Unit unitEvidence `json:"unit"`
	} `json:"mappings"`
	Poster *string `json:"poster_path"`
}

var posterPattern = regexp.MustCompile(`^/[A-Za-z0-9_-]+\.(jpg|jpeg|png|webp)$`)

func snapshot(ctx context.Context, q *db.Queries, lease ports.JobLease, p ports.MediaSidecarPayload, kind string) (ports.MediaSidecarSnapshot, error) {
	var out ports.MediaSidecarSnapshot
	root, err := q.LockMediaSidecarRoot(ctx, p.FileID)
	if err != nil {
		return out, err
	}
	unit, err := q.LockMediaSidecarUnit(ctx, p.UnitID)
	if err != nil {
		return out, err
	}
	file, err := q.LockMediaSidecarFile(ctx, p.FileID)
	if err != nil {
		return out, err
	}
	media, err := q.LockMediaSidecarMedia(ctx, p.UnitID)
	if err != nil {
		return out, err
	}
	mappings, err := q.LockMediaSidecarMappings(ctx, p.FileID)
	if err != nil {
		return out, err
	}
	for _, mapping := range mappings {
		if mapping.RetiredAt.Valid || mapping.UnitID == p.UnitID {
			continue
		}
		if _, err = q.LockMediaSidecarUnit(ctx, mapping.UnitID); err != nil {
			return out, err
		}
	}
	if _, err = q.LockMediaSidecarFacts(ctx, db.LockMediaSidecarFactsParams{FileID: p.FileID, UnitID: p.UnitID}); err != nil {
		return out, err
	}
	identities, err := q.LockMediaSidecarIdentities(ctx, media.ID)
	if err != nil {
		return out, err
	}
	var tmdb int64
	count := 0
	for _, id := range identities {
		if strings.HasPrefix(id.Namespace, "tmdb.") {
			if id.Namespace != "tmdb."+media.MediaType {
				return out, ports.ErrMediaSidecarUnavailable
			}
			count++
			tmdb, err = strconv.ParseInt(id.ExternalID, 10, 64)
			if err != nil || tmdb <= 0 {
				return out, ports.ErrMediaSidecarUnavailable
			}
		}
	}
	if count != 1 || strings.TrimSpace(media.Title) == "" || (media.Year.Valid && (media.Year.Int32 < 1 || media.Year.Int32 > 9999)) {
		return out, ports.ErrMediaSidecarUnavailable
	}
	if !safeRelative(file.RelPath) || !safeRelative(root.MarkerFilename) || strings.Contains(root.MarkerFilename, "/") || !strings.HasPrefix(root.Path, "/") || path.Clean(root.Path) != root.Path || strings.ContainsAny(root.Path, "\\\x00") {
		return out, ports.ErrMediaSidecarUnavailable
	}
	row, err := q.GetMediaSidecarSnapshot(ctx, db.GetMediaSidecarSnapshotParams{SourceEventID: p.SourceEventID, FileID: p.FileID, UnitID: p.UnitID, JobID: lease.ID, Kind: kind})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ports.ErrMediaSidecarUnavailable
	}
	if err != nil {
		return out, err
	}
	var evidence snapshotEvidence
	if json.Unmarshal(row.Snapshot, &evidence) != nil || evidence.RootIdentity == nil || evidence.RootIdentity.Inode <= 0 || evidence.RootIdentity.Device < 0 {
		return out, ports.ErrMediaSidecarUnavailable
	}
	identity, ok := sourceIdentity(file, evidence.RootIdentity.Device)
	if !ok {
		return out, ports.ErrMediaSidecarUnavailable
	}
	out = ports.MediaSidecarSnapshot{
		Binding: row.Binding, Source: p, RootID: root.ID, RootPath: root.Path, RootMediaType: root.MediaType, MarkerFilename: root.MarkerFilename, RootHealth: root.Health, RootGeneration: root.Generation, RootHealthRevision: root.HealthRevision,
		RootIdentity: ports.FileIdentity{Device: evidence.RootIdentity.Device, Inode: evidence.RootIdentity.Inode}, FileRelative: file.RelPath, FileFingerprint: file.FileFingerprint.String, FileIdentity: identity, FileMappingRevision: file.MappingRevision, FileMutationSequence: file.LastMutationSeq,
		MediaID: media.ID, MediaType: media.MediaType, Title: media.Title, OriginalTitle: media.OriginalTitle.String, Year: intPointer(media.Year), TMDBID: tmdb, MediaMetadataVersion: media.MetadataVersion, UnitMetadataVersion: unit.MetadataVersion, UnitFactsVersion: unit.FactsVersion, UnitKey: unit.UnitKey, SeasonNumber: intPointer(unit.SeasonNumber), EpisodeNumber: intPointer(unit.EpisodeNumber),
	}
	for _, mapping := range evidence.Mappings {
		u := mapping.Unit
		out.Mappings = append(out.Mappings, ports.MediaSidecarMapping{UnitID: u.ID, MediaID: u.MediaID, UnitKey: u.UnitKey, SeasonNumber: u.Season, EpisodeNumber: u.Episode, MetadataVersion: u.MetadataVersion})
	}
	if evidence.Poster != nil && len(*evidence.Poster) <= 256 && posterPattern.MatchString(*evidence.Poster) {
		out.PosterPath = *evidence.Poster
	}
	return out, nil
}
func intPointer(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	n := v.Int32
	return &n
}
func safeRelative(v string) bool {
	return v != "" && v != "." && !strings.HasPrefix(v, "/") && path.Clean(v) == v && v != ".." && !strings.HasPrefix(v, "../") && !strings.ContainsAny(v, "\\\x00")
}
func sourceIdentity(file db.LibraryFile, device int64) (ports.FileIdentity, bool) {
	var out ports.FileIdentity
	if !file.FileFingerprint.Valid {
		return out, false
	}
	parts := strings.Split(file.FileFingerprint.String, ":")
	if len(parts) != 4 || parts[0] != "stat.v1" {
		return out, false
	}
	values := make([]int64, 3)
	for i, part := range parts[1:] {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != part {
			return out, false
		}
		values[i] = n
	}
	if values[0] <= 0 || values[1] < 0 || values[1] != file.SizeBytes || values[2] != file.MtimeNs {
		return out, false
	}
	return ports.FileIdentity{Device: device, Inode: values[0], Size: values[1], Mtime: values[2]}, true
}

func validateResult(s ports.MediaSidecarSnapshot, c ports.MediaSidecarCompletion) (bool, error) {
	nfo := "movie.nfo"
	if s.MediaType == "tv" {
		nfo = "tvshow.nfo"
	} else if s.MediaType != "movie" {
		return false, ports.ErrMediaSidecarResult
	}
	seen := make(map[string]bool)
	identities := make(map[[2]int64]bool)
	review := false
	for _, artifact := range c.Artifacts {
		if (artifact.Name != nfo && artifact.Name != "poster.jpg" && artifact.Name != "logo.png" && artifact.Name != "clearart.png" && artifact.Name != "banner.jpg" && (artifact.Name != "disc.png" || s.MediaType != "movie")) || seen[artifact.Name] {
			return false, ports.ErrMediaSidecarResult
		}
		seen[artifact.Name] = true
		result := artifact.Result
		id := result.Identity
		valid := id.Device == s.RootIdentity.Device && id.Inode > 0 && id.Size >= 0
		if id != (ports.FileIdentity{}) {
			key := [2]int64{id.Device, id.Inode}
			if !valid || identities[key] || (id.Device == s.FileIdentity.Device && id.Inode == s.FileIdentity.Inode) || (id.Device == s.RootIdentity.Device && id.Inode == s.RootIdentity.Inode) {
				return false, ports.ErrMediaSidecarResult
			}
			identities[key] = true
		}
		switch result.Disposition {
		case ports.SidecarCreated, ports.SidecarUncertain:
			if !result.Published || !valid || id.Size <= 0 || id.Size > ports.MaxSidecarBytes {
				return false, ports.ErrMediaSidecarResult
			}
			review = review || result.Disposition == ports.SidecarUncertain
		case ports.SidecarPreservedRegular:
			if result.Published || !valid {
				return false, ports.ErrMediaSidecarResult
			}
		case ports.SidecarNotPublished, ports.SidecarUnsafeExisting:
			if result.Published || id != (ports.FileIdentity{}) {
				return false, ports.ErrMediaSidecarResult
			}
			review = true
		default:
			return false, ports.ErrMediaSidecarResult
		}
	}
	if !seen[nfo] {
		return false, ports.ErrMediaSidecarResult
	}
	return review, nil
}

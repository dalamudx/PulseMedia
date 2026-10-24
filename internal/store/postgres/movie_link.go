package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MovieLinkStore struct {
	pool              *pgxpool.Pool
	ids               ports.IDGen
	work              *ports.MovieRecognitionWork
	requirePhysical   bool
	minimumMediaBytes int64
}

func NewMovieLinkStore(pool *pgxpool.Pool, ids ports.IDGen) *MovieLinkStore {
	return &MovieLinkStore{pool: pool, ids: ids, minimumMediaBytes: 1}
}

func NewMovieLinkStoreWithMinimumMediaBytes(pool *pgxpool.Pool, ids ports.IDGen, minimum int64) *MovieLinkStore {
	s := NewMovieLinkStore(pool, ids)
	s.minimumMediaBytes = minimum
	return s
}

var _ ports.MovieLinkRepository = (*MovieLinkStore)(nil)

type movieLinkMapping struct {
	UnitID      uuid.UUID  `json:"unit_id"`
	RetiredAt   *time.Time `json:"retired_at"`
	UnitRetired bool       `json:"unit_retired"`
}

func movieFileSnapshot(r db.GetMovieLinkSnapshotRow) ports.MovieFileSnapshot {
	return ports.MovieFileSnapshot{Root: ports.ScanRootSnapshot{ID: r.RootID, Path: r.RootPath, MediaType: r.RootMediaType, MarkerFilename: r.MarkerFilename, Health: r.Health, Generation: r.Generation, HealthRevision: r.HealthRevision}, FileID: r.ID, RelPath: r.RelPath, SizeBytes: r.SizeBytes, MtimeNS: r.MtimeNs, MutationSeq: r.LastMutationSeq, Fingerprint: r.FileFingerprint.String, FingerprintKnown: r.FileFingerprint.Valid, MappingRevision: r.MappingRevision, MappingStamp: []byte(r.MappingStamp)}
}
func movieLinkError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrMovieLinkStale
	}
	return e
}
func (s *MovieLinkStore) LoadSource(ctx context.Context, root uuid.UUID, path string) (ports.MovieFileSnapshot, error) {
	if root == uuid.Nil || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return ports.MovieFileSnapshot{}, ports.ErrRejected
	}
	row, e := db.New(s.pool).GetMovieLinkSnapshot(ctx, db.GetMovieLinkSnapshotParams{RootID: root, RelPath: path})
	if e != nil {
		return ports.MovieFileSnapshot{}, movieLinkError(e)
	}
	source := movieFileSnapshot(row)
	if eligible, _ := naming.IsEligibleMediaFile(source.RelPath, source.SizeBytes, s.minimumMediaBytes); !eligible {
		return ports.MovieFileSnapshot{}, ports.ErrMovieLinkStale
	}
	if source.Root.Health != "online" || source.Root.MediaType != "movie" {
		return ports.MovieFileSnapshot{}, ports.ErrMovieLinkStale
	}
	if e = GuardUntrackedMovieRecognition(ctx, db.New(s.pool), root, path); e != nil {
		return ports.MovieFileSnapshot{}, e
	}
	if s.work != nil {
		if source.FileID != s.work.FileID || source.Root.Generation != s.work.Generation || path != s.work.RelPath {
			return ports.MovieFileSnapshot{}, ports.ErrMovieRecognitionLease
		}
		if e = s.guardRecognition(ctx, db.New(s.pool), root, source.FileID); e != nil {
			return ports.MovieFileSnapshot{}, e
		}
	}
	return source, nil
}
func sameMovieFile(a, b ports.MovieFileSnapshot) bool {
	return a.Root == b.Root && a.FileID == b.FileID && a.RelPath == b.RelPath && a.SizeBytes == b.SizeBytes && a.MtimeNS == b.MtimeNS && a.FingerprintKnown == b.FingerprintKnown && a.Fingerprint == b.Fingerprint
}
func normalizeMovieIMDb(id string) (string, bool) {
	if id == "" {
		return "", true
	}
	id = strings.ToLower(id)
	if len(id) < 9 || len(id) > 12 || !strings.HasPrefix(id, "tt") {
		return "", false
	}
	for _, c := range id[2:] {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	n, e := strconv.ParseInt(id[2:], 10, 64)
	return id, e == nil && n > 0
}
func (s *MovieLinkStore) Link(ctx context.Context, in ports.MovieLinkInput) (ports.MovieLinkResult, error) {
	var empty ports.MovieLinkResult
	meta := in.Metadata
	source := in.Source
	if eligible, _ := naming.IsEligibleMediaFile(source.RelPath, source.SizeBytes, s.minimumMediaBytes); !eligible {
		return empty, ports.ErrMovieLinkStale
	}
	var quality map[string]json.RawMessage
	imdb, validIMDb := normalizeMovieIMDb(meta.IMDbID)
	if meta.TMDBID <= 0 || strings.TrimSpace(meta.Title) == "" || meta.Year < 0 || meta.Year > 9999 || (meta.MediaType != "" && meta.MediaType != "movie") || !validIMDb || json.Unmarshal(in.QualityJSON, &quality) != nil || quality == nil {
		return empty, ports.ErrRejected
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return empty, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e = s.guardFileCheck(ctx, q, in); e != nil {
		return empty, e
	}
	if e = s.guardRecognition(ctx, q, source.Root.ID, source.FileID); e != nil {
		return empty, e
	}
	if s.work != nil && source.Root.Generation != s.work.Generation {
		return empty, ports.ErrMovieRecognitionLease
	}
	root, e := q.LockTransferRoot(ctx, source.Root.ID)
	if e != nil {
		return empty, movieLinkError(e)
	}
	if scanRootSnapshot(root) != source.Root || root.Health != "online" || root.MediaType != "movie" {
		return empty, ports.ErrMovieLinkStale
	}
	if e = GuardUntrackedMovieRecognition(ctx, q, source.Root.ID, source.RelPath); e != nil {
		return empty, e
	}
	tmdb := strconv.FormatInt(meta.TMDBID, 10)
	keys := []string{"movie_link:tmdb.movie:" + tmdb}
	if imdb != "" {
		keys = append(keys, "movie_link:imdb:"+imdb)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if e = q.LockMovieLinkIdentity(ctx, key); e != nil {
			return empty, e
		}
	}
	var work db.Medium
	existing, e := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.movie", ExternalID: tmdb})
	isNew := errors.Is(e, pgx.ErrNoRows)
	if e != nil && !isNew {
		return empty, e
	}
	if imdb != "" {
		other, lookup := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "imdb", ExternalID: imdb})
		if lookup == nil && (isNew || other.ID != existing.ID) {
			return empty, ports.ErrMovieLinkConflict
		}
		if lookup != nil && !errors.Is(lookup, pgx.ErrNoRows) {
			return empty, lookup
		}
	}
	if isNew {
		work, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: s.ids.NewUUID(), Title: meta.Title, OriginalTitle: pgtype.Text{String: meta.OriginalTitle, Valid: meta.OriginalTitle != ""}, Year: pgtype.Int4{Int32: int32(meta.Year), Valid: meta.Year > 0}, MediaType: "movie"})
		if e != nil {
			return empty, e
		}
		if _, e = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: s.ids.NewUUID(), MediaID: work.ID, Namespace: "tmdb.movie", ExternalID: tmdb}); e != nil {
			return empty, e
		}
		if imdb != "" {
			if _, e = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: s.ids.NewUUID(), MediaID: work.ID, Namespace: "imdb", ExternalID: imdb}); e != nil {
				return empty, e
			}
		}
	} else {
		work = db.Medium{ID: existing.ID, Title: existing.Title, OriginalTitle: existing.OriginalTitle, Year: existing.Year, MediaType: existing.MediaType, MetadataVersion: existing.MetadataVersion}
		if work.MediaType != "movie" {
			return empty, ports.ErrMovieLinkConflict
		}
	}
	unit, e := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: work.ID, UnitKey: "movie"})
	if errors.Is(e, pgx.ErrNoRows) {
		unit, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: s.ids.NewUUID(), MediaID: work.ID, UnitKey: "movie"})
	}
	if e != nil {
		return empty, e
	}
	if unit.Retired {
		return empty, ports.ErrMovieLinkConflict
	}
	work, e = q.GetMediaByID(ctx, work.ID)
	if e != nil {
		return empty, e
	}
	ids, e := q.ListScanFileSourceUnits(ctx, source.FileID)
	if e != nil {
		return empty, e
	}
	ids = append(ids, unit.ID)
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	var previous uuid.UUID
	for _, id := range ids {
		if id == previous {
			continue
		}
		previous = id
		locked, lockErr := q.LockTransferUnit(ctx, id)
		if lockErr != nil {
			return empty, movieLinkError(lockErr)
		}
		if id == unit.ID {
			unit = locked
		}
	}
	if unit.Retired || unit.MediaID != work.ID || unit.UnitKey != "movie" {
		return empty, ports.ErrMovieLinkConflict
	}
	registered, lookupErr := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.movie", ExternalID: tmdb})
	if lookupErr != nil {
		return empty, movieLinkError(lookupErr)
	}
	if registered.ID != work.ID || registered.MetadataVersion != work.MetadataVersion || registered.MediaType != "movie" {
		return empty, ports.ErrMovieLinkStale
	}
	if _, e = q.LockTransferOldFile(ctx, source.FileID); e != nil {
		return empty, movieLinkError(e)
	}
	row, e := q.GetMovieLinkSnapshot(ctx, db.GetMovieLinkSnapshotParams{RootID: source.Root.ID, RelPath: source.RelPath})
	if e != nil {
		return empty, movieLinkError(e)
	}
	current := movieFileSnapshot(row)
	if eligible, _ := naming.IsEligibleMediaFile(current.RelPath, current.SizeBytes, s.minimumMediaBytes); !eligible {
		return empty, ports.ErrMovieLinkStale
	}
	if !sameMovieFile(source, current) {
		return empty, ports.ErrMovieLinkStale
	}
	var mappings []movieLinkMapping
	if e = json.Unmarshal(current.MappingStamp, &mappings); e != nil {
		return empty, e
	}
	if len(mappings) > 1 || (len(mappings) == 1 && (mappings[0].UnitID != unit.ID || mappings[0].RetiredAt != nil || mappings[0].UnitRetired)) {
		return empty, ports.ErrMovieLinkConflict
	}
	facts, e := q.GetMovieLinkFactState(ctx, db.GetMovieLinkFactStateParams{UnitID: unit.ID, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, FileID: pgtype.UUID{Bytes: source.FileID, Valid: true}, QualityJson: in.QualityJSON})
	if e != nil {
		return empty, e
	}
	stampSame := source.MutationSeq == current.MutationSeq && source.MappingRevision == current.MappingRevision && bytes.Equal(source.MappingStamp, current.MappingStamp)
	canRecoverOwnLink := bytes.Equal(source.MappingStamp, []byte("[]")) && len(mappings) == 1 && facts.ReplayCount == 1
	if !stampSame && !canRecoverOwnLink {
		return empty, ports.ErrMovieLinkStale
	}
	result := ports.MovieLinkResult{Media: ports.LinkedMovie{ID: work.ID, Title: work.Title, OriginalTitle: work.OriginalTitle.String, Year: int(work.Year.Int32), MediaType: work.MediaType, MetadataVersion: work.MetadataVersion}, Unit: ports.LinkedMovieUnit{ID: unit.ID, MediaID: unit.MediaID, UnitKey: unit.UnitKey}}
	if facts.HistoryCount > 0 {
		if len(mappings) != 1 || facts.ReplayCount != 1 {
			return empty, ports.ErrMovieLinkConflict
		}
		if e = s.guardFileCheck(ctx, q, in); e != nil {
			return empty, e
		}
		if e = s.finishRecognition(ctx, q, result, ""); e != nil {
			return empty, e
		}
		if _, e = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); e != nil {
			return empty, e
		}
		if e = GuardUntrackedMovieRecognition(ctx, q, source.Root.ID, source.RelPath); e != nil {
			return empty, e
		}
		if e = tx.Commit(ctx); e != nil {
			return empty, e
		}
		return result, nil
	}
	if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: source.FileID, UnitID: unit.ID}); e != nil {
		return empty, e
	}
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: s.ids.NewUUID(), UnitID: unit.ID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: source.FileID, Valid: true}, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, QualityProfile: in.QualityJSON}); e != nil {
		return empty, e
	}
	seq, e := q.IncrementRootMutationSeq(ctx, root.ID)
	if e != nil {
		return empty, e
	}
	if e = q.StampMovieLinkedFile(ctx, db.StampMovieLinkedFileParams{ID: source.FileID, LastMutationSeq: seq}); e != nil {
		return empty, e
	}
	if e = s.guardFileCheck(ctx, q, in); e != nil {
		return empty, e
	}
	if e = s.finishRecognition(ctx, q, result, ""); e != nil {
		return empty, e
	}
	if _, e = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); e != nil {
		return empty, e
	}
	if e = GuardUntrackedMovieRecognition(ctx, q, source.Root.ID, source.RelPath); e != nil {
		return empty, e
	}
	if e = tx.Commit(ctx); e != nil {
		return empty, e
	}
	return result, nil
}

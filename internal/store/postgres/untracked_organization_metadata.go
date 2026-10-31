package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// This bridge is intentionally store-to-store; pgx never enters a ports contract.
type UntrackedOrganizationUnit struct {
	MediaID, UnitID uuid.UUID
	MetadataVersion int64
	UnitKey         string
	TMDBEpisodeID   int64
}

type untrackedOrganizationUnitRow struct {
	OperationID          uuid.UUID                 `json:"operation_id"`
	UnitID               uuid.UUID                 `json:"unit_id"`
	MediaID              uuid.UUID                 `json:"media_id"`
	MetadataVersion      int64                     `json:"metadata_version"`
	MediaMetadataVersion int64                     `json:"media_metadata_version"`
	UnitKey              string                    `json:"unit_key"`
	TMDBEpisodeID        int64                     `json:"tmdb_episode_id"`
	CandidateUnit        ports.UntrackedReviewUnit `json:"candidate_unit"`
	MediaIdentity        organizationIdentity      `json:"media_identity"`
}

type organizationIdentity struct {
	Namespace  string `json:"namespace"`
	ExternalID string `json:"external_id"`
}

func organizationIdentityLock(ctx context.Context, q *db.Queries, c ports.UntrackedReviewCandidate) error {
	// Reuse the existing subscription/selected-movie canonical identity namespace.
	return q.LockMovieLinkIdentity(ctx, c.Key)
}

func organizationMetadataSeal(ctx context.Context, q *db.Queries, c ports.UntrackedReviewCandidate) ([]byte, error) {
	return q.GetUntrackedOrganizationMetadata(ctx, db.GetUntrackedOrganizationMetadataParams{Namespace: c.Namespace, ExternalID: strconv.FormatInt(c.TMDBID, 10)})
}

func organizationMediaMatches(m db.Medium, c ports.UntrackedReviewCandidate, kind string) bool {
	return m.ID != uuid.Nil && m.MediaType == kind && m.Title == c.Title && m.OriginalTitle.String == c.OriginalTitle &&
		m.Year.Valid == (c.Year > 0) && (!m.Year.Valid || int(m.Year.Int32) == c.Year) && m.MetadataVersion > 0
}

func organizationUnitMatches(u db.MediaUnit, w ports.UntrackedReviewUnit, mid uuid.UUID, kind string, in ports.UntrackedReviewInput) bool {
	if u.ID == uuid.Nil || u.MediaID != mid || u.UnitKey != w.UnitKey || u.Retired || u.MetadataVersion < 1 {
		return false
	}
	// An absolute number is supplemental metadata, never an inferred season/episode mapping.
	// Only the user's exact frozen canonical selection can authorize this existing unit.
	if u.AbsoluteNumber.Valid && (kind != "tv" || !in.CanonicalSelectionValid() || in.CanonicalUnitID != u.ID.String() || in.UnitMetadataVersion != u.MetadataVersion || in.Season != w.Season || in.Episode != w.Episode || in.TMDBEpisodeID != w.TMDBEpisodeID) {
		return false
	}
	if kind == "movie" {
		return !u.SeasonNumber.Valid && !u.EpisodeNumber.Valid && !u.AirDate.Valid
	}
	if !u.SeasonNumber.Valid || !u.EpisodeNumber.Valid || int(u.SeasonNumber.Int32) != w.Season || int(u.EpisodeNumber.Int32) != w.Episode {
		return false
	}
	if u.AirDate.Valid != (w.AirDate != nil) {
		return false
	}
	if w.AirDate != nil {
		return u.AirDate.Time.Format("2006-01-02") == w.AirDate.Format("2006-01-02")
	}
	return true
}

// Publication uses the existing canonical row/episode identity primitives. It never
// calls subscription merge helpers, which also publish Wanted and retirement.
func (s *UntrackedOrganizationStore) publishUnits(ctx context.Context, tx pgx.Tx, m ports.UntrackedOrganizationMaterial) ([]untrackedOrganizationUnitRow, error) {
	q, c := db.New(tx), *m.Confirmation.Material.Review.Evidence.Candidate
	kind := m.Confirmation.Material.Scope.TargetMediaType
	if err := organizationIdentityLock(ctx, q, c); err != nil {
		return nil, err
	}
	found, err := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: c.Namespace, ExternalID: strconv.FormatInt(c.TMDBID, 10)})
	var mid uuid.UUID
	if errors.Is(err, pgx.ErrNoRows) {
		work, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: s.ids.NewUUID(), Title: c.Title, OriginalTitle: pgtype.Text{String: c.OriginalTitle, Valid: c.OriginalTitle != ""}, Year: pgtype.Int4{Int32: int32(c.Year), Valid: c.Year > 0}, MediaType: kind})
		if e != nil {
			return nil, e
		}
		mid = work.ID
		if _, err = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: s.ids.NewUUID(), MediaID: mid, Namespace: c.Namespace, ExternalID: strconv.FormatInt(c.TMDBID, 10)}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		mid = found.ID
	}
	work, err := q.LockMappingMedia(ctx, mid)
	if err != nil {
		return nil, err
	}
	if !organizationMediaMatches(work, c, kind) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	if kind == "movie" {
		existing, e := q.ListMediaUnits(ctx, mid)
		if e != nil {
			return nil, e
		}
		for _, u := range existing {
			if u.UnitKey != "movie" {
				return nil, ports.ErrUntrackedOrganizationStale
			}
		}
	}
	units := make([]db.MediaUnit, 0, len(c.Units))
	for _, w := range c.Units {
		u, e := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: mid, UnitKey: w.UnitKey})
		if errors.Is(e, pgx.ErrNoRows) {
			if kind == "movie" {
				u, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: s.ids.NewUUID(), MediaID: mid, UnitKey: w.UnitKey})
			} else {
				date := pgtype.Date{}
				if w.AirDate != nil {
					date = pgtype.Date{Time: *w.AirDate, Valid: true}
				}
				u, e = q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: s.ids.NewUUID(), MediaID: mid, UnitKey: w.UnitKey, SeasonNumber: pgtype.Int4{Int32: int32(w.Season), Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(w.Episode), Valid: true}, AirDate: date})
			}
		}
		if e != nil {
			return nil, e
		}
		if !organizationUnitMatches(u, w, mid, kind, m.Confirmation.Material.Review.Request.Input) {
			return nil, ports.ErrUntrackedOrganizationStale
		}
		if kind == "tv" {
			if _, e = q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: u.ID, ExternalID: w.TMDBEpisodeID, TmdbSeriesID: c.TMDBID}); e != nil {
				return nil, e
			}
			i, e := q.GetTMDBEpisodeIdentityForUnit(ctx, u.ID)
			if e != nil {
				return nil, e
			}
			if i.ExternalID != w.TMDBEpisodeID || i.TmdbSeriesID != c.TMDBID {
				return nil, ports.ErrUntrackedOrganizationStale
			}
		}
		units = append(units, u)
	}
	// Identity insert triggers can advance canonical versions. Freeze only now.
	work, err = q.GetMediaByID(ctx, mid)
	if err != nil {
		return nil, err
	}
	sorted := append([]db.MediaUnit(nil), units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID.String() < sorted[j].ID.String() })
	current := map[uuid.UUID]db.MediaUnit{}
	for _, u := range sorted {
		locked, e := q.LockTransferUnit(ctx, u.ID)
		if e != nil {
			return nil, e
		}
		current[u.ID] = locked
	}
	out := make([]untrackedOrganizationUnitRow, 0, len(units))
	for i, u := range units {
		u = current[u.ID]
		w := c.Units[i]
		if !organizationUnitMatches(u, w, mid, kind, m.Confirmation.Material.Review.Request.Input) {
			return nil, ports.ErrUntrackedOrganizationStale
		}
		out = append(out, untrackedOrganizationUnitRow{OperationID: m.Operation.ID, UnitID: u.ID, MediaID: mid, MetadataVersion: u.MetadataVersion, MediaMetadataVersion: work.MetadataVersion, UnitKey: w.UnitKey, TMDBEpisodeID: w.TMDBEpisodeID, CandidateUnit: reviewClone(w), MediaIdentity: organizationIdentity{Namespace: c.Namespace, ExternalID: strconv.FormatInt(c.TMDBID, 10)}})
	}
	return out, nil
}

// The query returns an array; retain strict required fields on every row.
func organizationUnitRows(raw []byte) ([]untrackedOrganizationUnitRow, error) {
	var documents []json.RawMessage
	if statsDecode(raw, &documents) != nil || len(documents) == 0 {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	units := make([]untrackedOrganizationUnitRow, len(documents))
	for i, document := range documents {
		if checkinDecode(document, &units[i]) != nil {
			return nil, ports.ErrUntrackedOrganizationStale
		}
	}
	return units, nil
}

func organizationFrozenUnits(ctx context.Context, q *db.Queries, m ports.UntrackedOrganizationMaterial) ([]untrackedOrganizationUnitRow, error) {
	raw, err := q.GetUntrackedOrganizationUnits(ctx, m.Operation.ID)
	if err != nil {
		return nil, err
	}
	units, err := organizationUnitRows(raw)
	if err != nil || len(units) != len(m.Confirmation.Material.Review.Evidence.Candidate.Units) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	expected := map[string]ports.UntrackedReviewUnit{}
	for _, w := range m.Confirmation.Material.Review.Evidence.Candidate.Units {
		expected[w.UnitKey] = w
	}
	seen := map[uuid.UUID]bool{}
	for _, u := range units {
		w, ok := expected[u.UnitKey]
		c := m.Confirmation.Material.Review.Evidence.Candidate
		if !ok || seen[u.UnitID] || u.UnitID == uuid.Nil || u.MediaID == uuid.Nil || u.OperationID != m.Operation.ID || u.MetadataVersion < 1 || u.MediaMetadataVersion < 1 || u.TMDBEpisodeID != w.TMDBEpisodeID || u.MediaIdentity.Namespace != c.Namespace || u.MediaIdentity.ExternalID != strconv.FormatInt(c.TMDBID, 10) || !reviewJSONSame(reviewJSON(u.CandidateUnit), reviewJSON(w)) {
			return nil, ports.ErrUntrackedOrganizationStale
		}
		seen[u.UnitID] = true
		delete(expected, u.UnitKey)
	}
	return units, nil
}

func organizationValidateUnits(ctx context.Context, q *db.Queries, m ports.UntrackedOrganizationMaterial, units []untrackedOrganizationUnitRow, lock bool) error {
	c, kind := *m.Confirmation.Material.Review.Evidence.Candidate, m.Confirmation.Material.Scope.TargetMediaType
	if lock {
		if err := organizationIdentityLock(ctx, q, c); err != nil {
			return err
		}
	}
	sorted := append([]untrackedOrganizationUnitRow(nil), units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].UnitID.String() < sorted[j].UnitID.String() })
	identity, err := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: c.Namespace, ExternalID: strconv.FormatInt(c.TMDBID, 10)})
	if err != nil {
		return err
	}
	var work db.Medium
	if lock {
		work, err = q.LockMappingMedia(ctx, identity.ID)
	} else {
		work, err = q.GetMediaByID(ctx, identity.ID)
	}
	if err != nil {
		return err
	}
	if !organizationMediaMatches(work, c, kind) {
		return ports.ErrUntrackedOrganizationStale
	}
	for _, f := range sorted {
		var u db.MediaUnit
		if lock {
			u, err = q.LockTransferUnit(ctx, f.UnitID)
		} else {
			u, err = q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: f.MediaID, UnitKey: f.UnitKey})
		}
		if err != nil {
			return err
		}
		if work.ID != f.MediaID || work.MetadataVersion != f.MediaMetadataVersion || u.ID != f.UnitID || u.MetadataVersion != f.MetadataVersion || !organizationUnitMatches(u, f.CandidateUnit, f.MediaID, kind, m.Confirmation.Material.Review.Request.Input) {
			return ports.ErrUntrackedOrganizationStale
		}
		if kind == "tv" {
			i, e := q.GetTMDBEpisodeIdentityForUnit(ctx, u.ID)
			if e != nil {
				return e
			}
			if i.ExternalID != f.TMDBEpisodeID || i.TmdbSeriesID != c.TMDBID {
				return ports.ErrUntrackedOrganizationStale
			}
		}
	}
	return nil
}

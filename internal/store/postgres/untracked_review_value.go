package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const untrackedConfirmationState = "confirmed_pending_organization"

type untrackedReviewRow struct {
	ID         uuid.UUID                     `json:"id"`
	OwnerID    uuid.UUID                     `json:"owner_id"`
	ScopeID    uuid.UUID                     `json:"scope_id"`
	FileID     uuid.UUID                     `json:"file_id"`
	JobID      uuid.UUID                     `json:"job_id"`
	CommandKey string                        `json:"command_key"`
	InputHash  string                        `json:"input_hash"`
	Request    ports.UntrackedReviewRequest  `json:"request"`
	Scope      ports.UntrackedScope          `json:"scope_snapshot"`
	File       ports.UntrackedDiscoveredFile `json:"file_snapshot"`
}

type untrackedReviewCapture struct {
	store    *UntrackedReviewStore
	snapshot ports.UntrackedReviewSnapshot
	original untrackedReviewRow
	deadline time.Time
}

func (c *untrackedReviewCapture) UntrackedReviewSnapshot() ports.UntrackedReviewSnapshot {
	return reviewClone(c.snapshot)
}

type untrackedConfirmationCapture struct {
	store                 *UntrackedReviewStore
	actor                 ports.Actor
	material              ports.UntrackedConfirmationMaterial
	key, hash             string
	deadline, issuedLocal time.Time
}

func (c *untrackedConfirmationCapture) UntrackedConfirmationMaterial() ports.UntrackedConfirmationMaterial {
	return reviewClone(c.material)
}

// These types contain only JSON values. Round trips also detach nested unit/date pointers.
func reviewClone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

func reviewJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func reviewError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ports.ErrUntrackedReviewStale) || errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
		return ports.ErrUntrackedReviewStale
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	if errors.Is(err, ErrRestoreIsolated) {
		return ports.ErrUnavailable
	}
	return statsError(err)
}

func reviewActor(a *ports.Actor) (*ports.Actor, error) {
	if a == nil {
		return nil, ports.ErrForbidden
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	return checkinActor(a)
}

func reviewInputValid(in ports.UntrackedReviewInput) bool {
	return in.ScopeVersion > 0 && in.FileRevision > 0 && (in.MediaType == "movie" || in.MediaType == "tv") && in.TMDBID >= 0
}

func reviewHashValid(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}

func reviewText(s string, n int, required bool) bool {
	if !utf8.ValidString(s) || len(s) > n || (required && strings.TrimSpace(s) == "") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func reviewEvidenceValid(in ports.UntrackedReviewInput, e ports.UntrackedReviewEvidence) bool {
	selection := "automatic"
	if in.TMDBID > 0 {
		selection = "explicit"
	}
	if e.Selection != selection || !reviewText(e.ParserVersion, 128, e.Complete) || !reviewText(e.ParsedTitle, 1024, false) || e.ParsedYear < 0 || e.ParsedYear > 9999 {
		return false
	}
	if !e.Complete {
		return e.Candidate == nil && doubanSyncASCII(e.Reason, 128, false)
	}
	if e.Reason != "ready" || e.Candidate == nil {
		return false
	}
	c := e.Candidate
	ns := "tmdb." + in.MediaType
	if c.Namespace != ns || c.TMDBID <= 0 || (in.TMDBID > 0 && c.TMDBID != in.TMDBID) || c.Key != fmt.Sprintf("%s:%d", ns, c.TMDBID) || !reviewText(c.Title, 1024, true) || !reviewText(c.OriginalTitle, 1024, false) || c.Year < 0 || c.Year > 9999 || len(c.Units) < 1 || len(c.Units) > 128 {
		return false
	}
	if in.MediaType == "movie" {
		u := c.Units[0]
		return len(c.Units) == 1 && u.UnitKey == "movie" && u.TMDBEpisodeID == 0 && u.Season == 0 && u.Episode == 0 && u.AirDate == nil
	}
	keys := make(map[string]bool, len(c.Units))
	ids := make(map[int64]bool, len(c.Units))
	for _, u := range c.Units {
		if u.Season <= 0 || u.Episode <= 0 || u.TMDBEpisodeID <= 0 || u.UnitKey != fmt.Sprintf("S%02dE%02d", u.Season, u.Episode) || keys[u.UnitKey] || ids[u.TMDBEpisodeID] {
			return false
		}
		if u.AirDate != nil {
			t := *u.AirDate
			if t.IsZero() || t.Year() < 1 || t.Year() > 9999 || t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
				return false
			}
		}
		keys[u.UnitKey], ids[u.TMDBEpisodeID] = true, true
	}
	return true
}

func reviewScopeCanonical(s ports.UntrackedScope) ports.UntrackedScope {
	s.CreatedAt, s.UpdatedAt, s.ClientVersion = s.CreatedAt.UTC(), s.UpdatedAt.UTC(), s.ClientVersion.UTC()
	return s
}

func reviewRequestCanonical(r ports.UntrackedReviewRequest) ports.UntrackedReviewRequest {
	r.CreatedAt = r.CreatedAt.UTC()
	return r
}

// Telemetry is deliberately absent from identity and from the material hash.
func reviewStableFile(f ports.UntrackedDiscoveredFile) ports.UntrackedDiscoveredFile {
	f.FirstObservedAt, f.LastObservedAt, f.Observations = time.Time{}, time.Time{}, 0
	return f
}

func reviewFileSame(a, b ports.UntrackedDiscoveredFile) bool {
	return reviewStableFile(a) == reviewStableFile(b)
}

func reviewFileValid(f ports.UntrackedDiscoveredFile, scope ports.UntrackedScope) bool {
	return f.ID != uuid.Nil && f.ScopeID == scope.ID && f.ScopeVersion > 0 && f.ScopeVersion <= scope.Version && f.Revision > 0 && f.Observations > 0 && !f.FirstObservedAt.IsZero() && !f.LastObservedAt.Before(f.FirstObservedAt) && discoveryObservationValid(ports.UntrackedFileObservation{RelativePath: f.RelativePath, Identity: f.Identity})
}

func reviewMaterialHash(r untrackedReviewRow, e ports.UntrackedReviewEvidence) string {
	e = reviewClone(e)
	if e.Candidate != nil {
		for i := range e.Candidate.Units {
			if d := e.Candidate.Units[i].AirDate; d != nil {
				t := d.UTC()
				e.Candidate.Units[i].AirDate = &t
			}
		}
	}
	return watchDigest(struct {
		Request  ports.UntrackedReviewRequest
		Scope    ports.UntrackedScope
		File     ports.UntrackedDiscoveredFile
		Evidence ports.UntrackedReviewEvidence
	}{reviewRequestCanonical(r.Request), reviewScopeCanonical(r.Scope), reviewStableFile(r.File), e})
}

func reviewRowValid(r untrackedReviewRow) bool {
	x := r.Request
	return r.ID != uuid.Nil && r.OwnerID != uuid.Nil && r.ScopeID != uuid.Nil && r.FileID != uuid.Nil && r.JobID != uuid.Nil && doubanSyncASCII(r.CommandKey, 160, false) && reviewInputValid(x.Input) && r.InputHash == watchDigest(x.Input) && x.ID == r.ID && x.ActorID == r.OwnerID && x.ScopeID == r.ScopeID && x.FileID == r.FileID && x.JobID == r.JobID && !x.CreatedAt.IsZero() && untrackedScopeValid(r.Scope) && r.Scope.Enabled && r.Scope.ID == r.ScopeID && r.Scope.OwnerID == r.OwnerID && x.Input.ScopeVersion == r.Scope.Version && r.File.ScopeVersion == r.Scope.Version && x.Input.FileRevision == r.File.Revision && x.Input.MediaType == r.Scope.TargetMediaType && reviewFileValid(r.File, r.Scope) && r.File.ID == r.FileID
}

func reviewRowSame(a, b untrackedReviewRow) bool {
	a.Request, b.Request = reviewRequestCanonical(a.Request), reviewRequestCanonical(b.Request)
	a.Scope, b.Scope = reviewScopeCanonical(a.Scope), reviewScopeCanonical(b.Scope)
	a.File.FirstObservedAt, a.File.LastObservedAt = a.File.FirstObservedAt.UTC(), a.File.LastObservedAt.UTC()
	b.File.FirstObservedAt, b.File.LastObservedAt = b.File.FirstObservedAt.UTC(), b.File.LastObservedAt.UTC()
	return reflect.DeepEqual(a, b)
}

func reviewPayload(raw []byte) (uuid.UUID, error) {
	var p struct {
		RequestID uuid.UUID `json:"request_id"`
	}
	var fields map[string]json.RawMessage
	if checkinDecode(raw, &p) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || p.RequestID == uuid.Nil {
		return uuid.Nil, ports.ErrUntrackedReviewStale
	}
	return p.RequestID, nil
}

func reviewConfirmationInputValid(in ports.UntrackedConfirmationInput) bool {
	if in.ReviewID == uuid.Nil || !reviewHashValid(in.MaterialHash) || in.ScopeVersion < 1 || in.FileRevision < 1 || !reviewText(in.CandidateKey, 128, true) {
		return false
	}
	switch in.Action {
	case "hardlink", "copy", "reflink", "move":
	default:
		return false
	}
	return discoveryObservationValid(ports.UntrackedFileObservation{RelativePath: in.TargetRelativePath, Identity: ports.FileIdentity{Inode: 1}})
}

func reviewEvidenceState(e *ports.UntrackedReviewEvidence) string {
	if e == nil {
		return ""
	}
	if e.Complete {
		return "ready"
	}
	return "review_required"
}

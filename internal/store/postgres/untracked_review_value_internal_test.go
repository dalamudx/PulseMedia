package postgres

import (
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func reviewTVTestEvidence() ports.UntrackedReviewEvidence {
	return ports.UntrackedReviewEvidence{Selection: "automatic", ParserVersion: "parser-v1", Complete: true, Reason: "ready", Candidate: &ports.UntrackedReviewCandidate{Namespace: "tmdb.tv", Key: "tmdb.tv:12", TMDBID: 12, Title: "Show", Units: []ports.UntrackedReviewUnit{{UnitKey: "S01E01", Season: 1, Episode: 1, TMDBEpisodeID: 101}, {UnitKey: "S01E02", Season: 1, Episode: 2, TMDBEpisodeID: 102}}}}
}

func TestUntrackedReviewTypedEvidenceInvariant(t *testing.T) {
	in := ports.UntrackedReviewInput{ScopeVersion: 1, FileRevision: 1, MediaType: "tv"}
	if !reviewEvidenceValid(in, reviewTVTestEvidence()) {
		t.Fatal("valid TV evidence rejected")
	}
	for _, mutation := range []string{"namespace", "numeric-key", "episode-id", "duplicate-id", "duplicate-unit", "zero-season", "zero-episode", "noncanonical", "date", "overflow", "incomplete-with-candidate", "reason-with-ready", "blank-parser", "control-title"} {
		t.Run(mutation, func(t *testing.T) {
			e := reviewTVTestEvidence()
			switch mutation {
			case "namespace":
				e.Candidate.Namespace = "tmdb.movie"
			case "numeric-key":
				e.Candidate.Key = "12"
			case "episode-id":
				e.Candidate.Units[0].TMDBEpisodeID = 0
			case "duplicate-id":
				e.Candidate.Units[1].TMDBEpisodeID = e.Candidate.Units[0].TMDBEpisodeID
			case "duplicate-unit":
				e.Candidate.Units[1] = e.Candidate.Units[0]
			case "zero-season":
				e.Candidate.Units[0].Season = 0
			case "zero-episode":
				e.Candidate.Units[0].Episode = 0
			case "noncanonical":
				e.Candidate.Units[0].UnitKey = "S1E1"
			case "date":
				d := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
				e.Candidate.Units[0].AirDate = &d
			case "overflow":
				e.Candidate.Units = make([]ports.UntrackedReviewUnit, 129)
			case "incomplete-with-candidate":
				e.Complete = false
				e.Reason = "incomplete"
			case "reason-with-ready":
				e.Reason = "ambiguous"
			case "blank-parser":
				e.ParserVersion = ""
			case "control-title":
				e.Candidate.Title = "Show\nsecret"
			}
			if reviewEvidenceValid(in, e) {
				t.Fatal("invalid evidence accepted", mutation)
			}
		})
	}
	e := reviewTVTestEvidence()
	e.Candidate.Units = make([]ports.UntrackedReviewUnit, 128)
	for i := range e.Candidate.Units {
		e.Candidate.Units[i] = ports.UntrackedReviewUnit{UnitKey: fmt.Sprintf("S01E%02d", i+1), Season: 1, Episode: i + 1, TMDBEpisodeID: int64(1000 + i)}
	}
	if !reviewEvidenceValid(in, e) {
		t.Fatal("bounded 128-unit coverage rejected")
	}
	explicit := in
	explicit.TMDBID = 99
	if reviewEvidenceValid(explicit, e) {
		t.Fatal("explicit intent was substituted")
	}
	e.Selection = "explicit"
	e.Candidate.TMDBID = 99
	e.Candidate.Key = "tmdb.tv:99"
	if !reviewEvidenceValid(explicit, e) {
		t.Fatal("exact explicit identity rejected")
	}
	for _, reason := range []string{"unresolved", "ambiguous", "incomplete"} {
		if !reviewEvidenceValid(in, ports.UntrackedReviewEvidence{Selection: "automatic", Reason: reason}) {
			t.Fatal(reason)
		}
	}
	if reviewEvidenceValid(in, ports.UntrackedReviewEvidence{Selection: "automatic"}) {
		t.Fatal("missing stable reason accepted")
	}
}

func TestUntrackedReviewMaterialHashStableTelemetryAndTimezones(t *testing.T) {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	r := untrackedReviewRow{Request: ports.UntrackedReviewRequest{ID: uuid.New(), CreatedAt: now}, Scope: ports.UntrackedScope{CreatedAt: now, UpdatedAt: now, ClientVersion: now}, File: ports.UntrackedDiscoveredFile{ID: uuid.New(), ScopeID: uuid.New(), ScopeVersion: 1, Revision: 2, RelativePath: "show.mkv", Identity: ports.FileIdentity{Device: 1, Inode: 2, Size: 3, Mtime: 4}, FirstObservedAt: now, LastObservedAt: now, Observations: 1}}
	e := reviewTVTestEvidence()
	hash := reviewMaterialHash(r, e)
	same := reviewClone(r)
	same.File.Observations = 999
	same.File.FirstObservedAt = now.Add(time.Hour)
	same.File.LastObservedAt = now.Add(2 * time.Hour)
	same.Request.CreatedAt = now.In(time.FixedZone("offset", 19800))
	same.Scope.CreatedAt = same.Request.CreatedAt
	same.Scope.UpdatedAt = same.Request.CreatedAt
	same.Scope.ClientVersion = same.Request.CreatedAt
	if reviewMaterialHash(same, e) != hash || !reviewFileSame(r.File, same.File) {
		t.Fatal("telemetry or timestamp zone changed stable material")
	}
	for _, field := range []string{"revision", "path", "device", "inode", "size", "mtime", "scope-version"} {
		changed := reviewClone(r)
		switch field {
		case "revision":
			changed.File.Revision++
		case "path":
			changed.File.RelativePath = "other.mkv"
		case "device":
			changed.File.Identity.Device++
		case "inode":
			changed.File.Identity.Inode++
		case "size":
			changed.File.Identity.Size++
		case "mtime":
			changed.File.Identity.Mtime++
		case "scope-version":
			changed.File.ScopeVersion++
		}
		if reviewMaterialHash(changed, e) == hash || reviewFileSame(changed.File, r.File) {
			t.Fatal("stable identity omitted", field)
		}
	}
	changed := reviewClone(e)
	changed.ParserVersion = "parser-v2"
	if reviewMaterialHash(r, changed) == hash {
		t.Fatal("parser evidence omitted")
	}
}

func TestUntrackedReviewCaptureDeepClone(t *testing.T) {
	date := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	e := reviewTVTestEvidence()
	e.Candidate.Units[0].AirDate = &date
	c := &untrackedConfirmationCapture{material: ports.UntrackedConfirmationMaterial{Review: ports.UntrackedReview{Evidence: &e}}}
	exported := c.UntrackedConfirmationMaterial()
	exported.Review.Evidence.Candidate.Units[0].UnitKey = "forged"
	*exported.Review.Evidence.Candidate.Units[0].AirDate = date.AddDate(1, 0, 0)
	if c.material.Review.Evidence.Candidate.Units[0].UnitKey != "S01E01" || !c.material.Review.Evidence.Candidate.Units[0].AirDate.Equal(date) {
		t.Fatal("capture aliases exported values")
	}
}

func TestUntrackedReviewSealJSONPreservesIntegerPrecision(t *testing.T) {
	if reviewJSONSame([]byte(`{"id":9007199254740992}`), []byte(`{"id":9007199254740993}`)) {
		t.Fatal("distinct integer evidence collapsed")
	}
	if !reviewJSONSame([]byte(`{"id":42,"key":"tmdb.movie:42"}`), []byte(`{"key":"tmdb.movie:42","id":42}`)) {
		t.Fatal("object key order changed evidence")
	}
}

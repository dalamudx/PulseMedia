package personfollow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/google/uuid"
)

func followSettings() personfollow.Settings {
	root := uuid.New()
	return personfollow.Settings{PersonID: 525, Role: "director", Enabled: true, MinReleaseDate: "2026-10-01", IntervalMinutes: 1440, MovieRootID: &root, QualityPolicyID: "default", QualityPolicyRevision: 1}
}

func TestPersonFollowSettingsBoundaries(t *testing.T) {
	for _, role := range []string{"actor", "director"} {
		s := followSettings()
		s.Role = role
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(*personfollow.Settings){
		"person":            func(s *personfollow.Settings) { s.PersonID = 0 },
		"role":              func(s *personfollow.Settings) { s.Role = "producer" },
		"role_case":         func(s *personfollow.Settings) { s.Role = "Director" },
		"missing_date":      func(s *personfollow.Settings) { s.MinReleaseDate = "" },
		"invalid_date":      func(s *personfollow.Settings) { s.MinReleaseDate = "2026-02-30" },
		"date_time":         func(s *personfollow.Settings) { s.MinReleaseDate = "2026-10-01T00:00:00Z" },
		"ancient_date":      func(s *personfollow.Settings) { s.MinReleaseDate = "0000-01-01" },
		"no_roots":          func(s *personfollow.Settings) { s.MovieRootID = nil },
		"nil_root":          func(s *personfollow.Settings) { id := uuid.Nil; s.MovieRootID = &id },
		"duplicate_roots":   func(s *personfollow.Settings) { id := *s.MovieRootID; s.TVRootID = &id },
		"small_interval":    func(s *personfollow.Settings) { s.IntervalMinutes = 14 },
		"large_interval":    func(s *personfollow.Settings) { s.IntervalMinutes = 10081 },
		"missing_policy":    func(s *personfollow.Settings) { s.QualityPolicyID = "" },
		"policy_whitespace": func(s *personfollow.Settings) { s.QualityPolicyID = " default" },
		"policy_revision":   func(s *personfollow.Settings) { s.QualityPolicyRevision = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := followSettings()
			mutate(&s)
			if !errors.Is(s.Validate(), personfollow.ErrInvalidSettings) {
				t.Fatal("invalid follow accepted")
			}
		})
	}
	s := followSettings()
	s.Enabled = false
	s.MovieRootID = nil
	if err := s.Validate(); err != nil {
		t.Fatal("disabled draft requires an online root", err)
	}
	for _, interval := range []int{15, 10080} {
		s.IntervalMinutes = interval
		if err := s.Validate(); err != nil {
			t.Fatal("inclusive interval boundary rejected", err)
		}
	}
	s.MinReleaseDate = "2024-02-29"
	if err := s.Validate(); err != nil {
		t.Fatal("valid leap day rejected", err)
	}
}

func TestPersonFollowBindingSemantics(t *testing.T) {
	downloader := uuid.New()
	indexer := uuid.New()
	s := followSettings()
	for _, b := range []*personfollow.Bindings{nil, {IndexerIDs: []uuid.UUID{}}, {IndexerIDs: []uuid.UUID{indexer}, DownloaderID: &downloader}} {
		s.Bindings = b
		if err := s.Validate(); err != nil {
			t.Fatal("valid binding rejected", err)
		}
	}
	oversized := make([]uuid.UUID, 65)
	for i := range oversized {
		oversized[i] = uuid.New()
	}
	nilID := uuid.Nil
	for _, b := range []*personfollow.Bindings{{}, {IndexerIDs: []uuid.UUID{}, DownloaderID: &downloader}, {IndexerIDs: []uuid.UUID{indexer}}, {IndexerIDs: []uuid.UUID{indexer, indexer}, DownloaderID: &downloader}, {IndexerIDs: []uuid.UUID{uuid.Nil}, DownloaderID: &downloader}, {IndexerIDs: []uuid.UUID{indexer}, DownloaderID: &nilID}, {IndexerIDs: oversized, DownloaderID: &downloader}} {
		s.Bindings = b
		if !errors.Is(s.Validate(), personfollow.ErrInvalidSettings) {
			t.Fatal("unsafe binding accepted")
		}
	}
}

func TestPersonFollowNormalizationCopiesInputs(t *testing.T) {
	a := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	b := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	downloader := uuid.New()
	s := followSettings()
	originalRoot := *s.MovieRootID
	s.Bindings = &personfollow.Bindings{IndexerIDs: []uuid.UUID{b, a}, DownloaderID: &downloader}
	originalDownloader := downloader
	out := s.Normalized()
	if !slices.Equal(out.Bindings.IndexerIDs, []uuid.UUID{a, b}) || !slices.Equal(s.Bindings.IndexerIDs, []uuid.UUID{b, a}) {
		t.Fatal("normalization mutated the caller or failed to sort")
	}
	*out.MovieRootID = uuid.New()
	*out.Bindings.DownloaderID = uuid.New()
	out.Bindings.IndexerIDs[0] = uuid.New()
	if *s.MovieRootID != originalRoot || *s.Bindings.DownloaderID != originalDownloader || s.Bindings.IndexerIDs[0] != b {
		t.Fatal("caller shares mutable output storage")
	}
	s.Bindings = nil
	if s.Normalized().Bindings != nil {
		t.Fatal("omitted bindings lost preservation semantics")
	}
	s.Bindings = &personfollow.Bindings{IndexerIDs: []uuid.UUID{}}
	if s.Normalized().Bindings.IndexerIDs == nil {
		t.Fatal("explicit empty bindings became omission")
	}
}

package personfollow_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/google/uuid"
)

func personWorkSettings() domain.Settings {
	root := uuid.New()
	return domain.Settings{PersonID: 525, Role: "director", Enabled: true, MinReleaseDate: "2026-10-01", IntervalMinutes: 1440, MovieRootID: &root, QualityPolicyID: "default", QualityPolicyRevision: 1}
}
func TestPersonWorkEligibility(t *testing.T) {
	settings := personWorkSettings()
	for _, tc := range []struct{ name, date, kind, want string }{{"inclusive", "2026-10-01", "movie", "pending"}, {"earlier", "2026-09-30", "movie", "excluded"}, {"unknown", "", "movie", "review_required"}, {"future", "2099-01-01", "movie", "pending"}, {"unconfigured_type", "2026-10-01", "tv", "excluded"}} {
		t.Run(tc.name, func(t *testing.T) {
			work := domain.Work{TMDBID: 550, MediaType: tc.kind, Title: "A real work", ReleaseDate: tc.date}
			state, err := settings.Classify(work)
			if err != nil || state != tc.want {
				t.Fatal(state, err)
			}
		})
	}
	settings.Enabled = false
	if _, err := settings.Classify(domain.Work{TMDBID: 550, MediaType: "movie", Title: "A real work", ReleaseDate: "2026-10-01"}); !errors.Is(err, domain.ErrInvalidSettings) {
		t.Fatal("disabled settings authorized a candidate", err)
	}
}
func TestPersonWorkNormalizationAndIdentity(t *testing.T) {
	movie := domain.Work{TMDBID: 550, MediaType: "movie", Title: "Movie", ReleaseDate: "2026-10-01"}
	tv := domain.Work{TMDBID: 550, MediaType: "tv", Title: "Series", ReleaseDate: "2026-10-02"}
	unknown := domain.Work{TMDBID: 900, MediaType: "movie", Title: "Unknown date"}
	source := []domain.Work{unknown, movie, tv, movie}
	before := slices.Clone(source)
	works, err := domain.NormalizeWorks(source)
	if err != nil || len(works) != 3 || works[0] != tv || works[1] != movie || works[2] != unknown {
		t.Fatal("lost provider identity or stable date ordering", works, err)
	}
	if !reflect.DeepEqual(before, source) {
		t.Fatal("normalizing changed caller evidence")
	}
	empty, err := domain.NormalizeWorks([]domain.Work{})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("empty success became unavailable", err)
	}
}
func TestPersonWorkRejectsCorruptEvidence(t *testing.T) {
	valid := domain.Work{TMDBID: 550, MediaType: "movie", Title: "A real work", ReleaseDate: "2026-10-01"}
	for _, change := range []func(*domain.Work){func(w *domain.Work) { w.TMDBID = 0 }, func(w *domain.Work) { w.MediaType = "person" }, func(w *domain.Work) { w.Title = " " }, func(w *domain.Work) { w.Title = string([]byte{0xff}) }, func(w *domain.Work) { w.OriginalTitle = strings.Repeat("x", 1025) }, func(w *domain.Work) { w.ReleaseDate = "2026-02-30" }, func(w *domain.Work) { w.ReleaseDate = "1799-01-01" }} {
		bad := valid
		change(&bad)
		if works, err := domain.NormalizeWorks([]domain.Work{valid, bad}); !errors.Is(err, domain.ErrInvalidWork) || works != nil {
			t.Fatal("invalid evidence partially accepted", works, err)
		}
	}
	conflict := valid
	conflict.Title = "Conflicting identity evidence"
	for _, input := range [][]domain.Work{nil, {valid, conflict}, make([]domain.Work, domain.MaxWorks+1)} {
		if works, err := domain.NormalizeWorks(input); !errors.Is(err, domain.ErrInvalidWork) || works != nil {
			t.Fatal("ambiguous or oversized evidence accepted", err)
		}
	}
}
func TestPersonWorkCreationKeyBinding(t *testing.T) {
	config, root := uuid.New(), uuid.New()
	work := domain.Work{TMDBID: 550, MediaType: "movie", Title: "A real work", ReleaseDate: "2026-10-01"}
	key := domain.CreationKey(config, 1, root, work)
	if !strings.HasPrefix(key, "person-follow:") || len(key) > 160 || key != domain.CreationKey(config, 1, root, work) {
		t.Fatal("unstable command key")
	}
	changed := work
	changed.Title = "A translated title"
	if domain.CreationKey(config, 1, root, changed) != key {
		t.Fatal("display title changed execution identity")
	}
	changed = work
	changed.MediaType = "tv"
	for _, other := range []string{domain.CreationKey(uuid.New(), 1, root, work), domain.CreationKey(config, 2, root, work), domain.CreationKey(config, 1, uuid.New(), work), domain.CreationKey(config, 1, root, changed)} {
		if other == key {
			t.Fatal("key lost configuration, version, root, or media identity")
		}
	}
	if domain.CreationKey(uuid.Nil, 1, root, work) != "" || domain.CreationKey(config, 0, root, work) != "" || domain.CreationKey(config, 1, uuid.Nil, work) != "" {
		t.Fatal("invalid execution identity received a command key")
	}
}

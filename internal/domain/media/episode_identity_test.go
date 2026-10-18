package media

import "testing"

func TestTVIdentityConflictIsNegativeEvidenceOnly(t *testing.T) {
	for _, c := range []struct {
		raw      string
		expected int64
		conflict bool
	}{{"[Team] Example - 25 [1080p]", 100, false}, {"[Team] Example - 25 [1080p] {tmdb-100}", 100, false}, {"[Team] Example - 25 [1080p] {tmdb-999}", 100, true}, {"{tmdb-100} {tmdb-999}", 100, true}, {"{tmdb-999} {tmdb-100}", 100, true}, {"{tmdb-9223372036854775808}", 100, true}, {"{tmdb-100}", 0, true}} {
		if HasConflictingTVIdentity(c.raw, c.expected) != c.conflict {
			t.Fatal("scoped explicit identity conflict not respected")
		}
	}
}

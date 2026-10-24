package mediarequest_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"reflect"
	"strings"
	"testing"
)

func TestMediaRequestIntentCanonicalAndIsolated(t *testing.T) {
	in := mediarequest.Intent{MediaType: "tv", TMDBID: 1399, Seasons: []int{7, 2}}
	got, err := mediarequest.CanonicalIntent(in)
	if err != nil || !reflect.DeepEqual(got.Seasons, []int{2, 7}) || !reflect.DeepEqual(in.Seasons, []int{7, 2}) {
		t.Fatal("canonicalization changed caller material", err)
	}
	got.Seasons[0] = 99
	if in.Seasons[1] != 2 {
		t.Fatal("request scope shares caller memory")
	}
	got, err = mediarequest.CanonicalIntent(mediarequest.Intent{MediaType: "movie", TMDBID: 550})
	if err != nil || got.Seasons == nil {
		t.Fatal("movie all scope", err)
	}
	if _, err = mediarequest.CanonicalIntent(mediarequest.Intent{MediaType: "tv", TMDBID: 1399, Seasons: []int{0}, IncludeSpecials: true}); err != nil {
		t.Fatal("explicit specials", err)
	}
}
func TestMediaRequestIntentRejectsUntrustedShape(t *testing.T) {
	for name, in := range map[string]mediarequest.Intent{
		"missing_type": {TMDBID: 1}, "unknown_type": {MediaType: "anime", TMDBID: 1}, "case_alias": {MediaType: "TV", TMDBID: 1}, "zero_id": {MediaType: "movie"}, "negative_id": {MediaType: "movie", TMDBID: -1}, "unsafe_id": {MediaType: "movie", TMDBID: 9007199254740992},
		"movie_seasons": {MediaType: "movie", TMDBID: 1, Seasons: []int{1}}, "movie_specials": {MediaType: "movie", TMDBID: 1, IncludeSpecials: true}, "unknown_season": {MediaType: "tv", TMDBID: 1, Seasons: []int{-1}}, "large_season": {MediaType: "tv", TMDBID: 1, Seasons: []int{1000}}, "duplicate_season": {MediaType: "tv", TMDBID: 1, Seasons: []int{2, 1, 2}}, "implicit_specials": {MediaType: "tv", TMDBID: 1, Seasons: []int{0}}, "too_many": {MediaType: "tv", TMDBID: 1, Seasons: make([]int, 65)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mediarequest.CanonicalIntent(in); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}
func TestMediaRequestTerminalTransitions(t *testing.T) {
	for action, want := range map[string]string{"approve": mediarequest.Approved, "reject": mediarequest.Rejected, "withdraw": mediarequest.Withdrawn} {
		got, err := mediarequest.Transition(mediarequest.Pending, action)
		if err != nil || got != want {
			t.Fatal(action, err)
		}
		for _, state := range []string{mediarequest.Approved, mediarequest.Rejected, mediarequest.Withdrawn, "unknown", ""} {
			if _, err = mediarequest.Transition(state, action); err == nil {
				t.Fatal("terminal request changed", state, action)
			}
		}
	}
	if _, err := mediarequest.Transition(mediarequest.Pending, "pause"); err == nil {
		t.Fatal("subscription action became request authority")
	}
}
func TestMediaRequestReasonBoundaries(t *testing.T) {
	if got, err := mediarequest.CanonicalReason("  重复请求  "); err != nil || got != "重复请求" {
		t.Fatal(got, err)
	}
	for _, reason := range []string{strings.Repeat("界", 513), "line\nbreak", "nul\x00", string([]byte{255})} {
		if _, err := mediarequest.CanonicalReason(reason); err == nil {
			t.Fatal("unsafe reason accepted")
		}
	}
	if _, err := mediarequest.CanonicalReason(strings.Repeat("界", 512)); err != nil {
		t.Fatal(err)
	}
}

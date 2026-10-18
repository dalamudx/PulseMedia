package acquisition_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	acq "github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func selectionID(n byte) uuid.UUID { var id uuid.UUID; id[15] = n; return id }
func selectionFixture() (*ports.Actor, acq.TVSelectionPreviewInput) {
	stamp := strings.Repeat("a", 64)
	v := acq.TVSelectionVersion{ActorID: selectionID(1), SubscriptionID: selectionID(2), MediaID: selectionID(3), IntentVersion: 4, MediaVersion: 5, PolicyID: "tv", PolicyRevision: 6, ScopeStamp: stamp, ConfigurationStamp: stamp}
	v.SelectionPolicyStamp, _ = acq.TVSelectionPolicyStamp(quality.PackPolicy{Mode: quality.PackModePreferPack, MinMissingRatio: 0.5})
	actor := &ports.Actor{ID: v.ActorID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	in := acq.TVSelectionPreviewInput{Expected: v, Current: acq.TVSelectionCurrent{SourceTMDBID: 100, Version: v, AccountAuthorized: true, ObjectAuthorized: true, Active: true, RootReady: true, EndpointsReady: true}, Policy: ports.PolicyDocument{ID: "tv", Revision: 6, Rules: quality.PolicyRules{CutoffScore: 999, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 10, quality.Res2160p: 20}, SourceOrder: map[quality.Source]int{quality.SourceWEBDL: 0}, RejectTerms: []string{"bad"}}}, PackPolicy: quality.PackPolicy{Mode: quality.PackModePreferPack, MinMissingRatio: 0.5}, Season: ports.CompleteSeasonMetadata{Season: 1, SourceTMDBID: 100, MediaVersion: 5, ContentHash: stamp}}
	profile := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceWEBDL, SourceParserVersion: quality.SourceTokenParserVersion}
	rows := []media.NumberingRow{}
	for i, ep := range []int{1, 3, 7} {
		id := selectionID(byte(10 + i))
		provider := int64(200 + i)
		key := media.FormatEpisodeUnitKey(1, ep)
		in.Season.Units = append(in.Season.Units, ports.SeasonMetadataUnit{UnitID: id, UnitVersion: 2, TMDBEpisodeID: provider, EpisodeNumber: ep})
		in.Current.Canonical = append(in.Current.Canonical, ports.EpisodeSourceUnit{ID: id, Version: 2, EpisodeID: provider, Key: key, Season: 1, Episode: ep})
		in.Wanted = append(in.Wanted, acq.TVSelectionWanted{UnitID: id, UnitVersion: 2, Selected: true, Missing: true, Aired: true, Unprotected: true})
		row := media.NumberingRow{SourceNumber: ep, EpisodeID: provider, UnitID: id, UnitKey: key, UnitVersion: 2}
		rows = append(rows, row)
		cid := selectionID(byte(30 + i))
		in.Episodes = append(in.Episodes, acq.TVSelectionEpisode{Review: ports.EpisodeCandidateReviewReceipt{ID: selectionID(byte(50 + i)), SubscriptionID: v.SubscriptionID, CandidateID: cid, UnitID: id, InputStamp: stamp, Status: "reviewed_not_executable"}, Facts: acq.TVSelectionCandidateFacts{CandidateID: cid, Version: v, CurrentInputStamp: stamp, EvidenceCurrent: true, StandardNumbering: true, Name: "Fixture.S01E01.1080p.WEB-DL", Quality: profile, Rows: []media.NumberingRow{row}}})
	}
	cid := selectionID(20)
	in.Packs = []acq.TVSelectionPack{{Review: ports.TVCoverageCandidateReviewReceipt{ID: selectionID(40), SubscriptionID: v.SubscriptionID, CandidateID: cid, InputStamp: stamp, MaterialStamp: stamp, Status: "reviewed_not_executable"}, CurrentMaterialStamp: stamp, Facts: acq.TVSelectionCandidateFacts{CandidateID: cid, Version: v, CurrentInputStamp: stamp, EvidenceCurrent: true, StandardNumbering: true, Name: "Fixture.S01E01E03E07.1080p.WEB-DL", Quality: profile, Rows: rows}}}
	return actor, in
}

func resealSelectionPolicy(in *acq.TVSelectionPreviewInput) {
	stamp, err := acq.TVSelectionPolicyStamp(in.PackPolicy)
	if err != nil {
		panic(err)
	}
	in.Expected.SelectionPolicyStamp = stamp
	in.Current.Version.SelectionPolicyStamp = stamp
	for i := range in.Packs {
		in.Packs[i].Facts.Version.SelectionPolicyStamp = stamp
	}
	for i := range in.Episodes {
		in.Episodes[i].Facts.Version.SelectionPolicyStamp = stamp
	}
}

func TestTVSelectionPreviewDirectBehavior(t *testing.T) {
	a, in := selectionFixture()
	before, _ := json.Marshal(in)
	got, err := acq.PreviewTVSelection(context.Background(), a, in)
	if err != nil || got.Status != "preview_not_executable" || got.ExecutionAllowed || got.Decision.Kind != "pack" || len(got.Excluded) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// A valid zero score passes missing acquisition even below upgrade cutoff.
	in.Policy.Rules.ResolutionOrder[quality.Res1080p] = 0
	got, err = acq.PreviewTVSelection(context.Background(), a, in)
	if err != nil || got.Decision.Kind != "pack" || got.Decision.Score != 0 {
		t.Fatalf("zero score: %+v %v", got, err)
	}
	_, original := selectionFixture()
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("fixture unexpectedly changed")
	}
	in.PackPolicy.Mode = quality.PackModePreferEpisodes
	resealSelectionPolicy(&in)
	got, err = acq.PreviewTVSelection(context.Background(), a, in)
	if err != nil || got.Decision.Kind != "episodes" || len(got.Decision.CandidateIDs) != 3 || got.ExecutionAllowed {
		t.Fatalf("episode preference: %+v %v", got, err)
	}
	in.PackPolicy.Mode = quality.PackModeNone
	resealSelectionPolicy(&in)
	in.Packs[0].Facts.Quality.Resolution = quality.Res2160p
	got, err = acq.PreviewTVSelection(context.Background(), a, in)
	if err != nil || got.Decision.Kind != "pack" {
		t.Fatalf("none must not become prefer episodes: %+v %v", got, err)
	}
}

func TestTVSelectionPreviewPackGuards(t *testing.T) {
	tests := []struct {
		name, reason string
		mutate       func(*acq.TVSelectionPreviewInput)
	}{
		{"missing_coverage", "incomplete_pack_coverage", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Rows = in.Packs[0].Facts.Rows[:2] }},
		{"duplicate_coverage", "incomplete_pack_coverage", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Rows[2] = in.Packs[0].Facts.Rows[1] }},
		{"extra_coverage", "incomplete_pack_coverage", func(in *acq.TVSelectionPreviewInput) {
			in.Packs[0].Facts.Rows = append(in.Packs[0].Facts.Rows, in.Packs[0].Facts.Rows[0])
		}},
		{"unit_version", "incomplete_pack_coverage", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Rows[0].UnitVersion++ }},
		{"provider_identity", "incomplete_pack_coverage", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Rows[0].EpisodeID++ }},
		{"key", "incomplete_pack_coverage", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Rows[0].UnitKey = "S00E01" }},
		{"source_number", "incomplete_pack_coverage", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Rows[0].SourceNumber = 2 }},
		{"unknown", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.EvidenceCurrent = false }},
		{"absolute_numbering", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.StandardNumbering = false }},
		{"material_changed", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].CurrentMaterialStamp = strings.Repeat("b", 64) }},
		{"input_changed", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.CurrentInputStamp = strings.Repeat("b", 64) }},
		{"candidate_version", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Version.PolicyRevision++ }},
		{"missing_review", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Review.ID = uuid.Nil }},
		{"wrong_subscription", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Review.SubscriptionID = selectionID(99) }},
		{"wrong_candidate", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Review.CandidateID = selectionID(99) }},
		{"status", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Review.Status = "tracking" }},
		{"sensitive_name", "pack_review_changed", func(in *acq.TVSelectionPreviewInput) {
			in.Packs[0].Facts.Name = "https://example.test/?passkey=private"
		}},
		{"quality_unknown", "quality_rejected", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Quality.SourceParserVersion = "old" }},
		{"quality_reject", "quality_rejected", func(in *acq.TVSelectionPreviewInput) { in.Packs[0].Facts.Name = "bad.S01E01E03E07" }},
		{"unselected", "full_set_unavailable", func(in *acq.TVSelectionPreviewInput) { in.Wanted[0].Selected = false }},
		{"satisfied", "full_set_unavailable", func(in *acq.TVSelectionPreviewInput) { in.Wanted[0].Missing = false }},
		{"unaired", "full_set_unavailable", func(in *acq.TVSelectionPreviewInput) { in.Wanted[0].Aired = false }},
		{"protected", "full_set_unavailable", func(in *acq.TVSelectionPreviewInput) { in.Wanted[0].Unprotected = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, in := selectionFixture()
			tt.mutate(&in)
			in.PackPolicy.Mode = quality.PackModeRequirePack
			resealSelectionPolicy(&in)
			got, err := acq.PreviewTVSelection(context.Background(), a, in)
			if err != nil || got.Decision.Kind != "hold" || got.Decision.Reason != "required_pack_unavailable" || len(got.Excluded) < 1 || got.Excluded[0].Reason != tt.reason || got.ExecutionAllowed {
				t.Fatalf("got %+v err=%v", got, err)
			}
			in.PackPolicy.Mode = quality.PackModePreferPack
			resealSelectionPolicy(&in)
			got, err = acq.PreviewTVSelection(context.Background(), a, in)
			if err != nil || got.Decision.Kind == "pack" || got.ExecutionAllowed {
				t.Fatalf("preference bypass: %+v %v", got, err)
			}
		})
	}
}

func TestTVSelectionPreviewSingleGuards(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*acq.TVSelectionPreviewInput)
	}{
		{"no_review", func(in *acq.TVSelectionPreviewInput) { in.Episodes[0].Review.ID = uuid.Nil }},
		{"wrong_unit", func(in *acq.TVSelectionPreviewInput) { in.Episodes[0].Review.UnitID = selectionID(99) }},
		{"changed_input", func(in *acq.TVSelectionPreviewInput) {
			in.Episodes[0].Facts.CurrentInputStamp = strings.Repeat("b", 64)
		}},
		{"changed_policy", func(in *acq.TVSelectionPreviewInput) { in.Episodes[0].Facts.Version.PolicyRevision++ }},
		{"unknown_source", func(in *acq.TVSelectionPreviewInput) { in.Episodes[0].Facts.EvidenceCurrent = false }},
		{"unknown_rows", func(in *acq.TVSelectionPreviewInput) { in.Episodes[0].Facts.Rows = nil }},
		{"multi_episode", func(in *acq.TVSelectionPreviewInput) {
			in.Episodes[0].Facts.Rows = append(in.Episodes[0].Facts.Rows, in.Episodes[1].Facts.Rows[0])
		}},
		{"wrong_version", func(in *acq.TVSelectionPreviewInput) { in.Episodes[0].Facts.Rows[0].UnitVersion++ }},
		{"reject_quality", func(in *acq.TVSelectionPreviewInput) { in.Episodes[0].Facts.Name = "bad" }},
		{"protected", func(in *acq.TVSelectionPreviewInput) { in.Wanted[0].Unprotected = false }},
		{"unaired", func(in *acq.TVSelectionPreviewInput) { in.Wanted[0].Aired = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, in := selectionFixture()
			in.Packs = nil
			tt.mutate(&in)
			in.PackPolicy.Mode = quality.PackModePreferEpisodes
			resealSelectionPolicy(&in)
			got, err := acq.PreviewTVSelection(context.Background(), a, in)
			if err != nil || got.Decision.Kind != "hold" || len(got.Excluded) != 1 || got.ExecutionAllowed {
				t.Fatalf("partial singles allowed: %+v %v", got, err)
			}
		})
	}
	t.Run("missing_only", func(t *testing.T) {
		a, in := selectionFixture()
		in.Wanted[0].Missing = false
		in.Wanted[1].Selected = false
		got, err := acq.PreviewTVSelection(context.Background(), a, in)
		if err != nil || got.Decision.Kind != "episodes" || !reflect.DeepEqual(got.Decision.CandidateIDs, []string{in.Episodes[2].Facts.CandidateID.String()}) {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("no_missing", func(t *testing.T) {
		a, in := selectionFixture()
		for i := range in.Wanted {
			in.Wanted[i].Missing = false
		}
		in.PackPolicy.Mode = quality.PackModeRequirePack
		resealSelectionPolicy(&in)
		got, err := acq.PreviewTVSelection(context.Background(), a, in)
		if err != nil || got.Decision.Kind != "hold" || got.Decision.Reason != "no_missing_targets" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestTVSelectionPreviewVersionAndSeasonGuards(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*acq.TVSelectionPreviewInput)
	}{
		{"selection_policy_changed", func(in *acq.TVSelectionPreviewInput) { in.PackPolicy.MinMissingRatio = 0.7 }},
		{"selection_mode_changed", func(in *acq.TVSelectionPreviewInput) { in.PackPolicy.Mode = quality.PackModeRequirePack }},
		{"expected_intent", func(in *acq.TVSelectionPreviewInput) { in.Expected.IntentVersion++ }},
		{"expected_media", func(in *acq.TVSelectionPreviewInput) { in.Expected.MediaVersion++ }},
		{"expected_scope", func(in *acq.TVSelectionPreviewInput) { in.Expected.ScopeStamp = strings.Repeat("b", 64) }},
		{"expected_config", func(in *acq.TVSelectionPreviewInput) { in.Expected.ConfigurationStamp = strings.Repeat("b", 64) }},
		{"published_revision", func(in *acq.TVSelectionPreviewInput) { in.Policy.Revision++ }},
		{"policy_identity", func(in *acq.TVSelectionPreviewInput) { in.Policy.ID = "other" }},
		{"parent_provider_identity", func(in *acq.TVSelectionPreviewInput) { in.Current.SourceTMDBID++ }},
		{"season_version", func(in *acq.TVSelectionPreviewInput) { in.Season.MediaVersion++ }},
		{"special", func(in *acq.TVSelectionPreviewInput) { in.Season.Season = 0 }},
		{"unknown_metadata", func(in *acq.TVSelectionPreviewInput) { in.Season.ContentHash = "" }},
		{"duplicate_unit", func(in *acq.TVSelectionPreviewInput) { in.Season.Units[1] = in.Season.Units[0] }},
		{"duplicate_provider", func(in *acq.TVSelectionPreviewInput) {
			in.Season.Units[1].TMDBEpisodeID = in.Season.Units[0].TMDBEpisodeID
		}},
		{"duplicate_episode", func(in *acq.TVSelectionPreviewInput) {
			in.Season.Units[1].EpisodeNumber = in.Season.Units[0].EpisodeNumber
		}},
		{"retired", func(in *acq.TVSelectionPreviewInput) { in.Current.Canonical[0].Retired = true }},
		{"stale_unit", func(in *acq.TVSelectionPreviewInput) { in.Current.Canonical[0].Version++ }},
		{"bad_key", func(in *acq.TVSelectionPreviewInput) { in.Current.Canonical[0].Key = "S00E01" }},
		{"extra_canonical", func(in *acq.TVSelectionPreviewInput) {
			in.Current.Canonical = append(in.Current.Canonical, in.Current.Canonical[0])
		}},
		{"missing_canonical", func(in *acq.TVSelectionPreviewInput) { in.Current.Canonical = in.Current.Canonical[:2] }},
		{"duplicate_canonical", func(in *acq.TVSelectionPreviewInput) { in.Current.Canonical[1] = in.Current.Canonical[0] }},
		{"missing_wanted", func(in *acq.TVSelectionPreviewInput) { in.Wanted = in.Wanted[:2] }},
		{"duplicate_wanted", func(in *acq.TVSelectionPreviewInput) { in.Wanted[1] = in.Wanted[0] }},
		{"stale_wanted", func(in *acq.TVSelectionPreviewInput) { in.Wanted[0].UnitVersion++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, in := selectionFixture()
			tt.mutate(&in)
			got, err := acq.PreviewTVSelection(context.Background(), a, in)
			if got != nil || !errors.Is(err, acq.ErrTVSelectionChanged) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestTVSelectionPreviewAuthorizationAndLiveGuards(t *testing.T) {
	for _, field := range []string{"account", "object", "actor", "role", "scopes"} {
		t.Run(field, func(t *testing.T) {
			a, in := selectionFixture()
			switch field {
			case "account":
				in.Current.AccountAuthorized = false
			case "object":
				in.Current.ObjectAuthorized = false
			case "actor":
				in.Current.Version.ActorID = selectionID(99)
			case "role":
				a.Role = "viewer"
			case "scopes":
				a.Scopes = []string{}
			}
			got, err := acq.PreviewTVSelection(context.Background(), a, in)
			if got != nil || !errors.Is(err, ports.ErrForbidden) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	for _, field := range []string{"active", "root", "endpoints"} {
		t.Run(field, func(t *testing.T) {
			a, in := selectionFixture()
			switch field {
			case "active":
				in.Current.Active = false
			case "root":
				in.Current.RootReady = false
			case "endpoints":
				in.Current.EndpointsReady = false
			}
			got, err := acq.PreviewTVSelection(context.Background(), a, in)
			if err != nil || got.Decision.Kind != "hold" || got.Decision.Reason != "current_target_unavailable" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	t.Run("nil_actor", func(t *testing.T) {
		_, in := selectionFixture()
		_, err := acq.PreviewTVSelection(context.Background(), nil, in)
		if !errors.Is(err, ports.ErrUnauthorized) {
			t.Fatal(err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		a, in := selectionFixture()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := acq.PreviewTVSelection(ctx, a, in)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("invalid_policy", func(t *testing.T) {
		a, in := selectionFixture()
		in.Policy.Rules.SourceOrder = nil
		_, err := acq.PreviewTVSelection(context.Background(), a, in)
		if !errors.Is(err, ports.ErrInvalidPolicy) {
			t.Fatal(err)
		}
	})
	t.Run("implicit_pack_mode", func(t *testing.T) {
		a, in := selectionFixture()
		in.PackPolicy.Mode = ""
		_, err := acq.PreviewTVSelection(context.Background(), a, in)
		if !errors.Is(err, quality.ErrInvalidTVSelection) {
			t.Fatal(err)
		}
	})
	t.Run("duplicate_candidate", func(t *testing.T) {
		a, in := selectionFixture()
		in.Episodes[0].Facts.CandidateID = in.Packs[0].Facts.CandidateID
		_, err := acq.PreviewTVSelection(context.Background(), a, in)
		if !errors.Is(err, quality.ErrInvalidTVSelection) {
			t.Fatal(err)
		}
	})
}

func TestTVSelectionPreviewOrderAndNoMutation(t *testing.T) {
	a, in := selectionFixture()
	in.PackPolicy.Mode = quality.PackModePreferEpisodes
	resealSelectionPolicy(&in)
	before, _ := json.Marshal(in)
	first, err := acq.PreviewTVSelection(context.Background(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("preview mutated caller evidence")
	}
	for i := 0; i < 3; i++ {
		in.Wanted = append(in.Wanted[1:], in.Wanted[0])
		in.Current.Canonical = append(in.Current.Canonical[1:], in.Current.Canonical[0])
		in.Season.Units = append(in.Season.Units[1:], in.Season.Units[0])
		in.Episodes = append(in.Episodes[1:], in.Episodes[0])
		in.Packs[0].Facts.Rows = append(in.Packs[0].Facts.Rows[1:], in.Packs[0].Facts.Rows[0])
		got, err := acq.PreviewTVSelection(context.Background(), a, in)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatalf("order changed result %+v %v", got, err)
		}
	}
}

func TestTVSelectionPreviewConcurrentReaders(t *testing.T) {
	a, in := selectionFixture()
	want, err := acq.PreviewTVSelection(context.Background(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		value *acq.TVSelectionPreview
		err   error
	}
	results := make(chan result, 8)
	for i := 0; i < 8; i++ {
		go func() { got, err := acq.PreviewTVSelection(context.Background(), a, in); results <- result{got, err} }()
	}
	for i := 0; i < 8; i++ {
		got := <-results
		if got.err != nil || !reflect.DeepEqual(want, got.value) {
			t.Fatalf("concurrent read: %+v %v", got.value, got.err)
		}
	}
}


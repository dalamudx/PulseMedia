package torrent

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type wrappedMovieUpgradeProof struct {
	ports.MovieUpgradeMetainfoProof
}

func TestMovieUpgradeNativeVerifierRejectsForgedProofsAndStaleTargets(t *testing.T) {
	p, err := InspectMovieUpgradeMetainfo([]byte(movieUpgradeRaw), proofMovieTarget())
	if err != nil {
		t.Fatal(err)
	}
	verifier := ports.MovieUpgradeMetainfoVerifier(Inspector{})
	if err := verifier.VerifyMovieUpgradeMetainfo(t.Context(), p, proofMovieTarget()); err != nil {
		t.Fatal(err)
	}
	var nilProof *MovieUpgradeMetainfoProof
	tampered := *p
	tampered.raw = bytes.Clone(p.raw)
	tampered.raw[0] = 'x'
	for name, claim := range map[string]ports.MovieUpgradeMetainfoProof{
		"nil_interface":                  nil,
		"typed_nil":                      nilProof,
		"zero_value":                     &MovieUpgradeMetainfoProof{},
		"custom_wrapper_reporting_valid": &wrappedMovieUpgradeProof{p},
		"mutated_raw":                    &tampered,
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifier.VerifyMovieUpgradeMetainfo(t.Context(), claim, proofMovieTarget()); !errors.Is(err, ports.ErrMovieUpgradeMetainfoReview) {
				t.Fatalf("forged proof accepted: %v", err)
			}
		})
	}
	for name, change := range map[string]func(*media.MovieUpgradeTarget){
		"media": func(target *media.MovieUpgradeTarget) {
			target.MediaID = uuid.MustParse("33333333-3333-4333-8333-333333333333")
		},
		"unit": func(target *media.MovieUpgradeTarget) {
			target.UnitID = uuid.MustParse("44444444-4444-4444-8444-444444444444")
		},
		"media_version":  func(target *media.MovieUpgradeTarget) { target.MediaVersion++ },
		"unit_version":   func(target *media.MovieUpgradeTarget) { target.UnitVersion++ },
		"type":           func(target *media.MovieUpgradeTarget) { target.MediaType = media.TypeTV },
		"unit_key":       func(target *media.MovieUpgradeTarget) { target.UnitKey = "S01E01" },
		"title":          func(target *media.MovieUpgradeTarget) { target.OriginalTitle = "Other" },
		"year":           func(target *media.MovieUpgradeTarget) { target.Year++ },
		"parser_version": func(target *media.MovieUpgradeTarget) { target.ParserVersion = "other" },
		"identity":       func(target *media.MovieUpgradeTarget) { target.Identities[0].ExternalID = "551" },
		"alias":          func(target *media.MovieUpgradeTarget) { target.Aliases[0] = "Other" },
	} {
		t.Run(name, func(t *testing.T) {
			target := proofMovieTarget()
			change(&target)
			if err := verifier.VerifyMovieUpgradeMetainfo(t.Context(), p, target); !errors.Is(err, ports.ErrMovieUpgradeMetainfoReview) {
				t.Fatalf("changed target accepted: %v", err)
			}
		})
	}
	t.Run("caller_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := verifier.VerifyMovieUpgradeMetainfo(ctx, p, proofMovieTarget()); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation changed: %v", err)
		}
	})
}

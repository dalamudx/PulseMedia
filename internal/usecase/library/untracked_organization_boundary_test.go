package library

import (
	"context"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestUntrackedOrganizationMissingDependenciesAndCanceledReads(t *testing.T) {
	for _, mode := range []string{"nil service", "nil repository", "nil filesystem", "canceled", "nil receipt", "wrong original confirmation", "zero read operation"} {
		t.Run(mode, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			ctx := t.Context()
			want := ports.ErrUnavailable
			switch mode {
			case "nil service":
				f.service = nil
			case "nil repository":
				f.service.repo = nil
			case "nil filesystem":
				f.service.files = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "nil receipt":
				f.repo.receipt = nil
			case "wrong original confirmation":
				f.repo.receipt.Operation.ConfirmationID = uuid.New()
				f.repo.preparation = &ports.UntrackedOrganizationPreparation{Receipt: f.repo.receipt}
			case "zero read operation":
				_, err := f.service.Read(ctx, f.base.actor, f.repo.scopeID, f.repo.fileID, uuid.Nil)
				if err != ports.ErrRejected || f.repo.reads != 0 {
					t.Fatal("zero operation reached the repository", err)
				}
				return
			}
			if mode == "nil receipt" {
				if out, err := f.receipt(ctx); out != nil || err != want {
					t.Fatal("nil receipt accepted", err)
				}
			} else {
				if out, err := f.organize(ctx); out != nil || err != want {
					t.Fatal("dependency or cancellation did not fail closed", err)
				}
			}
			if mode == "nil service" || mode == "nil repository" || mode == "canceled" {
				if out, err := f.receipt(ctx); out != nil || err != want {
					t.Fatal("receipt bypassed dependency or cancellation", err)
				}
				if out, err := f.read(ctx); out != nil || err != want {
					t.Fatal("read bypassed dependency or cancellation", err)
				}
				if f.repo.prepares+f.repo.receipts+f.repo.reads != 0 {
					t.Fatal("invalid call reached repository")
				}
			}
			if mode == "nil filesystem" && f.repo.prepares != 1 {
				t.Fatal("filesystem availability was checked before preparation")
			}
			if f.repo.commits != 0 || f.base.files.opens.Load() != 0 {
				t.Fatal("invalid call performed I/O or commit")
			}
		})
	}
}

func TestUntrackedOrganizationTypedExplicitAndSparseCoverage(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run("explicit/"+kind, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			if kind == "tv" {
				untrackedOrganizationTV(f, false)
			}
			m := &f.repo.capture.material.Confirmation.Material
			m.Review.Request.Input.TMDBID = 42
			m.Review.Evidence.Selection = "explicit"
			f.resealMaterial()
			if out, err := f.organize(t.Context()); err != nil || out == nil || f.repo.commits != 1 {
				t.Fatal("typed explicit confirmation rejected", err)
			}
			untrackedReviewAssertClosed(t, f.base.files)
		})
	}
	for _, what := range []string{"duplicate episode ID", "unsorted units", "unit key", "mixed season", "nonmidnight air date", "wrong explicit identity"} {
		t.Run(what, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			untrackedOrganizationTV(f, false)
			m := &f.repo.capture.material.Confirmation.Material
			units := m.Review.Evidence.Candidate.Units
			switch what {
			case "duplicate episode ID":
				units[1].TMDBEpisodeID = units[0].TMDBEpisodeID
			case "unsorted units":
				units[0], units[1] = units[1], units[0]
			case "unit key":
				units[1].UnitKey = "S01E02"
			case "mixed season":
				units[1].Season = 2
			case "nonmidnight air date":
				invalidDate := units[1].AirDate.Add(1)
				units[1].AirDate = &invalidDate
			case "wrong explicit identity":
				m.Review.Request.Input.TMDBID = 99
				m.Review.Evidence.Selection = "explicit"
			}
			if out, err := f.organize(t.Context()); out != nil || err != ports.ErrUnavailable || f.repo.commits != 0 || f.base.files.opens.Load() != 0 {
				t.Fatal("malformed typed coverage reached I/O", err)
			}
		})
	}
}

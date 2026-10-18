package search_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

func TestMovieRoundSelectorCannotForgeAdmission(t *testing.T) {
	for _, mode := range []string{"missing_selector", "unknown_id", "appended_candidate", "blocked_material_mutation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := runtimeFixture(t)
			round := f.collect(t)
			job := f.finalJob(t, round)
			permit := authority(job)
			rows, err := f.q.ListRoundQueryCandidates(ctx, round.ID)
			if err != nil {
				t.Fatal(err)
			}
			var winner uuid.UUID
			for _, row := range rows {
				if row.Guid == "correct" {
					winner = row.ID
				}
			}
			if winner == uuid.Nil {
				t.Fatal("fixture missing qualified candidate")
			}
			if mode == "blocked_material_mutation" {
				if err = f.q.SetSubscriptionReleaseBlock(ctx, db.SetSubscriptionReleaseBlockParams{SubscriptionID: f.sub, CandidateID: winner, Blocked: true}); err != nil {
					t.Fatal(err)
				}
			}
			type persistedState struct {
				round, job                                string
				reservations, submissions, audits, events int
			}
			readState := func() persistedState {
				t.Helper()
				var state persistedState
				if e := f.pool.QueryRow(ctx, `SELECT
					(SELECT row_to_json(r)::text FROM search_round r WHERE id=$1),
					(SELECT row_to_json(j)::text FROM job j WHERE id=$2),
					(SELECT count(*) FROM unit_reservation),
					(SELECT count(*) FROM job WHERE kind='download_submission'),
					(SELECT count(*) FROM audit_log WHERE action='acquisition.create'),
					(SELECT count(*) FROM outbox_event WHERE event_type='acquisition.initiated')`, round.ID, job.ID).Scan(
					&state.round, &state.job, &state.reservations, &state.submissions, &state.audits, &state.events,
				); e != nil {
					t.Fatal(e)
				}
				return state
			}
			before := readState()
			calls := 0
			var selector ports.MovieRoundSelector
			if mode != "missing_selector" {
				selector = func(in ports.MovieRoundSelectionInput) ports.MovieRoundSelection {
					calls++
					selected := uuid.New()
					switch mode {
					case "appended_candidate":
						in.Candidates = append(in.Candidates, ports.MovieRoundCandidate{ID: selected, Admissible: true})
					case "blocked_material_mutation":
						selected = winner
						seen := false
						for i := range in.Candidates {
							if in.Candidates[i].ID == winner {
								seen = true
								if in.Candidates[i].Admissible {
									t.Error("blocked candidate reached selector as admissible")
								}
								in.Candidates[i].Admissible = true
							}
						}
						if !seen {
							t.Error("fixture missing blocked material")
						}
					}
					return ports.MovieRoundSelection{SelectedCandidateID: &selected, BestScore: 999999}
				}
			}
			gen := idgen.New()
			clk := clock.New()
			store := postgres.NewAcquisitionStore(f.pool, gen, clk, selector)
			coord := usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStore(f.pool, gen), clk, acquisition.NewService(store))
			decision, err := coord.FinalizeLeasedRound(ctx, round.ID, permit)
			if decision != nil || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatalf("forged selection accepted: decision=%+v err=%v", decision, err)
			}
			wantCalls := 1
			if mode == "missing_selector" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("selector calls=%d want=%d", calls, wantCalls)
			}
			if after := readState(); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected selector changed round/job or leaked side effects: before=%+v after=%+v", before, after)
			}
			if mode == "blocked_material_mutation" {
				if err = f.q.SetSubscriptionReleaseBlock(ctx, db.SetSubscriptionReleaseBlockParams{SubscriptionID: f.sub, CandidateID: winner, Blocked: false}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.coord.FinalizeLeasedRound(ctx, round.ID, permit); err != nil {
				t.Fatal("original round/token failed to recover with real selector", err)
			}
			persisted, err := f.q.GetSearchRoundByID(ctx, round.ID)
			if err != nil || persisted.State != "completed" || !persisted.SelectedCandidateID.Valid || persisted.SelectedCandidateID.Bytes != winner {
				t.Fatalf("recovered wrong round/selection: round=%+v err=%v", persisted, err)
			}
			after := readState()
			if after.reservations != 1 || after.submissions != 1 || after.audits != 1 || after.events != 1 {
				t.Fatalf("recovery did not produce one atomic acquisition: %+v", after)
			}
		})
	}
}

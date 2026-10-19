package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

type automaticTVUnavailableDecoder struct{}

func (automaticTVUnavailableDecoder) DecryptSecret([]byte) ([]byte, error) {
	return nil, errors.New("private decoder detail must stay private")
}
func TestAutomaticTVBeginAndLoaderFaultRollbackPG(t *testing.T) {
	for _, stage := range []string{"begin", "loader"} {
		t.Run(stage, func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			w := f.load(t, f.start(t))
			s := automaticTVSpec(t, w, 0)
			if stage == "loader" {
				broken := postgres.NewSearchExecutionStore(f.pool, automaticTVUnavailableDecoder{})
				before := automaticTVState(t, f)
				_, p, e := broken.AdmitQuery(t.Context(), s)
				if p != nil {
					p.Close()
				}
				if !errors.Is(e, ports.ErrUnavailable) || before != automaticTVState(t, f) {
					t.Fatal("loader fault not closed", e)
				}
				return
			}
			_, p, e := f.execution.AdmitQuery(t.Context(), s)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			execIntent(t, f.pool, `CREATE FUNCTION tv_begin_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'begin association fault'; END $$; CREATE TRIGGER tv_begin_fault BEFORE INSERT ON automatic_tv_query FOR EACH ROW EXECUTE FUNCTION tv_begin_fault()`)
			before := automaticTVState(t, f)
			if _, e = p.BeginQuery(t.Context()); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal(e)
			}
			if before != automaticTVState(t, f) {
				t.Fatal("Begin leaked running query/source")
			}
		})
	}
}
func TestAutomaticTVWindowAndMovieRoutingPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	for _, window := range []time.Duration{-time.Second, time.Nanosecond, 2*time.Minute + time.Nanosecond} {
		if _, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted, Window: window}); !errors.Is(e, ports.ErrRejected) {
			t.Fatal("invalid window", window, e)
		}
	}
	receipt := f.create(t, creationInput(55104, "movie"), creationMovie(55104), "movie-routing")
	if _, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: receipt.Wanted[0].ID}); !errors.Is(e, ports.ErrAutomaticTVNotApplicable) {
		t.Fatal("actual movie not routed", e)
	}
	execIntent(t, f.pool, `UPDATE media_unit SET air_date=NULL WHERE id=$1`, f.unit)
	if _, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: f.wanted}); e == nil || errors.Is(e, ports.ErrAutomaticTVNotApplicable) {
		t.Fatal("TV hold would fall through to movie", e)
	}
}
func TestAutomaticTVBeginStaleTokenAndRecoveryIdentityPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	w := f.load(t, f.start(t))
	s := automaticTVSpec(t, w, 0)
	_, p, e := f.execution.AdmitQuery(t.Context(), s)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	execIntent(t, f.pool, `UPDATE job SET lease_token = (lease_token + 1) WHERE id=$1`, w.Task.Lease.ID)
	before := automaticTVState(t, f)
	if _, e = p.BeginQuery(t.Context()); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
		t.Fatal("stale Begin", e)
	}
	if before != automaticTVState(t, f) {
		t.Fatal("stale Begin wrote")
	}
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second',payload='{}' WHERE id=$1`, w.Task.Lease.ID)
	before = automaticTVState(t, f)
	if _, e = f.repo.RecoverAutomaticTVSources(context.Background(), 10); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
		t.Fatal("foreign recovery job accepted", e)
	}
	if before != automaticTVState(t, f) {
		t.Fatal("foreign recovery job mutated")
	}
}

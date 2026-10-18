package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type r3Proof struct {
	outcome string
	at      time.Time
}
type r3Verifier struct{ proof *r3Proof }

func (v *r3Verifier) VerifyMovieUpgradeObservation(ctx context.Context, p ports.MovieUpgradeObservationProof, b ports.MovieUpgradeObservationBinding) (ports.MovieUpgradeVerifiedObservation, error) {
	if e := ctx.Err(); e != nil {
		return ports.MovieUpgradeVerifiedObservation{}, e
	}
	if p != v.proof || v.proof == nil {
		return ports.MovieUpgradeVerifiedObservation{}, errors.New("not trusted query evidence")
	}
	return ports.MovieUpgradeVerifiedObservation{Binding: b, Outcome: v.proof.outcome, ReturnedAt: v.proof.at}, nil
}
func r3Native(t *testing.T, ack string, send bool) *nativeUpgradeFixture {
	t.Helper()
	n := nativeUpgrade(t, ack, false)
	ctx := context.Background()
	if _, e := postgres.Migrate(ctx, n.dsn, observation69MigrationDir(t)); e != nil {
		t.Fatal(e)
	}
	if send {
		out, e := n.repo.SendMovieUpgrade(ctx, n.actor, n.operation, uuid.New(), n.proof, "r3-source")
		if e != nil || out == nil {
			t.Fatalf("native source: %v", e)
		}
		want := "unknown"
		if ack == "Ok." {
			want = "confirmed"
		}
		if out.Status != want {
			t.Fatal("unexpected source outcome", out.Status)
		}
	}
	return n
}
func r3Open(t *testing.T, n *nativeUpgradeFixture, v *r3Verifier) ports.MovieUpgradeObservationSession {
	t.Helper()
	repo, e := postgres.NewMovieUpgradeObservationRepository(n.preparation, v)
	if e != nil {
		t.Fatal(e)
	}
	s, e := repo.OpenMovieUpgradeObservationSession(context.Background(), n.operation)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	return s
}
func r3Protected(t *testing.T, n *nativeUpgradeFixture) string {
	t.Helper()
	parts := []string{}
	for _, table := range []string{"library_file", "library_file_unit", "media_unit", "unit_fact", "acquisition_reservation", "unit_reservation", "download_requirement", "download_task", "movie_upgrade_authority", "movie_upgrade_preparation", "movie_upgrade_send_authorization", "movie_upgrade_send_dispatch", "movie_upgrade_send_result"} {
		parts = append(parts, "(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM "+table+" x)")
	}
	var digest string
	if e := n.p.QueryRow(context.Background(), "SELECT encode(sha256(convert_to(jsonb_build_array("+strings.Join(parts, ",")+")::text,'UTF8')),'hex')").Scan(&digest); e != nil {
		t.Fatal(e)
	}
	return digest
}
func r3Count(t *testing.T, n *nativeUpgradeFixture, table string) int {
	t.Helper()
	var count int
	if e := n.p.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); e != nil {
		t.Fatal(e)
	}
	return count
}
func r3Now(t *testing.T, n *nativeUpgradeFixture) time.Time {
	t.Helper()
	var at time.Time
	if e := n.p.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&at); e != nil {
		t.Fatal(e)
	}
	return at
}

func TestMovieUpgradeR3ObservationOutcomes(t *testing.T) {
	for _, outcome := range []string{"exact_match", "absent", "ambiguous", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			n := r3Native(t, "uncertain", true)
			before := r3Protected(t, n)
			v := &r3Verifier{}
			s := r3Open(t, n, v)
			admit, e := s.AdmitMovieUpgradeObservation(context.Background(), n.actor, uuid.New(), "observe")
			if e != nil {
				t.Fatal(e)
			}
			if admit.ExecutionAllowed || admit.TaskID == uuid.Nil || r3Count(t, n, "movie_upgrade_observation_permit") != 1 {
				t.Fatal("bad admission")
			}
			v.proof = &r3Proof{outcome: outcome, at: r3Now(t, n)}
			out, e := s.RecordMovieUpgradeObservation(context.Background(), n.actor, "observed", v.proof)
			if e != nil {
				t.Fatal(e)
			}
			if out.Outcome != outcome || out.Disposition != "manual_review" || out.ExecutionAllowed {
				t.Fatal("invalid disposition")
			}
			replay, e := s.RecordMovieUpgradeObservation(context.Background(), n.actor, "observed", v.proof)
			if e != nil {
				t.Fatal(e)
			}
			a, _ := json.Marshal(out)
			b, _ := json.Marshal(replay)
			if string(a) != string(b) {
				t.Fatal("receipt changed")
			}
			if r3Count(t, n, "movie_upgrade_observation_result") != 1 || r3Protected(t, n) != before || n.adds.Load() != 1 {
				t.Fatal("observation changed original graph or sent Add")
			}
		})
	}
}
func TestMovieUpgradeR3RequiresActualUnknownReturn(t *testing.T) {
	for _, ack := range []string{"missing", "Ok."} {
		t.Run(ack, func(t *testing.T) {
			n := r3Native(t, ack, ack != "missing")
			before := r3Protected(t, n)
			s := r3Open(t, n, &r3Verifier{})
			_, e := s.AdmitMovieUpgradeObservation(context.Background(), n.actor, uuid.New(), "blocked")
			if !errors.Is(e, ports.ErrMovieUpgradeObservationReview) {
				t.Fatal("missing/confirmed send must require review", e)
			}
			if r3Count(t, n, "movie_upgrade_observation") != 0 || r3Count(t, n, "movie_upgrade_observation_permit") != 0 || r3Protected(t, n) != before {
				t.Fatal("rejected admission changed graph")
			}
		})
	}
}
func TestMovieUpgradeR3PermitSurvivesClose(t *testing.T) {
	n := r3Native(t, "uncertain", true)
	s := r3Open(t, n, &r3Verifier{})
	if _, e := s.AdmitMovieUpgradeObservation(context.Background(), n.actor, uuid.New(), "admit"); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	other := r3Open(t, n, &r3Verifier{})
	if _, e := other.AdmitMovieUpgradeObservation(context.Background(), n.actor, uuid.New(), "takeover"); !errors.Is(e, ports.ErrMovieUpgradeObservationReview) {
		t.Fatal("permit must block takeover", e)
	}
	if r3Count(t, n, "movie_upgrade_observation_permit") != 1 || n.adds.Load() != 1 {
		t.Fatal("permit removed or Add repeated")
	}
}
func TestMovieUpgradeR3RejectsCallerProofAndToken(t *testing.T) {
	n := r3Native(t, "uncertain", true)
	v := &r3Verifier{}
	s := r3Open(t, n, v)
	if _, e := s.AdmitMovieUpgradeObservation(context.Background(), n.actor, uuid.New(), "admit"); e != nil {
		t.Fatal(e)
	}
	before := r3Protected(t, n)
	if _, e := s.RecordMovieUpgradeObservation(context.Background(), n.actor, "forged", struct{ Returned bool }{true}); e == nil {
		t.Fatal("caller proof accepted")
	}
	token := *n.actor
	token.Scopes = []string{"read"}
	if _, e := s.RecordMovieUpgradeObservation(context.Background(), &token, "token", &r3Proof{}); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("token accepted", e)
	}
	if r3Count(t, n, "movie_upgrade_observation_result") != 0 || r3Protected(t, n) != before {
		t.Fatal("rejection changed graph")
	}
}
func TestMovieUpgradeR3TailRollback(t *testing.T) {
	for _, phase := range []string{"admit", "result"} {
		t.Run(phase, func(t *testing.T) {
			n := r3Native(t, "uncertain", true)
			v := &r3Verifier{}
			s := r3Open(t, n, v)
			if phase == "result" {
				if _, e := s.AdmitMovieUpgradeObservation(context.Background(), n.actor, uuid.New(), "admit"); e != nil {
					t.Fatal(e)
				}
			}
			before := r3Protected(t, n)
			sql := fmt.Sprintf(`CREATE SEQUENCE r3_tail_hit;CREATE FUNCTION r3_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='upgrade.observation-%s' THEN PERFORM nextval('r3_tail_hit');UPDATE library_file SET size_bytes=size_bytes+1 WHERE id='%s';END IF;RETURN NEW;END $$;CREATE TRIGGER r3_tail AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION r3_tail()`, phase, n.file)
			if _, e := n.p.Exec(context.Background(), sql); e != nil {
				t.Fatal(e)
			}
			var err error
			if phase == "admit" {
				_, err = s.AdmitMovieUpgradeObservation(context.Background(), n.actor, uuid.New(), "admit")
			} else {
				v.proof = &r3Proof{outcome: "exact_match", at: r3Now(t, n)}
				_, err = s.RecordMovieUpgradeObservation(context.Background(), n.actor, "record", v.proof)
			}
			if err == nil {
				t.Fatal("tail mutation accepted")
			}
			var hit bool
			if e := n.p.QueryRow(context.Background(), "SELECT is_called FROM r3_tail_hit").Scan(&hit); e != nil || !hit {
				t.Fatal("counterexample did not reach tail", e)
			}
			if r3Protected(t, n) != before || r3Count(t, n, "movie_upgrade_observation_result") != 0 {
				t.Fatal("tail did not roll back")
			}
			want := 0
			if phase == "result" {
				want = 1
			}
			if r3Count(t, n, "movie_upgrade_observation_permit") != want {
				t.Fatal("permit rollback failed")
			}
		})
	}
}

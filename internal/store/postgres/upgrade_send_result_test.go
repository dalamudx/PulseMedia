package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sendArmMigrationDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	entries, e := os.ReadDir(filepath.Join("..", "..", "..", "migrations"))
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		n := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(n, ".sql") || n >= "00067" {
			continue
		}
		b, e := os.ReadFile(filepath.Join("..", "..", "..", "migrations", n))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(d, n), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return d
}

// This verifier is an explicitly injected test dependency; no production proof issuer exists yet.
type testSendReturnProof struct {
	v ports.MovieUpgradeSendVerifiedReturn
}
type testSendReturnVerifier struct{}

func (testSendReturnVerifier) VerifyMovieUpgradeSendReturn(ctx context.Context, claim ports.MovieUpgradeSendReturnProof, expected ports.MovieUpgradeSendReturnBinding) (ports.MovieUpgradeSendVerifiedReturn, error) {
	if e := ctx.Err(); e != nil {
		return ports.MovieUpgradeSendVerifiedReturn{}, e
	}
	p, ok := claim.(*testSendReturnProof)
	if !ok || p == nil || p.v.Binding != expected {
		return ports.MovieUpgradeSendVerifiedReturn{}, errors.New("invalid test return evidence")
	}
	return p.v, nil
}
func returnedFixture(t *testing.T, status string) (*preparedUpgradeFixture, ports.MovieUpgradeSendSession, *testSendReturnProof) {
	t.Helper()
	f := armFixture(t)
	if v, e := postgres.Migrate(context.Background(), f.dsn, sendResultMigrationDir(t)); e != nil || v != 67 {
		t.Fatal(v, e)
	}
	r, e := postgres.NewMovieUpgradeSendResultRepository(f.preparation, testSendReturnVerifier{})
	if e != nil {
		t.Fatal(e)
	}
	s, e := r.OpenMovieUpgradeSendSession(context.Background(), f.operation)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	if _, e = s.ArmMovieUpgradeSend(context.Background(), f.actor, uuid.New(), f.proof, "return-arm"); e != nil {
		t.Fatal(e)
	}
	p := &testSendReturnProof{}
	p.v.Status = status
	b := &p.v.Binding
	e = f.p.QueryRow(context.Background(), `SELECT a.operation_id,a.task_id,a.owner,a.token,encode(sha256(convert_to(to_jsonb(a)::text,'UTF8')),'hex'),encode(p.raw_sha256,'hex'),clock_timestamp() FROM movie_upgrade_send_authorization a JOIN movie_upgrade_preparation p ON p.operation_id=a.operation_id WHERE a.operation_id=$1`, f.operation).Scan(&b.OperationID, &b.TaskID, &b.Owner, &b.Token, &b.ArmSHA256, &b.RawSHA256, &p.v.ReturnedAt)
	if e != nil {
		t.Fatal(e)
	}
	return f, s, p
}
func returnGraph(t *testing.T, f *preparedUpgradeFixture) string {
	t.Helper()
	var raw string
	if e := f.p.QueryRow(context.Background(), `SELECT jsonb_build_object('a',to_jsonb(a),'p',to_jsonb(p),'t',to_jsonb(t),'r',(SELECT jsonb_agg(to_jsonb(x)) FROM unit_reservation x WHERE operation_id=a.operation_id),'d',(SELECT jsonb_agg(to_jsonb(x)) FROM download_requirement x WHERE operation_id=a.operation_id),'l',(SELECT jsonb_agg(to_jsonb(x)) FROM acquisition_reservation x WHERE operation_id=a.operation_id))::text FROM movie_upgrade_send_authorization a JOIN movie_upgrade_preparation p ON p.operation_id=a.operation_id JOIN download_task t ON t.id=a.task_id WHERE a.operation_id=$1`, f.operation).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	return raw
}
func noReturn(t *testing.T, f *preparedUpgradeFixture) {
	t.Helper()
	var n int
	if e := f.p.QueryRow(context.Background(), `SELECT(SELECT count(*) FROM movie_upgrade_send_result)+(SELECT count(*) FROM audit_log WHERE action='upgrade.send-result')+(SELECT count(*) FROM outbox_event WHERE event_type='upgrade.send-result')+(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.send-result:%')`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
func TestMovieUpgradeSendReturnPG(t *testing.T) {
	for _, status := range []string{"confirmed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f, s, p := returnedFixture(t, status)
			graph := returnGraph(t, f)
			old := f.oldState(t)
			a, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "return", p)
			if e != nil {
				t.Fatal(e)
			}
			if a.Status != status || a.ExecutionAllowed || a.RecordedAt.Before(a.ReturnedAt) {
				t.Fatal(a)
			}
			b, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "return", p)
			if e != nil {
				t.Fatal(e)
			}
			aa, _ := json.Marshal(a)
			bb, _ := json.Marshal(b)
			if string(aa) != string(bb) {
				t.Fatal("receipt changed")
			}
			if returnGraph(t, f) != graph || f.oldState(t) != old {
				t.Fatal("immutable graph changed")
			}
			var n int
			if e = f.p.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='upgrade.send-result'`).Scan(&n); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			p.v.Status = "unknown"
			if status == "unknown" {
				p.v.Status = "confirmed"
			}
			if _, e = s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "return", p); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
				t.Fatal(e)
			}
		})
	}
}
func TestMovieUpgradeSendReturnConfigChangePG(t *testing.T) {
	f, s, p := returnedFixture(t, "confirmed")
	graph := returnGraph(t, f)
	f.exec(t, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.root)
	f.exec(t, `UPDATE download_client SET url=url||'/changed' WHERE id=$1`, f.client)
	f.exec(t, `UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.fact)
	if _, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "config-change", p); e != nil {
		t.Fatal(e)
	}
	if returnGraph(t, f) != graph {
		t.Fatal("original task rebound")
	}
}
func TestMovieUpgradeSendReturnRejectsPG(t *testing.T) {
	for _, kind := range []string{"fake", "nil", "foreign", "future", "owner", "revoked", "closed"} {
		t.Run(kind, func(t *testing.T) {
			f, s, p := returnedFixture(t, "confirmed")
			var claim ports.MovieUpgradeSendReturnProof = p
			switch kind {
			case "fake":
				claim = struct{ Returned bool }{true}
			case "nil":
				claim = (*testSendReturnProof)(nil)
			case "foreign":
				p.v.Binding.OperationID = uuid.New()
			case "future":
				p.v.ReturnedAt = time.Now().Add(time.Hour).Truncate(time.Microsecond)
			case "owner":
				p.v.Binding.Owner = uuid.New()
			case "revoked":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID)
			case "closed":
				if e := s.Close(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.RecordMovieUpgradeSendResult(context.Background(), f.actor, "reject", claim); e == nil {
				t.Fatal("invalid evidence accepted")
			}
			noReturn(t, f)
		})
	}
}

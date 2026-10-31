package mediasidecar

import (
	"bytes"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"sync"
	"testing"
)

const candidateSRT = "1\n00:00:01,000 --> 00:00:02,000\n中文字幕\n\n"

func candidateFixture(t *testing.T) (fixture, *CandidateRepository, *ports.Actor, ports.MediaSidecarSnapshot) {
	t.Helper()
	f, _, snapshot := subtitleFixture(t, "movie")
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	_, e := db.New(f.pool).CreateUser(t.Context(), db.CreateUserParams{ID: uuid.MustParse(a.ID), Username: "candidate-admin", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	return f, NewCandidateRepository(f.pool, testIDs{}, crypto.NewSecretProtector(bytes.Repeat([]byte{14}, 32))), a, snapshot
}
func candidateQuery(t *testing.T, f fixture, r *CandidateRepository, a *ports.Actor, snapshot ports.MediaSidecarSnapshot, key string) (ports.SubtitleCandidateTicket, ports.SubtitleCandidateState) {
	t.Helper()
	ticket, _, e := r.BeginCandidateQuery(t.Context(), a, f.file, snapshot.FileMappingRevision, key)
	if e != nil {
		t.Fatal("begin query", e)
	}
	out, e := r.FinishCandidateQuery(t.Context(), ticket, "candidates", []ports.SubtitleCandidateContent{{Content: []byte(candidateSRT)}})
	if e != nil {
		t.Fatal("finish query", e)
	}
	return ticket, out
}
func TestSubtitleCandidatesEncryptedVersionAndConfirmationPG(t *testing.T) {
	f, r, a, snapshot := candidateFixture(t)
	ctx := t.Context()
	empty, e := r.ReadCandidates(ctx, a, f.file)
	if e != nil || empty.Outcome != "not_queried" || empty.MappingRevision != snapshot.FileMappingRevision {
		t.Fatal("read initial revision", empty, e)
	}
	_, out := candidateQuery(t, f, r, a, snapshot, "query-one")
	if len(out.Candidates) != 1 || out.Outcome != "candidates" {
		t.Fatal(out)
	}
	var cipher []byte
	if e = f.pool.QueryRow(ctx, `SELECT content_encrypted FROM subtitle_candidate`).Scan(&cipher); e != nil || bytes.Contains(cipher, []byte(candidateSRT)) {
		t.Fatal("candidate plaintext", e)
	}
	repeat, original, e := r.BeginCandidateQuery(ctx, a, f.file, snapshot.FileMappingRevision, "query-one")
	if e != nil || repeat.QueryID != uuid.Nil || original.QueryID == nil || *original.QueryID != *out.QueryID {
		t.Fatal("query replay", original, e)
	}
	ticket, state, body, e := r.BeginCandidateConfirmation(ctx, a, f.file, out.Candidates[0].ID, snapshot.FileMappingRevision)
	if e != nil || ticket.QueryID == uuid.Nil || state.Outcome != "confirming" || string(body) != candidateSRT {
		t.Fatal("begin confirmation", state, e)
	}
	again, _, _, e := r.BeginCandidateConfirmation(ctx, a, f.file, out.Candidates[0].ID, snapshot.FileMappingRevision)
	if e != nil || again.QueryID != uuid.Nil {
		t.Fatal("confirmation replay acquired permit", e)
	}
	completion := subtitleCompletion(ticket.Snapshot)
	completion.Publication.Identity.Size = int64(len(body))
	final, e := r.FinishCandidateConfirmation(ctx, ticket, completion)
	if e != nil || final.Outcome != "confirmed" {
		t.Fatal("complete confirmation", final, e)
	}
	var selected uuid.UUID
	var permit bool
	if e = f.pool.QueryRow(ctx, `SELECT selected_candidate_id,confirm_permit FROM subtitle_candidate_query`).Scan(&selected, &permit); e != nil || !permit || selected != out.Candidates[0].ID {
		t.Fatal("selected candidate was not frozen", e)
	}
	if _, e = f.pool.Exec(ctx, `UPDATE subtitle_candidate_query SET confirm_permit=false,selected_candidate_id=NULL`); e == nil {
		t.Fatal("permanent permit cleared")
	}
	for _, statement := range []string{`UPDATE subtitle_candidate SET content_encrypted=content_encrypted`, `DELETE FROM subtitle_candidate`, `TRUNCATE subtitle_candidate_query CASCADE`} {
		if _, e = f.pool.Exec(ctx, statement); e == nil {
			t.Fatal("candidate authority history was mutable", statement)
		}
	}
}
func TestSubtitleCandidatesStalePermissionExpiryAndConcurrentPermitPG(t *testing.T) {
	t.Run("source-version", func(t *testing.T) {
		f, r, a, snapshot := candidateFixture(t)
		_, out := candidateQuery(t, f, r, a, snapshot, "query-stale")
		f.exec(t, `UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=$1`, f.file)
		_, _, _, e := r.BeginCandidateConfirmation(t.Context(), a, f.file, out.Candidates[0].ID, snapshot.FileMappingRevision)
		if !errors.Is(e, ports.ErrMediaSidecarStale) {
			t.Fatal("stale source accepted", e)
		}
		state, e := r.ReadCandidates(t.Context(), a, f.file)
		if e != nil || state.Outcome != "not_queried" || state.MappingRevision != snapshot.FileMappingRevision+1 || len(state.Candidates) != 0 {
			t.Fatal("stale candidates exposed", state, e)
		}
	})
	t.Run("permission", func(t *testing.T) {
		f, r, a, snapshot := candidateFixture(t)
		ticket, _, e := r.BeginCandidateQuery(t.Context(), a, f.file, snapshot.FileMappingRevision, "query-permission")
		if e != nil {
			t.Fatal(e)
		}
		f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(a.ID))
		if e = r.ValidateCandidate(t.Context(), ticket); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("revoked role accepted", e)
		}
	})
	t.Run("expired-lease", func(t *testing.T) {
		f, r, a, snapshot := candidateFixture(t)
		ticket, _, e := r.BeginCandidateQuery(t.Context(), a, f.file, snapshot.FileMappingRevision, "query-expiry")
		if e != nil {
			t.Fatal(e)
		}
		f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, ticket.Lease.ID)
		if _, e = r.FinishCandidateQuery(t.Context(), ticket, "not_found", nil); !errors.Is(e, ports.ErrMediaSidecarStale) {
			t.Fatal("expired lease committed", e)
		}
		state, e := r.ReadCandidates(t.Context(), a, f.file)
		if e != nil || state.Outcome != "review_required" {
			t.Fatal("expired outcome", state, e)
		}
	})
	t.Run("concurrent-permit", func(t *testing.T) {
		f, r, a, snapshot := candidateFixture(t)
		_, out := candidateQuery(t, f, r, a, snapshot, "query-concurrent")
		tickets := make([]ports.SubtitleCandidateTicket, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				tickets[i], _, _, errs[i] = r.BeginCandidateConfirmation(t.Context(), a, f.file, out.Candidates[0].ID, snapshot.FileMappingRevision)
			}(i)
		}
		wg.Wait()
		permits := 0
		for i, tk := range tickets {
			if errs[i] != nil {
				t.Fatal(errs[i])
			}
			if tk.QueryID != uuid.Nil {
				permits++
			}
		}
		if permits != 1 {
			t.Fatal("physical permit count", permits)
		}
	})
}

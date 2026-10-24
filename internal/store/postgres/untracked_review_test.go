package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type untrackedReviewFixture struct {
	*untrackedDiscoveryFixture
	review *postgres.UntrackedReviewStore
	file   ports.UntrackedDiscoveredFile
	input  ports.UntrackedReviewInput
}

func newUntrackedReviewFixture(t *testing.T, kinds ...string) *untrackedReviewFixture {
	t.Helper()
	f := newUntrackedDiscoveryFixture(t, kinds...)
	kind := "movie"
	if len(kinds) == 1 {
		kind = kinds[0]
	}
	f.request(t)
	c := f.claim(t)
	if err := f.discovery.RecordUntrackedDiscovery(t.Context(), c, discoverySample()); err != nil {
		t.Fatal(err)
	}
	if err := f.discovery.CompleteUntrackedDiscovery(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	file := f.files(t)[0]
	return &untrackedReviewFixture{untrackedDiscoveryFixture: f, review: postgres.NewUntrackedReviewStore(f.p, idgen.New(), hintClock{}), file: file, input: ports.UntrackedReviewInput{ScopeVersion: 1, FileRevision: file.Revision, MediaType: kind}}
}

func (f *untrackedReviewFixture) requestReview(t *testing.T, key string) *ports.UntrackedReviewRequestReceipt {
	t.Helper()
	r, e := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, f.input, key)
	if e != nil || r == nil || r.ExecutionAllowed {
		t.Fatal(r, e)
	}
	return r
}

func (f *untrackedReviewFixture) claimReview(t *testing.T) ports.UntrackedReviewCapture {
	t.Helper()
	j, e := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.UntrackedReviewJobKind}, Column2: "review-worker", Column3: 120000})
	if e != nil {
		t.Fatal(e)
	}
	c, e := f.review.LoadUntrackedReview(t.Context(), ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken})
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func reviewMovieEvidence() ports.UntrackedReviewEvidence {
	return ports.UntrackedReviewEvidence{ParserVersion: "fixture-v1", ParsedTitle: "Movie", Selection: "automatic", Complete: true, Reason: "ready", Candidate: &ports.UntrackedReviewCandidate{Key: "tmdb.movie:550", Namespace: "tmdb.movie", TMDBID: 550, Title: "Movie", Year: 1999, Units: []ports.UntrackedReviewUnit{{UnitKey: "movie"}}}}
}

func (f *untrackedReviewFixture) completeReview(t *testing.T, key string) *ports.UntrackedReview {
	t.Helper()
	r := f.requestReview(t, key)
	c := f.claimReview(t)
	if err := f.review.CompleteUntrackedReview(t.Context(), c, reviewMovieEvidence()); err != nil {
		t.Fatal(err)
	}
	v, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, r.Request.ID)
	if err != nil || v.Stale || v.ExecutionAllowed || v.State != "ready" || v.MaterialHash == "" {
		t.Fatal(v, err)
	}
	return v
}

func reviewConfirmation(v *ports.UntrackedReview) ports.UntrackedConfirmationInput {
	return ports.UntrackedConfirmationInput{ReviewID: v.Request.ID, MaterialHash: v.MaterialHash, ScopeVersion: v.Request.Input.ScopeVersion, FileRevision: v.Request.Input.FileRevision, CandidateKey: v.Evidence.Candidate.Key, Action: "hardlink", TargetRelativePath: "Movies/Movie (1999)/Movie.mkv"}
}

func (f *untrackedReviewFixture) prepareConfirmation(t *testing.T, in ports.UntrackedConfirmationInput, key string) ports.UntrackedConfirmationCapture {
	t.Helper()
	p, err := f.review.PrepareUntrackedConfirmation(t.Context(), f.a, f.scope, f.file.ID, in, key)
	if err != nil || p == nil || p.Capture == nil || p.Receipt != nil {
		t.Fatal(p, err)
	}
	return p.Capture
}

// The entire protected business graph is compared, including empty table contents.
func reviewProtectedGraph(t *testing.T, f *untrackedReviewFixture) string {
	t.Helper()
	return publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM media x),(SELECT jsonb_agg(to_jsonb(x)) FROM media_unit x),(SELECT jsonb_agg(to_jsonb(x)) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x)) FROM wanted_item x),(SELECT jsonb_agg(to_jsonb(x)) FROM download_task x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_file x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_file_unit x))::text`)
}

func TestUntrackedReviewRequestConcurrentKeysAndVersionsPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	before := reviewProtectedGraph(t, f)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	receipts := make(chan *ports.UntrackedReviewRequestReceipt, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.review.RequestUntrackedReview(ctx, f.a, f.scope, f.file.ID, f.input, "same-key")
			receipts <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *ports.UntrackedReviewRequestReceipt
	for r := range receipts {
		if first == nil {
			first = r
		}
		if !sameHistoricalJSON(first, r) {
			t.Fatal("same key changed original", first, r)
		}
	}
	f.count(t, `SELECT count(*) FROM untracked_review_request`, 1)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_review'`, 1)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.review.requested'`, 1)
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='untracked.review.requested'`, 1)
	for _, field := range []string{"revision", "scope", "id", "kind"} {
		bad := f.input
		switch field {
		case "revision":
			bad.FileRevision++
		case "scope":
			bad.ScopeVersion++
		case "id":
			bad.TMDBID = 42
		case "kind":
			bad.MediaType = "tv"
		}
		_, e := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, bad, "same-key")
		checkinErr(t, e, ports.ErrEndpointVersionConflict)
	}
	bad := f.input
	bad.FileRevision++
	_, e := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, bad, "new-key")
	checkinErr(t, e, ports.ErrUntrackedReviewStale)
	f.requestReview(t, "different-key")
	f.count(t, `SELECT count(*) FROM untracked_review_request`, 2)
	if reviewProtectedGraph(t, f) != before {
		t.Fatal("request mutated protected graph")
	}
}

func TestUntrackedReviewReceiptsAndReadOnlyAfterConfigurationPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	v := f.completeReview(t, "original")
	original, err := f.review.ReadUntrackedReviewRequestReceipt(t.Context(), f.a, f.scope, f.file.ID, "original")
	if err != nil {
		t.Fatal(err)
	}
	before := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_discovered_file x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x))::text`)
	if _, err = f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, v.Request.ID); err != nil {
		t.Fatal(err)
	}
	if after := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_discovered_file x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x))::text`); after != before {
		t.Fatal("read mutated storage")
	}
	// A routine discovery observation changes telemetry, not review identity.
	f.exec(t, `UPDATE untracked_discovered_file SET observations=observations+1,last_observed_at=clock_timestamp() WHERE id=$1`, f.file.ID)
	same, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, v.Request.ID)
	if err != nil || same.Stale || same.MaterialHash != v.MaterialHash {
		t.Fatal(same, err)
	}
	disabled := f.in
	disabled.ExpectedVersion = 1
	disabled.Enabled = false
	f.save(t, disabled, "disable-after-review")
	f.exec(t, `UPDATE download_client SET remote_path_map='{ "/different":"/elsewhere" }',password_encrypted='changed' WHERE id=$1`, f.client)
	replay, err := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, f.input, "original")
	if err != nil || !reflect.DeepEqual(replay, original) {
		t.Fatal(replay, original, err)
	}
	read, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, v.Request.ID)
	if err != nil || !read.Stale || read.ExecutionAllowed || !reflect.DeepEqual(read.Evidence, v.Evidence) {
		t.Fatal(read, err)
	}
	_, err = f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, f.input, "after-change")
	if err == nil {
		t.Fatal("new request accepted stale material")
	}
}

func TestUntrackedReviewCurrentActorAndExplicitBoundaryPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	r := f.requestReview(t, "auth")
	other := f.user(t)
	member := *f.a
	member.Role = "member"
	token := *f.a
	token.Scopes = []string{}
	for _, a := range []*ports.Actor{nil, other, &member, &token} {
		_, err := f.review.RequestUntrackedReview(t.Context(), a, f.scope, f.file.ID, f.input, "auth")
		if err == nil {
			t.Fatal("unauthorized request", a)
		}
		_, err = f.review.ReadUntrackedReview(t.Context(), a, f.scope, f.file.ID, r.Request.ID)
		if err == nil {
			t.Fatal("unauthorized read", a)
		}
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	_, err := f.review.ReadUntrackedReviewRequestReceipt(t.Context(), f.a, f.scope, f.file.ID, "auth")
	checkinErr(t, err, ports.ErrForbidden)
	f.exec(t, `UPDATE app_user SET is_active=true WHERE id=$1`, uuid.MustParse(f.a.ID))
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1 WHERE singleton`, uuid.New())
	_, err = f.review.ReadUntrackedReviewRequestReceipt(t.Context(), f.a, f.scope, f.file.ID, "auth")
	checkinErr(t, err, ports.ErrUnavailable)
}

func TestUntrackedReviewLeasePrivateAndCancelledPG(t *testing.T) {
	for _, mutation := range []string{"owner", "token", "state", "expiry", "kind", "actor", "root", "mapping", "secret", "file", "scope"} {
		t.Run(mutation, func(t *testing.T) {
			f := newUntrackedReviewFixture(t)
			f.requestReview(t, "lease")
			c := f.claimReview(t)
			lease := c.UntrackedReviewSnapshot().Lease
			switch mutation {
			case "owner":
				f.exec(t, `UPDATE job SET lease_owner='different' WHERE id=$1`, lease.ID)
			case "token":
				f.exec(t, `UPDATE job SET (lease_token)=ROW($2::bigint) WHERE id=$1`, lease.ID, int64(99))
			case "state":
				f.exec(t, `UPDATE job SET state='failed' WHERE id=$1`, lease.ID)
			case "expiry":
				f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID)
			case "kind":
				f.exec(t, `UPDATE job SET kind='untracked_discovery' WHERE id=$1`, lease.ID)
			case "actor":
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.a.ID))
			case "root":
				f.exec(t, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.root)
			case "mapping":
				f.exec(t, `UPDATE download_client SET remote_path_map='{"/remote":"/other"}' WHERE id=$1`, f.client)
			case "secret":
				f.exec(t, `UPDATE download_client SET password_encrypted='changed' WHERE id=$1`, f.client)
			case "file":
				f.exec(t, `UPDATE untracked_discovered_file SET revision=revision+1,inode=inode+1 WHERE id=$1`, f.file.ID)
			case "scope":
				in := f.in
				in.ExpectedVersion = 1
				in.Enabled = false
				f.save(t, in, "disable")
			}
			before := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x))::text`)
			if e := f.review.ValidateUntrackedReview(t.Context(), c); e == nil {
				t.Fatal("changed authority accepted")
			}
			if e := f.review.CompleteUntrackedReview(t.Context(), c, reviewMovieEvidence()); e == nil {
				t.Fatal("stale result published")
			}
			f.count(t, `SELECT count(*) FROM untracked_review_result`, 0)
			if after := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x))::text`); before != after {
				t.Fatal("stale callback mutated storage")
			}
		})
	}
	f := newUntrackedReviewFixture(t)
	f.requestReview(t, "private")
	c := f.claimReview(t)
	snapshot := c.UntrackedReviewSnapshot()
	snapshot.File.Identity.Inode++
	snapshot.Request.Input.TMDBID = 42
	snapshot.Scope.LocalRoot = "/forged"
	if err := f.review.ValidateUntrackedReview(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	other := postgres.NewUntrackedReviewStore(f.p, idgen.New(), hintClock{})
	checkinErr(t, other.ValidateUntrackedReview(t.Context(), c), ports.ErrRejected)
	forged := forgedReviewCapture{snapshot: c.UntrackedReviewSnapshot()}
	checkinErr(t, f.review.CompleteUntrackedReview(t.Context(), forged, reviewMovieEvidence()), ports.ErrRejected)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	checkinErr(t, f.review.CompleteUntrackedReview(ctx, c, reviewMovieEvidence()), context.Canceled)
}

type forgedReviewCapture struct{ snapshot ports.UntrackedReviewSnapshot }

func (c forgedReviewCapture) UntrackedReviewSnapshot() ports.UntrackedReviewSnapshot {
	return c.snapshot
}

func TestUntrackedReviewEvidenceAndUnresolvedPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	f.requestReview(t, "evidence")
	c := f.claimReview(t)
	for _, change := range []string{"selection", "namespace", "key", "id", "reason", "parser", "unit", "movie-episode", "complete", "overflow"} {
		e := reviewMovieEvidence()
		switch change {
		case "selection":
			e.Selection = "explicit"
		case "namespace":
			e.Candidate.Namespace = "tmdb.tv"
		case "key":
			e.Candidate.Key = "550"
		case "id":
			e.Candidate.TMDBID = 0
		case "reason":
			e.Reason = "ambiguous"
		case "parser":
			e.ParserVersion = ""
		case "unit":
			e.Candidate.Units[0].UnitKey = "S01E01"
		case "movie-episode":
			e.Candidate.Units[0].TMDBEpisodeID = 77
		case "complete":
			e.Complete = false
		case "overflow":
			e.Candidate.Units = make([]ports.UntrackedReviewUnit, 129)
		}
		checkinErr(t, f.review.CompleteUntrackedReview(t.Context(), c, e), ports.ErrRejected)
	}
	unresolved := ports.UntrackedReviewEvidence{Selection: "automatic", Reason: "ambiguous"}
	if err := f.review.CompleteUntrackedReview(t.Context(), c, unresolved); err != nil {
		t.Fatal(err)
	}
	x := c.UntrackedReviewSnapshot()
	v, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, x.Request.ID)
	if err != nil || v.Evidence.Complete || v.Evidence.Candidate != nil || v.Evidence.Reason != "ambiguous" {
		t.Fatal(v, err)
	}
	in := ports.UntrackedConfirmationInput{ReviewID: v.Request.ID, MaterialHash: v.MaterialHash, ScopeVersion: 1, FileRevision: f.file.Revision, CandidateKey: "tmdb.movie:550", Action: "copy", TargetRelativePath: "a.mkv"}
	_, err = f.review.PrepareUntrackedConfirmation(t.Context(), f.a, f.scope, f.file.ID, in, "cannot-confirm")
	checkinErr(t, err, ports.ErrUntrackedReviewStale)
}

func TestUntrackedReviewExplicitIDAndRecoveryPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	f.input.TMDBID = 42
	f.requestReview(t, "explicit")
	c := f.claimReview(t)
	e := reviewMovieEvidence()
	e.Selection = "explicit"
	checkinErr(t, f.review.CompleteUntrackedReview(t.Context(), c, e), ports.ErrRejected)
	e.Candidate.TMDBID = 42
	e.Candidate.Key = "tmdb.movie:42"
	if err := f.review.CompleteUntrackedReview(t.Context(), c, e); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"expired-a", "expired-b", "live"} {
		f.requestReview(t, key)
	}
	a, b, live := f.claimReview(t), f.claimReview(t), f.claimReview(t)
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=ANY($1::uuid[])`, []uuid.UUID{a.UntrackedReviewSnapshot().Lease.ID, b.UntrackedReviewSnapshot().Lease.ID})
	n, err := f.review.RecoverUntrackedReviews(t.Context(), 1)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	n, err = f.review.RecoverUntrackedReviews(t.Context(), 64)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = f.review.ValidateUntrackedReview(t.Context(), live); err != nil {
		t.Fatal(err)
	}
	checkinErr(t, f.review.CompleteUntrackedReview(t.Context(), a, e), ports.ErrUntrackedReviewStale)
	checkinErr(t, f.review.CompleteUntrackedReview(t.Context(), b, e), ports.ErrUntrackedReviewStale)
	for _, limit := range []int{-1, 65} {
		_, err = f.review.RecoverUntrackedReviews(t.Context(), limit)
		checkinErr(t, err, ports.ErrRejected)
	}
}

func TestUntrackedConfirmationConcurrentOriginalAndNoPublicationPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	v := f.completeReview(t, "review")
	in := reviewConfirmation(v)
	before := reviewProtectedGraph(t, f)
	jobBefore := publisherFrozen(t, f.p, `SELECT jsonb_agg(to_jsonb(j) ORDER BY id)::text FROM job j`)
	c1, c2 := f.prepareConfirmation(t, in, "confirm"), f.prepareConfirmation(t, in, "confirm")
	// Exported material is a deep copy, including candidate units and evidence.
	m := c1.UntrackedConfirmationMaterial()
	m.Input.Action = "move"
	m.Review.Evidence.Candidate.Units[0].UnitKey = "forged"
	m.Scope.LocalRoot = "/forged"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan *ports.UntrackedConfirmationReceipt, 2)
	errs := make(chan error, 2)
	for _, c := range []ports.UntrackedConfirmationCapture{c1, c2} {
		wg.Add(1)
		go func(c ports.UntrackedConfirmationCapture) {
			defer wg.Done()
			r, e := f.review.CommitUntrackedConfirmation(ctx, c)
			results <- r
			errs <- e
		}(c)
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *ports.UntrackedConfirmationReceipt
	for r := range results {
		if first == nil {
			first = r
		}
		if !reflect.DeepEqual(first, r) || r.State != "confirmed_pending_organization" || r.ExecutionAllowed {
			t.Fatal(first, r)
		}
	}
	f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 1)
	f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.review.confirmed'`, 1)
	f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='untracked.review.confirmed'`, 1)
	if reviewProtectedGraph(t, f) != before || publisherFrozen(t, f.p, `SELECT jsonb_agg(to_jsonb(j) ORDER BY id)::text FROM job j`) != jobBefore {
		t.Fatal("confirmation published business facts or work")
	}
	in.Action = "copy"
	_, err := f.review.PrepareUntrackedConfirmation(t.Context(), f.a, f.scope, f.file.ID, in, "confirm")
	checkinErr(t, err, ports.ErrEndpointVersionConflict)
	in.Action = "hardlink"
	disable := f.in
	disable.ExpectedVersion = 1
	disable.Enabled = false
	f.save(t, disable, "disable")
	f.exec(t, `UPDATE download_client SET password_encrypted='changed',remote_path_map='{}' WHERE id=$1`, f.client)
	p, err := f.review.PrepareUntrackedConfirmation(t.Context(), f.a, f.scope, f.file.ID, in, "confirm")
	if err != nil || p.Capture != nil || !reflect.DeepEqual(p.Receipt, first) {
		t.Fatal(p, err)
	}
	restored, err := f.review.ReadUntrackedConfirmationReceipt(t.Context(), f.a, f.scope, f.file.ID, "confirm")
	if err != nil || !reflect.DeepEqual(restored, first) {
		t.Fatal(restored, err)
	}
}

func TestUntrackedConfirmationExpectedMaterialAndPrivateExpiryPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	clock := &untrackedExpiryClock{}
	f.review = postgres.NewUntrackedReviewStore(f.p, idgen.New(), clock)
	v := f.completeReview(t, "review")
	in := reviewConfirmation(v)
	for _, field := range []string{"hash", "candidate", "revision", "scope", "path", "action"} {
		bad := in
		switch field {
		case "hash":
			bad.MaterialHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		case "candidate":
			bad.CandidateKey = "tmdb.tv:550"
		case "revision":
			bad.FileRevision++
		case "scope":
			bad.ScopeVersion++
		case "path":
			bad.TargetRelativePath = "../escape.mkv"
		case "action":
			bad.Action = "symlink"
		}
		if _, err := f.review.PrepareUntrackedConfirmation(t.Context(), f.a, f.scope, f.file.ID, bad, "invalid"); err == nil {
			t.Fatal("invalid confirmation", field)
		}
	}
	c := f.prepareConfirmation(t, in, "private")
	other := postgres.NewUntrackedReviewStore(f.p, idgen.New(), clock)
	_, err := other.CommitUntrackedConfirmation(t.Context(), c)
	checkinErr(t, err, ports.ErrRejected)
	forged := forgedConfirmationCapture{material: c.UntrackedConfirmationMaterial()}
	_, err = f.review.CommitUntrackedConfirmation(t.Context(), forged)
	checkinErr(t, err, ports.ErrRejected)
	clock.expired.Store(true)
	_, err = f.review.CommitUntrackedConfirmation(t.Context(), c)
	checkinErr(t, err, ports.ErrUntrackedReviewStale)
	f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 0)
}

type forgedConfirmationCapture struct {
	material ports.UntrackedConfirmationMaterial
}

func (c forgedConfirmationCapture) UntrackedConfirmationMaterial() ports.UntrackedConfirmationMaterial {
	return c.material
}

func TestUntrackedReviewLostReceiptFailsClosedPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	r := f.requestReview(t, "lost")
	// Simulate damaged historical storage as the migration owner, never normal DML.
	f.exec(t, `ALTER TABLE untracked_review_request_receipt DISABLE TRIGGER untracked_review_receipt_immutable`)
	f.exec(t, `DELETE FROM untracked_review_request_receipt WHERE request_id=$1`, r.Request.ID)
	_, err := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, f.input, "lost")
	checkinErr(t, err, ports.ErrUntrackedReviewStale)
	_, err = f.review.ReadUntrackedReviewRequestReceipt(t.Context(), f.a, f.scope, f.file.ID, "lost")
	checkinErr(t, err, ports.ErrUntrackedReviewStale)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_review'`, 1)
}

func TestUntrackedReviewNonUTCOriginalReceiptPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	r := f.requestReview(t, "zone")
	// Re-encode the same instant to model a historical non-UTC JSON receipt.
	r.Request.CreatedAt = r.Request.CreatedAt.In(time.FixedZone("fixture", 5*60*60+30*60))
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(r.Request)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, sql := range []string{`ALTER TABLE untracked_review_request DISABLE TRIGGER untracked_review_request_immutable`, `ALTER TABLE untracked_review_request_receipt DISABLE TRIGGER untracked_review_receipt_immutable`} {
		if _, err = tx.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(t.Context(), `UPDATE untracked_review_request SET request=$2 WHERE id=$1`, r.Request.ID, request); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE untracked_review_request_receipt SET receipt=$2 WHERE request_id=$1`, r.Request.ID, raw); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`ALTER TABLE untracked_review_request ENABLE TRIGGER untracked_review_request_immutable`, `ALTER TABLE untracked_review_request_receipt ENABLE TRIGGER untracked_review_receipt_immutable`} {
		if _, err = tx.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	disable := f.in
	disable.ExpectedVersion = 1
	disable.Enabled = false
	f.save(t, disable, "disable-zone")
	f.exec(t, `UPDATE download_client SET remote_path_map='{}' WHERE id=$1`, f.client)
	actual, err := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, f.input, "zone")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(actual)
	if string(got) != string(raw) {
		t.Fatal("original offset was normalized", string(got), string(raw))
	}
}

func TestUntrackedReviewInvalidRequestInputPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	for _, bad := range []ports.UntrackedReviewInput{{ScopeVersion: 1, FileRevision: 1, MediaType: "movie", TMDBID: -1}, {ScopeVersion: 1, FileRevision: 1, MediaType: "tv"}, {FileRevision: 1, MediaType: "movie"}, {ScopeVersion: 1, MediaType: "movie"}, {ScopeVersion: 1, FileRevision: 1, MediaType: "unknown"}} {
		if _, err := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, bad, "invalid"); err == nil {
			t.Fatal(bad)
		}
	}
	_, err := f.review.RequestUntrackedReview(t.Context(), f.a, uuid.Nil, f.file.ID, f.input, "id")
	checkinErr(t, err, ports.ErrRejected)
	_, err = f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, f.input, "non canonical key")
	checkinErr(t, err, ports.ErrRejected)
	_, err = f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, uuid.New(), f.input, "unknown")
	checkinErr(t, err, ports.ErrNotFound)
	f.count(t, `SELECT count(*) FROM untracked_review_request`, 0)
}

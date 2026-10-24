package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type untrackedOrganizationFixture struct {
	*untrackedReviewFixture
	organization *postgres.UntrackedOrganizationStore
	confirmation *ports.UntrackedConfirmationReceipt
}

func organizationConfirm(t *testing.T, f *untrackedReviewFixture, e ports.UntrackedReviewEvidence, action string) *untrackedOrganizationFixture {
	t.Helper()
	request := f.requestReview(t, "organization-review")
	capture := f.claimReview(t)
	e.ParserVersion = "untracked-review.v1/" + media.EpisodeParserVersion
	if err := f.review.CompleteUntrackedReview(t.Context(), capture, e); err != nil {
		t.Fatal(err)
	}
	view, err := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, request.Request.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := reviewConfirmation(view)
	in.Action = action
	if f.input.MediaType == "tv" {
		in.TargetRelativePath = "Shows/Series/Season 01/Series.S01E02E07.mkv"
	}
	c := f.prepareConfirmation(t, in, "organization-confirm")
	receipt, err := f.review.CommitUntrackedConfirmation(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return &untrackedOrganizationFixture{untrackedReviewFixture: f, organization: postgres.NewUntrackedOrganizationStore(f.p, idgen.New(), hintClock{}), confirmation: receipt}
}

func newOrganizationFixture(t *testing.T, action string) *untrackedOrganizationFixture {
	t.Helper()
	return organizationConfirm(t, newUntrackedReviewFixture(t), reviewMovieEvidence(), action)
}

func (f *untrackedOrganizationFixture) prepare(t *testing.T, key string) ports.UntrackedOrganizationCapture {
	t.Helper()
	p, e := f.organization.PrepareUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, ports.UntrackedOrganizationInput{ConfirmationID: f.confirmation.Material.ID}, key)
	if e != nil || p == nil || p.Capture == nil || p.Receipt != nil {
		t.Fatal(p, e)
	}
	return p.Capture
}

func (f *untrackedOrganizationFixture) commit(t *testing.T, key string) *ports.UntrackedOrganizationReceipt {
	t.Helper()
	r, e := f.organization.CommitUntrackedOrganization(t.Context(), f.prepare(t, key))
	if e != nil || r == nil || r.ExecutionAllowed || r.State != "queued" {
		t.Fatal(r, e)
	}
	return r
}

func organizationProtectedGraph(t *testing.T, f *untrackedOrganizationFixture) string {
	t.Helper()
	return publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM library_file x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_file_unit x),(SELECT jsonb_agg(to_jsonb(x)) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x)) FROM wanted_item x),(SELECT jsonb_agg(to_jsonb(x)) FROM download_requirement x),(SELECT jsonb_agg(to_jsonb(x)) FROM download_task x),(SELECT jsonb_agg(to_jsonb(x)) FROM release_candidate x),(SELECT jsonb_agg(to_jsonb(x)) FROM subscription x),(SELECT jsonb_agg(to_jsonb(x)) FROM unit_reservation x),(SELECT jsonb_agg(to_jsonb(x)) FROM acquisition_reservation x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_review_confirmation x))::text`)
}

func TestUntrackedOrganizationExactGraphAllActionsPG(t *testing.T) {
	for _, action := range []string{"hardlink", "copy", "reflink", "move"} {
		t.Run(action, func(t *testing.T) {
			f := newOrganizationFixture(t, action)
			before := organizationProtectedGraph(t, f)
			c := f.prepare(t, "graph")
			material := c.UntrackedOrganizationMaterial()
			if material.Confirmation.CommandKey != "organization-confirm" || !reflect.DeepEqual(material.Confirmation, *f.confirmation) {
				t.Fatal("lost original confirmation")
			}
			material.Confirmation.Material.Input.TargetRelativePath = "tampered.mkv"
			material.Confirmation.Material.Review.Evidence.Candidate.Units[0].UnitKey = "tampered"
			r, e := f.organization.CommitUntrackedOrganization(t.Context(), c)
			if e != nil {
				t.Fatal(e)
			}
			if r.Operation.ID == uuid.Nil || r.Operation.PlanID == uuid.Nil || r.Operation.StepID == uuid.Nil || r.Operation.JobID == uuid.Nil || r.ExecutionAllowed {
				t.Fatal(r)
			}
			f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 1)
			f.count(t, `SELECT count(*) FROM untracked_organization_unit`, 1)
			f.count(t, `SELECT count(*) FROM transfer_plan WHERE id=$1 AND operation_id=$2 AND state='approved' AND requires_approval AND strategy=$3 AND old_file_id IS NULL`, 1, r.Operation.PlanID, r.Operation.ID, action)
			f.count(t, `SELECT count(*) FROM transfer_step WHERE id=$1 AND plan_id=$2 AND state='pending' AND action=$3 AND source_root='/scope-source' AND source_rel='external/movie.mkv' AND target_rel='Movies/Movie (1999)/Movie.mkv' AND source_path='/scope-source/external/movie.mkv' AND source_device=2 AND source_inode=333 AND source_size=100 AND source_mtime=200 AND target_evidence->>'untracked_organization_id'=$4`, 1, r.Operation.StepID, r.Operation.PlanID, action, r.Operation.ID.String())
			f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND kind='untracked_organization' AND state='queued' AND max_attempts=3 AND payload=jsonb_build_object('operation_id',$2::uuid)`, 1, r.Operation.JobID, r.Operation.ID)
			f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.organization.queued'`, 1)
			f.count(t, `SELECT count(*) FROM outbox_event WHERE event_type='untracked.organization.queued'`, 1)
			f.count(t, `SELECT count(*) FROM untracked_organization_permit`, 0)
			f.count(t, `SELECT count(*) FROM untracked_organization_observation`, 0)
			f.count(t, `SELECT count(*) FROM untracked_organization_completion`, 0)
			var quality []byte
			if e = f.p.QueryRow(t.Context(), `SELECT quality_profile FROM transfer_step WHERE id=$1`, r.Operation.StepID).Scan(&quality); e != nil {
				t.Fatal(e)
			}
			expected, _ := json.Marshal(media.RecognizeRelease("movie.mkv").Quality)
			var a, b any
			_ = json.Unmarshal(quality, &a)
			_ = json.Unmarshal(expected, &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatal("invented filename quality", a, b)
			}
			if after := organizationProtectedGraph(t, f); after != before {
				t.Fatal("queue wrote protected business graph")
			}
			view, e := f.organization.ReadUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, r.Operation.ID)
			if e != nil || view.State != "queued" || view.ExecutionAllowed || view.CompletedFileID != nil {
				t.Fatal(view, e)
			}
		})
	}
}

func TestUntrackedOrganizationSameAndDifferentKeysPG(t *testing.T) {
	f := newOrganizationFixture(t, "hardlink")
	captures := make([]ports.UntrackedOrganizationCapture, 8)
	for i := range captures {
		captures[i] = f.prepare(t, "same")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan *ports.UntrackedOrganizationReceipt, 8)
	errs := make(chan error, 8)
	for _, c := range captures {
		wg.Add(1)
		go func(c ports.UntrackedOrganizationCapture) {
			defer wg.Done()
			r, e := f.organization.CommitUntrackedOrganization(ctx, c)
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
	var first *ports.UntrackedOrganizationReceipt
	for r := range results {
		if first == nil {
			first = r
		}
		if !reflect.DeepEqual(first, r) {
			t.Fatal("same key changed original")
		}
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 1)
	other := f.prepare(t, "different")
	if _, e := f.organization.CommitUntrackedOrganization(t.Context(), other); e == nil {
		t.Fatal("confirmation authorized twice")
	}
	f.count(t, `SELECT count(*) FROM transfer_plan`, 1)
	f.count(t, `SELECT count(*) FROM transfer_step`, 1)
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_organization'`, 1)
	_, e := f.organization.PrepareUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, ports.UntrackedOrganizationInput{ConfirmationID: uuid.New()}, "same")
	if !errors.Is(e, ports.ErrEndpointVersionConflict) {
		t.Fatal(e)
	}
}

func TestUntrackedOrganizationConcurrentDifferentKeysPG(t *testing.T) {
	f := newOrganizationFixture(t, "copy")
	one, two := f.prepare(t, "one"), f.prepare(t, "two")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ch := make(chan error, 2)
	var wg sync.WaitGroup
	for _, c := range []ports.UntrackedOrganizationCapture{one, two} {
		wg.Add(1)
		go func(c ports.UntrackedOrganizationCapture) {
			defer wg.Done()
			_, e := f.organization.CommitUntrackedOrganization(ctx, c)
			ch <- e
		}(c)
	}
	wg.Wait()
	close(ch)
	success := 0
	for e := range ch {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("unique confirmation winners=%d", success)
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 1)
	f.count(t, `SELECT count(*) FROM transfer_plan`, 1)
	f.count(t, `SELECT count(*) FROM transfer_step`, 1)
}

func TestUntrackedOrganizationOriginalReadDisabledAndGuardPG(t *testing.T) {
	f := newOrganizationFixture(t, "hardlink")
	r := f.commit(t, "original")
	before := organizationProtectedGraph(t, f)
	j, e := db.New(f.p).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.UntrackedOrganizationJobKind}, Column2: "organization-worker", Column3: 120000})
	if e != nil {
		t.Fatal(e)
	}
	lease := ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	locked, e := f.organization.LoadUntrackedOrganizationExecution(t.Context(), lease)
	if e != nil || locked.Operation != r.Operation || locked.Step.ID != r.Operation.StepID || len(locked.Units) != 1 {
		t.Fatal(locked, e)
	}
	locked.Confirmation.Material.Input.TargetRelativePath = "detached.mkv"
	again, e := f.organization.LoadUntrackedOrganizationExecution(t.Context(), lease)
	if e != nil || again.Confirmation.Material.Input.TargetRelativePath == "detached.mkv" {
		t.Fatal(again, e)
	}
	bad := lease
	bad.Token++
	if _, e = f.organization.LoadUntrackedOrganizationExecution(t.Context(), bad); e == nil {
		t.Fatal("stale lease accepted")
	}
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	_, e = postgres.GuardUntrackedOrganization(t.Context(), tx, r.Operation.ID, lease, true)
	_ = tx.Rollback(context.Background())
	if e == nil {
		t.Fatal("terminal guard accepted unfinished job")
	}
	disabled := f.in
	disabled.Enabled = false
	disabled.ExpectedVersion = 1
	f.save(t, disabled, "disabled")
	f.exec(t, `UPDATE download_client SET remote_path_map='{}',password_encrypted='offline' WHERE id=$1`, f.client)
	receipt, e := f.organization.ReadUntrackedOrganizationReceipt(t.Context(), f.a, f.scope, f.file.ID, "original")
	if e != nil || !reflect.DeepEqual(receipt, r) {
		t.Fatal(receipt, r, e)
	}
	p, e := f.organization.PrepareUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, ports.UntrackedOrganizationInput{ConfirmationID: f.confirmation.Material.ID}, "original")
	if e != nil || p.Capture != nil || !reflect.DeepEqual(p.Receipt, r) {
		t.Fatal(p, e)
	}
	view, e := f.organization.ReadUntrackedOrganization(t.Context(), f.a, f.scope, f.file.ID, r.Operation.ID)
	if e != nil || view.State != "running" || view.ExecutionAllowed {
		t.Fatal(view, e)
	}
	if after := organizationProtectedGraph(t, f); after != before {
		t.Fatal("read or guard wrote business graph")
	}
	if _, e = f.organization.LoadUntrackedOrganizationExecution(t.Context(), lease); e == nil {
		t.Fatal("disabled scope retained execution")
	}
}

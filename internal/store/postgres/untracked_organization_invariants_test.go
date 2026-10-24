package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestUntrackedOrganizationCookieAdminAndObjectBoundaryPG(t *testing.T) {
	f := newOrganizationFixture(t, "hardlink")
	r := f.commit(t, "owner")
	foreign := f.user(t)
	member := *f.a
	member.Role = "member"
	token := *f.a
	token.Scopes = []string{"read", "write"}
	system := *f.a
	system.Type = ports.ActorTypeSystem
	for _, a := range []*ports.Actor{nil, foreign, &member, &token, &system} {
		if _, e := f.organization.ReadUntrackedOrganizationReceipt(t.Context(), a, f.scope, f.file.ID, "owner"); e == nil {
			t.Fatal("unauthorized receipt", a)
		}
		if _, e := f.organization.ReadUntrackedOrganization(t.Context(), a, f.scope, f.file.ID, r.Operation.ID); e == nil {
			t.Fatal("unauthorized view", a)
		}
		if _, e := f.organization.PrepareUntrackedOrganization(t.Context(), a, f.scope, f.file.ID, ports.UntrackedOrganizationInput{ConfirmationID: f.confirmation.Material.ID}, "owner"); e == nil {
			t.Fatal("unauthorized original-key replay", a)
		}
	}
	if _, e := f.organization.ReadUntrackedOrganizationReceipt(t.Context(), f.a, f.scope, uuid.New(), "owner"); e == nil {
		t.Fatal("wrong file exposed receipt")
	}
}

func TestUntrackedOrganizationConflictingCanonicalMetadataPG(t *testing.T) {
	for _, kind := range []string{"title", "year", "media_type", "retired", "unit_key"} {
		t.Run(kind, func(t *testing.T) {
			f := newOrganizationFixture(t, "hardlink")
			mid, uid := uuid.New(), uuid.New()
			title, year, mediaType := "Movie", 1999, "movie"
			if kind == "title" {
				title = "Different"
			}
			if kind == "year" {
				year = 2000
			}
			if kind == "media_type" {
				mediaType = "tv"
			}
			f.exec(t, `INSERT INTO media(id,title,year,media_type) VALUES($1,$2,$3,$4)`, mid, title, year, mediaType)
			f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','550')`, uuid.New(), mid)
			unitKey := "movie"
			if kind == "unit_key" {
				unitKey = "S01E01"
			}
			f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,retired) VALUES($1,$2,$3,$4)`, uid, mid, unitKey, kind == "retired")
			capture := f.prepare(t, "conflict")
			_, err := f.organization.CommitUntrackedOrganization(t.Context(), capture)
			if err == nil {
				t.Fatal("conflicting canonical metadata committed", kind)
			}
			f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
		})
	}
}

func TestUntrackedOrganizationPermanentPermitAllowsObserveOnlyLeasePG(t *testing.T) {
	f := newOrganizationFixture(t, "copy")
	r := f.commit(t, "permit")
	q := db.New(f.p)
	j, err := q.ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.UntrackedOrganizationJobKind}, Column2: "physical-job", Column3: 120000})
	if err != nil {
		t.Fatal(err)
	}
	lease := ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	frame, err := f.organization.LoadUntrackedOrganizationExecution(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	content := ports.TransferContent{File: f.file.Identity}
	content.SHA256[0] = 42
	body, _ := json.Marshal(content)
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE transfer_plan SET state='executing',execution_owner='physical',execution_token=1,lease_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`, r.Operation.PlanID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE transfer_step SET state='running' WHERE id=$1`, r.Operation.StepID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO transfer_content_evidence(step_id,content) VALUES($1,$2)`, frame.Step.ID, body); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO untracked_organization_permit(operation_id,job_id,job_owner,job_token,execution_owner,execution_token,content_size,content_sha256,proof_hash,issued_at) VALUES($1,$2,$3,$4,'physical',1,$5,$6,$7,clock_timestamp())`, r.Operation.ID, j.ID, lease.Owner, lease.Token, content.File.Size, content.SHA256[:], r.Operation.MaterialHash); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Recovery may observe with a new current job lease. The physical plan token and
	// permanent permit remain the original execution identity even after lease expiry.
	f.exec(t, `UPDATE transfer_plan SET state='review_required',lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.Operation.PlanID)
	observerToken := lease.Token
	observerToken++
	f.exec(t, `UPDATE job SET lease_owner='observer',lease_token=$2,lease_until=clock_timestamp()+interval '2 minutes',attempts=attempts+1 WHERE id=$1`, j.ID, observerToken)
	observer := ports.JobLease{ID: j.ID, Owner: "observer", Token: observerToken}
	if _, err = f.organization.LoadUntrackedOrganizationExecution(t.Context(), observer); err != nil {
		t.Fatal("fresh lease could not observe original permit", err)
	}
	tx, err = f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `UPDATE transfer_plan SET execution_owner='observer',execution_token=2 WHERE id=$1`, r.Operation.PlanID)
	if err == nil {
		_, err = tx.Exec(t.Context(), `SET CONSTRAINTS ALL IMMEDIATE`)
	}
	_ = tx.Rollback(context.Background())
	if err == nil {
		t.Fatal("permanent physical permit rebound")
	}
	var issued time.Time
	if err = f.p.QueryRow(t.Context(), `SELECT issued_at FROM untracked_organization_permit WHERE operation_id=$1`, r.Operation.ID).Scan(&issued); err != nil || issued.IsZero() {
		t.Fatal(issued, err)
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_permit WHERE operation_id=$1 AND job_owner=$2 AND job_token=$3 AND execution_owner='physical' AND execution_token=1`, 1, r.Operation.ID, lease.Owner, lease.Token)
}

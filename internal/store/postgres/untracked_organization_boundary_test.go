package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type organizationClock struct{ now time.Time }

func (c *organizationClock) Now() time.Time                  { return c.now }
func (c *organizationClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

func TestUntrackedOrganizationCaptureExpiryAndInstancePG(t *testing.T) {
	f := newOrganizationFixture(t, "copy")
	clock := &organizationClock{now: time.Now()}
	f.organization = postgres.NewUntrackedOrganizationStore(f.p, idgen.New(), clock)
	capture := f.prepare(t, "expires")
	other := postgres.NewUntrackedOrganizationStore(f.p, idgen.New(), clock)
	if _, e := other.CommitUntrackedOrganization(t.Context(), capture); e == nil {
		t.Fatal("foreign instance accepted")
	}
	clock.now = clock.now.Add(16 * time.Second)
	if _, e := f.organization.CommitUntrackedOrganization(t.Context(), capture); e == nil {
		t.Fatal("expired capture accepted")
	}
	f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
	f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
}

func TestUntrackedOrganizationCurrentMaterialChangesPG(t *testing.T) {
	for _, change := range []string{"actor", "isolation", "mapping", "secret", "root", "baseline", "marker", "file", "scope", "metadata_insert", "media_version", "unit_version"} {
		t.Run(change, func(t *testing.T) {
			f := newOrganizationFixture(t, "hardlink")
			if change == "media_version" || change == "unit_version" {
				mid, uid, iid := uuid.New(), uuid.New(), uuid.New()
				f.exec(t, `INSERT INTO media(id,title,year,media_type) VALUES($1,'Movie',1999,'movie')`, mid)
				f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','550')`, iid, mid)
				f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, uid, mid)
			}
			c := f.prepare(t, "changed")
			switch change {
			case "actor":
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.a.ID))
			case "isolation":
				f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1 WHERE singleton`, uuid.New())
			case "mapping":
				f.exec(t, `UPDATE download_client SET remote_path_map='{}' WHERE id=$1`, f.client)
			case "secret":
				f.exec(t, `UPDATE download_client SET password_encrypted='changed' WHERE id=$1`, f.client)
			case "root":
				f.exec(t, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.root)
			case "baseline":
				f.exec(t, `UPDATE scan_finalization SET root_inode=root_inode+1 WHERE root_id=$1`, f.root)
			case "marker":
				f.exec(t, `UPDATE library_root SET marker_filename='changed' WHERE id=$1`, f.root)
			case "file":
				f.exec(t, `UPDATE untracked_discovered_file SET revision=revision+1 WHERE id=$1`, f.file.ID)
			case "scope":
				in := f.in
				in.Enabled = false
				in.ExpectedVersion = 1
				f.save(t, in, "disabled")
			case "metadata_insert":
				f.exec(t, `INSERT INTO media(id,title,year,media_type) VALUES($1,'Movie',1999,'movie')`, uuid.New())
				f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) SELECT $1,id,'tmdb.movie','550' FROM media`, uuid.New())
			case "media_version":
				f.exec(t, `UPDATE media SET metadata_version=metadata_version+1`)
			case "unit_version":
				f.exec(t, `UPDATE media_unit SET metadata_version=metadata_version+1`)
			}
			if _, e := f.organization.CommitUntrackedOrganization(t.Context(), c); e == nil {
				t.Fatal("changed material committed", change)
			}
			f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
			f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
			f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_organization'`, 0)
		})
	}
}

func TestUntrackedOrganizationPositiveObservationTelemetryStablePG(t *testing.T) {
	f := newOrganizationFixture(t, "hardlink")
	c := f.prepare(t, "stable")
	f.exec(t, `UPDATE untracked_discovered_file SET observations=observations+1,last_observed_at=clock_timestamp() WHERE id=$1`, f.file.ID)
	r, e := f.organization.CommitUntrackedOrganization(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	if r.Operation.MaterialHash != c.UntrackedOrganizationMaterial().Operation.MaterialHash {
		t.Fatal("telemetry changed stable material")
	}
}

func TestUntrackedOrganizationDeferredTailRollbackPG(t *testing.T) {
	changes := map[string]string{
		"audit":   `UPDATE audit_log SET created_at=created_at+interval '1 second' WHERE id=NEW.id;`,
		"event":   `UPDATE outbox_event SET payload='{}' WHERE event_type='untracked.organization.queued';`,
		"job":     `UPDATE job SET payload='{}' WHERE kind='untracked_organization';`,
		"actor":   `UPDATE app_user SET role='member' WHERE id=NEW.actor_id::uuid;`,
		"mapping": `UPDATE download_client SET remote_path_map='{}';`,
		"secret":  `UPDATE download_client SET password_encrypted='changed';`,
		"root":    `UPDATE library_root SET generation=generation+1;`,
		"file":    `UPDATE untracked_discovered_file SET revision=revision+1;`,
		"media":   `UPDATE media SET title='conflicting';`,
		"unit":    `UPDATE media_unit SET retired=true;`,
	}
	for name, mutation := range changes {
		t.Run(name, func(t *testing.T) {
			f := newOrganizationFixture(t, "reflink")
			c := f.prepare(t, "tail")
			before := organizationProtectedGraph(t, f)
			f.exec(t, `CREATE FUNCTION organization_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+mutation+` RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER organization_tail_fault AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.action='untracked.organization.queued') EXECUTE FUNCTION organization_tail_fault()`)
			if _, e := f.organization.CommitUntrackedOrganization(t.Context(), c); e == nil {
				t.Fatal("deferred tail accepted", name)
			}
			f.count(t, `SELECT count(*) FROM untracked_organization_operation`, 0)
			f.count(t, `SELECT count(*) FROM untracked_organization_unit`, 0)
			f.count(t, `SELECT count(*) FROM transfer_plan`, 0)
			f.count(t, `SELECT count(*) FROM transfer_step`, 0)
			f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_organization'`, 0)
			f.count(t, `SELECT count(*) FROM media`, 0)
			f.count(t, `SELECT count(*) FROM media_unit`, 0)
			if after := organizationProtectedGraph(t, f); after != before {
				t.Fatal("tail rollback leaked graph writes")
			}
		})
	}
}

func TestUntrackedOrganizationTransferFieldsFrozenAllActionsPG(t *testing.T) {
	for _, action := range []string{"hardlink", "copy", "reflink", "move"} {
		t.Run(action, func(t *testing.T) {
			f := newOrganizationFixture(t, action)
			r := f.commit(t, "frozen")
			mutations := []struct {
				sql string
				id  uuid.UUID
			}{
				{`UPDATE transfer_plan SET root_path='/other' WHERE id=$1`, r.Operation.PlanID},
				{`UPDATE transfer_plan SET strategy='copy' WHERE id=$1 AND strategy<>'copy'`, r.Operation.PlanID},
				{`DELETE FROM transfer_plan WHERE id=$1`, r.Operation.PlanID},
				{`UPDATE transfer_step SET source_path='/other' WHERE id=$1`, r.Operation.StepID},
				{`UPDATE transfer_step SET target_rel='other.mkv' WHERE id=$1`, r.Operation.StepID},
				{`UPDATE transfer_step SET source_inode=source_inode+1 WHERE id=$1`, r.Operation.StepID},
				{`UPDATE transfer_step SET quality_profile='{"resolution":"invalid-frozen-quality"}' WHERE id=$1`, r.Operation.StepID},
				{`UPDATE transfer_step SET target_evidence=target_evidence-'untracked_organization_id' WHERE id=$1`, r.Operation.StepID},
				{`UPDATE transfer_step SET target_evidence=target_evidence||'{"file_action":{}}' WHERE id=$1`, r.Operation.StepID},
				{`UPDATE transfer_step SET state='completed' WHERE id=$1`, r.Operation.StepID},
				{`DELETE FROM transfer_step WHERE id=$1`, r.Operation.StepID},
			}
			for _, m := range mutations {
				if action == "copy" && strings.Contains(m.sql, "strategy='copy'") {
					continue
				}
				tx, e := f.p.Begin(t.Context())
				if e != nil {
					t.Fatal(e)
				}
				_, e = tx.Exec(t.Context(), m.sql, m.id)
				if e == nil {
					_, e = tx.Exec(t.Context(), `SET CONSTRAINTS ALL IMMEDIATE`)
				}
				_ = tx.Rollback(context.Background())
				if e == nil {
					t.Fatal("frozen mutation accepted", action, m.sql)
				}
			}
		})
	}
}

func TestUntrackedOrganizationOriginalOffsetsPreservedPG(t *testing.T) {
	// The schema104 fixture stores explicit non-UTC timestamps before confirmation.
	f := newUntrackedReviewFixture(t)
	request := f.requestReview(t, "offset")
	capture := f.claimReview(t)
	evidence := reviewMovieEvidence()
	evidence.ParserVersion = "untracked-review.v1/" + media.EpisodeParserVersion
	if e := f.review.CompleteUntrackedReview(t.Context(), capture, evidence); e != nil {
		t.Fatal(e)
	}
	view, e := f.review.ReadUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, request.Request.ID)
	if e != nil {
		t.Fatal(e)
	}
	confirm := f.prepareConfirmation(t, reviewConfirmation(view), "offset-confirm")
	original, e := f.review.CommitUntrackedConfirmation(t.Context(), confirm)
	if e != nil {
		t.Fatal(e)
	}
	// Insert an equivalent original receipt into a separate, immutable confirmation row;
	// never UPDATE schema104 history to manufacture an offset test.
	shifted := *original
	shifted.Material = confirm.UntrackedConfirmationMaterial()
	shifted.Material.ID = uuid.New()
	shifted.CommandKey = "offset-confirm-copy"
	zone := time.FixedZone("+08", 8*60*60)
	shifted.Material.CreatedAt = shifted.Material.CreatedAt.In(zone)
	body, e := json.Marshal(shifted)
	if e != nil {
		t.Fatal(e)
	}
	var inputHash string
	if e = f.p.QueryRow(t.Context(), `SELECT input_hash FROM untracked_review_confirmation WHERE id=$1`, original.Material.ID).Scan(&inputHash); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO untracked_review_confirmation(id,owner_id,scope_id,file_id,review_id,command_key,input_hash,receipt) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, shifted.Material.ID, uuid.MustParse(f.a.ID), f.scope, f.file.ID, view.Request.ID, shifted.CommandKey, inputHash, body)
	org := &untrackedOrganizationFixture{untrackedReviewFixture: f, organization: postgres.NewUntrackedOrganizationStore(f.p, idgen.New(), hintClock{}), confirmation: &shifted}
	r := org.commit(t, "offset-organization")
	restored, e := org.organization.ReadUntrackedOrganizationReceipt(t.Context(), f.a, f.scope, f.file.ID, "offset-organization")
	if e != nil || !reflect.DeepEqual(restored, r) {
		t.Fatal(restored, r, e)
	}
	var stored []byte
	if e = f.p.QueryRow(t.Context(), `SELECT material->'confirmation' FROM untracked_organization_operation WHERE id=$1`, r.Operation.ID).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	var persisted ports.UntrackedConfirmationReceipt
	if e = json.Unmarshal(stored, &persisted); e != nil {
		t.Fatal(e)
	}
	actual, _ := json.Marshal(persisted)
	wanted, _ := json.Marshal(shifted)
	if string(actual) != string(wanted) || !strings.Contains(string(actual), "+08:00") {
		t.Fatal("original offset changed", string(actual), string(wanted))
	}
}

func TestUntrackedOrganizationHistoryAnd105RoundtripPG(t *testing.T) {
	t.Run("history", func(t *testing.T) {
		f := newOrganizationFixture(t, "hardlink")
		f.commit(t, "history")
		for _, table := range []string{"untracked_organization_operation", "untracked_organization_unit", "untracked_organization_permit", "untracked_organization_observation", "untracked_organization_completion"} {
			for _, sql := range []string{"DELETE FROM " + table, "TRUNCATE " + table + " CASCADE"} {
				tx, e := f.p.Begin(t.Context())
				if e != nil {
					t.Fatal(e)
				}
				_, e = tx.Exec(t.Context(), sql)
				_ = tx.Rollback(context.Background())
				if e == nil {
					t.Fatal("history mutation accepted", sql)
				}
			}
		}
		raw, e := os.ReadFile("../../../migrations/00105_untracked_organization.sql")
		if e != nil {
			t.Fatal(e)
		}
		_, down, ok := strings.Cut(string(raw), "-- +goose Down")
		if !ok {
			t.Fatal("missing Down")
		}
		tx, e := f.p.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(t.Context(), down)
		_ = tx.Rollback(context.Background())
		if e == nil {
			t.Fatal("history downgrade accepted")
		}
	})
	t.Run("empty", func(t *testing.T) {
		history := repositoryMigrationsThrough(t, 105)
		f := newCheckinFixtureAt(t, history, 105)
		version, e := postgres.Rollback(t.Context(), f.dsn, history)
		if e != nil || version != 104 {
			t.Fatal(version, e)
		}
		version, e = postgres.Migrate(t.Context(), f.dsn, history)
		if e != nil || version != 105 {
			t.Fatal(version, e)
		}
	})
}

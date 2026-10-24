package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestUntrackedReviewDeferredRequestTailRollbackPG(t *testing.T) {
	for _, mutation := range []string{"audit", "event", "job", "actor", "mapping", "root", "file"} {
		t.Run(mutation, func(t *testing.T) {
			f := newUntrackedReviewFixture(t)
			sql := map[string]string{
				"audit":   `UPDATE audit_log SET created_at=created_at+interval '1 second' WHERE id=NEW.id;`,
				"event":   `UPDATE outbox_event SET created_at=created_at+interval '1 second' WHERE aggregate_type='untracked_review' AND event_type='untracked.review.requested';`,
				"job":     `UPDATE job SET payload='{}' WHERE kind='untracked_review';`,
				"actor":   `UPDATE app_user SET is_active=false WHERE id=NEW.actor_id::uuid;`,
				"mapping": `UPDATE download_client SET remote_path_map='{}';`,
				"root":    `UPDATE library_root SET generation=generation+1;`,
				"file":    `UPDATE untracked_discovered_file SET revision=revision+1;`,
			}[mutation]
			f.exec(t, `CREATE FUNCTION review_request_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+sql+` RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER review_request_tail AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.action='untracked.review.requested') EXECUTE FUNCTION review_request_tail()`)
			before := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x),(SELECT jsonb_agg(to_jsonb(x)) FROM app_user x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_root x),(SELECT jsonb_agg(to_jsonb(x)) FROM download_client x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_discovered_file x))::text`)
			if _, err := f.review.RequestUntrackedReview(t.Context(), f.a, f.scope, f.file.ID, f.input, "tail"); err == nil {
				t.Fatal("request tail committed", mutation)
			}
			f.count(t, `SELECT count(*) FROM untracked_review_request`, 0)
			f.count(t, `SELECT count(*) FROM untracked_review_request_receipt`, 0)
			if after := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x),(SELECT jsonb_agg(to_jsonb(x)) FROM app_user x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_root x),(SELECT jsonb_agg(to_jsonb(x)) FROM download_client x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_discovered_file x))::text`); after != before {
				t.Fatal("request rollback leaked effects")
			}
		})
	}
}

func TestUntrackedReviewDeferredCompleteTailRollbackPG(t *testing.T) {
	for _, mutation := range []string{"audit", "event", "lease", "state", "secret", "baseline", "marker", "file"} {
		t.Run(mutation, func(t *testing.T) {
			f := newUntrackedReviewFixture(t)
			f.requestReview(t, "complete-tail")
			c := f.claimReview(t)
			sql := map[string]string{
				"audit":    `UPDATE audit_log SET payload='{}' WHERE id=NEW.id;`,
				"event":    `UPDATE outbox_event SET created_at=created_at+interval '1 second' WHERE event_type='untracked.review.completed';`,
				"lease":    `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind='untracked_review';`,
				"state":    `UPDATE job SET state='running' WHERE kind='untracked_review';`,
				"secret":   `UPDATE download_client SET password_encrypted='changed';`,
				"baseline": `UPDATE scan_finalization SET root_inode=root_inode+1;`,
				"marker":   `UPDATE library_root SET marker_filename='different-marker';`,
				"file":     `UPDATE untracked_discovered_file SET size=size+1;`,
			}[mutation]
			f.exec(t, `CREATE FUNCTION review_complete_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+sql+` RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER review_complete_tail AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.action='untracked.review.completed') EXECUTE FUNCTION review_complete_tail()`)
			before := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x))::text`)
			if err := f.review.CompleteUntrackedReview(t.Context(), c, reviewMovieEvidence()); err == nil {
				t.Fatal("complete tail committed", mutation)
			}
			f.count(t, `SELECT count(*) FROM untracked_review_result`, 0)
			if after := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x))::text`); after != before {
				t.Fatal("complete rollback leaked effects")
			}
		})
	}
}

func TestUntrackedConfirmationDeferredTailAndDBDeadlinePG(t *testing.T) {
	for _, mutation := range []string{"audit", "event", "job", "file", "root", "deadline"} {
		t.Run(mutation, func(t *testing.T) {
			f := newUntrackedReviewFixture(t)
			v := f.completeReview(t, "confirm-tail")
			c := f.prepareConfirmation(t, reviewConfirmation(v), "confirm")
			sql := map[string]string{
				"audit":    `UPDATE audit_log SET target_id='different' WHERE id=NEW.id;`,
				"event":    `UPDATE outbox_event SET seq=seq+100 WHERE event_type='untracked.review.confirmed';`,
				"job":      `UPDATE job SET result='{}' WHERE kind='untracked_review';`,
				"file":     `UPDATE untracked_discovered_file SET inode=inode+1;`,
				"root":     `UPDATE library_root SET generation=generation+1;`,
				"deadline": `PERFORM pg_sleep(15.05);`,
			}[mutation]
			f.exec(t, `CREATE FUNCTION review_confirmation_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+sql+` RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER review_confirmation_tail AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.action='untracked.review.confirmed') EXECUTE FUNCTION review_confirmation_tail()`)
			before := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_root x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_discovered_file x))::text`)
			if _, err := f.review.CommitUntrackedConfirmation(t.Context(), c); err == nil {
				t.Fatal("confirmation tail committed", mutation)
			}
			f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 0)
			if after := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x)) FROM job x),(SELECT jsonb_agg(to_jsonb(x)) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x)) FROM outbox_event x),(SELECT jsonb_agg(to_jsonb(x)) FROM library_root x),(SELECT jsonb_agg(to_jsonb(x)) FROM untracked_discovered_file x))::text`); after != before {
				t.Fatal("confirmation rollback leaked effects")
			}
		})
	}
}

func TestUntrackedConfirmationCurrentMaterialChangesPG(t *testing.T) {
	for _, change := range []string{"actor", "isolation", "mapping", "secret", "root", "file", "scope"} {
		t.Run(change, func(t *testing.T) {
			f := newUntrackedReviewFixture(t)
			v := f.completeReview(t, "confirmation")
			c := f.prepareConfirmation(t, reviewConfirmation(v), "confirm")
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
			case "file":
				f.exec(t, `UPDATE untracked_discovered_file SET revision=revision+1 WHERE id=$1`, f.file.ID)
			case "scope":
				in := f.in
				in.ExpectedVersion = 1
				in.Enabled = false
				f.save(t, in, "disable")
			}
			if _, err := f.review.CommitUntrackedConfirmation(t.Context(), c); err == nil {
				t.Fatal("changed confirmation committed", change)
			}
			f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 0)
			f.count(t, `SELECT count(*) FROM audit_log WHERE action='untracked.review.confirmed'`, 0)
		})
	}
}

func TestUntrackedReviewHistoryImmutableAndDowngradePG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	v := f.completeReview(t, "history")
	c := f.prepareConfirmation(t, reviewConfirmation(v), "confirm")
	if _, err := f.review.CommitUntrackedConfirmation(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"untracked_review_request", "untracked_review_request_receipt", "untracked_review_result", "untracked_review_confirmation"} {
		for _, verb := range []string{"UPDATE " + table + " SET " + map[string]string{"untracked_review_request": "command_key=command_key", "untracked_review_request_receipt": "receipt=receipt", "untracked_review_result": "material_hash=material_hash", "untracked_review_confirmation": "receipt=receipt"}[table], "DELETE FROM " + table, "TRUNCATE " + table + " CASCADE"} {
			tx, err := f.p.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(t.Context(), verb)
			_ = tx.Rollback(context.Background())
			if err == nil {
				t.Fatal("history mutation accepted", verb)
			}
		}
	}
	raw, err := os.ReadFile("../../../migrations/00104_untracked_review.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok {
		t.Fatal("missing downgrade")
	}
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), down); err == nil {
		t.Fatal("history-bearing downgrade accepted")
	}
}

func TestUntrackedReviewEmpty104To103To104PG(t *testing.T) {
	historical := repositoryMigrationsThrough(t, 104)
	f := newCheckinFixtureAt(t, historical, 104)
	version, err := postgres.Rollback(t.Context(), f.dsn, historical)
	if err != nil || version != 103 {
		t.Fatal("empty 104 downgrade", version, err)
	}
	// No other migration range is crossed by this test.
	version, err = postgres.Migrate(t.Context(), f.dsn, historical)
	if err != nil || version != 104 {
		t.Fatal("103 upgrade", version, err)
	}
}

func TestUntrackedConfirmationAllowedActionsAndCleanVideoPathsPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	v := f.completeReview(t, "actions")
	for _, action := range []string{"hardlink", "copy", "reflink", "move"} {
		in := reviewConfirmation(v)
		in.Action = action
		c := f.prepareConfirmation(t, in, action)
		r, err := f.review.CommitUntrackedConfirmation(t.Context(), c)
		if err != nil || r.ExecutionAllowed || r.State != "confirmed_pending_organization" {
			t.Fatal(r, err)
		}
	}
	for _, path := range []string{"/absolute.mkv", "a/../b.mkv", "a//b.mkv", "a\\b.mkv", "movie.txt", ".", "../movie.mkv"} {
		in := reviewConfirmation(v)
		in.TargetRelativePath = path
		_, err := f.review.PrepareUntrackedConfirmation(t.Context(), f.a, f.scope, f.file.ID, in, "bad-path")
		checkinErr(t, err, ports.ErrRejected)
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_review'`, 1)
	f.count(t, `SELECT count(*) FROM untracked_review_confirmation`, 4)
}

package postgres_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type taskBindingFixture struct {
	seedConfigFixture
	task, job uuid.UUID
	stamp     string
}

func newTaskBindingFixture(t *testing.T) taskBindingFixture {
	t.Helper()
	f := taskBindingFixture{seedConfigFixture: newSeedConfigFixture(t), task: uuid.New(), job: uuid.New()}
	if v, err := postgres.Migrate(t.Context(), f.dsn, repositoryMigrationsThrough(t, 112)); err != nil || v != 112 {
		t.Fatal("binding schema", v, err)
	}
	q := db.New(f.p)
	idx, err := q.CreateIndexer(t.Context(), db.CreateIndexerParams{ID: uuid.New(), Name: "owned binding source", Url: "https://binding.example", ApiKeyEncrypted: []byte("owned-encrypted-fixture"), Categories: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	candidate, op := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,info_hash,size_bytes,quality_profile,source_config_version) VALUES($1,$2,'Owned Movie 2026', $1::uuid::text,decode('01','hex'),$3,4096,'{}',1)`, candidate, idx.ID, strings.Repeat("a", 40))
	if _, err = q.CreateDownloadTask(t.Context(), db.CreateDownloadTaskParams{ID: f.task, OperationID: op, ClientID: f.client.ID, CandidateID: candidate, InfoHash: strings.Repeat("a", 40), State: "submitting", SavePath: "/owned"}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"operation_id": op.String(), "candidate_id": candidate.String()})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO job(id,kind,state,payload,attempts,max_attempts,lease_owner,lease_token,lease_until) VALUES($1,'download_submission','running',$2,1,3,'owned-original',1,clock_timestamp()+interval '1 minute')`, f.job, payload)
	bound, err := q.GetBoundSubmissionClient(t.Context(), f.client.ID)
	if err != nil || len(bound.ConfigStamp) != 64 {
		t.Fatal("client snapshot", err)
	}
	f.stamp = bound.ConfigStamp
	return f
}

func (f taskBindingFixture) params() db.CreateDownloadTaskClientBindingParams {
	return db.CreateDownloadTaskClientBindingParams{TaskID: f.task, ClientID: f.client.ID, ClientStamp: f.stamp, JobID: f.job, LeaseOwner: "owned-original", LeaseToken: 1}
}

func TestDownloadTaskBindingSnapshotPG(t *testing.T) {
	f := newTaskBindingFixture(t)
	q := db.New(f.p)
	stamp, err := q.DownloadGlobalRatesBinding(t.Context(), f.client.ID)
	if err != nil || stamp != f.stamp {
		t.Fatal("configuration fingerprints disagree", err)
	}
	if err = q.CreateDownloadTaskClientBinding(t.Context(), f.params()); err != nil {
		t.Fatal("capture original endpoint", err)
	}
	saved, err := q.GetDownloadTaskClientBinding(t.Context(), f.task)
	if err != nil || saved.ClientStamp != f.stamp || saved.ClientID != f.client.ID || saved.JobID != f.job || !saved.PreparedAt.Valid {
		t.Fatal("original binding", err)
	}
	for _, sql := range []string{`UPDATE download_task_client_binding SET client_stamp=repeat('b',64)`, `DELETE FROM download_task_client_binding`, `TRUNCATE download_task_client_binding`} {
		if _, err = f.p.Exec(t.Context(), sql); err == nil {
			t.Fatal("history mutation accepted")
		}
	}
	f.exec(t, `UPDATE download_client SET name='display only' WHERE id=$1`, f.client.ID)
	named, err := q.GetBoundSubmissionClient(t.Context(), f.client.ID)
	if err != nil || named.ConfigStamp != saved.ClientStamp {
		t.Fatal("display name invalidated binding", err)
	}
	f.exec(t, `UPDATE download_client SET url='https://replacement.example' WHERE id=$1`, f.client.ID)
	changed, err := q.GetBoundSubmissionClient(t.Context(), f.client.ID)
	if err != nil || changed.ConfigStamp == saved.ClientStamp {
		t.Fatal("endpoint change undetected", err)
	}
	restored, err := q.GetDownloadTaskClientBinding(t.Context(), f.task)
	if err != nil || restored != saved {
		t.Fatal("historical binding changed", err)
	}
}

func TestDownloadTaskBindingLateClientPG(t *testing.T) {
	f := newTaskBindingFixture(t)
	f.exec(t, `UPDATE download_client SET url='https://late.example' WHERE id=$1`, f.client.ID)
	if err := db.New(f.p).CreateDownloadTaskClientBinding(t.Context(), f.params()); err == nil {
		t.Fatal("stale client accepted")
	}
	if n := f.count(t, "download_task_client_binding"); n != 0 {
		t.Fatal("late binding committed", n)
	}
}

func TestDownloadTaskBindingCommitLeasePG(t *testing.T) {
	for _, kind := range []string{"expired", "owner", "token", "operation"} {
		t.Run(kind, func(t *testing.T) {
			f := newTaskBindingFixture(t)
			tx, err := f.p.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			if err = db.New(tx).CreateDownloadTaskClientBinding(t.Context(), f.params()); err != nil {
				t.Fatal(err)
			}
			sql := map[string]string{
				"expired":   `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`,
				"owner":     `UPDATE job SET lease_owner='late-owner' WHERE id=$1`,
				"token":     `UPDATE job SET lease_token=lease_token+1 WHERE id=$1`,
				"operation": `UPDATE job SET payload=jsonb_set(payload,'{operation_id}',to_jsonb('unrelated'::text)) WHERE id=$1`,
			}[kind]
			if _, err = tx.Exec(t.Context(), sql, f.job); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err == nil {
				t.Fatal("late lease accepted")
			}
			if n := f.count(t, "download_task_client_binding"); n != 0 {
				t.Fatal("binding survived rollback", n)
			}
		})
	}
}

func TestDownloadTaskBindingHistoricalCapabilityPG(t *testing.T) {
	f := newSeedConfigFixture(t)
	q := db.New(f.p)
	old, err := q.GetBoundSubmissionClient(t.Context(), f.client.ID)
	if err != nil || old.ConfigStamp != "" {
		t.Fatal("legacy schema fabricated evidence", err)
	}
	if v, err := postgres.Migrate(t.Context(), f.dsn, repositoryMigrationsThrough(t, 112)); err != nil || v != 112 {
		t.Fatal(v, err)
	}
	current, err := q.GetBoundSubmissionClient(t.Context(), f.client.ID)
	if err != nil || len(current.ConfigStamp) != 64 || current.DownloadClient.ID != f.client.ID {
		t.Fatal("new capability", err)
	}
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `ALTER TABLE download_task_client_binding RENAME TO owned_hidden_binding`); err != nil {
		t.Fatal(err)
	}
	retained, err := db.New(tx).GetBoundSubmissionClient(t.Context(), f.client.ID)
	if err != nil || retained.ConfigStamp != current.ConfigStamp {
		t.Fatal("storage fault silently disabled binding", err)
	}
	if err = db.New(tx).CreateDownloadTaskClientBinding(t.Context(), db.CreateDownloadTaskClientBindingParams{TaskID: uuid.New(), ClientID: f.client.ID, ClientStamp: retained.ConfigStamp, JobID: uuid.New(), LeaseOwner: "owned", LeaseToken: 1}); err == nil {
		t.Fatal("missing storage accepted")
	}
}

func TestDownloadTaskBindingSchema112HistoryPG(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newSeedConfigFixture(t)
		history := repositoryMigrationsThrough(t, 112)
		if v, err := postgres.Migrate(t.Context(), f.dsn, history); err != nil || v != 112 {
			t.Fatal("install", v, err)
		}
		if v, err := postgres.Rollback(t.Context(), f.dsn, history); err != nil || v != 111 {
			t.Fatal("empty down", v, err)
		}
		if v, err := postgres.Migrate(t.Context(), f.dsn, history); err != nil || v != 112 {
			t.Fatal("reinstall", v, err)
		}
	})
	t.Run("history", func(t *testing.T) {
		f := newTaskBindingFixture(t)
		q := db.New(f.p)
		if err := q.CreateDownloadTaskClientBinding(t.Context(), f.params()); err != nil {
			t.Fatal(err)
		}
		before, err := q.GetDownloadTaskClientBinding(t.Context(), f.task)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = postgres.Rollback(t.Context(), f.dsn, repositoryMigrationsThrough(t, 112)); err == nil {
			t.Fatal("nonempty binding downgraded")
		}
		after, err := q.GetDownloadTaskClientBinding(t.Context(), f.task)
		if err != nil || before != after {
			t.Fatal("failed downgrade changed original binding", err)
		}
	})
}

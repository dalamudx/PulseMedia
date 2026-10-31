package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func tvControlUpgradeCommand(t *testing.T, f *dedicatedControlFixture) error {
	t.Helper()
	var c ports.TVEpisodeUpgradeCommand
	c.OperationID = f.task.OperationID
	c.Action = "observe"
	c.Confirmed = true
	if e := f.pool.QueryRow(t.Context(), `SELECT subscription_id,unit_id,old_file_id,candidate_id,version FROM tv_episode_upgrade_authority WHERE operation_id=$1`, c.OperationID).Scan(&c.SubscriptionID, &c.UnitID, &c.CurrentFileID, &c.CandidateID, &c.ExpectedVersion); e != nil {
		t.Fatal(e)
	}
	s := postgres.NewTVEpisodeUpgradeStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)), nil, nil, nil, nil)
	_, e := s.CommandTVEpisodeUpgrade(t.Context(), f.actor, c, "original-while-control")
	return e
}

func TestTVEpisodeUpgradeControlNativeHTTPPG(t *testing.T) {
	f := newDedicatedControl(t, "tv-episode-upgrade")
	before := f.protected(t)
	for _, action := range []string{"pause", "resume"} {
		got, e := f.apply(t.Context(), action, action)
		if e != nil || got == nil || got.State != "confirmed" {
			t.Fatal(action, got, e)
		}
	}
	f.loseACK.Store(true)
	got, e := f.apply(t.Context(), "lost-ack", "pause")
	if e != nil || got == nil || got.State != "unknown" {
		t.Fatal(got, e)
	}
	if _, e = f.apply(t.Context(), "replacement", "resume"); !errors.Is(e, ports.ErrDownloadControlConflict) {
		t.Fatal("unknown allowed replacement", e)
	}
	if e = tvControlUpgradeCommand(t, f); !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
		t.Fatal("unknown allowed upgrade command", e)
	}
	reads := f.reads.Load()
	for range 2 {
		got, e = f.apply(t.Context(), "lost-ack", "pause")
		if e != nil || got.State != "unknown" {
			t.Fatal(got, e)
		}
		got, e = f.service.Get(t.Context(), f.actor, f.task.OperationID, "lost-ack")
		if e != nil || got.State != "unknown" {
			t.Fatal(got, e)
		}
	}
	if f.sends.Load() != 3 || f.reads.Load() != reads {
		t.Fatal("receipt or replay called native peer")
	}
	got, e = f.service.Observe(t.Context(), f.actor, f.task.OperationID, "lost-ack")
	if e != nil || got.State != "confirmed" || f.sends.Load() != 3 {
		t.Fatal("observation resent", got, e)
	}
	if _, e = f.apply(t.Context(), "resume-after-observation", "resume"); e != nil {
		t.Fatal(e)
	}
	if f.protected(t) != before {
		t.Fatal("control changed original task/protection graph")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_authority WHERE scope='tv-episode-upgrade'`); n != 4 {
		t.Fatal("missing immutable authority", n)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`); n != 0 {
		t.Fatal("physical return leaked hold", n)
	}
	if e = postgres.RejectTVEpisodeUpgradeOperation(t.Context(), f.pool, f.task.OperationID); !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
		t.Fatal("generic guard weakened", e)
	}
}

func TestTVEpisodeUpgradeControlCurrentAuthorityPG(t *testing.T) {
	changes := map[string]string{
		"owner":    `UPDATE app_user SET is_active=false WHERE id=(SELECT actor_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"intent":   `UPDATE subscription SET intent_version=intent_version+1 WHERE id=(SELECT subscription_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"endpoint": `UPDATE download_client SET url='https://changed.invalid' WHERE id=(SELECT client_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"unit":     `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=(SELECT unit_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"file":     `UPDATE library_file SET size_bytes=size_bytes+1 WHERE id=(SELECT old_file_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"mapping":  `UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id=(SELECT old_file_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"fact":     `UPDATE unit_fact SET expires_at=clock_timestamp() WHERE id=(SELECT old_fact_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"root":     `UPDATE library_root SET health='offline' WHERE id=(SELECT root_id FROM tv_episode_upgrade_authority WHERE operation_id=$1)`,
		"source":   `UPDATE indexer SET config_version=config_version+1 WHERE id=(SELECT c.indexer_id FROM tv_episode_upgrade_authority a JOIN release_candidate c ON c.id=a.candidate_id WHERE a.operation_id=$1)`,
	}
	for name, sql := range changes {
		t.Run(name, func(t *testing.T) {
			f := newDedicatedControl(t, "tv-episode-upgrade")
			hook := func() { f.onRead.Store(nil); execIntent(t, f.pool, sql, f.task.OperationID) }
			f.onRead.Store(&hook)
			if _, e := f.apply(t.Context(), "stale", "pause"); e == nil {
				t.Fatal("changed authority accepted")
			}
			if f.sends.Load() != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_command`) != 0 {
				t.Fatal("stale authority dispatched or committed")
			}
		})
	}
	t.Run("native-identity", func(t *testing.T) {
		f := newDedicatedControl(t, "tv-episode-upgrade")
		f.badTag.Store(true)
		if _, e := f.apply(t.Context(), "bad-tag", "pause"); !errors.Is(e, ports.ErrDownloadControlConflict) || f.sends.Load() != 0 {
			t.Fatal(e)
		}
	})
}

func TestTVEpisodeUpgradeControlPhysicalExclusivityPG(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "cancellation"
		if lost {
			name = "database-disconnection"
		}
		t.Run(name, func(t *testing.T) {
			f := newDedicatedControl(t, "tv-episode-upgrade")
			before := f.protected(t)
			entered, release := make(chan struct{}), make(chan struct{})
			hook := func() { close(entered); <-release }
			f.onSend.Store(&hook)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := f.apply(ctx, "inflight", "pause"); done <- e }()
			var once sync.Once
			drain := func() {
				once.Do(func() {
					close(release)
					e := <-done
					if lost && e == nil {
						t.Error("disconnected backend published success")
					}
					if !lost && e != nil {
						t.Error(e)
					}
				})
			}
			defer drain()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("native send not entered")
			}
			cancel()
			if lost {
				execIntent(t, f.pool, `SELECT pg_terminate_backend(backend_pid) FROM download_control_command WHERE request_key='inflight'`)
			}
			if _, e := f.apply(t.Context(), "replacement", "resume"); !errors.Is(e, ports.ErrDownloadControlConflict) {
				t.Fatal("replacement admitted", e)
			}
			if _, e := f.service.Observe(t.Context(), f.actor, f.task.OperationID, "inflight"); !errors.Is(e, ports.ErrDownloadControlConflict) {
				t.Fatal("live call observed", e)
			}
			if e := tvControlUpgradeCommand(t, f); !errors.Is(e, ports.ErrTVEpisodeUpgradeConflict) && !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
				t.Fatal("upgrade command admitted", e)
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`); n != 1 {
				t.Fatal("physical hold released early", n)
			}
			for _, sql := range []string{`UPDATE download_control_authority SET digest=repeat('f',64)`, `DELETE FROM download_control_physical`, `TRUNCATE download_control_physical`, `UPDATE download_task SET state='completed'`, `DELETE FROM download_task`, `UPDATE download_task SET operation_id=gen_random_uuid()`} {
				if _, e := f.pool.Exec(t.Context(), sql); e == nil {
					t.Fatal("tamper accepted", sql)
				}
			}
			drain()
			want := 0
			if lost {
				want = 1
			}
			if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`); n != want {
				t.Fatal("incorrect physical release", n, want)
			}
			if f.sends.Load() != 1 || f.protected(t) != before {
				t.Fatal("physical command changed original protections")
			}
			if lost {
				if n := intentCount(t, f.pool, `SELECT count(*) FROM download_control_return`); n != 0 {
					t.Fatal("forged return")
				}
				if e := tvControlUpgradeCommand(t, f); !errors.Is(e, ports.ErrTVEpisodeUpgradeObservation) {
					t.Fatal("orphan hold allowed original upgrade", e)
				}
			}
		})
	}
}

func TestTVEpisodeUpgradeControlSharedPermissionRollbackPG(t *testing.T) {
	t.Run("shared", func(t *testing.T) {
		f := newDedicatedControl(t, "tv-episode-upgrade")
		execIntent(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence) SELECT $2,operation_id,unit_id,root_id,'system',$3,'{}' FROM tv_episode_upgrade_authority WHERE operation_id=$1`, f.task.OperationID, uuid.New(), uuid.NewString())
		if _, e := f.apply(t.Context(), "unconfirmed-shared", "pause"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal(e)
		}
		got, e := f.service.Apply(t.Context(), f.actor, f.task.OperationID, "confirmed-shared", ports.DownloadControlCommand{Action: "pause", Confirmed: true, AffectShared: true})
		if e != nil || got.State != "confirmed" || f.sends.Load() != 1 {
			t.Fatal(got, e)
		}
		// A member with no demand cannot read the administrator's original receipt.
		other := uuid.New()
		execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'mp27-member',password_hash,'member',true FROM app_user WHERE id=$2`, other, uuid.MustParse(f.actor.ID))
		member := &ports.Actor{ID: other.String(), Type: ports.ActorTypeUser, Role: ports.RoleMember}
		if _, e = f.service.Get(t.Context(), member, f.task.OperationID, "confirmed-shared"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("foreign receipt exposed", e)
		}
		if _, e = f.service.Apply(t.Context(), member, f.task.OperationID, "member", ports.DownloadControlCommand{Action: "resume", Confirmed: true, AffectShared: true}); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("member escalated", e)
		}
	})
	t.Run("audit-rollback", func(t *testing.T) {
		f := newDedicatedControl(t, "tv-episode-upgrade")
		execIntent(t, f.pool, `CREATE FUNCTION mp27_fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='download.control.authorized' THEN RAISE EXCEPTION 'owned audit fault'; END IF; RETURN NEW; END $$`)
		execIntent(t, f.pool, `CREATE TRIGGER mp27_fail_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION mp27_fail_audit()`)
		if _, e := f.apply(t.Context(), "rollback", "pause"); e == nil {
			t.Fatal("audit failure accepted")
		}
		if f.sends.Load() != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_command`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_authority`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_control_physical`) != 0 {
			t.Fatal("rollback leaked command or hold")
		}
	})
}

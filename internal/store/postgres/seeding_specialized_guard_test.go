package postgres_test

import (
	"testing"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	seed "github.com/dalamudx/PulseMedia/internal/usecase/seeding"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These paths own separate ingestion ledgers. Their native completion cannot
// substitute for the ordinary task's immutable send/ingestion binding.
func TestSeedingAutomaticTVAuthorityGuardPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	sent := automaticTVPagingSubmission(t, f, 1)
	assertSeedingSpecializedRetained(t, f.pool, f.actor, sent.command.TaskID)
}
func TestSeedingCompleteTVAuthorityGuardPG(t *testing.T) {
	f := newCreationFixture(t)
	execIntent(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	f.actor.Role = ports.RoleAdmin
	credential, err := crypto.Encrypt(make([]byte, 32), []byte("owned-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, client := uuid.New(), uuid.New()
	execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,health) VALUES($1,'seeding-guard','https://guard.invalid','fixture','online')`, endpoint)
	if _, e := db.New(f.pool).CreateDownloadClient(t.Context(), db.CreateDownloadClientParams{ID: client, Name: "guard", ClientType: "qbittorrent", Url: "https://guard.invalid", Username: "owned", PasswordEncrypted: credential, RemotePathMap: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	execIntent(t, f.pool, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, f.tv)
	scan := uuid.New()
	execIntent(t, f.pool, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, f.tv)
	execIntent(t, f.pool, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.tv)
	req := completeTVPagingTarget(t, f, endpoint, client, 1)
	assertSeedingSpecializedRetained(t, f.pool, f.actor, req.TaskID)
}
func TestSeedingUpgradeAuthorityGuardPG(t *testing.T) {
	f := newMovieUpgradeWatchFixture(t)
	var task uuid.UUID
	if e := f.p.QueryRow(t.Context(), `SELECT id FROM download_task WHERE operation_id=$1`, f.operation).Scan(&task); e != nil {
		t.Fatal(e)
	}
	assertSeedingSpecializedRetained(t, f.p, f.actor, task)
}
func assertSeedingSpecializedRetained(t *testing.T, pool *pgxpool.Pool, actor *ports.Actor, task uuid.UUID) {
	t.Helper()
	q := db.New(pool)
	original, e := q.GetSubmissionTask(t.Context(), task)
	if e != nil {
		t.Fatal(e)
	}
	client, e := q.GetDownloadClientByID(t.Context(), original.ClientID)
	if e != nil {
		t.Fatal(e)
	}
	execIntent(t, pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(actor.ID))
	admin := *actor
	admin.Role = ports.RoleAdmin
	seconds := int64(1)
	input := ports.SeedingConfigInput{Enabled: true, IntervalSeconds: 1200, ExpectedClientUpdatedAt: client.UpdatedAt.Time, Rule: rule.Rule{Mode: rule.MatchAll, MinSeedingSeconds: &seconds}}
	if _, e = seed.NewConfigService(postgres.NewSeedingConfigStore(pool, idgen.New())).Save(t.Context(), &admin, client.ID, input, "specialized-config"); e != nil {
		t.Fatal(e)
	}
	config, e := q.GetSeedingConfig(t.Context(), client.ID)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := q.ReadOrdinarySeedingProtection(t.Context(), db.ReadOrdinarySeedingProtectionParams{TaskID: task, ClientStamp: config.ClientStamp})
	if e != nil || proof.Owned {
		t.Fatal("specialized authority treated as ordinary send", proof, e)
	}
	// Make the scheduling deadline due without changing the business task/ledger.
	execIntent(t, pool, `INSERT INTO seeding_removal_schedule(task_id,config_version,next_due_at,state) VALUES($1,1,clock_timestamp()-interval '1 second','idle')`, task)
	before := tvWatchSnapshot(t, &tvHintFixture{p: pool})
	scheduler := postgres.NewSeedingScheduleStore(pool, idgen.New())
	if e = scheduler.EnqueueDue(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = scheduler.RecoverExpired(t.Context()); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM job WHERE kind='seeding_removal')+(SELECT count(*) FROM seeding_removal_dispatch)+(SELECT count(*) FROM seeding_removal_permit)+(SELECT count(*) FROM seeding_removal_execution)`).Scan(&count); e != nil || count != 0 {
		t.Fatal("specialized task granted removal", count, e)
	}
	if after := tvWatchSnapshot(t, &tvHintFixture{p: pool}); before != after {
		t.Fatal("read-only rejection changed protected graph")
	}
	t.Log("M10_SEEDING_SPECIALIZED_GUARD_PG_PASS authority=REAL_SPECIALIZED task=RETAINED ordinary_binding=ABSENT jobs=ZERO permits=ZERO executions=ZERO protected=UNCHANGED")
}

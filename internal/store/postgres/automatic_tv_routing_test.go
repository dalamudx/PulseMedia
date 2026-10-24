package postgres_test

import (
	"bytes"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func TestAutomaticTVInitialRoutingBindingPG(t *testing.T) {
	for _, mode := range []string{"media_rule", "site_override", "single_client", "manual_binding", "changed_rules", "changed_client", "tail_rollback", "unavailable_rule", "concurrent_start", "conflicting_sites"} {
		t.Run(mode, func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			ctx := t.Context()
			q := db.New(f.pool)
			target := f.client
			if mode != "single_client" {
				target = uuid.New()
				original, err := q.GetDownloadClientByID(ctx, f.client)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: target, Name: "TV target", ClientType: "transmission", Url: "https://other-downloader.test", PasswordEncrypted: original.PasswordEncrypted, RemotePathMap: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			sites := []uuid.UUID{f.indexer}
			second := uuid.Nil
			if mode == "conflicting_sites" {
				second = uuid.New()
				execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) SELECT $1,'Second TV source',url,api_key_encrypted,categories,health FROM indexer WHERE id=$2`, second, f.indexer)
				execIntent(t, f.pool, `INSERT INTO indexer_configuration(indexer_id,protocol,url_encrypted,behavior_version,configuration_version) SELECT $1,protocol,url_encrypted,behavior_version,configuration_version FROM indexer_configuration WHERE indexer_id=$2`, second, f.indexer)
				execIntent(t, f.pool, `INSERT INTO indexer_lifecycle(indexer_id,enabled) VALUES($1,true)`, second)
				sites = append(sites, second)
			}
			binding := subscription.NewBindingService(postgres.NewSubscriptionBindingStore(f.pool, idgen.New()))
			before, err := q.GetSubscriptionByID(ctx, f.sub)
			if err != nil {
				t.Fatal(err)
			}
			command := ports.SubscriptionBindingsCommand{ExpectedVersion: before.IntentVersion, IndexerIDs: sites}
			var originalReceipt *ports.SubscriptionBindingsInfo
			var receiptBytes []byte
			if mode != "manual_binding" {
				originalReceipt, err = binding.Configure(ctx, f.actor, f.sub, command, "route-tv-clear")
				if err != nil {
					t.Fatal(err)
				}
				record, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(f.actor.Type), ActorID: f.actor.ID, IdempotencyKey: "route-tv-clear"})
				if err != nil {
					t.Fatal(err)
				}
				receiptBytes = append([]byte(nil), record.ResponseBody...)
			}
			before, err = q.GetSubscriptionByID(ctx, f.sub)
			if err != nil {
				t.Fatal(err)
			}
			readIntent := func() []byte {
				t.Helper()
				var raw []byte
				if err := f.pool.QueryRow(ctx, `SELECT jsonb_build_object('subscription',to_jsonb(s),'wanted',(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM wanted_item w WHERE w.subscription_id=s.id)) FROM subscription s WHERE s.id=$1`, f.sub).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			beforeRaw := readIntent()
			rules := []endpoint.DownloadRouteRule{{MediaType: "tv", DownloaderID: target}}
			if mode == "single_client" {
				rules = nil
			}
			if mode == "site_override" {
				rules = []endpoint.DownloadRouteRule{{MediaType: "tv", DownloaderID: f.client}, {IndexerID: f.indexer, DownloaderID: target}}
			}
			if mode == "unavailable_rule" {
				rules[0].DownloaderID = uuid.New()
			}
			if mode == "conflicting_sites" {
				rules = append(rules, endpoint.DownloadRouteRule{IndexerID: second, DownloaderID: f.client})
			}
			f.repo = postgres.NewAutomaticTVRoundStoreWithRouting(f.pool, idgen.New(), rules)
			if mode == "tail_rollback" {
				execIntent(t, f.pool, `CREATE FUNCTION reject_tv_routing_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'TV routing tail fault'; END $$; CREATE CONSTRAINT TRIGGER reject_tv_routing_tail AFTER INSERT ON automatic_tv_round DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_tv_routing_tail()`)
			}
			if mode == "unavailable_rule" || mode == "conflicting_sites" || mode == "tail_rollback" {
				if _, err = f.repo.StartAutomaticTVRound(ctx, ports.AutomaticTVRoundStart{WantedID: f.wanted}); err == nil {
					t.Fatal("invalid or rolled-back routing started a round")
				}
				if !bytes.Equal(beforeRaw, readIntent()) {
					t.Fatal("failed route changed subscription or Wanted")
				}
				if intentCount(t, f.pool, `SELECT (SELECT count(*) FROM automatic_tv_round)+(SELECT count(*) FROM audit_log WHERE action='subscription.downloader_routed')+(SELECT count(*) FROM outbox_event WHERE event_type='subscription.downloader_routed')+(SELECT count(*) FROM job WHERE kind IN ('automatic_tv_source','automatic_tv_finalize','download_submission'))`) != 0 {
					t.Fatal("failed route leaked work or success records")
				}
				if mode != "tail_rollback" {
					return
				}
				execIntent(t, f.pool, `DROP TRIGGER reject_tv_routing_tail ON automatic_tv_round`)
			}
			var round *ports.AutomaticTVRound
			if mode == "concurrent_start" {
				var wg sync.WaitGroup
				rounds := make(chan *ports.AutomaticTVRound, 8)
				errs := make(chan error, 8)
				for range 8 {
					wg.Go(func() {
						r, e := f.repo.StartAutomaticTVRound(ctx, ports.AutomaticTVRoundStart{WantedID: f.wanted})
						rounds <- r
						errs <- e
					})
				}
				wg.Wait()
				close(rounds)
				close(errs)
				for e := range errs {
					if e != nil {
						t.Fatal(e)
					}
				}
				for r := range rounds {
					if round == nil {
						round = r
					} else if r.ID != round.ID {
						t.Fatal("concurrent auto-binding created duplicate rounds")
					}
				}
			} else {
				round = f.start(t)
			}
			expected := target
			expectedVersion := before.IntentVersion + 1
			autoCount := 1
			if mode == "manual_binding" {
				expected = f.client
				expectedVersion = before.IntentVersion
				autoCount = 0
			}
			current, err := q.GetSubscriptionByID(ctx, f.sub)
			if err != nil || !current.DownloaderID.Valid || uuid.UUID(current.DownloaderID.Bytes) != expected || current.IntentVersion != expectedVersion || round.Snapshot.ClientID != expected || round.Snapshot.IntentVersion != expectedVersion {
				t.Fatal("binding and snapshot did not commit the same identity/version", err)
			}
			var mismatched int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND intent_version<>$2`, f.sub, expectedVersion).Scan(&mismatched); err != nil || mismatched != 0 {
				t.Fatal("Wanted retained stale intents", err)
			}
			if intentCount(t, f.pool, `SELECT count(*) FROM audit_log WHERE action='subscription.downloader_routed'`) != autoCount || intentCount(t, f.pool, `SELECT count(*) FROM outbox_event WHERE event_type='subscription.downloader_routed'`) != autoCount {
				t.Fatal("automatic binding audit/event was not unique")
			}
			if originalReceipt != nil {
				restored, err := binding.Configure(ctx, f.actor, f.sub, command, "route-tv-clear")
				if err != nil || restored.IntentVersion != originalReceipt.IntentVersion || restored.DownloaderID != nil {
					t.Fatal("original binding receipt was replaced", err)
				}
				record, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(f.actor.Type), ActorID: f.actor.ID, IdempotencyKey: "route-tv-clear"})
				if err != nil || !bytes.Equal(receiptBytes, record.ResponseBody) {
					t.Fatal("historical receipt bytes changed", err)
				}
			}
			if mode == "changed_rules" {
				f.repo = postgres.NewAutomaticTVRoundStoreWithRouting(f.pool, idgen.New(), []endpoint.DownloadRouteRule{{MediaType: "tv", DownloaderID: f.client}})
			}
			if mode == "changed_client" {
				execIntent(t, f.pool, `UPDATE download_client SET url='https://changed.test' WHERE id=$1`, target)
				job, err := q.ClaimJob(ctx, db.ClaimJobParams{Column1: []string{"automatic_tv_source"}, Column2: "automatic-tv-test", Column3: 60000})
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.repo.LoadAutomaticTVSource(ctx, ports.AutomaticTVSourceTask{RoundID: round.ID, IndexerID: f.indexer, Lease: ports.JobLease{ID: job.ID, Owner: "automatic-tv-test", Token: job.LeaseToken}})
				if !errors.Is(err, ports.ErrAcquisitionRoundAborted) {
					t.Fatal("changed selected client was accepted", err)
				}
				return
			}
			work := f.load(t, round)
			if work.SnapshotHash != round.SnapshotHash {
				t.Fatal("load changed the original frozen selection")
			}
		})
	}
}

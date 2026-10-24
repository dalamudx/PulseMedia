package search_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func routedMovieCoordinator(f *roundFixture, rules []endpoint.DownloadRouteRule) *usecaseSearch.RoundCoordinator {
	gen, clk := idgen.New(), clock.New()
	return usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStoreWithRouting(f.pool, gen, rules), clk, acquisition.NewService(postgres.NewAcquisitionStore(f.pool, gen, clk, acquisition.SelectMovieRound)))
}

func TestMovieRoutingFreezesSourceChoiceBeforeReservationPG(t *testing.T) {
	for _, mode := range []string{"media_rule", "site_overrides_media", "manual_binding", "rule_changed_after_freeze", "selected_endpoint_changed", "unselected_endpoint_changed", "tail_rollback", "unavailable_rule"} {
		t.Run(mode, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx := context.Background()
			original, err := f.q.GetDownloadClientByID(ctx, f.downloader)
			if err != nil {
				t.Fatal(err)
			}
			target := uuid.New()
			if _, err = f.q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: target, Name: "TV name is not an instruction", ClientType: "qbittorrent", Url: "https://second.invalid", PasswordEncrypted: original.PasswordEncrypted, RemotePathMap: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			if mode != "manual_binding" {
				before, err := f.q.GetSubscriptionByID(ctx, f.sub)
				if err != nil {
					t.Fatal(err)
				}
				bindings := subscription.NewBindingService(postgres.NewSubscriptionBindingStore(f.pool, idgen.New()))
				command := ports.SubscriptionBindingsCommand{IndexerIDs: []uuid.UUID{f.idx}, ExpectedVersion: before.IntentVersion}
				receipt, err := bindings.Configure(ctx, f.actor, f.sub, command, "route-automatic")
				if err != nil || receipt.DownloaderID != nil {
					t.Fatal("clearing the manual binding failed", err)
				}
			}
			rules := []endpoint.DownloadRouteRule{{MediaType: "movie", DownloaderID: target}}
			if mode == "site_overrides_media" {
				rules = []endpoint.DownloadRouteRule{{MediaType: "movie", DownloaderID: f.downloader}, {IndexerID: f.idx, DownloaderID: target}}
			}
			if mode == "unavailable_rule" {
				rules[0].DownloaderID = uuid.New()
			}
			f.coord = routedMovieCoordinator(f, rules)
			if mode == "unavailable_rule" {
				if _, err = f.coord.StartScheduledRound(ctx, f.wanted, time.Minute); !errors.Is(err, ports.ErrUnavailable) {
					t.Fatal("unavailable explicit rule silently fell back", err)
				}
				var rounds, jobs, reservations int
				if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM search_round),(SELECT count(*) FROM job WHERE kind IN ('search_endpoint','download_submission')),(SELECT count(*) FROM unit_reservation)`).Scan(&rounds, &jobs, &reservations); err != nil || rounds != 0 || jobs != 0 || reservations != 0 {
					t.Fatal("failed route left business work", err)
				}
				return
			}
			round := f.collect(t)
			persisted, err := f.q.GetSearchRoundByID(ctx, round.ID)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot ports.RoundSnapshot
			if err = json.Unmarshal(persisted.DecisionSnapshot, &snapshot); err != nil {
				t.Fatal(err)
			}
			expected := target
			if mode == "manual_binding" {
				expected = f.downloader
				if snapshot.SelectedDownloaderID == nil || *snapshot.SelectedDownloaderID != expected || strings.Contains(string(persisted.DecisionSnapshot), "routed_downloaders") {
					t.Fatal("manual/legacy snapshot was rewritten")
				}
			} else {
				selected, ok := snapshot.RoutedDownloaders[f.idx.String()]
				if !ok || selected.DownloaderID != target || selected.ConfigDigest == "" || selected.PolicyVersion != 1 {
					t.Fatal("source choice was not frozen")
				}
				if mode == "site_overrides_media" && selected.Basis != "site_rule" {
					t.Fatal("site rule did not take precedence")
				}
			}
			if mode == "rule_changed_after_freeze" {
				f.coord = routedMovieCoordinator(f, []endpoint.DownloadRouteRule{{MediaType: "movie", DownloaderID: f.downloader}})
			}
			if mode == "selected_endpoint_changed" || mode == "unselected_endpoint_changed" {
				changed := target
				if mode == "unselected_endpoint_changed" {
					changed = f.downloader
				}
				if _, err = f.pool.Exec(ctx, `UPDATE download_client SET url='https://changed.invalid' WHERE id=$1`, changed); err != nil {
					t.Fatal(err)
				}
			}
			job := f.finalJob(t, round)
			if mode == "tail_rollback" {
				if _, err = f.pool.Exec(ctx, `CREATE FUNCTION fail_routed_movie() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='completed' THEN RAISE EXCEPTION 'routed movie tail fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_routed_movie BEFORE UPDATE ON search_round FOR EACH ROW EXECUTE FUNCTION fail_routed_movie()`); err != nil {
					t.Fatal(err)
				}
				if _, err = f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job)); err == nil {
					t.Fatal("tail failure committed")
				}
				var leaked int
				if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM job WHERE kind='download_submission')`).Scan(&leaked); err != nil || leaked != 0 {
					t.Fatal("tail rollback leaked business work", err)
				}
				if _, err = f.pool.Exec(ctx, `DROP TRIGGER fail_routed_movie ON search_round`); err != nil {
					t.Fatal(err)
				}
			}
			decision, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
			if mode == "selected_endpoint_changed" {
				if err == nil || decision == nil || decision.State != "aborted" || decision.OperationID != uuid.Nil {
					t.Fatal("stale selected endpoint was accepted", err)
				}
				var leaked int
				if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM job WHERE kind='download_submission')`).Scan(&leaked); err != nil || leaked != 0 {
					t.Fatal("aborted route created business work", err)
				}
				return
			}
			if err != nil || decision == nil || decision.State != "completed" {
				t.Fatal("routed reservation failed", err)
			}
			var selected uuid.UUID
			var count int
			if err = f.pool.QueryRow(ctx, `SELECT (payload->>'client_id')::uuid,(SELECT count(*) FROM job WHERE kind='download_submission') FROM job WHERE kind='download_submission' AND payload->>'operation_id'=$1`, decision.OperationID.String()).Scan(&selected, &count); err != nil || selected != expected || count != 1 {
				t.Fatal("send job did not retain the original source-specific client", err)
			}
			after, err := f.q.GetSearchRoundByID(ctx, round.ID)
			if err != nil || string(after.DecisionSnapshot) != string(persisted.DecisionSnapshot) {
				t.Fatal("finalization rewrote frozen evidence", err)
			}
		})
	}
}

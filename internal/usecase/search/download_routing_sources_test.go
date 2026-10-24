package search_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestMovieRoutingUsesActualWinningSourcePG(t *testing.T) {
	for _, mode := range []string{"first_source", "second_source"} {
		t.Run(mode, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx := context.Background()
			original, err := f.q.GetDownloadClientByID(ctx, f.downloader)
			if err != nil {
				t.Fatal(err)
			}
			secondClient, secondSource := uuid.New(), uuid.New()
			if _, err = f.q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: secondClient, Name: "Second configured client", ClientType: "transmission", Url: "https://second-client.invalid", PasswordEncrypted: original.PasswordEncrypted, RemotePathMap: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			source, err := f.q.GetIndexerByID(ctx, f.idx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.q.CreateIndexer(ctx, db.CreateIndexerParams{ID: secondSource, Name: "Second source", Url: "https://second-tracker.example", ApiKeyEncrypted: source.ApiKeyEncrypted, Categories: []byte(`[]`)}); err != nil {
				t.Fatal(err)
			}
			title := "Fight.Club.1998.2160p.BluRay"
			expected := f.downloader
			if mode == "second_source" {
				title = "Fight.Club.1999.2160p.BluRay"
				expected = secondClient
			}
			f.svc.RegisterClient(secondSource, &mockIndexerClient{releases: []*ports.ReleaseItem{{Title: title, GUID: "second-source-candidate", SizeBytes: 1024, DownloadURL: "https://second-tracker.example/dl", Seeders: 100}}})
			before, err := f.q.GetSubscriptionByID(ctx, f.sub)
			if err != nil {
				t.Fatal(err)
			}
			binding := subscription.NewBindingService(postgres.NewSubscriptionBindingStore(f.pool, idgen.New()))
			if _, err = binding.Configure(ctx, f.actor, f.sub, ports.SubscriptionBindingsCommand{ExpectedVersion: before.IntentVersion, IndexerIDs: []uuid.UUID{f.idx, secondSource}}, "multi-source-route"); err != nil {
				t.Fatal(err)
			}
			f.coord = routedMovieCoordinator(f, []endpoint.DownloadRouteRule{{MediaType: "movie", DownloaderID: f.downloader}, {IndexerID: secondSource, DownloaderID: secondClient}})
			round, err := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := f.q.GetSearchRoundByID(ctx, round.ID)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot ports.RoundSnapshot
			if err = json.Unmarshal(saved.DecisionSnapshot, &snapshot); err != nil || len(snapshot.RoutedDownloaders) != 2 || snapshot.RoutedDownloaders[f.idx.String()].DownloaderID != f.downloader || snapshot.RoutedDownloaders[secondSource.String()].DownloaderID != secondClient {
				t.Fatal("per-source choices were collapsed", err)
			}
			for range 2 {
				job, err := f.queue.Claim(ctx, []string{"search_endpoint"}, "multi-source-endpoint", time.Minute)
				if err != nil || job == nil {
					t.Fatal("missing source execution", err)
				}
				if err = f.svc.ExecuteEndpointJob(ctx, endpointTask(t, job)); err != nil {
					t.Fatal(err)
				}
			}
			finalJob := f.finalJob(t, round)
			decision, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(finalJob))
			if err != nil || decision == nil || decision.State != "completed" {
				t.Fatal("source winner could not reserve", err)
			}
			var clientID uuid.UUID
			if err = f.pool.QueryRow(ctx, `SELECT (payload->>'client_id')::uuid FROM job WHERE kind='download_submission' AND payload->>'operation_id'=$1`, decision.OperationID.String()).Scan(&clientID); err != nil || clientID != expected {
				t.Fatal("actual winner was routed to a different source's client", err)
			}
			winner, err := f.q.GetCandidateByID(ctx, *decision.SelectedCandidateID)
			if err != nil {
				t.Fatal(err)
			}
			expectedSource := f.idx
			if mode == "second_source" {
				expectedSource = secondSource
			}
			if winner.IndexerID != expectedSource {
				t.Fatal("quality/identity selection did not choose the expected real source")
			}
		})
	}
}

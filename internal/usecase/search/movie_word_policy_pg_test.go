package search_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func TestMovieWordPolicyActualSelectionPG(t *testing.T) {
	const high = "Fight.Club.1999.2160p.BluRay.x264-TRASH"
	const low = "Fight.Club.1999.1080p.BluRay.Atmos.x264-BOOST"
	for _, tc := range []struct {
		name  string
		words []quality.CustomWordRule
		title string
	}{
		{"positive_preference", []quality.CustomWordRule{{Word: "Atmos", Score: 500}}, low},
		{"negative_preference", []quality.CustomWordRule{{Word: "Atmos", Score: -1000}}, high},
		{"required", []quality.CustomWordRule{{Word: "Atmos", Required: true}}, low},
		{"excluded", []quality.CustomWordRule{{Word: "TRASH", Excluded: true}}, low},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx := t.Context()
			gen := idgen.New()
			service := policy.NewService(postgres.NewPolicyStore(f.pool, gen))
			doc, err := service.Read(ctx, "default", 0)
			if err != nil {
				t.Fatal(err)
			}
			rules := doc.Rules
			rules.CustomWords = tc.words
			const policyID = "actual-movie-words"
			if _, err = service.Command(ctx, f.actor, ports.PolicyCommand{Kind: "create", ID: policyID, Name: "Actual movie words", Rules: &rules}, "actual-words-create"); err != nil {
				t.Fatal(err)
			}
			before, err := f.q.GetSubscriptionByID(ctx, f.sub)
			if err != nil {
				t.Fatal(err)
			}
			id := policyID
			binding := subscription.NewBindingService(postgres.NewSubscriptionBindingStore(f.pool, gen))
			if _, err = binding.Configure(ctx, f.actor, f.sub, ports.SubscriptionBindingsCommand{ExpectedVersion: before.IntentVersion, IndexerIDs: []uuid.UUID{f.idx}, DownloaderID: &f.downloader, QualityPolicyID: &id}, "actual-words-bind"); err != nil {
				t.Fatal(err)
			}
			f.svc.RegisterClient(f.idx, &mockIndexerClient{releases: []*ports.ReleaseItem{
				{Title: high, GUID: "high", SizeBytes: 1024, DownloadURL: "https://tracker.invalid/high", Seeders: 1000},
				{Title: low, GUID: "low", SizeBytes: 1024, DownloadURL: "https://tracker.invalid/low", Seeders: 1},
			}})
			round := f.collect(t)
			job := f.finalJob(t, round)
			decision, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
			if err != nil || decision == nil || decision.State != "completed" || decision.SelectedCandidateID == nil {
				t.Fatal("word policy did not create a valid reservation", err)
			}
			winner, err := f.q.GetCandidateByID(ctx, *decision.SelectedCandidateID)
			if err != nil || winner.Title != tc.title {
				t.Fatal("wrong actual candidate selected", err)
			}
			var profile quality.Profile
			if json.Unmarshal(winner.QualityProfile, &profile) != nil || !quality.KnownProfile(profile) {
				t.Fatal("selected profile not trustworthy")
			}
			preview := quality.EvaluateCandidate(rules, winner.Title, profile)
			if !preview.Passed || decision.BestScore != preview.Score {
				t.Fatal("preference leaked into frozen quality score")
			}
			var reservations, submissions int
			if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM job WHERE kind='download_submission' AND payload->>'operation_id'=$1)`, decision.OperationID.String()).Scan(&reservations, &submissions); err != nil || reservations != 1 || submissions != 1 {
				t.Fatal("selection created duplicate or missing reservation/send", err)
			}
			frozen, err := f.q.GetSearchRoundByID(ctx, round.ID)
			var snapshot ports.RoundSnapshot
			if err != nil || json.Unmarshal(frozen.DecisionSnapshot, &snapshot) != nil || snapshot.PolicyID != policyID {
				t.Fatal("selection lost frozen word policy", err)
			}
		})
	}
}

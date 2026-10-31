package postgres_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	usecaseAcquisition "github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

type movieDiscoveryPageClient struct{ calls int }

func (c *movieDiscoveryPageClient) Search(ctx context.Context, k string, cats []int32, o, l int32) ([]*ports.ReleaseItem, error) {
	p, e := c.SearchPage(ctx, k, cats, o, l)
	if e != nil {
		return nil, e
	}
	return p.Items, nil
}
func (c *movieDiscoveryPageClient) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrRejected
}
func (c *movieDiscoveryPageClient) SearchPage(_ context.Context, k string, _ []int32, o, l int32) (*ports.IndexerPage, error) {
	c.calls++
	if k != "Fight Club 1999" || (o != 0 && o != 50) || l != 50 {
		return nil, ports.ErrBadResponse
	}
	total := int64(51)
	count := 50
	if o == 50 {
		count = 1
	}
	items := make([]*ports.ReleaseItem, 0, count)
	for i := 0; i < count; i++ {
		n := int(o) + i
		title := "Fight.Club.1999.1080p.Bluray.x264"
		if n == 50 {
			title = "Fight.Club.1999.2160p.Bluray.x265"
		}
		items = append(items, &ports.ReleaseItem{GUID: fmt.Sprintf("discovery-%d", n), Title: title, DownloadURL: fmt.Sprintf("https://source.test/item/%d", n), SizeBytes: 1000, Seeders: 5})
	}
	return &ports.IndexerPage{Offset: o, Limit: l, OffsetVerified: true, Total: &total, Items: items}, nil
}
func TestMovieDiscoveryProductionSearchPreviewPG(t *testing.T) {
	f, s := movieDiscoveryFixture(t)
	ctx := t.Context()
	before := discoveryState(t, f)
	scheduler := usecaseSearch.NewMovieDiscoveryService(s)
	if n, e := scheduler.RunBatch(ctx, 20); e != nil || n != 1 {
		t.Fatalf("tick %d %v", n, e)
	}
	status, e := scheduler.Read(ctx, f.actor, f.sub)
	if e != nil || status.SearchID == nil {
		t.Fatal(status, e)
	}
	searches := postgres.NewInteractiveSearchStore(f.p, idgen.New())
	task := interactiveClaim(t, f.p, &ports.InteractiveSearchHeader{ID: *status.SearchID}, f.source)
	lease, e := searches.LoadInteractiveSearchSource(ctx, task)
	if e != nil {
		t.Fatal("load source", e)
	}
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{17}, 32))
	executor := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.p), idgen.New(), codec, 0)
	client := &movieDiscoveryPageClient{}
	executor.RegisterClient(f.source, client)
	if e = executor.ExecuteInteractiveSearchSource(ctx, *lease); e != nil {
		t.Fatal("real query executor", e)
	}
	if e = searches.FinishInteractiveSearchSource(ctx, *lease, "succeeded"); e != nil {
		t.Fatal("finish source", e)
	}
	snapshot, e := usecaseSearch.NewInteractiveSearchService(searches, codec).Read(ctx, f.actor, *status.SearchID)
	if e != nil || snapshot == nil || len(snapshot.Candidates) != 51 || snapshot.TotalCandidates != 51 || snapshot.State != "partial" || !snapshot.Partial || snapshot.Truncated || len(snapshot.Sources) != 1 || snapshot.Sources[0].State != "succeeded" || !snapshot.Sources[0].Partial || len(snapshot.Sources[0].QueryIDs) != 2 || client.calls != 2 {
		t.Fatalf("candidate snapshot %+v %v calls=%d", snapshot, e, client.calls)
	}
	previews := usecaseAcquisition.NewUpgradePreviewService(postgres.NewUpgradePreviewStore(f.p))
	better := 0
	same := 0
	for _, candidate := range snapshot.Candidates {
		if candidate.Candidate.Guid != "discovery-50" && candidate.Candidate.Guid != "discovery-0" {
			continue
		}
		preview, e := previews.Preview(ctx, f.actor, ports.UpgradePreviewRequest{SubscriptionID: f.sub, CurrentFileID: f.file, CandidateID: candidate.Candidate.ID})
		if e != nil || preview.ExecutionAllowed {
			t.Fatalf("preview %+v %v", preview, e)
		}
		if candidate.Candidate.Guid == "discovery-50" {
			if !preview.Improvement || !preview.Comparable {
				t.Fatalf("better candidate %+v", preview)
			}
			better++
		}
		if candidate.Candidate.Guid == "discovery-0" {
			if preview.Improvement {
				t.Fatal("raw same-tier candidate called upgrade")
			}
			same++
		}
	}
	if better != 1 || same != 1 {
		t.Fatalf("qualification better=%d same=%d", better, same)
	}
	if after := discoveryState(t, f); after != before {
		t.Fatal("source query or preview created download, reservation or changed original file")
	}
	var pages int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM interactive_search_query a JOIN indexer_query_page p ON p.query_id=a.query_id WHERE a.search_id=$1`, *status.SearchID).Scan(&pages); e != nil || pages != 2 {
		t.Fatalf("page proof=%d %v", pages, e)
	}
}

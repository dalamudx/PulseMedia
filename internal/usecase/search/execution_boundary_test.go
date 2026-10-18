package search_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"reflect"
	"testing"
	"time"
)

type executionBoundaryRepo struct {
	ports.SearchRepository
	indexer ports.SearchIndexer
	spec    ports.SearchQuerySpec
	permit  *executionBoundaryPermit
	shared  *ports.IndexerQueryResult
}

func (r *executionBoundaryRepo) GetIndexer(context.Context, uuid.UUID) (ports.SearchIndexer, error) {
	return r.indexer, nil
}
func (r *executionBoundaryRepo) AdmitQuery(_ context.Context, spec ports.SearchQuerySpec) (ports.SearchQueryAdmission, ports.SearchExecutionPermit, error) {
	r.spec = spec
	r.permit.events = append(r.permit.events, "admit")
	if r.shared != nil {
		return ports.SearchQueryAdmission{Indexer: r.indexer, Shared: r.shared}, nil, nil
	}
	return ports.SearchQueryAdmission{Indexer: r.indexer}, r.permit, nil
}

type executionBoundaryPermit struct {
	events  []string
	commit  ports.SearchQueryCommit
	failure ports.SearchQueryFailure
}

func (p *executionBoundaryPermit) BeginQuery(context.Context) (*ports.IndexerQueryResult, error) {
	p.events = append(p.events, "begin")
	return &ports.IndexerQueryResult{QueryID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), State: "running"}, nil
}
func (p *executionBoundaryPermit) ReserveHealthSearch(context.Context) error {
	p.events = append(p.events, "reserve-health")
	return nil
}
func (p *executionBoundaryPermit) CommitQuery(_ context.Context, c ports.SearchQueryCommit) (*ports.IndexerQueryResult, error) {
	p.events = append(p.events, "commit")
	p.commit = c
	return &ports.IndexerQueryResult{State: "succeeded", ResultsCount: len(c.Candidates)}, nil
}
func (p *executionBoundaryPermit) FailQuery(_ context.Context, f ports.SearchQueryFailure, _ time.Duration) error {
	p.events = append(p.events, "fail")
	p.failure = f
	return nil
}
func (p *executionBoundaryPermit) Close() { p.events = append(p.events, "close") }

type executionBoundaryIDs struct{}

func (executionBoundaryIDs) NewUUID() uuid.UUID    { return uuid.New() }
func (executionBoundaryIDs) NewUUIDString() string { return uuid.NewString() }

type executionBoundarySecrets struct{ p *executionBoundaryPermit }

func (s executionBoundarySecrets) EncryptSecret([]byte) ([]byte, error) {
	s.p.events = append(s.p.events, "encrypt")
	return []byte("opaque-existing-envelope"), nil
}
func (executionBoundarySecrets) DecryptSecret([]byte) ([]byte, error) {
	return []byte("credential"), nil
}
func (executionBoundarySecrets) DigestSecret([]byte) (string, error) {
	panic("search GUIDs must retain original SHA256 format")
}

type executionBoundaryClient struct {
	p     *executionBoundaryPermit
	items []*ports.ReleaseItem
}

func (c *executionBoundaryClient) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	c.p.events = append(c.p.events, "network")
	return c.items, nil
}
func (c *executionBoundaryClient) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrUnavailable
}
func (c *executionBoundaryClient) Capabilities(context.Context) (ports.IndexerCapabilities, error) {
	c.p.events = append(c.p.events, "caps")
	return ports.IndexerCapabilities{MovieSearch: ports.IndexerSearchCapability{Available: true, SupportedParams: []string{"q"}}}, nil
}

func TestSearchPureBoundaryPreparesBeforeCommitAndKeepsSHAFormat(t *testing.T) {
	permit := &executionBoundaryPermit{}
	idx := ports.SearchIndexer{ID: uuid.New(), ConfigVersion: 7, Health: "online", Categories: []byte("[]")}
	repo := &executionBoundaryRepo{indexer: idx, permit: permit}
	svc := search.NewService(repo, executionBoundaryIDs{}, executionBoundarySecrets{permit}, 0)
	rawURL := "https://source.invalid/download?passkey=private"
	svc.RegisterClient(idx.ID, &executionBoundaryClient{p: permit, items: []*ports.ReleaseItem{{Title: "Fight.Club.1999.1080p.BluRay", GUID: rawURL, DownloadURL: rawURL, SizeBytes: 1024}}})
	result, err := svc.ExecuteSearch(context.Background(), idx.ID, "Fight Club", 0)
	if err != nil || result.ResultsCount != 1 {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(permit.events, []string{"admit", "begin", "network", "encrypt", "commit", "close"}) {
		t.Fatal(permit.events)
	}
	sum := sha256.Sum256([]byte(rawURL))
	candidate := permit.commit.Candidates[0]
	if candidate.Guid != "sha256:"+hex.EncodeToString(sum[:]) || string(candidate.LinkEncrypted) != "opaque-existing-envelope" || candidate.SourceConfigVersion != 7 || candidate.InfoHash != nil || candidate.PublishDate != nil {
		t.Fatal(candidate)
	}
	if repo.spec.MinimumInterval != 0 || permit.commit.Page == nil || permit.commit.Page.ItemCount != 1 {
		t.Fatal(repo.spec, permit.commit)
	}
}
func TestSearchPureBoundarySharedResultHasNoExecutionAuthority(t *testing.T) {
	permit := &executionBoundaryPermit{}
	idx := ports.SearchIndexer{ID: uuid.New(), ConfigVersion: 1}
	shared := &ports.IndexerQueryResult{QueryID: uuid.New(), State: "running", FromShared: true}
	repo := &executionBoundaryRepo{indexer: idx, permit: permit, shared: shared}
	svc := search.NewService(repo, executionBoundaryIDs{}, executionBoundarySecrets{permit})
	result, err := svc.ExecuteSearch(context.Background(), idx.ID, "shared", 0)
	if err != nil || result != shared || !reflect.DeepEqual(permit.events, []string{"admit"}) {
		t.Fatal(result, err, permit.events)
	}
	if repo.spec.MinimumInterval != 2*time.Second {
		t.Fatal("default durable interval changed", repo.spec.MinimumInterval)
	}
}
func TestSearchPureBoundaryRejectsInvalidReleaseBeforeEncryptionOrCommit(t *testing.T) {
	permit := &executionBoundaryPermit{}
	idx := ports.SearchIndexer{ID: uuid.New(), ConfigVersion: 1, Categories: []byte("[]")}
	repo := &executionBoundaryRepo{indexer: idx, permit: permit}
	svc := search.NewService(repo, executionBoundaryIDs{}, executionBoundarySecrets{permit}, 0)
	svc.RegisterClient(idx.ID, &executionBoundaryClient{p: permit, items: []*ports.ReleaseItem{{Title: "invalid", DownloadURL: "https://source.invalid", SizeBytes: -1}}})
	if _, err := svc.ExecuteSearch(context.Background(), idx.ID, "invalid", 0); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	if permit.failure != ports.SearchFailureInvalidResult || !reflect.DeepEqual(permit.events, []string{"admit", "begin", "network", "fail", "close"}) {
		t.Fatal(permit.failure, permit.events)
	}
}
func TestSearchPureBoundaryHealthCapsAndSearchReserveSeparately(t *testing.T) {
	permit := &executionBoundaryPermit{}
	idx := ports.SearchIndexer{ID: uuid.New(), ConfigVersion: 1, Categories: []byte("[]")}
	repo := &executionBoundaryRepo{indexer: idx, permit: permit}
	svc := search.NewService(repo, executionBoundaryIDs{}, executionBoundarySecrets{permit}, 0)
	svc.RegisterClient(idx.ID, &executionBoundaryClient{p: permit, items: []*ports.ReleaseItem{{Title: "ignored probe result"}}})
	if _, err := svc.ProbeIndexer(context.Background(), ports.IndexerHealthJob{ID: uuid.New(), IndexerID: idx.ID, ConfigVersion: 1, Owner: "owner", Token: 3}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(permit.events, []string{"admit", "begin", "caps", "reserve-health", "network", "commit", "close"}) || len(permit.commit.Candidates) != 0 || permit.commit.Page != nil {
		t.Fatal(permit.events, permit.commit)
	}
	if repo.spec.HealthJob == nil || repo.spec.HealthJob.Token != 3 || !repo.spec.ProbeOnly {
		t.Fatal(repo.spec)
	}
}

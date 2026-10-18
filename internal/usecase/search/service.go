package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type SearchResult = ports.IndexerQueryResult

type Service struct {
	repo              ports.SearchRepository
	idgen             ports.IDGen
	secrets           ports.SearchSecrets
	mu                sync.RWMutex
	clients           map[uuid.UUID]ports.IndexerClient
	factory           IndexerClientFactory
	configuredFactory ConfiguredIndexerClientFactory
	feedFactory       FeedClientFactory
	versions          map[uuid.UUID]int64
	minimumInterval   time.Duration
	healthStore       ports.IndexerHealthRepository
	endpointStore     ports.SearchEndpointRepository
}

func NewService(repo ports.SearchRepository, idgen ports.IDGen, secrets ports.SearchSecrets, minimum ...time.Duration) *Service {
	interval := 2 * time.Second
	if len(minimum) > 0 && minimum[0] >= 0 {
		interval = minimum[0]
	}

	return &Service{
		repo:    repo,
		idgen:   idgen,
		secrets: secrets,
		clients: make(map[uuid.UUID]ports.IndexerClient), versions: make(map[uuid.UUID]int64), minimumInterval: interval,
	}
}

func (s *Service) SetHealthRepository(repo ports.IndexerHealthRepository)    { s.healthStore = repo }
func (s *Service) SetEndpointRepository(repo ports.SearchEndpointRepository) { s.endpointStore = repo }

func (s *Service) RegisterClient(indexerID uuid.UUID, client ports.IndexerClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[indexerID] = client
}

func (s *Service) client(indexerID uuid.UUID) ports.IndexerClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clients[indexerID]
}

// Constructors run outside the registry lock; no network I/O holds it.
func (s *Service) ensureClient(idx ports.SearchIndexer) error {
	s.mu.RLock()
	current := s.clients[idx.ID]
	version := s.versions[idx.ID]
	s.mu.RUnlock()
	if current != nil && (version == idx.ConfigVersion || version == 0) {
		return nil
	}
	s.mu.RLock()
	factory, configured := s.factory, s.configuredFactory
	s.mu.RUnlock()
	if factory == nil && configured == nil {
		return ports.ErrUnavailable
	}
	key, err := s.secrets.DecryptSecret(idx.ApiKeyEncrypted)
	if err != nil {
		return ports.ErrUnavailable
	}
	var client ports.IndexerClient
	if configured != nil && idx.Protocol != "" {
		client = configured(idx.Protocol, idx.Url, string(key))
	} else if factory != nil {
		client = factory(idx.Url, string(key))
	}
	if client == nil {
		return ports.ErrUnavailable
	}
	s.mu.Lock()
	s.clients[idx.ID] = client
	s.versions[idx.ID] = idx.ConfigVersion
	s.mu.Unlock()
	return nil
}

// ExecuteSearch executes an indexer query with active query deduplication and release candidate persistence.
func (s *Service) ExecuteSearch(ctx context.Context, indexerID uuid.UUID, keyword string, page int32) (*SearchResult, error) {
	return s.execute(ctx, indexerID, keyword, page, uuid.Nil, 0, nil, false, nil)
}

// ExecuteRSS shares physical permits and persisted budgets with all searches.
func (s *Service) ExecuteRSS(ctx context.Context, indexerID, jobID uuid.UUID, version int64) (*SearchResult, error) {
	return s.execute(ctx, indexerID, "", 0, jobID, version, nil, false, nil)
}

func (s *Service) execute(ctx context.Context, indexerID uuid.UUID, keyword string, page int32, rssJob uuid.UUID, expected int64, healthJob *ports.IndexerHealthJob, probeOnly bool, chain *pageChain, actors ...*ports.Actor) (*SearchResult, error) {
	if page < 0 || page > math.MaxInt32/50 {
		return nil, ports.ErrBadResponse
	}
	indexer, err := s.repo.GetIndexer(ctx, indexerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load indexer: %w", err)
	}
	if expected != 0 && indexer.ConfigVersion != expected {
		return nil, ports.ErrUnavailable
	}
	if expected == 0 {
		expected = indexer.ConfigVersion
	}
	var actor *ports.Actor
	if len(actors) > 0 {
		actor = actors[0]
	}
	admission, permit, err := s.repo.AdmitQuery(ctx, ports.SearchQuerySpec{ID: s.idgen.NewUUID(), IndexerID: indexerID, Keyword: keyword, Page: page, ConfigVersion: expected, RSSJobID: rssJob, HealthJob: healthJob, Actor: actor, ProbeOnly: probeOnly, MinimumInterval: s.minimumInterval})
	if err != nil {
		return nil, err
	}
	if admission.Shared != nil {
		return admission.Shared, nil
	}
	if permit == nil {
		return nil, ports.ErrUnavailable
	}
	defer permit.Close()
	indexer = admission.Indexer
	var client ports.IndexerClient
	if rssJob == uuid.Nil {
		if err = s.ensureClient(indexer); err != nil {
			return nil, ports.ErrUnavailable
		}
		client = s.client(indexerID)
	} else {
		s.mu.RLock()
		factory := s.feedFactory
		s.mu.RUnlock()
		if factory == nil {
			client = s.client(indexerID)
		} else {
			key, e := s.secrets.DecryptSecret(indexer.ApiKeyEncrypted)
			if e != nil {
				return nil, ports.ErrUnavailable
			}
			feedURL := indexer.Url
			if len(indexer.RssUrlEncrypted) > 0 {
				plain, e := s.secrets.DecryptSecret(indexer.RssUrlEncrypted)
				if e != nil {
					return nil, ports.ErrUnavailable
				}
				feedURL = string(plain)
			}
			kind, e := configuredRSSKind(indexer)
			if e != nil {
				return nil, e
			}
			client = factory(kind, feedURL, string(key))
		}
		if client == nil {
			return nil, ports.ErrUnavailable
		}
	}
	run, err := permit.BeginQuery(ctx)
	if err != nil {
		return nil, err
	}
	if run.FromShared {
		return run, nil
	}
	queryID := run.QueryID
	fail := func(f ports.SearchQueryFailure, retry time.Duration) { _ = permit.FailQuery(ctx, f, retry) }
	if client == nil {
		fail(ports.SearchFailureUnavailable, 0)
		return nil, ports.ErrUnavailable
	}
	var categories []int32
	if json.Unmarshal(indexer.Categories, &categories) != nil {
		fail(ports.SearchFailureBadResponse, 0)
		return nil, ports.ErrBadResponse
	}
	var releases []*ports.ReleaseItem
	var pageProof *ports.IndexerPage
	if probe, ok := client.(ports.IndexerCapabilitiesProvider); probeOnly && ok {
		var caps ports.IndexerCapabilities
		caps, err = probe.Capabilities(ctx)
		if err == nil && (!caps.MovieSearch.Available || !slices.Contains(caps.MovieSearch.SupportedParams, "q")) {
			err = ports.ErrRejected
		}
		if err == nil {
			err = s.reserveHealthSearch(ctx, permit)
		}
		if err == nil {
			_, err = client.Search(ctx, "", categories, 0, 1)
		}
	} else if probeOnly {
		_, err = client.Search(ctx, "", categories, 0, 1)
	} else if pager, ok := client.(ports.IndexerPageClient); ok {
		pageProof, err = pager.SearchPage(ctx, keyword, categories, page*50, 50)
		if err == nil {
			if !validPageProof(pageProof, page*50, 50) || (chain != nil && !chain.accepts(pageProof)) {
				err = ports.ErrBadResponse
			} else {
				releases = pageProof.Items
			}
		}
	} else {
		releases, err = client.Search(ctx, keyword, categories, page*50, 50)
	}
	if err != nil {
		failure := ports.SearchFailureUnavailable
		if probeOnly {
			switch {
			case errors.Is(err, ports.ErrAuthRequired):
				failure = ports.SearchFailureAuth
			case errors.Is(err, ports.ErrTimeout):
				failure = ports.SearchFailureTimeout
			case errors.Is(err, ports.ErrBadResponse):
				failure = ports.SearchFailureBadResponse
			case errors.Is(err, ports.ErrRejected):
				failure = ports.SearchFailureUnsupported
			}
		}
		var retry time.Duration
		if rate, ok := ports.IsRateLimited(err); ok {
			failure = ports.SearchFailureRateLimited
			retry = rate.RetryAfter
		}
		fail(failure, retry)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return &SearchResult{QueryID: queryID, State: "failed"}, ports.ErrUnavailable
	}
	prepared, e := s.prepareCandidates(releases, indexerID, expected)
	if e != nil {
		fail(ports.SearchFailureInvalidResult, 0)
		return nil, e
	}
	if !probeOnly {
		prepared.Page = &ports.SearchPageEvidence{QueryID: queryID, PageOffset: int64(page) * 50, PageLimit: 50, ItemCount: int32(len(releases))}
		if pageProof != nil {
			prepared.Page.OffsetVerified = pageProof.OffsetVerified
			prepared.Page.Total = pageProof.Total
		}
	}
	return permit.CommitQuery(ctx, prepared)
}

type IndexerClientFactory func(baseURL, apiKey string) ports.IndexerClient

// ConfiguredIndexerClientFactory receives a stored protocol, never a guessed credential kind.
type ConfiguredIndexerClientFactory func(ports.IndexerProtocol, string, string) ports.IndexerClient

func (s *Service) SetConfiguredClientFactory(f ConfiguredIndexerClientFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configuredFactory = f
}

func (s *Service) SetClientFactory(f IndexerClientFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.factory = f
}

// CreateIndexer encrypts API key, inserts record, and instantiates client.
func (s *Service) CreateIndexer(ctx context.Context, name, urlStr, apiKey string, categories []int) (*ports.SearchIndexer, error) {
	encKey, err := s.secrets.EncryptSecret([]byte(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt indexer api key: %w", err)
	}

	catBytes, _ := json.Marshal(categories)
	indexerID := s.idgen.NewUUID()

	row, err := s.repo.CreateIndexer(ctx, ports.SearchIndexer{
		ID:              indexerID,
		Name:            name,
		Url:             urlStr,
		ApiKeyEncrypted: encKey,
		Categories:      catBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to insert indexer: %w", err)
	}

	// Creation persists configuration even when no runtime factory is installed.
	_ = s.ensureClient(row)

	return &row, nil
}

// TestIndexer decrypts key and executes probe query measuring latency.
// TestIndexer is retained for trusted internal probes and historical callers.
func (s *Service) TestIndexer(ctx context.Context, indexerID uuid.UUID) (bool, int64, string, error) {
	return s.testIndexer(ctx, indexerID, nil)
}

// TestIndexerForActor carries current administrator authority into each permit
// transaction, including the second budget admission after capabilities I/O.
func (s *Service) TestIndexerForActor(ctx context.Context, actor *ports.Actor, indexerID uuid.UUID) (bool, int64, string, error) {
	if e := authorizeIndexerManagement(actor); e != nil {
		return false, 0, "probe not authorized", e
	}
	frozen := *actor
	if actor.Scopes != nil {
		frozen.Scopes = append(make([]string, 0, len(actor.Scopes)), actor.Scopes...)
	}
	return s.testIndexer(ctx, indexerID, &frozen)
}

func (s *Service) testIndexer(ctx context.Context, indexerID uuid.UUID, actor *ports.Actor) (bool, int64, string, error) {
	idx, err := s.repo.GetIndexer(ctx, indexerID)
	if err != nil {
		return false, 0, "", fmt.Errorf("indexer not found: %w", err)
	}

	if err := s.ensureClient(idx); err != nil {
		return false, 0, "indexer client unavailable", err
	}
	client := s.client(indexerID)
	if client == nil {
		return false, 0, "indexer client unavailable", ports.ErrUnavailable
	}

	start := time.Now()
	result, probeErr := s.execute(ctx, indexerID, "", 0, uuid.Nil, idx.ConfigVersion, nil, true, nil, actor)
	err = probeErr
	if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrUserDisabled) {
		return false, 0, "probe not authorized", err
	}
	if err == nil && result.State != "succeeded" {
		err = ports.ErrUnavailable
	}
	latency := time.Since(start).Milliseconds()
	if result == nil || result.QueryID == uuid.Nil || result.FromShared {
		return false, latency, "indexer search deferred", ports.ErrUnavailable
	}
	if s.healthStore == nil {
		return false, latency, "health receipt unavailable", ports.ErrUnavailable
	}
	if e := s.healthStore.ReportManualHealth(ctx, indexerID, idx.ConfigVersion, result.QueryID); e != nil {
		return false, latency, "health receipt not confirmed", ports.ErrUnavailable
	}

	if err != nil {
		return false, latency, "indexer search failed", ports.ErrUnavailable
	}
	return true, latency, "ok", nil
}

// DeleteIndexer removes the indexer and unregisters its client.
func (s *Service) DeleteIndexer(ctx context.Context, indexerID uuid.UUID) error {
	if err := s.repo.DeleteIndexer(ctx, indexerID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.clients, indexerID)
	s.mu.Unlock()
	return nil
}

// AggregateSearch queries online indexers. One completed endpoint suffices for
// partial results, but a failed or still-running search is never proof of no results.
func (s *Service) AggregateSearch(ctx context.Context, keyword string) ([]ports.SearchCandidate, error) {
	rows, _, e := s.AggregateSearchDetailed(ctx, keyword)
	return rows, e
}
func (s *Service) AggregateSearchDetailed(ctx context.Context, keyword string) ([]ports.SearchCandidate, bool, error) {
	if e := ctx.Err(); e != nil {
		return nil, false, e
	}
	indexers, err := s.repo.ListIndexers(ctx)
	if err != nil {
		if e := ctx.Err(); e != nil {
			return nil, false, e
		}
		return nil, false, ports.ErrUnavailable
	}

	// Own a fixed set of workers through the end of every physical call, including
	// clients that ignore cancellation. Endpoint permits and budgets stay in execute.
	const endpointConcurrency = 4
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	endpointIDs := make([][]uuid.UUID, len(indexers))
	work := make(chan int)
	var workers sync.WaitGroup
	for range min(endpointConcurrency, len(indexers)) {
		workers.Go(func() {
			for i := range work {
				if callCtx.Err() != nil {
					continue
				}
				idx := indexers[i]
				endpointIDs[i] = s.aggregateEndpointPages(callCtx, idx.ID, idx.ConfigVersion, keyword)
			}
		})
	}
queue:
	for i, idx := range indexers {
		if idx.Health != "online" {
			continue
		}
		select {
		case <-callCtx.Done():
			break queue
		case work <- i:
		}
	}
	close(work)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	// Fold only after joining, in the original endpoint/page order rather than
	// completion order. Each worker writes a distinct slot in endpointIDs.
	failed := false
	queryIDs := make([]uuid.UUID, 0, len(indexers))
	for i, idx := range indexers {
		if idx.Health != "online" {
			continue
		}
		if len(endpointIDs[i]) > 0 {
			queryIDs = append(queryIDs, endpointIDs[i]...)
		} else {
			failed = true
		}
	}
	rows, partial, err := s.repo.ReadAggregateSnapshot(ctx, queryIDs, failed)
	if err != nil {
		if e := ctx.Err(); e != nil {
			return nil, false, e
		}
		return nil, false, ports.ErrUnavailable
	}
	return rows, partial, nil
}

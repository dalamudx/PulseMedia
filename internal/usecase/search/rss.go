package search

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/url"
	"time"
)

var ErrFeedConfig = errors.New("invalid RSS configuration")
var ErrFeedConflict = ports.ErrRSSConfigConflict

type FeedClientFactory func(kind, baseURL, apiKey string) ports.IndexerClient

func (s *Service) SetFeedClientFactory(f FeedClientFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedFactory = f
}

type FeedCommand struct {
	Enabled         bool   `json:"enabled"`
	Kind            string `json:"kind"`
	IntervalSeconds int32  `json:"interval_seconds"`
	ExpectedVersion int64  `json:"expected_version"`
	URL             string `json:"feed_url,omitempty"`
}
type FeedInfo = ports.RSSInfo
type FeedService struct {
	store   ports.RSSRepository
	secrets ports.SecretProtector
	search  ports.RSSFetcher
}

func NewFeedService(store ports.RSSRepository, secrets ports.SecretProtector, search ports.RSSFetcher) *FeedService {
	return &FeedService{store, secrets, search}
}
func (f *FeedService) Get(ctx context.Context, a *ports.Actor, id uuid.UUID) (*FeedInfo, error) {
	if a == nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	return f.store.GetRSS(ctx, id)
}
func (f *FeedService) Configure(ctx context.Context, a *ports.Actor, id uuid.UUID, c FeedCommand, key string) (*FeedInfo, error) {
	if a == nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if key == "" || len(key) > 160 || c.ExpectedVersion < 1 || c.IntervalSeconds < 30 || c.IntervalSeconds > 86400 || len(c.URL) > 8192 || (c.Kind != "rss" && c.Kind != "torznab") || (c.Enabled && c.Kind == "rss" && c.URL == "") {
		return nil, ErrFeedConfig
	}
	if c.URL != "" {
		u, e := url.Parse(c.URL)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return nil, ErrFeedConfig
		}
	}
	var cipher []byte
	if c.URL != "" {
		var e error
		cipher, e = f.secrets.EncryptSecret([]byte(c.URL))
		if e != nil {
			return nil, ports.ErrUnavailable
		}
	}
	raw, _ := json.Marshal(struct {
		ID      uuid.UUID
		Command FeedCommand
	}{id, c})
	hash, e := f.secrets.DigestSecret(raw)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	return f.store.ConfigureRSS(ctx, ports.PreparedRSSConfig{Actor: a, IndexerID: id, Settings: ports.RSSSettings{Enabled: c.Enabled, Kind: c.Kind, IntervalSeconds: c.IntervalSeconds, ExpectedVersion: c.ExpectedVersion}, EncryptedURL: cipher, IdempotencyKey: key, RequestHash: hash})
}
func (f *FeedService) QueueDue(ctx context.Context, limit int32) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrFeedConfig
	}
	return f.store.QueueDue(ctx, limit)
}
func (f *FeedService) ExecuteJob(ctx context.Context, p ports.RSSPollJob) error {
	if p.ID == uuid.Nil || p.IndexerID == uuid.Nil || p.ConfigVersion < 1 || p.Owner == "" {
		return ports.ErrBadResponse
	}
	stale, e := f.store.CheckRSS(ctx, p)
	if e != nil {
		return e
	}
	if stale {
		return f.store.CompleteRSS(ctx, p, nil)
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, e := f.search.ExecuteRSS(bounded, p.IndexerID, p.ID, p.ConfigVersion)
	cancel()
	if e != nil {
		return e
	}
	if result.State != "succeeded" {
		if e = f.store.BindRSSQuery(ctx, p, result.QueryID); e != nil {
			return e
		}
		return ports.ErrUnavailable
	}
	return f.store.CompleteRSS(ctx, p, &result.QueryID)
}
func (f *FeedService) RetryDelay(ctx context.Context, id uuid.UUID) time.Duration {
	return f.store.RetryRSSDelay(ctx, id)
}

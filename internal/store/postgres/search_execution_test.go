package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestSearchExecutionPermitAuthorityAndAtomicEvidence(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := postgres.NewSearchExecutionStore(pool)
	idx, err := repo.CreateIndexer(ctx, ports.SearchIndexer{ID: uuid.New(), Name: "permit", Url: "https://source.invalid", ApiKeyEncrypted: []byte("opaque"), Categories: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	spec := ports.SearchQuerySpec{ID: uuid.New(), IndexerID: idx.ID, Keyword: "frozen", ConfigVersion: idx.ConfigVersion}
	_, permit, err := repo.AdmitQuery(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(permit.Close)
	result, err := permit.BeginQuery(ctx)
	if err != nil || result.FromShared {
		t.Fatal(result, err)
	}
	sharing := spec
	sharing.ID = uuid.New()
	admission, write, err := repo.AdmitQuery(ctx, sharing)
	if err != nil || write != nil || admission.Shared == nil || admission.Shared.QueryID != spec.ID {
		t.Fatal("shared result received write authority", admission, write, err)
	}
	sharing.Keyword = "different"
	if _, write, err = repo.AdmitQuery(ctx, sharing); !errors.Is(err, ports.ErrUnavailable) || write != nil {
		t.Fatal("unrelated query borrowed permit", write, err)
	}
	candidate := ports.SearchCandidate{ID: uuid.New(), IndexerID: idx.ID, Title: "Movie.2026.1080p", Guid: "frozen-guid", LinkEncrypted: []byte("opaque-envelope"), SizeBytes: 1024, QualityProfile: []byte(`{}`), SourceConfigVersion: idx.ConfigVersion + 1}
	total := int64(1)
	commit := ports.SearchQueryCommit{Candidates: []ports.SearchCandidate{candidate}, Page: &ports.SearchPageEvidence{QueryID: spec.ID, PageLimit: 50, ItemCount: 1, OffsetVerified: true, Total: &total}}
	if _, err = permit.CommitQuery(ctx, commit); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("accepted mismatched source epoch", err)
	}
	var candidates, proofs int
	if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM release_candidate),(SELECT count(*) FROM indexer_query_page)").Scan(&candidates, &proofs); err != nil || candidates != 0 || proofs != 0 {
		t.Fatal("invalid commit leaked writes", candidates, proofs, err)
	}
	commit.Candidates[0].SourceConfigVersion = idx.ConfigVersion
	if result, err = permit.CommitQuery(ctx, commit); err != nil || result.State != "succeeded" {
		t.Fatal(result, err)
	}
	if _, err = permit.CommitQuery(ctx, commit); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("terminal permit allowed replay", err)
	}
	rows, partial, err := repo.ReadAggregateSnapshot(ctx, []uuid.UUID{spec.ID}, false)
	if err != nil || partial || len(rows) != 1 || rows[0].Guid != "frozen-guid" {
		t.Fatal(rows, partial, err)
	}
	permit.Close()
	permit.Close()
	sharing.Keyword = "next"
	sharing.ID = uuid.New()
	_, next, err := repo.AdmitQuery(ctx, sharing)
	if err != nil {
		t.Fatal("idempotent close retained physical permit", err)
	}
	defer next.Close()
	if _, err = next.BeginQuery(ctx); err != nil {
		t.Fatal(err)
	}
	if err = next.FailQuery(ctx, ports.SearchQueryFailure("https://secret.invalid?passkey=hidden"), 0); err != nil {
		t.Fatal(err)
	}
	var failure string
	if err = pool.QueryRow(ctx, "SELECT error FROM indexer_query WHERE id=$1", sharing.ID).Scan(&failure); err != nil || failure != "indexer search failed" || strings.Contains(failure, "hidden") {
		t.Fatal(failure, err)
	}
	next.Close()
	if _, err = next.BeginQuery(ctx); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("closed permit granted query", err)
	}
	if _, err = pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", idx.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = repo.ReadAggregateSnapshot(ctx, []uuid.UUID{spec.ID}, false); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("stale source survived final snapshot", err)
	}
}

func TestSearchExecutionPermitPersistsBoundedBackoff(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := postgres.NewSearchExecutionStore(pool)
	for _, test := range []struct {
		name                    string
		retry, minimum, maximum time.Duration
	}{{"default", 0, 55 * time.Second, 65 * time.Second}, {"bounded", 48 * time.Hour, 24*time.Hour - time.Minute, 24*time.Hour + time.Minute}} {
		t.Run(test.name, func(t *testing.T) {
			idx, err := repo.CreateIndexer(ctx, ports.SearchIndexer{ID: uuid.New(), Name: test.name, Url: "https://source.invalid", ApiKeyEncrypted: []byte("opaque"), Categories: []byte("[]")})
			if err != nil {
				t.Fatal(err)
			}
			spec := ports.SearchQuerySpec{ID: uuid.New(), IndexerID: idx.ID, Keyword: "limited", ConfigVersion: idx.ConfigVersion}
			_, permit, err := repo.AdmitQuery(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			defer permit.Close()
			if _, err = permit.BeginQuery(ctx); err != nil {
				t.Fatal(err)
			}
			if err = permit.FailQuery(ctx, ports.SearchFailureRateLimited, test.retry); err != nil {
				t.Fatal(err)
			}
			fresh, err := repo.GetIndexer(ctx, idx.ID)
			if err != nil || fresh.NextQueryAt == nil {
				t.Fatal(fresh, err)
			}
			delay := time.Until(*fresh.NextQueryAt)
			if delay < test.minimum || delay > test.maximum {
				t.Fatal("backoff outside historical bounds", delay)
			}
		})
	}
}

package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestMovieLinkInPlaceSourceChangesAndCancellation(t *testing.T) {
	for _, branch := range []string{"size", "mtime", "fingerprint", "mutation", "mapping_revision", "cancel"} {
		t.Run(branch, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var before string
			p := movieLinkProvider{before: func() {
				if branch == "cancel" {
					before = movieLinkState(t, f)
					cancel()
					return
				}
				changes := map[string]string{"size": "size_bytes=size_bytes+1", "mtime": "mtime_ns=mtime_ns+1", "fingerprint": "file_fingerprint='changed-stat'", "mutation": "last_mutation_seq=last_mutation_seq+1", "mapping_revision": "mapping_revision=mapping_revision+1"}
				if _, e := f.pool.Exec(ctx, "UPDATE library_file SET "+changes[branch]+" WHERE id=$1", f.file); e != nil {
					t.Fatal(e)
				}
				before = movieLinkState(t, f)
			}}
			m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), p)
			r, e := m.MatchAndLinkFile(ctx, f.root, f.path)
			expected := ports.ErrMovieLinkStale
			if branch == "cancel" {
				expected = context.Canceled
			}
			if !errors.Is(e, expected) || !r.RequiresReview {
				t.Fatalf("source mutation accepted: %v", e)
			}
			if movieLinkState(t, f) != before {
				t.Fatal("source mutation was overwritten or re-certified")
			}
		})
	}
}
func TestMovieLinkMappingChangesAfterUnitWait(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{})
	linked, e := m.MatchAndLinkFile(ctx, f.root, f.path)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, "SELECT id FROM media_unit WHERE id=$1 FOR UPDATE", linked.Unit.ID); e != nil {
		t.Fatal(e)
	}
	waiter := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(movieLinkWaitPool(t, f), f.gen), movieLinkProvider{})
	done := make(chan error, 1)
	go func() { _, err := waiter.MatchAndLinkFile(ctx, f.root, f.path); done <- err }()
	waitMovieLinkLock(t, f)
	if _, e = tx.Exec(ctx, "UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id=$1", f.file); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	before := movieLinkState(t, f)
	if e = <-done; !errors.Is(e, ports.ErrMovieLinkConflict) {
		t.Fatalf("retired mapping revived: %v", e)
	}
	if movieLinkState(t, f) != before {
		t.Fatal("retired mapping or fact altered")
	}
}
func TestMovieLinkMetadataChangesAfterUnitWait(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{})
	linked, e := m.MatchAndLinkFile(ctx, f.root, f.path)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, "SELECT id FROM media_unit WHERE id=$1 FOR UPDATE", linked.Unit.ID); e != nil {
		t.Fatal(e)
	}
	waiter := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(movieLinkWaitPool(t, f), f.gen), movieLinkProvider{})
	done := make(chan error, 1)
	go func() { _, err := waiter.MatchAndLinkFile(ctx, f.root, f.path); done <- err }()
	waitMovieLinkLock(t, f)
	if _, e = tx.Exec(ctx, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1", linked.Media.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	before := movieLinkState(t, f)
	if e = <-done; !errors.Is(e, ports.ErrMovieLinkStale) {
		t.Fatalf("metadata version change accepted: %v", e)
	}
	if movieLinkState(t, f) != before {
		t.Fatal("metadata change overwritten")
	}
}
func TestMovieLinkForeignRootFactsAreNotReused(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx := context.Background()
	m := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{})
	linked, e := m.MatchAndLinkFile(ctx, f.root, f.path)
	if e != nil {
		t.Fatal(e)
	}
	other, e := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: f.gen.NewUUID(), Path: t.TempDir(), MediaType: "movie", Name: "Other root"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE unit_fact SET root_id=$2 WHERE source_file_id=$1", f.file, pgtype.UUID{Bytes: other.ID, Valid: true}); e != nil {
		t.Fatal(e)
	}
	before := movieLinkState(t, f)
	if _, e = m.MatchAndLinkFile(ctx, f.root, f.path); !errors.Is(e, ports.ErrMovieLinkConflict) {
		t.Fatalf("foreign root fact reused for %s: %v", linked.Unit.ID, e)
	}
	if movieLinkState(t, f) != before {
		t.Fatal("foreign root history rewritten")
	}
}

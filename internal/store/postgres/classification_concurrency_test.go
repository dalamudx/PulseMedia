package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"sync"
	"testing"
)

func TestClassificationConcurrentPublishAndRootGenerationPG(t *testing.T) {
	f := newCreationFixture(t, 131)
	ctx := context.Background()
	if _, e := f.pool.Exec(ctx, `UPDATE app_user SET role='admin' WHERE id=$1`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	f.actor.Role = ports.RoleAdmin
	store := postgres.NewClassificationStore(f.pool, idgen.New())
	cmd := ports.ClassificationPublish{Rules: []classification.Rule{{ID: "movie", MediaType: "movie", RootID: f.movie}}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := store.PublishClassification(ctx, f.actor, cmd, key); results <- e }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ports.ErrClassificationConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	version := int64(1)
	in := creationInput(7901, "movie")
	in.ClassificationVersion = &version
	preview, e := store.PreviewClassification(ctx, f.actor, ports.ClassificationPreviewInput{Version: 1, TMDBID: in.TMDBID, MediaType: "movie"})
	if e != nil {
		t.Fatal(e)
	}
	in.ClassificationPreviewStamp = preview.PreviewStamp
	in.ClassificationPreviewRootID = preview.Result.RootID
	identity := creationIdentity("root-generation")
	hint := f.read(t, in, identity)
	if _, e := f.pool.Exec(ctx, `UPDATE library_root SET generation=generation+1 WHERE id=$1`, f.movie); e != nil {
		t.Fatal(e)
	}
	if _, e := f.store.CommitSubscriptionCreation(ctx, f.actor, in, hint, creationMovie(7901), identity, creationDecider(in, hint)); !errors.Is(e, ports.ErrSubscriptionBindingsConflict) {
		t.Fatal("root replacement accepted", e)
	}
	var count int
	if e := f.pool.QueryRow(ctx, `SELECT count(*) FROM subscription`).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed classification created intent", count, e)
	}
}

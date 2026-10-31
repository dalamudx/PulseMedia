package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"testing"
)

func TestClassificationPublicationPreviewFrozenCreationPG(t *testing.T) {
	f := newCreationFixture(t, 131)
	ctx := context.Background()
	store := postgres.NewClassificationStore(f.pool, idgen.New())
	admin := *f.actor
	admin.Role = ports.RoleAdmin
	if _, e := store.PublishClassification(ctx, &admin, ports.ClassificationPublish{}, "forged"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("forged role", e)
	}
	if _, e := store.PublishClassification(ctx, f.actor, ports.ClassificationPublish{}, "member"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("member published", e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE app_user SET role='admin' WHERE id=$1`, admin.ID); e != nil {
		t.Fatal(e)
	}
	f.actor = &admin
	input := creationInput(7301, "movie")
	input.RootID = &f.movie
	p := creationMovie(7301)
	p.ClassificationFacts = &classification.Facts{MediaType: "movie", Genres: []string{"Drama"}, Languages: []string{"en"}, Countries: []string{"US"}}
	first := f.create(t, input, p, "explicit-source")
	target := idgen.New().NewUUID()
	if _, e := db.New(f.pool).CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: target, Name: "Drama", Path: f.path + "/drama", MediaType: "movie"}); e != nil {
		t.Fatal(e)
	}
	rules := []classification.Rule{{ID: "english-drama", MediaType: "movie", RootID: target, Genres: []string{"Drama"}, Languages: []string{"en"}, Countries: []string{"US"}}}
	cmd := ports.ClassificationPublish{Rules: rules}
	doc, e := store.PublishClassification(ctx, &admin, cmd, "publish-one")
	if e != nil || doc.Version != 1 {
		t.Fatal(doc, e)
	}
	replay, e := store.PublishClassification(ctx, &admin, cmd, "publish-one")
	if e != nil || replay.Version != 1 {
		t.Fatal("replay", replay, e)
	}
	changed := cmd
	changed.ExpectedVersion = 1
	if _, e = store.PublishClassification(ctx, &admin, changed, "publish-one"); !errors.Is(e, ports.ErrClassificationConflict) {
		t.Fatal("changed original", e)
	}
	before := f.snapshot(t)
	mid := first.Subscription.MediaID
	preview, e := store.PreviewClassification(ctx, &admin, ports.ClassificationPreviewInput{Version: 1, MediaID: &mid})
	if e != nil || preview.Result.Status != "matched" || preview.Result.RootID == nil || *preview.Result.RootID != target {
		t.Fatal(preview, e)
	}
	if f.snapshot(t) != before {
		t.Fatal("preview mutated business rows")
	}
	matched := *preview
	unknown := int64(7302)
	preview, e = store.PreviewClassification(ctx, &admin, ports.ClassificationPreviewInput{Version: 1, TMDBID: &unknown, MediaType: "movie"})
	if e != nil || preview.Result.Status != "insufficient_evidence" {
		t.Fatal(preview, e)
	}
	version := int64(1)
	classified := ports.SubscriptionCreationInput{MediaID: &mid, ClassificationVersion: &version, ClassificationPreviewStamp: matched.PreviewStamp, ClassificationPreviewRootID: matched.Result.RootID, QualityPolicyID: "default", ScopeType: "all"}
	selected := f.create(t, classified, nil, "classified-create")
	if selected.Subscription.RootID != target {
		t.Fatal("actual target", selected)
	}
	var frozen string
	if e = f.pool.QueryRow(ctx, `SELECT result::text FROM subscription_classification_choice WHERE subscription_id=$1 AND root_id=$2 AND policy_version=1`, selected.Subscription.ID, target).Scan(&frozen); e != nil {
		t.Fatal(e)
	}
	rules[0].RootID = f.movie
	doc, e = store.PublishClassification(ctx, &admin, ports.ClassificationPublish{ExpectedVersion: 1, Rules: rules}, "publish-two")
	if e != nil || doc.Version != 2 {
		t.Fatal(doc, e)
	}
	if _, _, e = f.store.ReadSubscriptionCreationContext(ctx, &admin, classified, creationIdentity("stale-new")); !errors.Is(e, ports.ErrClassificationConflict) {
		t.Fatal("stale accepted", e)
	}
	_, receipt, e := f.store.ReadSubscriptionCreationContext(ctx, &admin, classified, creationIdentity("classified-create"))
	if e != nil || receipt == nil || receipt.Subscription.RootID != target {
		t.Fatal("old choice redirected", receipt, e)
	}
	var after string
	if e = f.pool.QueryRow(ctx, `SELECT result::text FROM subscription_classification_choice WHERE subscription_id=$1`, selected.Subscription.ID).Scan(&after); e != nil || after != frozen {
		t.Fatal("frozen evidence changed", e)
	}
	if _, e = f.pool.Exec(ctx, `UPDATE library_root SET health='offline' WHERE id=$1`, f.movie); e != nil {
		t.Fatal(e)
	}
	preview, e = store.PreviewClassification(ctx, &admin, ports.ClassificationPreviewInput{Version: 2, MediaID: &mid})
	if e != nil || preview.Result.Status != "target_unavailable" {
		t.Fatal(preview, e)
	}
	if _, e = store.PublishClassification(ctx, &admin, ports.ClassificationPublish{ExpectedVersion: 2, Rules: rules}, "offline"); !errors.Is(e, ports.ErrClassificationConflict) {
		t.Fatal("offline published", e)
	}
	if _, e = f.pool.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = store.ReadClassification(ctx, &admin); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("stale read", e)
	}
	if _, e = store.PublishClassification(ctx, &admin, cmd, "publish-one"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revoked replay", e)
	}
}

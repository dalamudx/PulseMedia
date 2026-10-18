package postgres_test

import (
	"bytes"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestIndexerManagementRSSContentVersionPG(t *testing.T) {
	p, _, secrets, actor, store := managementFixture(t)
	ctx := t.Context()
	created := managementMust(t, store, actor, managementCreate(t, secrets), "rss-compatible-source")
	q := db.New(p)
	before, err := q.GetIndexerConfiguration(ctx, created.Indexer.ID)
	if err != nil {
		t.Fatal("read sealed source failed")
	}
	base, err := q.GetIndexerByID(ctx, created.Indexer.ID)
	if err != nil {
		t.Fatal("read source failed")
	}
	original, _, err := postgres.ResolveIndexerConfiguration(ctx, q, secrets, base)
	if err != nil {
		t.Fatal("resolve original source failed")
	}
	rss := postgres.NewRSSStore(p, managementIDs{})
	prepared := ports.PreparedRSSConfig{Actor: actor, IndexerID: base.ID, Settings: ports.RSSSettings{Enabled: true, Kind: "torznab", IntervalSeconds: 600, ExpectedVersion: base.ConfigVersion}, EncryptedURL: []byte{}, IdempotencyKey: "rss-content-version", RequestHash: managementHash}
	info, err := rss.ConfigureRSS(ctx, prepared)
	if err != nil || info.ConfigVersion != base.ConfigVersion+1 {
		t.Fatal("RSS configuration did not advance total version", err)
	}
	after, err := q.GetIndexerConfiguration(ctx, base.ID)
	if err != nil || after.ConfigurationVersion != before.ConfigurationVersion || after.Protocol != before.Protocol || after.BehaviorVersion != before.BehaviorVersion || !bytes.Equal(after.UrlEncrypted, before.UrlEncrypted) {
		t.Fatal("RSS edit rewrote independent encrypted source content")
	}
	fresh, err := q.GetIndexerByID(ctx, base.ID)
	if err != nil || !bytes.Equal(fresh.ApiKeyEncrypted, base.ApiKeyEncrypted) {
		t.Fatal("RSS edit changed source credential")
	}
	resolved, modes, err := postgres.ResolveIndexerConfiguration(ctx, q, secrets, fresh)
	if err != nil || resolved.Url != original.Url || modes.Protocol != ports.IndexerProtocolTorznab {
		t.Fatal("RSS edit broke source URL or explicit protocol")
	}
	list, err := store.ListManagedIndexers(ctx, actor)
	if err != nil || len(list) != 1 || list[0].ConfigVersion != info.ConfigVersion {
		t.Fatal("RSS total version missing from safe management list", err)
	}
	count := managementCount(t, p, "idempotency_record")
	replay, err := rss.ConfigureRSS(ctx, prepared)
	if err != nil || replay.ConfigVersion != info.ConfigVersion || managementCount(t, p, "idempotency_record") != count {
		t.Fatal("RSS original-key recovery changed command results", err)
	}
	update := managementUpdate(&ports.IndexerManagementReceipt{Indexer: list[0]})
	edited := managementMust(t, store, actor, update, "edit-after-rss-version")
	if edited.Indexer.ConfigVersion != info.ConfigVersion+1 {
		t.Fatal("management edit did not fence RSS version")
	}
	final, err := q.GetIndexerConfiguration(ctx, base.ID)
	if err != nil || final.ConfigurationVersion != edited.Indexer.ConfigVersion || !bytes.Equal(final.UrlEncrypted, before.UrlEncrypted) {
		t.Fatal("management edit lost URL content or version")
	}
	managementNoSecrets(t, p, created, edited)
}

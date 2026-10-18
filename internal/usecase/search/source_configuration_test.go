package search_test

import (
	"bytes"
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

func TestSourceConfigurationRotationKeepsEncryptedReferencesSeparate(t *testing.T) {
	s, pool, q, key, idx := setupSearchTest(t)
	ctx := context.Background()
	s.RegisterClient(idx, &mockIndexerClient{releases: []*ports.ReleaseItem{{Title: "Movie.2020.1080p.BluRay", GUID: "same-guid", DownloadURL: "https://fixture.invalid/old?passkey=fixture-one"}}})
	first, e := s.ExecuteSearch(ctx, idx, "first", 0)
	if e != nil {
		t.Fatal(e)
	}
	one, e := q.ListQueryCandidates(ctx, first.QueryID)
	if e != nil || len(one) != 1 || one[0].SourceConfigVersion != 1 {
		t.Fatal("first source ref missing", e)
	}
	old := one[0]
	oldPlain, e := crypto.Decrypt(key, old.LinkEncrypted)
	if e != nil {
		t.Fatal(e)
	}
	// Same config/guid stays one immutable encrypted source despite new nonce.
	same, e := s.ExecuteSearch(ctx, idx, "same-config", 0)
	if e != nil {
		t.Fatal(e)
	}
	sameRows, e := q.ListQueryCandidates(ctx, same.QueryID)
	if e != nil || len(sameRows) != 1 || sameRows[0].ID != old.ID || !bytes.Equal(sameRows[0].LinkEncrypted, old.LinkEncrypted) {
		t.Fatal("same source ciphertext was mutated", e)
	}
	if _, e = pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", idx); e != nil {
		t.Fatal(e)
	}
	s.RegisterClient(idx, &mockIndexerClient{releases: []*ports.ReleaseItem{{Title: "Movie.2020.1080p.BluRay", GUID: "same-guid", DownloadURL: "https://fixture.invalid/new?passkey=fixture-two"}}})
	second, e := s.ExecuteSearch(ctx, idx, "second", 0)
	if e != nil {
		t.Fatal(e)
	}
	two, e := q.ListQueryCandidates(ctx, second.QueryID)
	if e != nil || len(two) != 1 || two[0].SourceConfigVersion != 2 || two[0].ID == old.ID {
		t.Fatal("new config reused old source ref", e)
	}
	plain, e := crypto.Decrypt(key, two[0].LinkEncrypted)
	if e != nil || bytes.Equal(plain, oldPlain) {
		t.Fatal("new config still uses old credential URL", e)
	}
	retained, e := q.GetCandidateByID(ctx, old.ID)
	if e != nil || !bytes.Equal(retained.LinkEncrypted, old.LinkEncrypted) {
		t.Fatal("rotation modified frozen source", e)
	}
}

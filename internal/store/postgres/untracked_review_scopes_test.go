package postgres_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func reviewScopeListClones(t *testing.T, f *untrackedReviewFixture, owner uuid.UUID, count int) {
	t.Helper()
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), `WITH ids AS MATERIALIZED (SELECT gen_random_uuid() id FROM generate_series(1,$3::integer))
 INSERT INTO untracked_directory_scope(id,owner_id,enabled,version,client_id,target_root_id,local_root,snapshot,created_at,updated_at)
 SELECT ids.id,$2,s.enabled,s.version,s.client_id,s.target_root_id,s.local_root,jsonb_set(jsonb_set(s.snapshot,'{id}',to_jsonb(ids.id::text)),'{owner_id}',to_jsonb($2::uuid::text)),s.created_at,s.updated_at FROM untracked_directory_scope s CROSS JOIN ids WHERE s.id=$1`, f.scope, owner, count)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), `INSERT INTO untracked_directory_scope_receipt(scope_id,owner_id,command_key,version,enabled,local_root,request_hash,input,snapshot)
 SELECT s.id,s.owner_id,r.command_key,s.version,s.enabled,s.local_root,r.request_hash,r.input,s.snapshot FROM untracked_directory_scope s CROSS JOIN untracked_directory_scope_receipt r
 WHERE r.scope_id=$1 AND r.version=1 AND s.owner_id=$2 AND NOT EXISTS(SELECT 1 FROM untracked_directory_scope_receipt existing WHERE existing.scope_id=s.id)`, f.scope, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUntrackedReviewScopeOptionsOwnerPaginationPG(t *testing.T) {
	f := newUntrackedReviewFixture(t, "tv")
	reviewScopeListClones(t, f, uuid.MustParse(f.a.ID), 100)
	other := f.user(t)
	reviewScopeListClones(t, f, uuid.MustParse(other.ID), 2)
	before := untrackedFacts(t, f.untrackedFixture)
	first, err := f.review.ReadUntrackedReviewScopes(t.Context(), f.a, "")
	if err != nil || len(first.Items) != 100 || !first.HasMore || first.NextCursor != first.Items[99].ID {
		t.Fatal(first, err)
	}
	second, err := f.review.ReadUntrackedReviewScopes(t.Context(), f.a, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.HasMore || second.NextCursor != "" {
		t.Fatal(second, err)
	}
	seen := map[string]bool{}
	previous := ""
	for _, o := range append(first.Items, second.Items...) {
		if o.ID <= previous || seen[o.ID] || o.LocalRoot != "/scope-source" || o.TargetRootPath != "/scope-target" || o.TargetMediaType != "tv" || !o.Enabled || o.Version != 1 {
			t.Fatal("scope option changed owner snapshot", o)
		}
		f.count(t, `SELECT count(*) FROM untracked_directory_scope WHERE id=$1 AND owner_id=$2`, 1, uuid.MustParse(o.ID), uuid.MustParse(f.a.ID))
		previous, seen[o.ID] = o.ID, true
	}
	otherPage, err := f.review.ReadUntrackedReviewScopes(t.Context(), other, "")
	if err != nil || len(otherPage.Items) != 2 || otherPage.HasMore {
		t.Fatal(otherPage, err)
	}
	for _, o := range otherPage.Items {
		if seen[o.ID] {
			t.Fatal("owner scope leaked")
		}
	}
	if after := untrackedFacts(t, f.untrackedFixture); after != before {
		t.Fatal("scope list mutated business graph")
	}
}

func TestUntrackedReviewScopeOptionsCursorAndRevocationPG(t *testing.T) {
	f := newUntrackedReviewFixture(t)
	for _, cursor := range []string{"not-a-uuid", uuid.Nil.String(), "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"} {
		_, err := f.review.ReadUntrackedReviewScopes(t.Context(), f.a, cursor)
		checkinErr(t, err, ports.ErrRejected)
	}
	other := f.user(t)
	empty, err := f.review.ReadUntrackedReviewScopes(t.Context(), other, "")
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore {
		t.Fatal(empty, err)
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.a.ID))
	_, err = f.review.ReadUntrackedReviewScopes(t.Context(), f.a, "")
	checkinErr(t, err, ports.ErrForbidden)
}

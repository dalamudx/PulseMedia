package postgres_test

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
)

func TestFileMappingMediaSearchPaginationPG(t *testing.T) {
	f := newCheckinFixture(t)
	f.exec(t, `INSERT INTO media(id,title,original_title,media_type,updated_at) SELECT md5('mapping-media-'||n)::uuid,'Series '||n,'Original '||n,'tv','2026-01-01T00:00:00Z' FROM generate_series(1,105) n`)
	f.exec(t, `INSERT INTO media(id,title,media_type,updated_at) SELECT md5('mapping-movie-'||n)::uuid,'Movie '||n,'movie','2026-02-01T00:00:00Z' FROM generate_series(1,120) n`)
	old := uuid.New()
	f.exec(t, `INSERT INTO media(id,title,original_title,media_type,updated_at) VALUES($1,'Old 100%_literal','原始片名','tv','2020-01-01T00:00:00Z')`, old)
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.tv','87654321')`, uuid.New(), old)
	store := postgres.NewFileMappingStore(f.p, idgen.New())
	before := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM media x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x))::text`)
	first, e := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{MediaType: "tv"})
	if e != nil || len(first.Media) != 100 || !first.MediaHasMore || first.MediaNextCursor == "" {
		t.Fatal("first", first, e)
	}
	next, e := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{MediaType: "tv", MediaCursor: first.MediaNextCursor})
	if e != nil || len(next.Media) != 6 || next.MediaHasMore || next.MediaNextCursor != "" {
		t.Fatal("next", next, e)
	}
	seen := map[uuid.UUID]bool{}
	for _, m := range append(first.Media, next.Media...) {
		if seen[m.ID] {
			t.Fatal("duplicate", m.ID)
		}
		seen[m.ID] = true
		if m.MediaType != "tv" {
			t.Fatal("type leaked", m)
		}
	}
	if !seen[old] {
		t.Fatal("old media unreachable")
	}
	for _, q := range []ports.FileMappingOptionsQuery{{MediaQuery: "100%_literal"}, {MediaQuery: "原始片名"}, {TMDBID: 87654321}} {
		page, err := store.ReadFileMappingOptions(t.Context(), f.a, q)
		if err != nil || len(page.Media) != 1 || page.Media[0].ID != old {
			t.Fatal("literal/original/native identity search", q, page, err)
		}
	}
	saved, e := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{MediaType: "movie", MediaID: old})
	if e != nil || saved.SelectedMedia == nil || saved.SelectedMedia.ID != old {
		t.Fatal("saved ID outside filter/page", saved, e)
	}
	for _, bad := range []ports.FileMappingOptionsQuery{{MediaCursor: first.MediaNextCursor}, {MediaType: "tv", MediaQuery: "Series", MediaCursor: first.MediaNextCursor}, {MediaType: "tv", TMDBID: 99, MediaCursor: first.MediaNextCursor}, {MediaCursor: "invalid"}, {MediaType: "music"}, {MediaQuery: " bad"}, {TMDBID: -1}} {
		if _, err := store.ReadFileMappingOptions(t.Context(), f.a, bad); !errors.Is(err, ports.ErrInvalidFileMapping) {
			t.Fatal("invalid filter/cursor accepted", bad, err)
		}
	}
	f.exec(t, `INSERT INTO media(id,title,media_type) VALUES($1,'Newer','tv')`, uuid.New())
	afterNew, e := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{MediaType: "tv", MediaCursor: first.MediaNextCursor})
	if e != nil || !sameHistoricalJSON(next, afterNew) {
		t.Fatal("new first-page insert moved continuation", afterNew, e)
	}
	// Exclude the intentional new-media fixture write from read-only comparison.
	f.exec(t, `DELETE FROM media WHERE title='Newer'`)
	after := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM media x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x))::text`)
	if before != after {
		t.Fatal("media choices changed business state")
	}
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.a.ID))
	if _, err := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{MediaID: old}); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revoked administrator got saved selection", err)
	}
}

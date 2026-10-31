package postgres_test

import (
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestFileMappingOptionsPaginationPG(t *testing.T) {
	f := newCheckinFixture(t)
	q := db.New(f.p)
	root, err := q.CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: uuid.New(), Name: "TV target", Path: t.TempDir(), MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := q.CreateLibraryRoot(t.Context(), db.CreateLibraryRootParams{ID: uuid.New(), Name: "Other root", Path: t.TempDir(), MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		root    uuid.UUID
		count   int
		created string
	}{{root.ID, 101, "2026-01-01T00:00:00Z"}, {foreign.ID, 120, "2026-02-01T00:00:00Z"}} {
		f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,created_at) SELECT md5($1::text||n::text)::uuid,$1::uuid,'Episode.'||n||'.mkv',100,1,$3::timestamptz FROM generate_series(1,$2::int) n`, seed.root, seed.count, seed.created)
	}
	store := postgres.NewFileMappingStore(f.p, idgen.New())
	before := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM library_file x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x))::text`)
	global, err := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{})
	if err != nil || len(global.Files) != 100 || !global.FilesHasMore {
		t.Fatal("global bound", global, err)
	}
	for _, file := range global.Files {
		if file.RootID != foreign.ID {
			t.Fatal("latest-first ordering changed")
		}
	}
	page, err := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{RootID: root.ID})
	if err != nil || len(page.Files) != 100 || !page.FilesHasMore || page.FilesNextCursor == "" {
		t.Fatal(page, err)
	}
	seen := map[uuid.UUID]bool{}
	previous := ""
	for _, file := range page.Files {
		if file.RootID != root.ID || file.ID.String() <= previous {
			t.Fatal("foreign root or unstable tie order", file)
		}
		previous = file.ID.String()
		seen[file.ID] = true
	}
	next, err := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{RootID: root.ID, Cursor: page.FilesNextCursor})
	if err != nil || len(next.Files) != 1 || next.FilesHasMore || next.FilesNextCursor != "" || seen[next.Files[0].ID] {
		t.Fatal("second page incomplete or duplicated", next, err)
	}
	after := publisherFrozen(t, f.p, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM library_file x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_fact x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x))::text`)
	if before != after {
		t.Fatal("file choices wrote business state")
	}
	// Cursor identity is self-contained: retiring the boundary file must not erase the next page.
	f.exec(t, `UPDATE library_file SET retired_at=clock_timestamp() WHERE id=$1`, page.Files[99].ID)
	f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'Newer.mkv',100,1)`, uuid.New(), root.ID)
	replayed, err := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{RootID: root.ID, Cursor: page.FilesNextCursor})
	if err != nil || !sameHistoricalJSON(next, replayed) {
		t.Fatal("retired cursor or new first-page insert shifted continuation", replayed, err)
	}
	for _, bad := range []ports.FileMappingOptionsQuery{{RootID: foreign.ID, Cursor: page.FilesNextCursor}, {Cursor: page.FilesNextCursor}, {RootID: root.ID, Cursor: "not_a_cursor"}} {
		if _, err := store.ReadFileMappingOptions(t.Context(), f.a, bad); !errors.Is(err, ports.ErrInvalidFileMapping) {
			t.Fatal("invalid scope/cursor accepted", err)
		}
	}
	empty, err := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{RootID: uuid.New()})
	if err != nil || len(empty.Files) != 0 || empty.FilesHasMore {
		t.Fatal("empty root invented files", empty, err)
	}
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.a.ID))
	if _, err := store.ReadFileMappingOptions(t.Context(), f.a, ports.FileMappingOptionsQuery{RootID: root.ID, Cursor: page.FilesNextCursor}); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("stale administrator actor read next page", err)
	}
}

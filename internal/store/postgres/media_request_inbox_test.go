package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"reflect"
	"testing"
)

func mediaInboxSnapshot(t *testing.T, f mediaRequestFixture) map[string][32]byte {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	out := make(map[string][32]byte, len(names))
	for _, name := range names {
		var raw []byte
		query := `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb) FROM ` + pgx.Identifier{"public", name}.Sanitize() + ` r`
		if err := f.pool.QueryRow(t.Context(), query).Scan(&raw); err != nil {
			t.Fatal("snapshot", name, err)
		}
		out[name] = sha256.Sum256(raw)
	}
	return out
}

func TestMediaRequestInboxExactCountAndPendingPaginationPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	ctx := t.Context()
	empty, err := f.store.ReadMediaRequestInbox(ctx, f.admin)
	if err != nil || empty == nil || empty.PendingCount != 0 || empty.ObservedAt.IsZero() {
		t.Fatal("known zero", empty, err)
	}
	for i := range 33 {
		a := f.owner
		if i >= 30 {
			a = f.other
		}
		f.submit(t, a, fmt.Sprintf("inbox-%d", i))
	}
	before := mediaInboxSnapshot(t, f)
	summary, err := f.store.ReadMediaRequestInbox(ctx, f.admin)
	if err != nil || summary == nil || summary.PendingCount != 33 {
		t.Fatal("global count must exceed a page", summary, err)
	}
	first, err := f.store.ListPendingMediaRequests(ctx, f.admin, 25, nil)
	if err != nil || len(first) != 25 {
		t.Fatal("first page", len(first), err)
	}
	cursor := first[24].ID
	second, err := f.store.ListPendingMediaRequests(ctx, f.admin, 25, &cursor)
	if err != nil || len(second) != 8 {
		t.Fatal("second page", len(second), err)
	}
	seen := map[uuid.UUID]bool{}
	for _, page := range [][]ports.MediaRequest{first, second} {
		for _, row := range page {
			if seen[row.ID] || row.State != "pending" {
				t.Fatal("duplicate or terminal row")
			}
			seen[row.ID] = true
		}
	}
	if len(seen) != 33 || !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("reads changed protected tables or lost requests")
	}
	_, err = f.store.CloseMediaRequest(ctx, f.admin, cursor, ports.MediaRequestCloseInput{ExpectedVersion: 1, Action: "reject", Reason: "explicit reviewed rejection"}, "close-page-cursor")
	if err != nil {
		t.Fatal(err)
	}
	before = mediaInboxSnapshot(t, f)
	summary, err = f.store.ReadMediaRequestInbox(ctx, f.admin)
	if err != nil || summary.PendingCount != 32 {
		t.Fatal("terminal count", summary, err)
	}
	second, err = f.store.ListPendingMediaRequests(ctx, f.admin, 25, &cursor)
	if err != nil || len(second) != 8 {
		t.Fatal("terminal cursor must retain its position", len(second), err)
	}
	page, err := f.store.ListPendingMediaRequests(ctx, f.admin, 100, nil)
	if err != nil || len(page) != 32 {
		t.Fatal("pending filter", len(page), err)
	}
	for _, row := range page {
		if row.ID == cursor || row.State != "pending" {
			t.Fatal("closed request leaked into pending page")
		}
	}
	if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("terminal-cursor reads wrote data")
	}
}

func TestMediaRequestInboxCurrentPermissionPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	ctx := t.Context()
	f.submit(t, f.owner, "protected-count")
	if _, err := f.store.ReadMediaRequestInbox(ctx, f.owner); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("member summary", err)
	}
	if _, err := f.store.ListPendingMediaRequests(ctx, f.other, 25, nil); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("member filter", err)
	}
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(f.admin.ID))
	before := mediaInboxSnapshot(t, f)
	if _, err := f.store.ReadMediaRequestInbox(ctx, f.admin); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("stale administrator", err)
	}
	if _, err := f.store.ListPendingMediaRequests(ctx, f.admin, 25, nil); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("stale administrator page", err)
	}
	if !reflect.DeepEqual(before, mediaInboxSnapshot(t, f)) {
		t.Fatal("denied reads wrote data")
	}
}

func TestMediaRequestInboxFailureAndInputPG(t *testing.T) {
	f := newMediaRequestFixture(t)
	ctx := t.Context()
	for _, limit := range []int{0, -1, 101} {
		if _, err := f.store.ListPendingMediaRequests(ctx, f.admin, limit, nil); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid limit", limit, err)
		}
	}
	missing := uuid.New()
	if _, err := f.store.ListPendingMediaRequests(ctx, f.admin, 25, &missing); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("missing cursor", err)
	}
	zero := uuid.Nil
	if _, err := f.store.ListPendingMediaRequests(ctx, f.admin, 25, &zero); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("nil cursor", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.store.ReadMediaRequestInbox(cancelled, f.admin); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation", err)
	}
	f.exec(t, `ALTER TABLE media_request RENAME TO media_request_inbox_unavailable`)
	defer f.exec(t, `ALTER TABLE media_request_inbox_unavailable RENAME TO media_request`)
	if _, err := f.store.ReadMediaRequestInbox(ctx, f.admin); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("missing store is not zero", err)
	}
	if _, err := f.store.ListPendingMediaRequests(ctx, f.admin, 25, nil); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("missing store is not empty", err)
	}
}

package postgres_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Any attempted filesystem operation panics; list reads must remain database-only.
type recycleSearchNoFilesystem struct {
	ports.LibraryMaintenanceFilesystem
}

func recycleSearchGraph(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, e := pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		names = append(names, name)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	graph := map[string]string{}
	for _, name := range names {
		var value string
		e = pool.QueryRow(t.Context(), fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM %s t`, pgx.Identifier{"public", name}.Sanitize())).Scan(&value)
		if e != nil {
			t.Fatal(name, e)
		}
		graph[name] = value
	}
	return graph
}

func TestLibraryMaintenanceRecycleSearchPG(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			var f maintenanceRetiredFixture
			if kind == "movie" {
				f = maintenanceMovieRetired(t)
			} else {
				f = maintenanceTVRetired(t)
			}
			var original string
			if e := f.pool.QueryRow(t.Context(), `SELECT original_rel FROM library_verified_recycle WHERE recycle_id=$1`, f.recycleID).Scan(&original); e != nil {
				t.Fatal(e)
			}
			store, e := postgres.NewLibraryMaintenanceStore(f.pool, &recycleSearchNoFilesystem{}, idgen.New())
			if e != nil {
				t.Fatal(e)
			}
			before := recycleSearchGraph(t, f.pool)
			other := "movie"
			if kind == "movie" {
				other = "tv"
			}
			for _, query := range []ports.LibraryRecycleQuery{
				{Limit: 1}, {Limit: 1, Kind: kind}, {Limit: 1, Kind: other},
				{Limit: 1, Kind: kind, Query: "  " + strings.ToUpper(original) + "  "},
				{Limit: 1, Query: "%"}, {Limit: 1, Query: "_"}, {Limit: 1, Query: "__not_a_real_retirement__"},
			} {
				page, e := store.ListRecycle(t.Context(), f.actor, query)
				want := 0
				if (query.Kind == "" || query.Kind == kind) && strings.Contains(strings.ToLower(original), strings.ToLower(strings.TrimSpace(query.Query))) {
					want = 1
				}
				if e != nil || page == nil || len(page.Items) != want || page.NextCursor != "" {
					t.Fatalf("filter kind=%q query=%q: page=%v err=%v want=%d", query.Kind, query.Query, page, e, want)
				}
				if want == 1 && page.Items[0].RecycleID != f.recycleID {
					t.Fatal("wrong retirement")
				}
			}
			legacy := base64.RawURLEncoding.EncodeToString([]byte(f.recycleID))
			page, e := store.ListRecycle(t.Context(), f.actor, ports.LibraryRecycleQuery{Limit: 1, Cursor: legacy})
			if e != nil || len(page.Items) != 0 {
				t.Fatal("legacy exclusive position", page, e)
			}
			query := ports.LibraryRecycleQuery{Limit: 1, Kind: kind, Query: original}
			raw, _ := json.Marshal(struct {
				Version int    `json:"v"`
				After   string `json:"after"`
				Kind    string `json:"kind"`
				Query   string `json:"q"`
			}{1, f.recycleID, kind, original})
			query.Cursor = base64.RawURLEncoding.EncodeToString(raw)
			page, e = store.ListRecycle(t.Context(), f.actor, query)
			if e != nil || len(page.Items) != 0 {
				t.Fatal("filtered exclusive position", page, e)
			}
			query.Kind = other
			if _, e = store.ListRecycle(t.Context(), f.actor, query); !errors.Is(e, ports.ErrRejected) {
				t.Fatal("cross-filter cursor", e)
			}
			query.Kind = kind
			query.Cursor = legacy
			if _, e = store.ListRecycle(t.Context(), f.actor, query); !errors.Is(e, ports.ErrRejected) {
				t.Fatal("legacy cross-filter", e)
			}
			if after := recycleSearchGraph(t, f.pool); !reflect.DeepEqual(before, after) {
				for name, value := range before {
					if after[name] != value {
						t.Error("read mutated table", name)
					}
				}
			}
			if _, e = f.pool.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = store.ListRecycle(t.Context(), f.actor, ports.LibraryRecycleQuery{Limit: 1, Kind: kind}); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("current revocation", e)
			}
		})
	}
}

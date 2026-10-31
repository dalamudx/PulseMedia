package postgres

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestDeliveryHistoryCursor(t *testing.T) {
	id := uuid.New()
	at := time.Date(2026, 10, 1, 3, 4, 5, 123456000, time.UTC)
	query := ports.DeliveryListQuery{Limit: 100, State: "dead", Cursor: encodeDeliveryListCursor(at, id, ports.DeliveryListQuery{State: "dead"})}
	got, e := decodeDeliveryListCursor(query)
	if e != nil || got.ID != id || !got.UpdatedAt.Equal(at) {
		t.Fatal(got, e)
	}
	for name, change := range map[string]func(*ports.DeliveryListQuery){
		"cross-state":   func(q *ports.DeliveryListQuery) { q.State = "pending" },
		"dropped-state": func(q *ports.DeliveryListQuery) { q.State = "" },
		"bad-state":     func(q *ports.DeliveryListQuery) { q.State = "failed" },
		"padding":       func(q *ports.DeliveryListQuery) { q.Cursor += "=" },
		"oversized":     func(q *ports.DeliveryListQuery) { q.Cursor = strings.Repeat("A", 513) },
		"small-limit":   func(q *ports.DeliveryListQuery) { q.Limit = 0 },
		"large-limit":   func(q *ports.DeliveryListQuery) { q.Limit = 101 },
		"unknown-field": func(q *ports.DeliveryListQuery) {
			raw, _ := base64.RawURLEncoding.DecodeString(q.Cursor)
			q.Cursor = base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`))
		},
		"invalid-json": func(q *ports.DeliveryListQuery) { q.Cursor = base64.RawURLEncoding.EncodeToString([]byte(`{} {}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := query
			change(&bad)
			if _, e := decodeDeliveryListCursor(bad); !errors.Is(e, ports.ErrInvalidDeliveryCommand) {
				t.Fatal(e)
			}
		})
	}
	query.Cursor = ""
	for _, state := range []string{"", "pending", "delivering", "delivered", "dead", "skipped"} {
		query.State = state
		if _, e := decodeDeliveryListCursor(query); e != nil {
			t.Fatal(state, e)
		}
	}
}

func TestDeliveryHistoryEventCursorCompatibility(t *testing.T) {
	id, event := uuid.New(), uuid.New()
	at := time.Date(2026, 10, 1, 3, 4, 5, 123456000, time.UTC)
	// The old encoding has no event_id member; its exact bytes remain accepted.
	legacy := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"state":"dead","at":"2026-10-01T03:04:05.123456Z","id":"` + id.String() + `"}`))
	old := ports.DeliveryListQuery{Limit: 1, State: "dead", Cursor: legacy}
	if _, e := decodeDeliveryListCursor(old); e != nil || encodeDeliveryListCursor(at, id, old) != legacy {
		t.Fatal("old cursor changed", e)
	}
	query := ports.DeliveryListQuery{Limit: 1, State: "dead", EventID: event}
	query.Cursor = encodeDeliveryListCursor(at, id, query)
	if got, e := decodeDeliveryListCursor(query); e != nil || got.EventID != event.String() {
		t.Fatal(got, e)
	}
	for name, change := range map[string]func(*ports.DeliveryListQuery){
		"different-event":   func(q *ports.DeliveryListQuery) { q.EventID = uuid.New() },
		"dropped-event":     func(q *ports.DeliveryListQuery) { q.EventID = uuid.Nil },
		"changed-state":     func(q *ports.DeliveryListQuery) { q.State = "pending" },
		"unscoped-to-event": func(q *ports.DeliveryListQuery) { q.Cursor = legacy },
	} {
		t.Run(name, func(t *testing.T) {
			bad := query
			change(&bad)
			if _, e := decodeDeliveryListCursor(bad); !errors.Is(e, ports.ErrInvalidDeliveryCommand) {
				t.Fatal(e)
			}
		})
	}
}

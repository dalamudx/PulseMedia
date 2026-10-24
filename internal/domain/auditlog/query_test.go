package auditlog_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
	"github.com/google/uuid"
)

var account = uuid.MustParse("00000000-0000-4000-8000-000000000001")
var auditID = uuid.MustParse("00000000-0000-4000-8000-000000000002")
var observed = time.Date(2026, 10, 23, 12, 0, 0, 123000, time.UTC)

func TestAuditCursorAccountFilterAndUTC(t *testing.T) {
	filter := auditlog.Filter{Action: "subscription.create", From: observed.Add(-time.Hour)}
	p := auditlog.Position{ID: auditID, CreatedAt: observed}
	value, err := auditlog.EncodeCursor(account, filter, p)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := filter
	equivalent.From = filter.From.In(time.FixedZone("offset", 8*3600))
	decoded, err := auditlog.DecodeCursor(value, account, equivalent)
	if err != nil || decoded == nil || decoded.ID != p.ID || !decoded.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("round trip: %v %v", decoded, err)
	}
	for _, change := range []func(*auditlog.Filter){
		func(f *auditlog.Filter) { f.ActorType = "system" }, func(f *auditlog.Filter) { f.ActorID = "system" }, func(f *auditlog.Filter) { f.Action = "subscription.pause" }, func(f *auditlog.Filter) { f.TargetType = "subscription" }, func(f *auditlog.Filter) { f.TargetID = auditID.String() }, func(f *auditlog.Filter) { f.From = f.From.Add(time.Microsecond) }, func(f *auditlog.Filter) { f.Until = observed },
	} {
		changed := filter
		change(&changed)
		if _, err := auditlog.DecodeCursor(value, account, changed); err != auditlog.ErrInvalidQuery {
			t.Fatalf("changed filter accepted: %v", err)
		}
	}
	if _, err := auditlog.DecodeCursor(value, auditID, filter); err != auditlog.ErrInvalidQuery {
		t.Fatalf("foreign account accepted: %v", err)
	}
}

func TestAuditCursorRejectsAmbiguousEncoding(t *testing.T) {
	value, err := auditlog.EncodeCursor(account, auditlog.Filter{}, auditlog.Position{ID: auditID, CreatedAt: observed})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	invalid := []string{"!", value + "=", strings.Repeat("a", 1025)}
	for _, body := range []string{strings.Replace(string(raw), "{", "{\"unknown\":1,", 1), strings.Replace(string(raw), "{", "{\"v\":1,", 1), string(raw) + " ", strings.Replace(string(raw), "\"v\":1", "\"v\":2", 1)} {
		invalid = append(invalid, base64.RawURLEncoding.EncodeToString([]byte(body)))
	}
	for i, input := range invalid {
		if _, err := auditlog.DecodeCursor(input, account, auditlog.Filter{}); err != auditlog.ErrInvalidQuery {
			t.Fatalf("invalid %d accepted: %v", i, err)
		}
	}
	for _, p := range []auditlog.Position{{ID: uuid.Nil, CreatedAt: observed}, {ID: auditID}, {ID: auditID, CreatedAt: observed.Add(time.Nanosecond)}} {
		if _, err := auditlog.EncodeCursor(account, auditlog.Filter{}, p); err != auditlog.ErrInvalidQuery {
			t.Fatalf("invalid position accepted: %v", err)
		}
	}
	if p, err := auditlog.DecodeCursor("", account, auditlog.Filter{}); err != nil || p != nil {
		t.Fatalf("first page: %v %v", p, err)
	}
}

func TestAuditFilterBoundsAndLiteralValues(t *testing.T) {
	for _, f := range []auditlog.Filter{{ActorType: "admin"}, {ActorID: " leading"}, {Action: "line\nbreak"}, {TargetType: strings.Repeat("x", 257)}, {TargetID: string([]byte{255})}, {From: observed, Until: observed}, {From: observed, Until: observed.Add(-time.Second)}, {From: observed.Add(time.Nanosecond)}} {
		if _, err := auditlog.Normalize(f); err != auditlog.ErrInvalidQuery {
			t.Fatalf("bad filter accepted: %v", err)
		}
	}
	literal := auditlog.Filter{Action: "literal_%'", TargetID: "对象", From: observed, Until: observed.Add(time.Microsecond)}
	got, err := auditlog.Normalize(literal)
	if err != nil || got != literal {
		t.Fatalf("literal filter changed: %+v %v", got, err)
	}
}

func TestAuditPositionOrdering(t *testing.T) {
	a := auditlog.Position{ID: account, CreatedAt: observed}
	b := auditlog.Position{ID: auditID, CreatedAt: observed}
	if !a.Before(b) || b.Before(a) || a.Before(a) {
		t.Fatal("same timestamp ordering must use UUID")
	}
	a.CreatedAt = a.CreatedAt.Add(time.Second)
	if a.Before(b) || !b.Before(a) {
		t.Fatal("timestamp must precede UUID ordering")
	}
}

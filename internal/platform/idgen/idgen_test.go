package idgen

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUUIDv7Generator(t *testing.T) {
	gen := New()
	id1 := gen.NewUUID()
	if id1 == uuid.Nil {
		t.Fatal("expected non-nil uuid")
	}
	if id1.Version() != 7 {
		t.Fatalf("expected version 7, got %d", id1.Version())
	}

	time.Sleep(2 * time.Millisecond)
	id2 := gen.NewUUID()
	if id1 == id2 {
		t.Fatal("expected distinct UUIDs")
	}

	// UUIDv7 has time-ordering in byte representation
	if bytes.Compare(id1[:], id2[:]) >= 0 {
		t.Fatalf("expected id1 < id2 for time ordering: %s vs %s", id1, id2)
	}

	str := gen.NewUUIDString()
	parsed, err := uuid.Parse(str)
	if err != nil {
		t.Fatalf("failed to parse string representation: %v", err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("expected version 7 for string generated uuid, got %d", parsed.Version())
	}
}

func TestMockIDGen(t *testing.T) {
	fixed1 := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
	fixed2 := uuid.MustParse("018e0000-0000-7000-8000-000000000002")

	mock := NewMock(fixed1, fixed2)
	if got := mock.NewUUID(); got != fixed1 {
		t.Fatalf("expected %v, got %v", fixed1, got)
	}
	if got := mock.NewUUID(); got != fixed2 {
		t.Fatalf("expected %v, got %v", fixed2, got)
	}
	// Fallback to last used
	if got := mock.NewUUID(); got != fixed2 {
		t.Fatalf("expected fallback %v, got %v", fixed2, got)
	}
}

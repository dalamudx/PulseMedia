package clock

import (
	"testing"
	"time"
)

func TestRealClock(t *testing.T) {
	c := New()
	now := c.Now()
	if now.IsZero() {
		t.Fatal("expected non-zero time from RealClock")
	}
	time.Sleep(2 * time.Millisecond)
	if c.Since(now) <= 0 {
		t.Fatal("expected positive duration from Since")
	}
}

func TestMockClock(t *testing.T) {
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	mock := NewMock(start)

	if got := mock.Now(); !got.Equal(start) {
		t.Fatalf("expected %v, got %v", start, got)
	}

	mock.Advance(10 * time.Minute)
	expected := start.Add(10 * time.Minute)
	if got := mock.Now(); !got.Equal(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}

	if since := mock.Since(start); since != 10*time.Minute {
		t.Fatalf("expected since=10m, got %v", since)
	}

	newTime := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	mock.Set(newTime)
	if got := mock.Now(); !got.Equal(newTime) {
		t.Fatalf("expected %v, got %v", newTime, got)
	}
}

package library_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/library"
	"math"
	"testing"
)

func TestMissingReviewBoundary(t *testing.T) {
	for _, c := range []struct {
		name             string
		missing, indexed int64
		review           bool
	}{
		{"empty", 0, 0, false}, {"below count", 9, 9, false}, {"exact twenty percent", 10, 50, false},
		{"over twenty percent", 10, 49, true}, {"below fraction", 10, 51, false},
		{"high fraction", 11, 50, true}, {"unknown negative", 10, -1, true},
		{"invalid missing", -1, 100, true}, {"large safe", math.MaxInt64 / 5, math.MaxInt64, false},
		{"large unsafe", math.MaxInt64/5 + 1, math.MaxInt64, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := library.MissingReviewRequired(c.missing, c.indexed); got != c.review {
				t.Fatalf("got %v want %v", got, c.review)
			}
		})
	}
}

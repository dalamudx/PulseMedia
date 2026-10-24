package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
	"testing"
	"time"
)

func TestAutomaticTVSourceFirstPageBudget(t *testing.T) {
	for _, mode := range []string{"wait_then_query", "cancel_wait", "too_long", "lookup_error", "negative_delay"} {
		t.Run(mode, func(t *testing.T) {
			svc, r, c := newAutomaticTVHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.delay = 30 * time.Millisecond
			started := time.Now()
			switch mode {
			case "cancel_wait":
				r.delay = time.Second
				timer := time.AfterFunc(10*time.Millisecond, cancel)
				defer timer.Stop()
			case "too_long":
				r.delay = 9 * time.Second
			case "lookup_error":
				r.delayErr = errors.New("private provider URL/key")
			case "negative_delay":
				r.delay = -time.Second
			}
			c.call = func(_ context.Context, q ports.TVEpisodeQuery) (*ports.IndexerPage, error) {
				if mode != "wait_then_query" {
					t.Error("first page sent before its budget was available")
				}
				if time.Since(started) < r.delay {
					t.Error("first page did not wait for shared budget")
				}
				return automaticTVPage(q, 0, 0), nil
			}
			err := svc.ExecuteAutomaticTVSource(ctx, r.work.Task)
			if mode == "wait_then_query" {
				if err != nil || r.calls.Load() != 1 || r.outcome != ports.AutomaticTVSourceSucceeded {
					t.Fatal("first page did not execute once after waiting", err, r.calls.Load(), r.outcome)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "private") || r.calls.Load() != 0 || len(r.specs) != 0 || r.commits.Load() != 0 {
				t.Fatal("budget rejection created query authority or network I/O", err, r.calls.Load(), len(r.specs), r.commits.Load())
			}
			if mode == "too_long" && !errors.Is(err, ports.ErrAutomaticTVSearchDeferred) {
				t.Fatal("long wait did not remain deferred", err)
			}
			if mode == "cancel_wait" && !errors.Is(err, context.Canceled) {
				t.Fatal("budget wait ignored cancellation", err)
			}
		})
	}
}

package httpapi_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestCompleteTVByteSenderNativePGConcurrentManagersBeforePreparation(t *testing.T) {
	s := newByteSenderFixture(t, completeTVRaw)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	s.fetchHook = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() {
		r, e := s.send(context.Background())
		if e == nil && (r == nil || r.State != "tracking") {
			e = errors.New("missing tracking")
		}
		done <- e
	}()
	defer func() {
		unblock()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("sender not joined")
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("fetch not started")
	}
	// No task exists yet. Only the session lock can exclude the second fetch.
	if s.preparedCounts(t) != ([6]int{}) {
		t.Fatal("unexpected preparation")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.newManager().SendCompleteTV(context.Background(), s.c.Lease, s.c.OperationID)
			if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
				errs <- errors.New("parallel sender admitted")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if s.fetches.Load() != 1 || s.adds.Load() != 0 {
		t.Fatal("overlapping fetch/Add")
	}
	unblock()
}
func TestCompleteTVByteSenderNativePGAmbiguous200(t *testing.T) {
	for _, body := range []string{"", "Fails.", "unrecognized-private-body", strings.Repeat("x", 4097)} {
		t.Run("body-"+string(rune('a'+len(body)%20)), func(t *testing.T) {
			s := newByteSenderFixture(t, completeTVRaw)
			s.addBody = body
			r, e := s.send(context.Background())
			if e != nil || r == nil || r.State != "unknown" || s.state(t) != "unknown" {
				t.Fatal(r, e)
			}
			if s.fetches.Load() != 1 || s.adds.Load() != 1 {
				t.Fatal("physical count")
			}
			s.protected(t)
			_, e = s.send(context.Background())
			if !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || s.fetches.Load() != 1 {
				t.Fatal("ambiguous result replay", e)
			}
		})
	}
}
func TestCompleteTVByteSenderNativePGConstructedConfigRace(t *testing.T) {
	s := newByteSenderFixture(t, completeTVRaw)
	s.manager.SetClientFactory(func(_, url, user, password string) (ports.DownloadClient, error) {
		// Mutate after the sender resolves actual credentials, before Prepare locks
		// them. The previously constructed instance must not be silently authorized.
		s.exec(t, `UPDATE download_client SET username='rotated-after-resolution'`)
		return qbittorrent.NewClient(url, user, password, nil)
	})
	r, e := s.send(context.Background())
	if r != nil || !errors.Is(e, ports.ErrCompleteTVSubmissionReview) || s.adds.Load() != 0 {
		t.Fatal(r, e)
	}
	if s.preparedCounts(t) != ([6]int{}) {
		t.Fatal("stale constructed config prepared")
	}
}

package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCompleteTVNativeObserverNativePGLostSessionEdges(t *testing.T) {
	for _, phase := range []string{"http", "commit"} {
		t.Run(phase, func(t *testing.T) {
			n := newNativeObserverFixture(t)
			entered, release := make(chan struct{}, 2), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			done := make(chan error, 1)
			repo := ports.CompleteTVObservationRepository(n.observer)
			if phase == "http" {
				n.hook = func() { entered <- struct{}{}; <-release }
			} else {
				repo = &observationBarrier{n.observer, entered, release}
			}
			go func() { _, e := n.manager.ObserveCompleteTV(context.Background(), repo, n.request); done <- e }()
			defer func() {
				unblock()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					t.Error("observer not joined")
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("not entered")
			}
			n.exec(t, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND objsubid=1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND pid<>pg_backend_pid()`)
			if phase == "http" {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				r, e := n.newManager().ObserveCompleteTV(ctx, n.observer, n.request)
				if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || n.lookups.Load() != 1 {
					t.Errorf("physical exclusion lost: receipt=%v error=%v requests=%d", r, e, n.lookups.Load())
				}
			}
			unblock()
			select {
			case e := <-done:
				if !errors.Is(e, ports.ErrUnavailable) {
					t.Errorf("lost physical session accepted: %v", e)
				}
				done <- e
			case <-time.After(10 * time.Second):
				t.Fatal("observer not returned")
			}
			var state string
			if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT state FROM download_task WHERE id=$1`, n.request.TaskID).Scan(&state); e != nil || state != "unknown" {
				t.Fatal("lost session wrote", state, e)
			}
		})
	}
}

func TestCompleteTVNativeObserverNativePGDuplicateIdentityFields(t *testing.T) {
	for _, key := range []string{"hash", "HASH", "tags", "save_path", "size"} {
		t.Run(key, func(t *testing.T) {
			n := newNativeObserverFixture(t)
			body := n.body()
			bad := `"wrong"`
			if key == "size" {
				bad = "1"
			}
			n.body = func() string { return strings.Replace(body, "[{", "[{\""+key+"\":"+bad+",", 1) }
			before := n.snapshot(t)
			r, e := n.observe(context.Background())
			if r != nil || !errors.Is(e, ports.ErrUnavailable) || n.snapshot(t) != before {
				t.Fatal("ambiguous object accepted", r, e)
			}
		})
	}
}

func TestCompleteTVNativeObserverNativePGNativeCancel(t *testing.T) {
	n := newNativeObserverFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	n.hook = func() { close(entered); <-release }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := n.observe(ctx); done <- e }()
	defer unblock()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("HTTP not entered")
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Errorf("native cancellation lost: %v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation not returned")
	}
}

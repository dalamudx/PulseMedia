package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHTTPDrainRetainsAcceptedNoncancellableHandler(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	owner := &runtimeHTTP{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}
	server := httptest.NewServer(owner)
	defer server.Close()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	result := make(chan error, 1)
	go func() {
		resp, err := http.Get(server.URL)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not entered")
	}
	owner.stopAccepting()
	drained := make(chan struct{})
	go func() { owner.wait(); close(drained) }()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("request accepted after stop")
	}
	select {
	case <-drained:
		t.Fatal("drain returned with physical handler still executing")
	default:
	}
	unblock()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("completed handler did not drain")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owned request did not complete")
	}
}

func TestHTTPShutdownDeadlineRetainsPhysicalHandler(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	owner := &runtimeHTTP{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
		<-release
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: owner}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(listener) }()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { _ = server.Close() }()
	defer unblock()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request not accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- owner.shutdown(ctx, server, serverDone, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("deadline did not cancel HTTP connection")
	}
	select {
	case <-shutdownDone:
		t.Fatal("logical deadline released physical handler ownership")
	default:
	}
	unblock()
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected deadline result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed physical handler did not drain")
	}
	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("owned HTTP request did not finish")
	}
}

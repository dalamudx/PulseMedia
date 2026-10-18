package app

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type drainNotice struct {
	notice chan struct{}
	once   sync.Once
}

func (w *drainNotice) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.notice) })
	return len(p), nil
}

func TestWorkerDrainDeadlineRetainsEveryPhysicalOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	first, second := make(chan struct{}), make(chan struct{})
	var once1, once2 sync.Once
	defer func() { once1.Do(func() { close(first) }); once2.Do(func() { close(second) }) }()
	writer := &drainNotice{notice: make(chan struct{})}
	joined := make(chan struct{})
	go func() { waitRuntime(ctx, slog.New(slog.NewTextHandler(writer, nil)), first, second); close(joined) }()
	select {
	case <-writer.notice:
	case <-time.After(2 * time.Second):
		t.Fatal("drain deadline not reported")
	}
	select {
	case <-joined:
		t.Fatal("deadline released unfinished physical operation")
	default:
	}
	once1.Do(func() { close(first) })
	select {
	case <-joined:
		t.Fatal("first completion released another unfinished operation")
	default:
	}
	once2.Do(func() { close(second) })
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("fully completed work failed to drain")
	}
}

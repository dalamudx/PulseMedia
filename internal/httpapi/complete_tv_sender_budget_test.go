package httpapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type sharedCompleteTVBudgetClient struct {
	ports.DownloadClient
	addEntered, testEntered chan struct{}
	release                 chan struct{}
}

func (c *sharedCompleteTVBudgetClient) CompleteTVByteSender() ports.DownloadClient { return c }
func (c *sharedCompleteTVBudgetClient) AddTorrent(context.Context, ports.AddTorrentParam) error {
	close(c.addEntered)
	<-c.release
	return errors.New("unknown test outcome")
}
func (c *sharedCompleteTVBudgetClient) Test(context.Context) error {
	c.testEntered <- struct{}{}
	<-c.release
	return nil
}

func TestCompleteTVByteSenderNativePGSharedManagerBudget(t *testing.T) {
	s := newByteSenderFixture(t, completeTVRaw)
	c := &sharedCompleteTVBudgetClient{addEntered: make(chan struct{}), testEntered: make(chan struct{}, 3), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(c.release) }) }
	id := sClientID(t, s)
	s.manager.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) { return c, nil })
	s.manager.RegisterClient(id, c)
	sendCtx, cancelSend := context.WithCancel(context.Background())
	defer cancelSend()
	sendDone, testDone := make(chan error, 1), make(chan error, 1)
	go func() { _, e := s.send(sendCtx); sendDone <- e }()
	defer func() {
		unblock()
		select {
		case e := <-sendDone:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("send not joined")
		}
	}()
	select {
	case <-c.addEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("no Add")
	}
	go func() { _, _, e := s.manager.TestClient(context.Background(), id); testDone <- e }()
	defer func() {
		unblock()
		select {
		case e := <-testDone:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			t.Error("client test not joined")
		}
	}()
	select {
	case <-c.testEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second budget slot not admitted")
	}
	cancelSend() // cancellation cannot return the occupied sender slot
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, e := s.manager.TestClient(ctx, id)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("third call bypassed shared budget", e)
	}
	select {
	case <-c.testEntered:
		t.Fatal("third physical test entered")
	default:
	}
	select {
	case <-sendDone:
		t.Fatal("canceled sender returned before Add")
	default:
	}
	s.protected(t)
}

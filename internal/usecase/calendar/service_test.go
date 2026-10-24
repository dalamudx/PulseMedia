package calendar_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/calendar"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time                  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }
func (c *clock) add(d time.Duration)             { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type provider struct {
	mu    sync.Mutex
	calls int
	err   error
	block <-chan struct{}
}

func (p *provider) GetCalendar(ctx context.Context) ([]ports.BangumiDay, error) {
	p.mu.Lock()
	p.calls++
	err, block := p.err, p.block
	p.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	score := 8.9
	return []ports.BangumiDay{{DayID: 1, DayName: "星期一", Items: []ports.BangumiItem{{ID: 1, Title: "葬送的芙莉莲", OrigTitle: "葬送のフリーレン", Rating: &score}, {ID: 2, Title: "ＤＲＡＧＯＮ", OrigTitle: "Dragon"}}}, {DayID: 7, DayName: "星期日", Items: []ports.BangumiItem{}}}, nil
}
func fixture() (*calendar.Service, *provider, *clock) {
	p := &provider{}
	c := &clock{t: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)}
	return calendar.NewService(p, c), p, c
}
func TestCalendarBilingualFilter(t *testing.T) {
	s, p, _ := fixture()
	for _, q := range []string{"芙莉莲", "フリーレン", " DRAGON ", "dragon"} {
		out, err := s.List(t.Context(), 1, q)
		if err != nil || len(out.Days) != 1 || len(out.Days[0].Items) != 1 {
			t.Fatal("bilingual filtering", q, err)
		}
	}
	out, err := s.List(t.Context(), 7, "芙莉莲")
	if err != nil || len(out.Days) != 1 || out.Days[0].Items == nil || len(out.Days[0].Items) != 0 || p.calls != 1 {
		t.Fatal("empty/one cached week", err)
	}
}
func TestCalendarCacheDeepCopyAndExpiry(t *testing.T) {
	s, p, c := fixture()
	out, err := s.List(t.Context(), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	out.Days[0].Items[0].Title = "changed"
	*out.Days[0].Items[0].Rating = 1
	out, _ = s.List(t.Context(), 0, "")
	if out.Days[0].Items[0].Title == "changed" || *out.Days[0].Items[0].Rating != 8.9 {
		t.Fatal("shared result")
	}
	c.add(5 * time.Minute)
	p.err = ports.ErrUnavailable
	if _, err = s.List(t.Context(), 0, ""); !errors.Is(err, ports.ErrUnavailable) || p.calls != 2 {
		t.Fatal("expired success hid failure", err)
	}
}
func TestCalendarInvalidAndCancelled(t *testing.T) {
	s, p, _ := fixture()
	for _, v := range []struct {
		day int
		q   string
	}{{-1, ""}, {8, ""}, {0, "bad\n"}, {0, string([]byte{255})}} {
		if _, err := s.List(t.Context(), v.day, v.q); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("bad input", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.List(ctx, 0, ""); !errors.Is(err, context.Canceled) || p.calls != 0 {
		t.Fatal("cancelled read reached provider", err)
	}
}
func TestCalendarConcurrentSingleRead(t *testing.T) {
	s, p, _ := fixture()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.List(t.Context(), 0, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p.calls != 1 {
		t.Fatal("concurrent upstream duplication")
	}
}

type watchedContext struct {
	context.Context
	once     sync.Once
	selected chan struct{}
}

func (c *watchedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.selected) })
	return c.Context.Done()
}
func TestCalendarWaitingCancellation(t *testing.T) {
	s, p, _ := fixture()
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	p.block = release
	done := make(chan error, 1)
	go func() { _, err := s.List(t.Context(), 0, ""); done <- err }()
	deadline := time.After(time.Second)
	for {
		p.mu.Lock()
		n := p.calls
		p.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("read not entered")
		case <-time.After(time.Millisecond):
		}
	}
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &watchedContext{Context: parent, selected: make(chan struct{})}
	waiting := make(chan error, 1)
	go func() { _, err := s.List(ctx, 0, ""); waiting <- err }()
	select {
	case <-ctx.selected:
	case <-time.After(time.Second):
		t.Fatal("second call did not wait on active read")
	}
	cancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("wait cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter blocked until native read returned")
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatal("cancelled waiter repeated native read")
	}
}

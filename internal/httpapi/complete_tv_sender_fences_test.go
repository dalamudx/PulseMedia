package httpapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/google/uuid"
)

type fixedSenderID struct{ id uuid.UUID }

func (i fixedSenderID) NewUUID() uuid.UUID    { return i.id }
func (i fixedSenderID) NewUUIDString() string { return i.id.String() }

func TestCompleteTVByteSenderNativePGAlreadyExistedRace(t *testing.T) {
	s := newByteSenderFixture(t, completeTVRaw)
	// A separate preparer wins while native fetch is in flight. The exact same
	// task/lease/proof permits only AlreadyExisted, never a fresh send receipt.
	s.manager = download.NewManager(s.f.a.DBPool(), fixedSenderID{s.c.TaskID}, s.key)
	s.manager.SetClientFactory(func(_, url, user, password string) (ports.DownloadClient, error) {
		return qbittorrent.NewClient(url, user, password, nil)
	})
	s.configureCompleteTV(s.manager)
	s.fetchHook = func() {
		r, e := s.prepare()
		if e != nil || r.AlreadyExisted {
			t.Error("competing preparation", r, e)
		}
	}
	r, e := s.send(context.Background())
	if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || s.fetches.Load() != 1 || s.adds.Load() != 0 {
		t.Fatal(r, e)
	}
	s.protected(t)
}
func TestCompleteTVByteSenderNativePGNativeFetchBound(t *testing.T) {
	s := newByteSenderFixture(t, strings.Repeat("x", ports.MaxTorrentMetainfoBytes+1))
	r, e := s.send(context.Background())
	if r != nil || !errors.Is(e, ports.ErrUnavailable) || s.fetches.Load() != 1 || s.adds.Load() != 0 || s.preparedCounts(t) != ([6]int{}) {
		t.Fatal(r, e)
	}
}
func TestCompleteTVByteSenderNativePGReadFailures(t *testing.T) {
	for _, mode := range []string{"read-unavailable", "credentials", "unsupported-client"} {
		t.Run(mode, func(t *testing.T) {
			s := newByteSenderFixture(t, completeTVRaw)
			switch mode {
			case "read-unavailable":
				s.exec(t, `ALTER TABLE complete_tv_submission_preparation RENAME TO unavailable_preparation`)
			case "credentials":
				s.manager = download.NewManager(s.f.a.DBPool(), idgen.New(), make([]byte, 32))
				s.manager.SetClientFactory(func(_, url, user, password string) (ports.DownloadClient, error) {
					return qbittorrent.NewClient(url, user, password, nil)
				})
				s.configureCompleteTV(s.manager)
			case "unsupported-client":
				s.manager.SetClientFactory(func(_, _, _, _ string) (ports.DownloadClient, error) { return &genericNoByteContract{}, nil })
			}
			r, e := s.send(context.Background())
			if r != nil || e == nil || strings.Contains(e.Error(), "private-") || s.fetches.Load() != 0 || s.adds.Load() != 0 {
				t.Fatal(r, e)
			}
			if mode != "unsupported-client" && !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("infrastructure/crypto error changed", e)
			}
		})
	}
}

type genericNoByteContract struct{ ports.DownloadClient }

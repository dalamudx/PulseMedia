package watchlist_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
)

func TestConfigBindingsInputAndIsolation(t *testing.T) {
	small := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	large := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	client := uuid.New()
	for _, tc := range []struct {
		name string
		b    *ports.DoubanSyncBindings
		bad  bool
	}{
		{"omitted", nil, false}, {"clear", &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{}}, false},
		{"pair", &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{large, small}, DownloaderID: &client}, false},
		{"missing_list", &ports.DoubanSyncBindings{}, true},
		{"missing_client", &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{small}}, true},
		{"client_only", &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{}, DownloaderID: &client}, true},
		{"duplicate", &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{small, small}, DownloaderID: &client}, true},
		{"zero", &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{uuid.Nil}, DownloaderID: &client}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, in, r := configFixture()
			in.Bindings = tc.b
			expected := tc.b
			if tc.name == "pair" {
				expected = &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{small, large}, DownloaderID: &client}
			}
			r.receipt.Config.Bindings = expected
			var passed *ports.DoubanSyncBindings
			r.change = func(_ *ports.Actor, x ports.DoubanSyncInput) { passed = x.Bindings }
			out, err := watchlist.NewConfigService(r).Save(t.Context(), a, in, "original-key")
			if tc.bad {
				if !errors.Is(err, ports.ErrRejected) || r.saves != 0 {
					t.Fatal("invalid binding reached repository")
				}
				return
			}
			if err != nil || out == nil {
				t.Fatal(err)
			}
			if tc.name == "pair" {
				if !slices.Equal(passed.IndexerIDs, []uuid.UUID{small, large}) || in.Bindings.IndexerIDs[0] != large {
					t.Fatal("input not isolated and sorted")
				}
				passed.IndexerIDs[0] = uuid.New()
				*passed.DownloaderID = uuid.New()
				if !slices.Equal(out.Config.Bindings.IndexerIDs, []uuid.UUID{small, large}) || *out.Config.Bindings.DownloaderID != client {
					t.Fatal("repository input aliases result")
				}
				out.Config.Bindings.IndexerIDs[0] = uuid.New()
				if expected.IndexerIDs[0] != small {
					t.Fatal("response aliases repository")
				}
			}
		})
	}
}

func TestConfigBindingsOmittedInheritanceAndMismatch(t *testing.T) {
	a, in, r := configFixture()
	idx, client := uuid.New(), uuid.New()
	r.receipt.Config.Bindings = &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{idx}, DownloaderID: &client}
	out, err := watchlist.NewConfigService(r).Save(t.Context(), a, in, "original-key")
	if err != nil || out == nil || out.Config.Bindings == nil {
		t.Fatal("omission rejected inherited bindings")
	}
	in.Bindings = &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{uuid.New()}, DownloaderID: &client}
	if _, err = watchlist.NewConfigService(r).Save(t.Context(), a, in, "original-key"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("mismatched explicit receipt trusted")
	}
}

func TestWishSyncFrozenBindings(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(map[bool]string{false: "pair", true: "clear"}[clear], func(t *testing.T) {
			r, p, m, _, lease := syncFixture("tv")
			idx, client := uuid.New(), uuid.New()
			wantClient := client
			b := &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{idx}, DownloaderID: &client}
			if clear {
				b = &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{}}
			}
			r.p.work.Config.Bindings = b
			originalProvider := p
			p = wishProvider(func(ctx context.Context, u, k string, start int) (ports.DoubanWishPage, error) {
				page, err := originalProvider(ctx, u, k, start)
				if !clear {
					b.IndexerIDs[0] = uuid.New()
					*b.DownloaderID = uuid.New()
				}
				return page, err
			})
			calls := 0
			c := wishCreator(func(_ context.Context, _ ports.DoubanWishCreationFence, _ *ports.Actor, in ports.SubscriptionCreationInput, _ string) (*ports.SubscriptionCreationReceipt, error) {
				calls++
				if in.IndexerIDs == nil {
					t.Fatal("explicit binding omitted")
				}
				if clear {
					if len(in.IndexerIDs) != 0 || in.DownloaderID != nil {
						t.Fatal("clear changed")
					}
				} else {
					if len(in.IndexerIDs) != 1 || in.IndexerIDs[0] != idx || in.DownloaderID == nil || *in.DownloaderID != wantClient {
						t.Fatal("frozen binding changed")
					}
					in.IndexerIDs[0] = uuid.New()
					*in.DownloaderID = uuid.New()
				}
				return &ports.SubscriptionCreationReceipt{Subscription: ports.SubscriptionRecord{UserID: r.p.work.Config.OwnerID, RootID: *r.p.work.Config.TVRootID}}, nil
			})
			if err := watchlist.NewSyncService(r, p, m, c).Execute(t.Context(), lease); err != nil || calls != 1 || r.p.finished != 1 {
				t.Fatalf("pipeline calls=%d err=%v", calls, err)
			}
		})
	}
}

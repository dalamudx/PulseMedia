package download

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type tvUpgradeFetcher func(context.Context, ports.CompleteTVTorrentSource) ([]byte, error)

func (f tvUpgradeFetcher) FetchCompleteTVTorrent(ctx context.Context, source ports.CompleteTVTorrentSource) ([]byte, error) {
	return f(ctx, source)
}

type tvUpgradeInspector func([]byte) (*ports.TorrentMetainfo, error)

func (f tvUpgradeInspector) InspectTorrent(raw []byte) (*ports.TorrentMetainfo, error) {
	return f(raw)
}

func tvUpgradeNativeFixture(t *testing.T) (*Manager, ports.TVEpisodeUpgradeNative, ports.TVEpisodeUpgradeNativeConfig) {
	t.Helper()
	m := &Manager{masterKey: []byte(strings.Repeat("k", 32)), clients: make(map[uuid.UUID]ports.DownloadClient)}
	n, err := NewTVEpisodeUpgradeNative(m,
		tvUpgradeFetcher(func(context.Context, ports.CompleteTVTorrentSource) ([]byte, error) { return []byte("metainfo"), nil }),
		tvUpgradeInspector(func(raw []byte) (*ports.TorrentMetainfo, error) {
			return &ports.TorrentMetainfo{TorrentBytes: raw, BytesSHA256: sha256.Sum256(raw), InfoHash: strings.Repeat("a", 40), TotalBytes: 10, Files: []media.TVTorrentFile{{Path: "show.S01E01.mkv", Size: 10}}}, nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	return m, n, ports.TVEpisodeUpgradeNativeConfig{ClientID: uuid.New(), ClientType: "qbittorrent", URL: "https://frozen.example", Username: "frozen-user", PasswordEncrypted: tvUpgradeEncrypt(t, m, "frozen-password")}
}

func tvUpgradeEncrypt(t *testing.T, m *Manager, value string) []byte {
	t.Helper()
	out, err := crypto.Encrypt(m.masterKey, []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTVEpisodeUpgradeNativeConstructor(t *testing.T) {
	m, n, _ := tvUpgradeNativeFixture(t)
	impl := n.(*tvEpisodeUpgradeNative)
	for _, tc := range []struct {
		manager   *Manager
		fetcher   ports.CompleteTVTorrentFetcher
		inspector ports.TorrentMetainfoInspector
	}{{nil, impl.fetcher, impl.inspector}, {&Manager{}, impl.fetcher, impl.inspector}, {m, nil, impl.inspector}, {m, impl.fetcher, nil}} {
		if _, err := NewTVEpisodeUpgradeNative(tc.manager, tc.fetcher, tc.inspector); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(err)
		}
	}
}

func TestTVEpisodeUpgradeNativeFetch(t *testing.T) {
	m, n, _ := tvUpgradeNativeFixture(t)
	impl := n.(*tvEpisodeUpgradeNative)
	link, key := tvUpgradeEncrypt(t, m, "https://source.example/private"), tvUpgradeEncrypt(t, m, "api-secret")
	calls := 0
	impl.fetcher = tvUpgradeFetcher(func(_ context.Context, source ports.CompleteTVTorrentSource) ([]byte, error) {
		calls++
		if source.EndpointURL != "https://source.example" || source.DownloadURL != "https://source.example/private" || source.APIKey != "api-secret" {
			t.Fatal("incorrect decrypted source")
		}
		return []byte("metainfo"), nil
	})
	proof, err := n.FetchTVEpisodeUpgrade(t.Context(), "https://source.example", link, key)
	if err != nil || proof == nil || !proof.ValidateBinding() || string(proof.Bytes()) != "metainfo" || calls != 1 {
		t.Fatal(proof, err, calls)
	}
	for _, pair := range [][2][]byte{{[]byte("bad"), key}, {link, []byte("bad")}} {
		if _, err := n.FetchTVEpisodeUpgrade(t.Context(), "https://source.example", pair[0], pair[1]); !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := n.FetchTVEpisodeUpgrade(ctx, "https://source.example", link, key); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err, calls)
	}
	fetchErr := errors.New("fetch failed")
	impl.fetcher = tvUpgradeFetcher(func(context.Context, ports.CompleteTVTorrentSource) ([]byte, error) { return nil, fetchErr })
	if _, err := n.FetchTVEpisodeUpgrade(t.Context(), "", link, key); !errors.Is(err, fetchErr) {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{nil, make([]byte, ports.MaxTorrentMetainfoBytes+1)} {
		impl.fetcher = tvUpgradeFetcher(func(context.Context, ports.CompleteTVTorrentSource) ([]byte, error) { return raw, nil })
		if _, err := n.FetchTVEpisodeUpgrade(t.Context(), "", link, key); !errors.Is(err, ports.ErrAutomaticTVMetainfoReview) {
			t.Fatal(err)
		}
	}
	impl.fetcher = tvUpgradeFetcher(func(context.Context, ports.CompleteTVTorrentSource) ([]byte, error) { return []byte("metainfo"), nil })
	impl.inspector = tvUpgradeInspector(func([]byte) (*ports.TorrentMetainfo, error) { return nil, errors.New("invalid torrent") })
	if _, err := n.FetchTVEpisodeUpgrade(t.Context(), "", link, key); !errors.Is(err, ports.ErrAutomaticTVMetainfoReview) {
		t.Fatal(err)
	}
}

type tvUpgradeNativeClient struct {
	ports.DownloadClient
	adds, lists, files int
}

func (c *tvUpgradeNativeClient) AddTorrent(context.Context, ports.AddTorrentParam) error {
	c.adds++
	return ports.ErrUnavailable
}
func (c *tvUpgradeNativeClient) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	c.lists++
	return []*ports.TorrentInfo{{InfoHash: strings.Repeat("a", 40)}}, nil
}
func (c *tvUpgradeNativeClient) GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error) {
	c.files++
	return []ports.TorrentFile{{}}, nil
}

type tvUpgradeAutomaticCapability struct {
	ports.DownloadClient
	native ports.DownloadClient
}

func (c tvUpgradeAutomaticCapability) AutomaticTVByteSender() ports.DownloadClient { return c.native }

type tvUpgradeMovieCapability struct {
	ports.DownloadClient
	native ports.DownloadClient
}

func (c tvUpgradeMovieCapability) MovieUpgradeByteSender() ports.DownloadClient { return c.native }

func TestTVEpisodeUpgradeNativeFrozenClientAndBudget(t *testing.T) {
	for _, movie := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "movie"}[movie], func(t *testing.T) {
			m, n, config := tvUpgradeNativeFixture(t)
			native := &tvUpgradeNativeClient{}
			m.RegisterClient(config.ClientID, &tvUpgradeNativeClient{})
			m.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
				if kind != config.ClientType || url != config.URL || user != config.Username || password != "frozen-password" {
					t.Fatal("factory did not use frozen credentials")
				}
				if movie {
					return tvUpgradeMovieCapability{native: native}, nil
				}
				return tvUpgradeAutomaticCapability{native: native}, nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client, release, err := n.OpenTVEpisodeUpgradeClient(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			m.SetClientFactory(func(string, string, string, string) (ports.DownloadClient, error) {
				t.Fatal("reloaded changed factory")
				return nil, nil
			})
			cancel()
			if err := client.AddTorrent(ctx, ports.AddTorrentParam{TorrentBytes: []byte("metainfo")}); !errors.Is(err, ports.ErrUnavailable) || native.adds != 1 {
				t.Fatal("Add retried or error lost", err, native.adds)
			}
			if _, err := client.ListTorrents(t.Context(), "tag"); err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetTorrentFiles(t.Context(), "hash"); err != nil {
				t.Fatal(err)
			}
			if native.lists != 1 || native.files != 1 || len(m.slots[config.ClientID]) != 1 {
				t.Fatal("cancel/Add/observation released caller-held budget")
			}
			second, err := m.acquireClientBudget(t.Context(), config.ClientID)
			if err != nil {
				t.Fatal(err)
			}
			defer second()
			if _, err := m.acquireClientBudget(ctx, config.ClientID); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			release()
			release()
			if len(m.slots[config.ClientID]) != 1 {
				t.Fatal("release not idempotent")
			}
		})
	}
}

func TestTVEpisodeUpgradeNativeOpenFailuresReleaseBudget(t *testing.T) {
	for _, name := range []string{"factory-missing", "factory-error", "factory-nil", "password", "ordinary-only", "sender-nil", "manifest-missing", "identity"} {
		t.Run(name, func(t *testing.T) {
			m, n, config := tvUpgradeNativeFixture(t)
			m.SetClientFactory(func(string, string, string, string) (ports.DownloadClient, error) {
				switch name {
				case "factory-error":
					return nil, errors.New("private factory error")
				case "factory-nil":
					return nil, nil
				case "ordinary-only":
					return &tvUpgradeNativeClient{}, nil
				case "sender-nil":
					return tvUpgradeAutomaticCapability{}, nil
				case "manifest-missing":
					return tvUpgradeAutomaticCapability{native: tvUpgradeMovieCapability{}}, nil
				default:
					return tvUpgradeAutomaticCapability{native: &tvUpgradeNativeClient{}}, nil
				}
			})
			if name == "factory-missing" {
				m.SetClientFactory(nil)
			}
			if name == "password" {
				config.PasswordEncrypted = []byte("bad")
			}
			if name == "identity" {
				config.ClientID = uuid.Nil
			}
			client, release, err := n.OpenTVEpisodeUpgradeClient(t.Context(), config)
			if err == nil || client != nil || release != nil || len(m.slots[config.ClientID]) != 0 {
				t.Fatal("failed open retained budget or returned client", err)
			}
		})
	}
}

func TestTVEpisodeUpgradeNativeProductionCapabilities(t *testing.T) {
	for _, kind := range []string{"qbittorrent", "transmission"} {
		t.Run(kind, func(t *testing.T) {
			m, n, config := tvUpgradeNativeFixture(t)
			config.ClientType = kind
			m.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
				if kind == "qbittorrent" {
					return qbittorrent.NewClient(url, user, password, nil)
				}
				return transmission.NewClient(url, user, password, nil)
			})
			_, release, err := n.OpenTVEpisodeUpgradeClient(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

package download

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// This fixture uses the production envelope format and only synthetic secrets.
type m5SourceCodec struct{ key []byte }

func (c m5SourceCodec) EncryptSecret(raw []byte) ([]byte, error) {
	return crypto.Encrypt(c.key, raw)
}
func (c m5SourceCodec) DecryptSecret(raw []byte) ([]byte, error) {
	return crypto.Decrypt(c.key, raw)
}
func (c m5SourceCodec) DigestSecret([]byte) (string, error) { return "unused", nil }

// TestM5SourceConfigurationPG exercises acquisition up to inspected metainfo;
// it does not submit a torrent to a downloader.
func TestM5SourceConfigurationPG(t *testing.T) {
	ctx := t.Context()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal("migrate source fixture failed")
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal("open source fixture failed")
	}
	t.Cleanup(pool.Close)
	var version int
	if err := pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version/10000 != 18 {
		t.Fatal("source fixture requires real PostgreSQL 18")
	}

	const info = "d6:lengthi4e4:name15:Show.S01E01.mkv12:piece lengthi4e6:pieces20:00000000000000000000e"
	raw := []byte("d4:info" + info + "e")
	hash := sha1.Sum([]byte(info))
	wantHash := hex.EncodeToString(hash[:])
	const credential = "contains=;passkey-synthetic"
	const requestURI = "/download/candidate.torrent?apikey=candidate-key-synthetic"
	var fetches atomic.Int32
	var exactRequest atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		exactRequest.Store(r.Method == http.MethodGet && r.RequestURI == requestURI && r.URL.Query().Get("apikey") == "candidate-key-synthetic" && r.Header.Get("Cookie") == "" && r.Header.Get("Authorization") == "")
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(origin.Close)
	fullURL := origin.URL + "/private/torznab/api?passkey=url-secret-synthetic&cat=5000"
	link := origin.URL + requestURI
	codec := m5SourceCodec{key: bytes.Repeat([]byte{43}, 32)}
	keyCipher, err := codec.EncryptSecret([]byte(credential))
	if err != nil {
		t.Fatal("encrypt fixture failed")
	}
	indexerID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO indexer(id,name,url,api_key_encrypted,categories,config_version,health,rss_kind) VALUES($1,'source fixture',$2,$3,'[2000,5000]',7,'online','torznab')`, indexerID, fullURL, keyCipher); err != nil {
		t.Fatal("insert source fixture failed")
	}
	q := db.New(pool)
	before, err := q.GetIndexerByID(ctx, indexerID)
	if err != nil {
		t.Fatal("read original source failed")
	}
	classifications := 0
	classifier := func(endpoint, key string) ports.IndexerProtocol {
		classifications++
		if endpoint != fullURL || key != credential {
			t.Fatal("migration classifier lost original source material")
		}
		return ports.IndexerProtocolTorznab
	}
	if err := postgres.EncryptLegacyIndexerConfigurations(ctx, pool, codec, classifier); err != nil {
		t.Fatal("encrypt legacy source configuration failed")
	}
	stored, err := q.GetIndexerByID(ctx, indexerID)
	if err != nil {
		t.Fatal("read stored source failed")
	}
	if stored.Url != origin.URL || classifications != 1 {
		t.Fatal("migration did not persist origin and classify once")
	}
	// Every base field, including key ciphertext, config version and timestamps,
	// must survive the migration unchanged except the display URL.
	restored := stored
	restored.Url = before.Url
	if !reflect.DeepEqual(restored, before) {
		t.Fatal("migration changed source fields beyond display URL")
	}
	configuration, err := q.GetIndexerConfiguration(ctx, indexerID)
	if err != nil || bytes.Contains(configuration.UrlEncrypted, []byte("url-secret-synthetic")) {
		t.Fatal("encrypted URL configuration missing or contains plaintext")
	}

	m := NewManager(pool, idgen.New(), codec.key)
	m.SetIndexerConfigurationDecoder(codec)
	if m.indexerDecoder == nil {
		t.Fatal("configuration decoder was not installed")
	}
	factoryCalls := 0
	m.SetTorrentSource(func(kind, endpoint, key string) ports.IndexerClient {
		factoryCalls++
		if kind != "torznab" || endpoint != fullURL || key != credential {
			t.Error("source factory did not receive original URL, protocol and credential")
		}
		return torznab.NewClient(endpoint, key, origin.Client())
	}, torrent.Inspector{})
	candidate := db.ReleaseCandidate{IndexerID: indexerID, SourceConfigVersion: before.ConfigVersion, InfoHash: pgtype.Text{String: wantHash, Valid: true}}
	payload, err := m.submissionPayload(ctx, candidate, link)
	if err != nil {
		t.Fatal("native source acquisition failed")
	}
	if factoryCalls != 1 || fetches.Load() != 1 || !exactRequest.Load() {
		t.Fatal("expected one native HTTP fetch with exact candidate URL and key")
	}
	if payload.InfoHash != wantHash || !bytes.Equal(payload.TorrentBytes, raw) || payload.DownloadURL != "" {
		t.Fatal("source payload did not preserve inspected metainfo bytes and infohash")
	}

	assertBlocked := func(t *testing.T) {
		t.Helper()
		calls, requests := factoryCalls, fetches.Load()
		got, err := m.submissionPayload(ctx, candidate, link)
		if !errors.Is(err, ports.ErrUnavailable) || got.InfoHash != "" || len(got.TorrentBytes) != 0 || got.DownloadURL != "" {
			t.Fatal("invalid configuration did not fail closed with empty payload")
		}
		if factoryCalls != calls || fetches.Load() != requests {
			t.Fatal("invalid configuration reached source factory or HTTP")
		}
		printed := fmt.Sprintf("%v %+v", err, err)
		for _, secret := range []string{fullURL, link, origin.URL, credential, "url-secret-synthetic", "candidate-key-synthetic"} {
			if strings.Contains(printed, secret) {
				t.Fatal("configuration error exposed source material")
			}
		}
	}
	t.Run("wrong decoder", func(t *testing.T) {
		m.SetIndexerConfigurationDecoder(m5SourceCodec{key: bytes.Repeat([]byte{99}, 32)})
		assertBlocked(t)
	})
	t.Run("missing decoder", func(t *testing.T) {
		m.SetIndexerConfigurationDecoder(nil)
		assertBlocked(t)
	})
	t.Run("tampered URL ciphertext", func(t *testing.T) {
		m.SetIndexerConfigurationDecoder(codec)
		tampered := bytes.Clone(configuration.UrlEncrypted)
		tampered[len(tampered)-1] ^= 1
		if _, err := pool.Exec(ctx, `UPDATE indexer_configuration SET url_encrypted=$2 WHERE indexer_id=$1`, indexerID, tampered); err == nil {
			t.Fatal("same-version ciphertext mutation was accepted")
		}
		// The old version is immutable. A corrupt new envelope still must fail
		// before creating a source client or sending a request.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal("begin corrupt-envelope fixture failed")
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var nextVersion int64
		if err = tx.QueryRow(ctx, `UPDATE indexer SET config_version=config_version+1 WHERE id=$1 RETURNING config_version`, indexerID).Scan(&nextVersion); err != nil || nextVersion != candidate.SourceConfigVersion+1 {
			t.Fatal("advance corrupt-envelope fixture failed")
		}
		if _, err = tx.Exec(ctx, `UPDATE indexer_configuration SET url_encrypted=$2,configuration_version=$3 WHERE indexer_id=$1`, indexerID, tampered, nextVersion); err != nil {
			t.Fatal("write corrupt-envelope fixture failed")
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal("commit corrupt-envelope fixture failed")
		}
		candidate.SourceConfigVersion = nextVersion
		assertBlocked(t)
	})
	var tasks int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM download_task").Scan(&tasks); err != nil || tasks != 0 {
		t.Fatal("source acquisition unexpectedly created download tasks")
	}
}

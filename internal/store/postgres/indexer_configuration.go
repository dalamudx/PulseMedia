package postgres

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IndexerConfigurationSchemaAvailable probes safely before any extension query.
func IndexerConfigurationSchemaAvailable(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	if pool == nil {
		return false, ports.ErrUnavailable
	}
	return indexerConfigurationSchemaAvailable(ctx, db.New(pool))
}

func indexerConfigurationSchemaAvailable(ctx context.Context, q *db.Queries) (bool, error) {
	if q == nil {
		return false, ports.ErrUnavailable
	}
	value, err := q.IndexerConfigurationSchemaAvailable(ctx)
	available, ok := value.(bool)
	if err != nil || !ok {
		return false, ports.ErrUnavailable
	}
	return available, nil
}

// ResolveIndexerConfiguration returns a copy with only the execution URL replaced.
// The supplied queries retain the caller's transaction and snapshot.
func ResolveIndexerConfiguration(ctx context.Context, q *db.Queries, decoder ports.CredentialDecoder, row db.Indexer) (db.Indexer, ports.IndexerExecutionModes, error) {
	raw, modes, err := ResolveIndexerURL(ctx, q, decoder, row.ID, row.ConfigVersion, row.Url)
	if err != nil {
		return db.Indexer{}, ports.IndexerExecutionModes{}, ports.ErrUnavailable
	}
	if decoder != nil {
		if _, err := decoder.DecryptSecret(row.ApiKeyEncrypted); err != nil {
			return db.Indexer{}, ports.IndexerExecutionModes{}, ports.ErrUnavailable
		}
	}
	row.Url = raw
	return row, modes, nil
}

// ResolveIndexerURL supports raw work queries without widening the historical model.
func ResolveIndexerURL(ctx context.Context, q *db.Queries, decoder ports.CredentialDecoder, id uuid.UUID, version int64, storedURL string) (string, ports.IndexerExecutionModes, error) {
	var zero ports.IndexerExecutionModes
	available, err := indexerConfigurationSchemaAvailable(ctx, q)
	if err != nil {
		return "", zero, ports.ErrUnavailable
	}
	if !available {
		if decoder == nil {
			return storedURL, ports.IndexerExecutionModes{LegacyV1: true}, nil
		}
		return "", zero, ports.ErrUnavailable
	}
	config, err := q.GetIndexerConfiguration(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) && decoder == nil {
		return storedURL, ports.IndexerExecutionModes{LegacyV1: true}, nil
	}
	if decoder == nil {
		return "", zero, ports.ErrUnavailable
	}
	if err != nil || config.ConfigurationVersion < 1 || config.ConfigurationVersion > version || !validIndexerProtocol(ports.IndexerProtocol(config.Protocol)) || (config.BehaviorVersion != "legacy-v1" && config.BehaviorVersion != "explicit-v1") {
		return "", zero, ports.ErrUnavailable
	}
	plaintext, err := decoder.DecryptSecret(config.UrlEncrypted)
	if err != nil {
		return "", zero, ports.ErrUnavailable
	}
	raw := string(plaintext)
	origin, err := indexerConfigurationOrigin(raw)
	if err != nil || origin != storedURL {
		return "", zero, ports.ErrUnavailable
	}
	return raw, ports.IndexerExecutionModes{Protocol: ports.IndexerProtocol(config.Protocol), LegacyV1: config.BehaviorVersion == "legacy-v1"}, nil
}

func validIndexerProtocol(protocol ports.IndexerProtocol) bool {
	return protocol == ports.IndexerProtocolTorznab || protocol == ports.IndexerProtocolNexusPHP || protocol == ports.IndexerProtocolRSS
}

func indexerConfigurationOrigin(raw string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", ports.ErrUnavailable
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" || u.Fragment != "" {
		return "", ports.ErrUnavailable
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}

// EncryptLegacyIndexerConfigurations runs before workers start. Each row commits
// independently, so a restart validates committed rows without re-encryption.
func EncryptLegacyIndexerConfigurations(ctx context.Context, pool *pgxpool.Pool, secrets ports.SearchSecrets, classify ports.LegacyIndexerProtocolClassifier) error {
	if pool == nil || secrets == nil || classify == nil {
		return ports.ErrUnavailable
	}
	available, err := IndexerConfigurationSchemaAvailable(ctx, pool)
	if err != nil || !available {
		return ports.ErrUnavailable
	}
	ids, err := db.New(pool).ListIndexerConfigurationIDs(ctx)
	if err != nil {
		return ports.ErrUnavailable
	}
	for _, id := range ids {
		if err := encryptLegacyIndexerConfiguration(ctx, pool, secrets, classify, id); err != nil {
			return ports.ErrUnavailable
		}
	}
	return nil
}

func encryptLegacyIndexerConfiguration(ctx context.Context, pool *pgxpool.Pool, secrets ports.SearchSecrets, classify ports.LegacyIndexerProtocolClassifier, id uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ports.ErrUnavailable
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	q := db.New(tx)
	row, err := q.LockIndexerConfigurationBase(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return ports.ErrUnavailable
	}
	_, err = q.GetIndexerConfiguration(ctx, id)
	if err == nil {
		if _, _, err = ResolveIndexerConfiguration(ctx, q, secrets, row); err != nil {
			return ports.ErrUnavailable
		}
		if err = tx.Commit(ctx); err != nil {
			return ports.ErrUnavailable
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrUnavailable
	}
	origin, err := indexerConfigurationOrigin(row.Url)
	if err != nil {
		return ports.ErrUnavailable
	}
	credential, err := secrets.DecryptSecret(row.ApiKeyEncrypted)
	if err != nil {
		return ports.ErrUnavailable
	}
	protocol := classify(row.Url, string(credential))
	if !validIndexerProtocol(protocol) {
		return ports.ErrUnavailable
	}
	ciphertext, err := secrets.EncryptSecret([]byte(row.Url))
	if err != nil || len(ciphertext) == 0 {
		return ports.ErrUnavailable
	}
	// Verify the envelope before replacing the only plaintext execution URL.
	plaintext, err := secrets.DecryptSecret(ciphertext)
	if err != nil || string(plaintext) != row.Url {
		return ports.ErrUnavailable
	}
	if err = q.InsertIndexerConfiguration(ctx, db.InsertIndexerConfigurationParams{IndexerID: id, Protocol: string(protocol), UrlEncrypted: ciphertext, BehaviorVersion: "legacy-v1", ConfigurationVersion: row.ConfigVersion}); err != nil {
		return ports.ErrUnavailable
	}
	if err = q.SetIndexerConfigurationOrigin(ctx, db.SetIndexerConfigurationOriginParams{ID: id, Url: origin}); err != nil {
		return ports.ErrUnavailable
	}
	auditID, err := uuid.NewRandom()
	if err != nil {
		return ports.ErrUnavailable
	}
	if err = q.AuditIndexerConfigurationEncrypted(ctx, db.AuditIndexerConfigurationEncryptedParams{ID: auditID, TargetID: id.String()}); err != nil {
		return ports.ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return ports.ErrUnavailable
	}
	return nil
}

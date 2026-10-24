package postgres

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SeedingClientSourceStore struct {
	pool    *pgxpool.Pool
	decoder ports.CredentialDecoder
}

func NewSeedingClientSourceStore(pool *pgxpool.Pool, decoder ports.CredentialDecoder) *SeedingClientSourceStore {
	return &SeedingClientSourceStore{pool: pool, decoder: decoder}
}

var _ ports.SeedingClientSourceReader = (*SeedingClientSourceStore)(nil)

// Read the endpoint and its fingerprint from one database snapshot. This never
// contacts the endpoint; the execution permit independently rechecks authority.
func (s *SeedingClientSourceStore) ReadSeedingClientSource(ctx context.Context, id uuid.UUID, stamp string) (ports.SeedingClientSource, error) {
	var out ports.SeedingClientSource
	if err := ctx.Err(); err != nil {
		return out, err
	}
	raw, err := hex.DecodeString(stamp)
	if id == uuid.Nil || err != nil || len(raw) != 32 || stamp != strings.ToLower(stamp) {
		return out, ports.ErrRejected
	}
	if s == nil || s.pool == nil || s.decoder == nil {
		return out, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, ports.ErrUnavailable
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	client, err := q.GetDownloadClientByID(ctx, id)
	if err != nil {
		return out, seedingConfigError(err)
	}
	current, err := q.DownloadGlobalRatesBinding(ctx, id)
	if err != nil {
		return out, ports.ErrUnavailable
	}
	if current != stamp {
		return out, ports.ErrSeedingConfigConflict
	}
	if client.ClientType != "qbittorrent" && client.ClientType != "transmission" {
		return out, ports.ErrRejected
	}
	credential, err := s.decoder.DecryptSecret(client.PasswordEncrypted)
	if err != nil {
		return out, ports.ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return out, ports.ErrUnavailable
	}
	return ports.SeedingClientSource{ClientID: id, ConfigStamp: stamp, Kind: client.ClientType, URL: client.Url, Username: client.Username, Credential: string(credential)}, nil
}

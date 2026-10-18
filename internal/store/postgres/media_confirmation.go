package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"path/filepath"
	"strings"
)

type MediaConfirmationStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewMediaConfirmationRepository(pool *pgxpool.Pool, ids ports.IDGen) *MediaConfirmationStore {
	return &MediaConfirmationStore{pool, ids}
}
func confirmationSnapshot(ctx context.Context, q *db.Queries, serverID, fileID, unitID uuid.UUID, online bool) (*ports.MediaConfirmationInput, error) {
	file, e := q.GetFileByID(ctx, fileID)
	if e != nil || file.RetiredAt.Valid {
		return nil, ports.ErrMediaFileRetired
	}
	root, e := q.GetLibraryRootByID(ctx, file.RootID)
	if e != nil {
		return nil, e
	}
	if root.Health != "online" || file.RelPath == "" || filepath.IsAbs(file.RelPath) || filepath.Clean(file.RelPath) != file.RelPath || file.RelPath == ".." || strings.HasPrefix(file.RelPath, "../") {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.ResolveImportedRefreshFile(ctx, db.ResolveImportedRefreshFileParams{FileID: fileID, UnitID: unitID}); e != nil {
		return nil, ports.ErrMediaFileRetired
	}
	server, e := q.GetMediaServerByID(ctx, serverID)
	if e != nil {
		return nil, ports.ErrNotFound
	}
	if online && server.Health != "online" {
		return nil, ports.ErrUnavailable
	}
	var mapping map[string]string
	if json.Unmarshal(server.PathMapping, &mapping) != nil {
		return nil, ports.ErrBadResponse
	}
	remote := naming.TranslateRemotePath(mapping, filepath.Join(root.Path, file.RelPath))
	if remote == "" {
		return nil, ports.ErrBadResponse
	}
	material, e := json.Marshal(struct {
		File                       db.LibraryFile
		Server                     db.MediaServer
		Unit                       uuid.UUID
		RootPath, Marker           string
		Generation, HealthRevision int64
	}{file, server, unitID, root.Path, root.MarkerFilename, root.Generation, root.HealthRevision})
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(material)
	return &ports.MediaConfirmationInput{Server: ports.MediaServerConnection{ID: serverID, Kind: server.ServerType, URL: server.Url, EncryptedKey: append([]byte(nil), server.ApiKeyEncrypted...)}, FileID: fileID, RootID: root.ID, UnitID: unitID, LocalPath: filepath.Join(root.Path, file.RelPath), RemotePath: remote, Stamp: hex.EncodeToString(sum[:])}, nil
}

func (s *MediaConfirmationStore) LoadMediaServerConnection(ctx context.Context, id uuid.UUID) (ports.MediaServerConnection, error) {
	row, e := db.New(s.pool).GetMediaServerByID(ctx, id)
	if e != nil {
		return ports.MediaServerConnection{}, ports.ErrNotFound
	}
	return ports.MediaServerConnection{ID: row.ID, Kind: row.ServerType, URL: row.Url, EncryptedKey: append([]byte(nil), row.ApiKeyEncrypted...)}, nil
}
func (s *MediaConfirmationStore) LoadMediaConfirmation(ctx context.Context, server, file, unit uuid.UUID) (*ports.MediaConfirmationInput, error) {
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	input, e := confirmationSnapshot(ctx, db.New(tx), server, file, unit, true)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return input, nil
}
func confirmationFact(f db.UnitFact) *ports.MediaConfirmationFact {
	return &ports.MediaConfirmationFact{ID: f.ID, UnitID: f.UnitID, SourceFileID: uuid.UUID(f.SourceFileID.Bytes), RootID: uuid.UUID(f.RootID.Bytes), Stage: f.Stage}
}
func (s *MediaConfirmationStore) CommitMediaConfirmation(ctx context.Context, input ports.MediaConfirmationInput, itemID string) (*ports.MediaConfirmationFact, error) {
	if len(input.Stamp) != 64 || strings.TrimSpace(itemID) == "" {
		return nil, ports.ErrBadResponse
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if _, e = q.LockMediaConfirmationServer(ctx, input.Server.ID); e != nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = q.LockTransferRoot(ctx, input.RootID); e != nil {
		return nil, e
	}
	unit, e := q.LockTransferUnit(ctx, input.UnitID)
	if e != nil || unit.Retired {
		return nil, ports.ErrMediaFileRetired
	}
	if _, e = q.LockTransferOldFile(ctx, input.FileID); e != nil {
		return nil, ports.ErrMediaFileRetired
	}
	current, e := confirmationSnapshot(ctx, q, input.Server.ID, input.FileID, input.UnitID, true)
	if e != nil {
		return nil, e
	}
	if !sameMediaSnapshot(*current, input) {
		return nil, ports.ErrUnavailable
	}
	params := db.GetCurrentMediaConfirmationParams{UnitID: input.UnitID, FileID: pgtype.UUID{Bytes: input.FileID, Valid: true}, RootID: pgtype.UUID{Bytes: input.RootID, Valid: true}}
	old, e := q.GetCurrentMediaConfirmation(ctx, params)
	if e == nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return confirmationFact(old), nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	fact, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: s.ids.NewUUID(), UnitID: input.UnitID, Stage: "confirmed", SourceFileID: pgtype.UUID{Bytes: input.FileID, Valid: true}, RootID: pgtype.UUID{Bytes: input.RootID, Valid: true}, QualityProfile: []byte("{}")})
	if e != nil {
		return nil, e
	}
	if _, e = q.IncrementRootMutationSeq(ctx, input.RootID); e != nil {
		return nil, e
	}
	payload, e := json.Marshal(map[string]any{"file_id": input.FileID, "unit_id": input.UnitID, "root_id": input.RootID, "server_id": input.Server.ID, "fact_id": fact.ID})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: input.Server.ID.String(), Action: "media.confirmed", TargetType: "library_file", TargetID: input.FileID.String(), Payload: payload}); e != nil {
		return nil, e
	}
	aggregate := input.FileID.String()
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "media_confirmation", AggregateID: aggregate}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "media_confirmation", AggregateID: aggregate})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "media_confirmation", AggregateID: aggregate, EventType: "media.confirmed", Payload: payload, Seq: int64(seq)}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return confirmationFact(fact), nil
}

func sameMediaSnapshot(a, b ports.MediaConfirmationInput) bool {
	return a.Stamp == b.Stamp && a.FileID == b.FileID && a.UnitID == b.UnitID && a.RootID == b.RootID && a.LocalPath == b.LocalPath && a.RemotePath == b.RemotePath && a.Server.ID == b.Server.ID && a.Server.Kind == b.Server.Kind && a.Server.URL == b.Server.URL && bytes.Equal(a.Server.EncryptedKey, b.Server.EncryptedKey)
}

var _ ports.MediaConfirmationRepository = (*MediaConfirmationStore)(nil)

package postgres

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/endpoint"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EndpointConfigurationStore owns only configuration transactions, never native I/O.
type EndpointConfigurationStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	secrets ports.SecretProtector
}

func NewEndpointConfigurationStore(pool *pgxpool.Pool, ids ports.IDGen, secrets ports.SecretProtector) *EndpointConfigurationStore {
	return &EndpointConfigurationStore{pool: pool, ids: ids, secrets: secrets}
}

var _ ports.EndpointConfigurationRepository = (*EndpointConfigurationStore)(nil)

func endpointConfigurationError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrInvalidEndpointConfiguration, ports.ErrNotFound, ports.ErrEndpointVersionConflict, ports.ErrEndpointInUse} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23503" || pg.Code == "23001") {
		return ports.ErrEndpointInUse
	}
	return ports.ErrUnavailable
}

func endpointConfigurationActor(a *ports.Actor, write bool) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) {
		return ports.ErrForbidden
	}
	if id, err := uuid.Parse(a.ID); err != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	if write && (a.Role != ports.RoleAdmin || a.Scopes != nil) {
		return ports.ErrForbidden
	}
	if !write && a.Scopes != nil {
		read := false
		for _, scope := range a.Scopes {
			read = read || scope == "read"
		}
		if !read {
			return ports.ErrForbidden
		}
	}
	return nil
}

func (s *EndpointConfigurationStore) begin(ctx context.Context, a *ports.Actor, f ports.EndpointFamily, write bool) (pgx.Tx, error) {
	if err := endpointConfigurationActor(a, write); err != nil {
		return nil, err
	}
	if !endpointConfigurationFamily(f) {
		return nil, ports.ErrInvalidEndpointConfiguration
	}
	if s == nil || s.pool == nil || (write && (s.ids == nil || s.secrets == nil)) {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	user, err := db.New(tx).LockSettingsUser(ctx, uuid.MustParse(a.ID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!user.IsActive || user.Role != a.Role)) {
		err = ports.ErrForbidden
	}
	if err != nil {
		endpointConfigurationRollback(ctx, tx)
		return nil, err
	}
	return tx, nil
}

func endpointConfigurationRollback(ctx context.Context, tx pgx.Tx) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(c)
}

// The private snapshot is reconstructed from the locked row on each probe phase.
// It is never returned, logged or used as an audit/event payload.
type endpointConfigurationSnapshot struct {
	Family    ports.EndpointFamily
	ID        uuid.UUID
	Name      string
	Protocol  string
	URL       string
	Username  string
	Cipher    []byte
	Mapping   json.RawMessage
	UpdatedAt time.Time
	health    string
}

func downloadConfiguration(row db.DownloadClient) endpointConfigurationSnapshot {
	return endpointConfigurationSnapshot{Family: ports.DownloadClientEndpoint, ID: row.ID, Name: row.Name, Protocol: row.ClientType, URL: row.Url, Username: row.Username, Cipher: row.PasswordEncrypted, Mapping: row.RemotePathMap, UpdatedAt: row.UpdatedAt.Time}
}
func mediaConfiguration(row db.MediaServer) endpointConfigurationSnapshot {
	return endpointConfigurationSnapshot{Family: ports.MediaServerEndpoint, ID: row.ID, Name: row.Name, Protocol: row.ServerType, URL: row.Url, Cipher: row.ApiKeyEncrypted, Mapping: row.PathMapping, UpdatedAt: row.UpdatedAt.Time, health: row.Health}
}
func (r endpointConfigurationSnapshot) safe() ports.EndpointConfiguration {
	return ports.EndpointConfiguration{ID: r.ID, Family: r.Family, Name: r.Name, Protocol: r.Protocol, URL: endpoint.DisplayURL(r.URL), Username: r.Username, Health: r.health, UpdatedAt: r.UpdatedAt}
}
func lockEndpointConfiguration(ctx context.Context, q *db.Queries, f ports.EndpointFamily, id uuid.UUID) (endpointConfigurationSnapshot, error) {
	if id == uuid.Nil {
		return endpointConfigurationSnapshot{}, ports.ErrNotFound
	}
	if f == ports.DownloadClientEndpoint {
		row, err := q.LockDownloadClientForEdit(ctx, id)
		return downloadConfiguration(row), err
	}
	row, err := q.LockMediaServerForEdit(ctx, id)
	return mediaConfiguration(row), err
}

func (s *EndpointConfigurationStore) ListEndpointConfigurations(ctx context.Context, a *ports.Actor, f ports.EndpointFamily) (out []ports.EndpointConfiguration, err error) {
	defer func() { err = endpointConfigurationError(ctx, err) }()
	tx, err := s.begin(ctx, a, f, false)
	if err != nil {
		return nil, err
	}
	defer endpointConfigurationRollback(ctx, tx)
	q := db.New(tx)
	out = make([]ports.EndpointConfiguration, 0)
	if f == ports.DownloadClientEndpoint {
		rows, e := q.ListDownloadClients(ctx)
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			out = append(out, downloadConfiguration(row).safe())
		}
	} else {
		rows, e := q.ListMediaServers(ctx)
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			out = append(out, mediaConfiguration(row).safe())
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *EndpointConfigurationStore) CreateEndpointConfiguration(ctx context.Context, a *ports.Actor, f ports.EndpointFamily, in ports.EndpointConfigurationInput) (out *ports.EndpointConfiguration, err error) {
	defer func() { err = endpointConfigurationError(ctx, err) }()
	tx, err := s.begin(ctx, a, f, true)
	if err != nil {
		return nil, err
	}
	defer endpointConfigurationRollback(ctx, tx)
	if err = validateEndpointConfiguration(f, in); err != nil {
		return nil, err
	}
	if !endpointConfigurationURL(in.URL) {
		return nil, ports.ErrInvalidEndpointConfiguration
	}
	cipher, err := s.secrets.EncryptSecret([]byte(in.Secret))
	if err != nil || len(cipher) == 0 {
		return nil, ports.ErrUnavailable
	}
	mapping := in.Mapping
	if len(mapping) == 0 {
		mapping = json.RawMessage(`{}`)
	}
	q := db.New(tx)
	id := s.ids.NewUUID()
	var current endpointConfigurationSnapshot
	if f == ports.DownloadClientEndpoint {
		row, e := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: id, Name: in.Name, ClientType: in.Protocol, Url: in.URL, Username: in.Username, PasswordEncrypted: cipher, RemotePathMap: mapping})
		if e != nil {
			return nil, e
		}
		current = downloadConfiguration(row)
	} else {
		row, e := q.CreateMediaServer(ctx, db.CreateMediaServerParams{ID: id, Name: in.Name, ServerType: in.Protocol, Url: in.URL, ApiKeyEncrypted: cipher, PathMapping: mapping})
		if e != nil {
			return nil, e
		}
		current = mediaConfiguration(row)
	}
	if err = s.record(ctx, q, a, current, "created", nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	safe := current.safe()
	return &safe, nil
}

func (s *EndpointConfigurationStore) UpdateEndpointConfiguration(ctx context.Context, a *ports.Actor, f ports.EndpointFamily, id uuid.UUID, in ports.EndpointConfigurationInput) (out *ports.EndpointConfiguration, err error) {
	defer func() { err = endpointConfigurationError(ctx, err) }()
	tx, err := s.begin(ctx, a, f, true)
	if err != nil {
		return nil, err
	}
	defer endpointConfigurationRollback(ctx, tx)
	q := db.New(tx)
	current, err := lockEndpointConfiguration(ctx, q, f, id)
	if err != nil {
		return nil, err
	}
	if in.ExpectedUpdatedAt.IsZero() || !in.ExpectedUpdatedAt.Equal(current.UpdatedAt) {
		return nil, ports.ErrEndpointVersionConflict
	}
	if err = validateEndpointConfiguration(f, in); err != nil {
		return nil, err
	}
	raw := in.URL
	if display := endpoint.DisplayURL(current.URL); raw == display && display != current.URL {
		raw = current.URL
	} else if !endpointConfigurationURL(raw) {
		return nil, ports.ErrInvalidEndpointConfiguration
	}
	var cipher []byte
	if in.Secret != "" {
		cipher, err = s.secrets.EncryptSecret([]byte(in.Secret))
		if err != nil || len(cipher) == 0 {
			return nil, ports.ErrUnavailable
		}
	}
	if f == ports.DownloadClientEndpoint {
		row, e := q.EditDownloadClient(ctx, db.EditDownloadClientParams{ID: id, Name: in.Name, ClientType: in.Protocol, Url: raw, Username: in.Username, PasswordEncrypted: cipher})
		if e != nil {
			return nil, e
		}
		current = downloadConfiguration(row)
	} else {
		row, e := q.EditMediaServer(ctx, db.EditMediaServerParams{ID: id, Name: in.Name, ServerType: in.Protocol, Url: raw, ApiKeyEncrypted: cipher})
		if e != nil {
			return nil, e
		}
		current = mediaConfiguration(row)
	}
	if err = s.record(ctx, q, a, current, "updated", nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	safe := current.safe()
	return &safe, nil
}

func (s *EndpointConfigurationStore) DeleteEndpointConfiguration(ctx context.Context, a *ports.Actor, f ports.EndpointFamily, id uuid.UUID) (err error) {
	defer func() { err = endpointConfigurationError(ctx, err) }()
	tx, err := s.begin(ctx, a, f, true)
	if err != nil {
		return err
	}
	defer endpointConfigurationRollback(ctx, tx)
	q := db.New(tx)
	current, err := lockEndpointConfiguration(ctx, q, f, id)
	if err != nil {
		return err
	}
	var count int64
	if f == ports.DownloadClientEndpoint {
		// Keep historical FK references; also fence queued/running jobs before a
		// task/authority exists, including nested manual/automatic/upgrade payloads.
		var referenced bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE state IN ('queued','running') AND jsonb_path_exists(payload, '$.**.client_id ? (@ == $id)', jsonb_build_object('id',$1::text)))`, id.String()).Scan(&referenced)
		if err != nil {
			return err
		}
		if referenced {
			return ports.ErrEndpointInUse
		}
		count, err = q.DeleteDownloadClientIfUnused(ctx, id)
	} else {
		tag, e := tx.Exec(ctx, `DELETE FROM media_server WHERE id=$1`, id)
		err = e
		count = tag.RowsAffected()
	}
	if err != nil {
		return err
	}
	if count != 1 {
		return ports.ErrEndpointInUse
	}
	if err = s.record(ctx, q, a, current, "deleted", nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *EndpointConfigurationStore) scope(current endpointConfigurationSnapshot) (*ports.EndpointProbeScope, error) {
	material, err := json.Marshal(current)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	stamp, err := s.secrets.DigestSecret(material)
	if err != nil || stamp == "" {
		return nil, ports.ErrUnavailable
	}
	return &ports.EndpointProbeScope{Family: current.Family, ID: current.ID, UpdatedAt: current.UpdatedAt, ConfigStamp: stamp}, nil
}

func (s *EndpointConfigurationStore) AuthorizeEndpointProbe(ctx context.Context, a *ports.Actor, f ports.EndpointFamily, id uuid.UUID) (out *ports.EndpointProbeScope, err error) {
	defer func() { err = endpointConfigurationError(ctx, err) }()
	tx, err := s.begin(ctx, a, f, true)
	if err != nil {
		return nil, err
	}
	defer endpointConfigurationRollback(ctx, tx)
	current, err := lockEndpointConfiguration(ctx, db.New(tx), f, id)
	if err != nil {
		return nil, err
	}
	out, err = s.scope(current)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *EndpointConfigurationStore) CompleteEndpointProbe(ctx context.Context, a *ports.Actor, scope ports.EndpointProbeScope, alive bool) (err error) {
	defer func() { err = endpointConfigurationError(ctx, err) }()
	tx, err := s.begin(ctx, a, scope.Family, true)
	if err != nil {
		return err
	}
	defer endpointConfigurationRollback(ctx, tx)
	q := db.New(tx)
	current, err := lockEndpointConfiguration(ctx, q, scope.Family, scope.ID)
	if err != nil {
		return err
	}
	actual, err := s.scope(current)
	if err != nil {
		return err
	}
	if scope.UpdatedAt.IsZero() || !scope.UpdatedAt.Equal(actual.UpdatedAt) || subtle.ConstantTimeCompare([]byte(scope.ConfigStamp), []byte(actual.ConfigStamp)) != 1 {
		return ports.ErrEndpointVersionConflict
	}
	if err = s.record(ctx, q, a, current, "probed", &alive); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *EndpointConfigurationStore) record(ctx context.Context, q *db.Queries, a *ports.Actor, current endpointConfigurationSnapshot, action string, alive *bool) error {
	payload, err := json.Marshal(struct {
		ID      uuid.UUID            `json:"id"`
		Family  ports.EndpointFamily `json:"family"`
		Version time.Time            `json:"version"`
		Alive   *bool                `json:"alive,omitempty"`
	}{current.ID, current.Family, current.UpdatedAt, alive})
	if err != nil {
		return err
	}
	aggregate := string(current.Family)
	event := aggregate + "." + action
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: event, TargetType: aggregate, TargetID: current.ID.String(), Payload: payload}); err != nil {
		return err
	}
	// Row locks serialize existing endpoints; the aggregate lock also covers create.
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: aggregate, AggregateID: current.ID.String()}); err != nil {
		return err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: aggregate, AggregateID: current.ID.String()})
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: aggregate, AggregateID: current.ID.String(), EventType: event, Seq: int64(seq), Payload: payload})
	return err
}

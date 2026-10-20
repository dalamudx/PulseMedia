package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DoubanWishSyncStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

var _ ports.DoubanWishSyncRepository = (*DoubanWishSyncStore)(nil)

func NewDoubanWishSyncStore(pool *pgxpool.Pool, ids ports.IDGen) *DoubanWishSyncStore {
	return &DoubanWishSyncStore{pool, ids}
}
func doubanWishDigest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func doubanWishLockKey(id uuid.UUID) int64 {
	h := sha256.Sum256([]byte("douban-wish:" + id.String()))
	return int64(binary.BigEndian.Uint64(h[:8]) & 0x7fffffffffffffff)
}

type doubanWishPayload struct {
	Version  int       `json:"version"`
	RunID    uuid.UUID `json:"run_id"`
	PageType string    `json:"page_type"`
	Start    int       `json:"start"`
}

func doubanWishPayloadJSON(run uuid.UUID, kind string, start int) []byte {
	b, _ := json.Marshal(doubanWishPayload{1, run, kind, start})
	return b
}
func (s *DoubanWishSyncStore) nextPage(ctx context.Context, tx pgx.Tx, run uuid.UUID, kind string, start int) error {
	id := s.ids.NewUUID()
	q := db.New(tx)
	if e := q.CreateDoubanWishJob(ctx, db.CreateDoubanWishJobParams{ID: id, Kind: ports.DoubanWishSyncJobKind, Payload: doubanWishPayloadJSON(run, kind, start)}); e != nil {
		return e
	}
	return q.InsertDoubanWishPage(ctx, db.InsertDoubanWishPageParams{JobID: id, RunID: run, MediaType: kind, PageStart: int32(start)})
}
func (s *DoubanWishSyncStore) EnqueueDue(ctx context.Context) error {
	candidates, e := db.New(s.pool).ListDueDoubanWishConfigs(ctx)
	if e != nil {
		return ports.ErrUnavailable
	}
	for _, c := range candidates {
		if e = s.enqueue(ctx, c.ID, c.OwnerID); e != nil {
			return doubanSyncError(e)
		}
	}
	return nil
}
func (s *DoubanWishSyncStore) enqueue(ctx context.Context, id, owner uuid.UUID) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer wishRollback(tx)
	q := db.New(tx)
	isolated, e := q.GuardDoubanSyncIsolation(ctx)
	if e != nil || isolated {
		return ports.ErrUnavailable
	}
	u, e := q.LockDoubanSyncOwner(ctx, owner)
	if e != nil {
		return e
	}
	if !u.IsActive || (u.Role != "admin" && u.Role != "member") {
		return nil
	}
	c, e := q.GetDoubanSyncConfig(ctx, owner)
	if e != nil {
		return e
	}
	if !c.Enabled || c.ID != id {
		return nil
	}
	due, e := q.DoubanWishConfigDue(ctx, db.DoubanWishConfigDueParams{ConfigID: id, ConfigVersion: c.Version})
	if e != nil || !due {
		return e
	}
	cfg := doubanSyncConfig(c)
	if e = wishRoots(ctx, tx, cfg); e != nil {
		return nil
	}
	run := s.ids.NewUUID()
	if e = q.InsertDoubanWishRun(ctx, db.InsertDoubanWishRunParams{ID: run, ConfigID: id, ConfigVersion: c.Version, OwnerID: owner}); e != nil {
		return e
	}
	kind := "movie"
	if cfg.MovieRootID == nil {
		kind = "tv"
	}
	if e = s.nextPage(ctx, tx, run, kind, 0); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func wishRollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func wishRoots(ctx context.Context, tx pgx.Tx, c ports.DoubanSyncConfig) error {
	ids := []uuid.UUID{}
	if c.MovieRootID != nil {
		ids = append(ids, *c.MovieRootID)
	}
	if c.TVRootID != nil {
		ids = append(ids, *c.TVRootID)
	}
	rows, e := db.New(tx).LockDoubanSyncRoots(ctx, ids)
	if e != nil {
		return e
	}
	if len(rows) != len(ids) {
		return ports.ErrUnavailable
	}
	for _, r := range rows {
		if r.Health != "online" || (c.MovieRootID != nil && r.ID == *c.MovieRootID && r.MediaType != "movie") || (c.TVRootID != nil && r.ID == *c.TVRootID && r.MediaType != "tv") {
			return ports.ErrUnavailable
		}
	}
	return nil
}

type doubanWishPermit struct {
	store  *DoubanWishSyncStore
	conn   *pgxpool.Conn
	lease  ports.JobLease
	work   ports.DoubanWishSyncWork
	mu     sync.Mutex
	closed bool
}

func (p *doubanWishPermit) Snapshot() ports.DoubanWishSyncWork {
	p.mu.Lock()
	defer p.mu.Unlock()
	w := p.work
	w.Config.MovieRootID = doubanSyncCopyID(w.Config.MovieRootID)
	w.Config.TVRootID = doubanSyncCopyID(w.Config.TVRootID)
	return w
}
func (p *doubanWishPermit) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, e := db.New(p.conn).UnlockDoubanWishPermit(ctx, doubanWishLockKey(p.work.Config.ID))
	if e != nil || !ok {
		raw := p.conn.Hijack()
		_ = raw.Close(ctx)
	} else {
		p.conn.Release()
	}
}
func (s *DoubanWishSyncStore) Acquire(ctx context.Context, lease ports.JobLease) (ports.DoubanWishSyncPermit, error) {
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrUnavailable
	}
	config, e := db.New(s.pool).GetDoubanWishConfigID(ctx, lease.ID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	held, e := db.New(conn).TryDoubanWishPermit(ctx, doubanWishLockKey(config))
	if e != nil || !held {
		conn.Release()
		return nil, ports.ErrUnavailable
	}
	p := &doubanWishPermit{store: s, conn: conn, lease: lease}
	p.work.Config.ID = config
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		p.Close()
		return nil, ports.ErrUnavailable
	}
	defer wishRollback(tx)
	if e = p.authorize(ctx, tx, true); e == nil {
		p.work.ExecutionID = s.ids.NewUUID()
		var n int64
		n, e = db.New(tx).RegisterDoubanWishExecution(ctx, db.RegisterDoubanWishExecutionParams{JobID: lease.ID, ExecutionID: pgtype.UUID{Bytes: p.work.ExecutionID, Valid: true}, ExecutionPid: pgtype.Int4{Int32: int32(conn.Conn().PgConn().PID()), Valid: true}, ExecutionOwner: pgtype.Text{String: lease.Owner, Valid: true}, ExecutionToken: pgtype.Int8{Int64: lease.Token, Valid: true}})
		if e == nil && n != 1 {
			e = ports.ErrUnavailable
		}
	}
	if e == nil {
		e = db.New(tx).StartDoubanWishRun(ctx, p.work.RunID)
	}
	if e == nil {
		e = p.tail(ctx, tx, false)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		p.Close()
		return nil, ports.ErrUnavailable
	}
	return p, nil
}

// Every operation takes the original job first, then the account/configuration,
// ledger and sorted roots. The physical permit is never released by recovery.
func (p *doubanWishPermit) authorize(ctx context.Context, tx pgx.Tx, initial bool) error {
	if p.closed {
		return ports.ErrUnavailable
	}
	q := db.New(tx)
	isolated, e := q.GuardDoubanSyncIsolation(ctx)
	if e != nil || isolated {
		return ports.ErrUnavailable
	}
	j, e := q.LockDoubanWishJob(ctx, p.lease.ID)
	if e != nil || j.Kind != ports.DoubanWishSyncJobKind || !j.LeaseOwner.Valid || j.LeaseOwner.String != p.lease.Owner || j.LeaseToken != p.lease.Token || j.State != "running" || !j.LeaseUntil.Valid {
		return ports.ErrUnavailable
	}
	b, e := q.GetDoubanWishBinding(ctx, p.lease.ID)
	if e != nil {
		return ports.ErrUnavailable
	}
	var frozen doubanWishPayload
	d := json.NewDecoder(bytes.NewReader(j.Payload))
	d.DisallowUnknownFields()
	if d.Decode(&frozen) != nil || frozen.Version != 1 || frozen.RunID != b.ID || frozen.PageType != b.MediaType || frozen.Start != int(b.PageStart) {
		return ports.ErrUnavailable
	}
	if !initial && (p.work.RunID != b.ID || p.work.Config.ID != b.ConfigID || p.work.Config.OwnerID != b.OwnerID || p.work.Config.Version != b.ConfigVersion || p.work.MediaType != b.MediaType || p.work.Start != int(b.PageStart)) {
		return ports.ErrUnavailable
	}
	u, e := q.LockDoubanWishAccount(ctx, b.OwnerID)
	if e != nil || !u.IsActive || (u.Role != "admin" && u.Role != "member") {
		return ports.ErrForbidden
	}
	c, e := q.GetDoubanSyncConfig(ctx, b.OwnerID)
	if e != nil || c.ID != b.ConfigID || c.Version != b.ConfigVersion || !c.Enabled {
		return ports.ErrUnavailable
	}
	cfg := doubanSyncConfig(c)
	if !initial && (p.work.Actor.Role != u.Role || doubanWishDigest(p.work.Config) != doubanWishDigest(cfg)) {
		return ports.ErrUnavailable
	}
	status, e := q.LockDoubanWishRun(ctx, b.ID)
	allowedStatus := status == "running" || initial && status == "queued"
	if e != nil || !allowedStatus {
		return ports.ErrUnavailable
	}
	page, e := q.LockDoubanWishPage(ctx, p.lease.ID)
	if e != nil || initial && page.ExecutionID.Valid || !initial && (!page.ExecutionID.Valid || uuid.UUID(page.ExecutionID.Bytes) != p.work.ExecutionID || !page.ExecutionPid.Valid || page.ExecutionPid.Int32 != int32(p.conn.Conn().PgConn().PID()) || !page.ExecutionOwner.Valid || page.ExecutionOwner.String != p.lease.Owner || !page.ExecutionToken.Valid || page.ExecutionToken.Int64 != p.lease.Token) || page.Finished {
		return ports.ErrUnavailable
	}
	if e = wishRoots(ctx, tx, cfg); e != nil {
		return e
	}
	if b.MediaType == "movie" && cfg.MovieRootID == nil || b.MediaType == "tv" && cfg.TVRootID == nil {
		return ports.ErrUnavailable
	}
	if initial {
		p.work = ports.DoubanWishSyncWork{RunID: b.ID, Config: cfg, Actor: ports.Actor{Type: ports.ActorTypeUser, ID: b.OwnerID.String(), Name: u.Username, Role: u.Role}, MediaType: b.MediaType, Start: int(b.PageStart)}
	}
	return p.tail(ctx, tx, false)
}
func (p *doubanWishPermit) tail(ctx context.Context, tx pgx.Tx, completed bool) error {
	q := db.New(tx)
	valid, e := q.DoubanWishLeaseTail(ctx, db.DoubanWishLeaseTailParams{Kind: ports.DoubanWishSyncJobKind, Owner: p.lease.Owner, Token: p.lease.Token, Completed: completed, ID: p.lease.ID})
	if e != nil || !valid.Valid || !valid.Bool || p.conn.Conn().IsClosed() {
		return ports.ErrUnavailable
	}
	held, e := q.DoubanWishPhysicalLockHeld(ctx, db.DoubanWishPhysicalLockHeldParams{LockKey: doubanWishLockKey(p.work.Config.ID), ExecutionPid: int32(p.conn.Conn().PgConn().PID())})
	if e != nil || !held {
		return ports.ErrUnavailable
	}
	return nil
}
func (p *doubanWishPermit) transact(ctx context.Context, fn func(pgx.Tx) error, completed bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ports.ErrUnavailable
	}
	tx, e := p.store.pool.Begin(ctx)
	if e != nil {
		return ports.ErrUnavailable
	}
	defer wishRollback(tx)
	if e = p.authorize(ctx, tx, false); e != nil {
		return doubanSyncError(e)
	}
	if e = fn(tx); e != nil {
		return doubanSyncError(e)
	}
	if e = p.tail(ctx, tx, completed); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return ports.ErrUnavailable
	}
	return nil
}
func (p *doubanWishPermit) Check(ctx context.Context) error {
	return p.transact(ctx, func(pgx.Tx) error { return nil }, false)
}
func wishNumeric(s string) bool {
	if len(s) < 1 || s[0] == '0' {
		return false
	}
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == s
}
func wishValidPage(w ports.DoubanWishSyncWork, v ports.DoubanWishPage) bool {
	if v.RequestedID != w.Config.DoubanUID || !doubanSyncASCII(v.CanonicalID, 64, true) || v.MediaType != w.MediaType || v.Start != w.Start || v.Start < 0 || v.Start >= 15000 || v.Start%15 != 0 || v.Total < 0 || v.Total > 1000000 || v.WindowEnd < v.Start || v.WindowEnd > 15000 || v.WindowEnd > v.Total || v.WindowEnd-v.Start > 15 || len(v.Items) > 15 {
		return false
	}
	if v.ProfileNumber != "" && !wishNumeric(v.ProfileNumber) {
		return false
	}
	if wishNumeric(v.RequestedID) {
		if v.CanonicalID != v.RequestedID && v.ProfileNumber != v.RequestedID {
			return false
		}
	} else if v.CanonicalID != v.RequestedID {
		return false
	}
	end := min(v.Start+15, v.Total, 15000)
	if v.WindowEnd != end || v.WindowComplete != (len(v.Items) == v.WindowEnd-v.Start) {
		return false
	}
	if v.NextStart != nil {
		if *v.NextStart != v.WindowEnd || *v.NextStart <= v.Start || *v.NextStart >= v.Total || *v.NextStart >= 15000 {
			return false
		}
	} else if v.WindowEnd < v.Total && v.WindowEnd < 15000 {
		return false
	}
	seen := map[string]bool{}
	for _, i := range v.Items {
		if !wishNumeric(i.DoubanID) || i.MediaType != v.MediaType || i.Year != 0 && (i.Year < 1800 || i.Year > 9999) || len(i.Titles) < 1 || len(i.Titles) > 8 || seen[i.DoubanID] {
			return false
		}
		seen[i.DoubanID] = true
		for _, title := range i.Titles {
			if !utf8.ValidString(title) || strings.TrimSpace(title) == "" || len(title) > 2048 || strings.IndexFunc(title, unicode.IsControl) >= 0 {
				return false
			}
		}
	}
	return true
}
func wishCopyPage(v ports.DoubanWishPage) ports.DoubanWishPage {
	b, _ := json.Marshal(v)
	var out ports.DoubanWishPage
	_ = json.Unmarshal(b, &out)
	return out
}
func (p *doubanWishPermit) StorePage(ctx context.Context, v ports.DoubanWishPage) ([]ports.DoubanWishSyncItem, error) {
	v = wishCopyPage(v)
	var out []ports.DoubanWishSyncItem
	e := p.transact(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		if !wishValidPage(p.work, v) {
			return ports.ErrBadResponse
		}
		binding, e := q.GetDoubanWishCanonical(ctx, p.work.RunID)
		if e != nil {
			return e
		}
		if binding.CanonicalID.Valid && binding.CanonicalID.String != v.CanonicalID || binding.ProfileNumber.Valid && binding.ProfileNumber.String != v.ProfileNumber {
			return ports.ErrBadResponse
		}
		old, e := q.GetDoubanWishPage(ctx, p.lease.ID)
		if e != nil {
			return e
		}
		if old != nil {
			var saved ports.DoubanWishPage
			if json.Unmarshal(old, &saved) != nil || doubanWishDigest(saved) != doubanWishDigest(v) {
				return ports.ErrBadResponse
			}
		} else {
			raw, _ := json.Marshal(v)
			if e = q.SaveDoubanWishPage(ctx, db.SaveDoubanWishPageParams{JobID: p.lease.ID, Page: raw}); e != nil {
				return e
			}
			incomplete := !v.WindowComplete || v.WindowEnd < v.Total && v.NextStart == nil
			changed, e := q.DoubanWishTotalChanged(ctx, db.DoubanWishTotalChangedParams{RunID: p.work.RunID, MediaType: v.MediaType, Total: int32(v.Total)})
			if e != nil {
				return e
			}
			if e = q.BindDoubanWishCanonical(ctx, db.BindDoubanWishCanonicalParams{ID: p.work.RunID, CanonicalID: v.CanonicalID, ProfileNumber: v.ProfileNumber, Incomplete: incomplete || changed}); e != nil {
				return e
			}
			root := p.work.Config.MovieRootID
			if v.MediaType == "tv" {
				root = p.work.Config.TVRootID
			}
			for _, item := range v.Items {
				raw, _ := json.Marshal(item)
				key := "douban-wish:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(p.work.Config.ID.String()+":"+fmt.Sprint(p.work.Config.Version)+":"+v.MediaType+":"+root.String()+":"+item.DoubanID)).String()
				n, e := q.InsertDoubanWishItem(ctx, db.InsertDoubanWishItemParams{ID: p.store.ids.NewUUID(), RunID: p.work.RunID, PageJobID: p.lease.ID, DoubanID: item.DoubanID, MediaType: item.MediaType, Source: raw, SourceHash: doubanWishDigest(item), CommandKey: key})
				if e != nil {
					return e
				}
				if n == 0 {
					if e = q.MarkDoubanWishIncomplete(ctx, p.work.RunID); e != nil {
						return e
					}
				}
			}
		}
		rows, e := q.ListDoubanWishPageItems(ctx, p.lease.ID)
		if e != nil {
			return e
		}
		for _, r := range rows {
			i := ports.DoubanWishSyncItem{ID: r.ID, State: r.State, CommandKey: r.CommandKey}
			if e = json.Unmarshal(r.Source, &i.Observation); e != nil {
				return e
			}
			out = append(out, i)
		}
		return nil
	}, false)
	return out, e
}
func (p *doubanWishPermit) StoreIdentity(ctx context.Context, id uuid.UUID, m ports.DoubanWishMatch) error {
	return p.transact(ctx, func(tx pgx.Tx) error {
		if m.State != "matched" && m.State != "unresolved" && m.State != "incomplete" && m.State != "ambiguous" || m.MediaType != p.work.MediaType || m.State == "matched" && m.TMDBID <= 0 || m.State != "matched" && m.TMDBID != 0 {
			return ports.ErrRejected
		}
		q := db.New(tx)
		r, e := q.LockDoubanWishItem(ctx, db.LockDoubanWishItemParams{ID: id, PageJobID: p.lease.ID})
		if e != nil {
			return ports.ErrUnavailable
		}
		var source ports.DoubanWishItem
		if json.Unmarshal(r.Source, &source) != nil || source.MediaType != m.MediaType || m.State == "matched" && source.Year == 0 {
			return ports.ErrRejected
		}
		if r.Match != nil {
			var saved ports.DoubanWishMatch
			if json.Unmarshal(r.Match, &saved) == nil && doubanWishDigest(saved) == doubanWishDigest(m) {
				return nil
			}
			return ports.ErrSubscriptionBindingsConflict
		}
		if r.State != "pending" {
			return ports.ErrSubscriptionBindingsConflict
		}
		raw, _ := json.Marshal(m)
		return q.SaveDoubanWishMatch(ctx, db.SaveDoubanWishMatchParams{ID: id, Match: raw, MatchHash: pgtype.Text{String: doubanWishDigest(m), Valid: true}, State: m.State})
	}, false)
}
func (p *doubanWishPermit) FinishItem(ctx context.Context, id uuid.UUID, state string) error {
	if state != "unresolved" && state != "incomplete" && state != "ambiguous" && state != "conflict" && state != "unavailable" {
		return ports.ErrRejected
	}
	return p.transact(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		old, e := q.GetDoubanWishItemState(ctx, db.GetDoubanWishItemStateParams{ID: id, PageJobID: p.lease.ID})
		if e != nil {
			return ports.ErrUnavailable
		}
		if old == state {
			return nil
		}
		if old != "pending" && old != "matched" {
			return ports.ErrSubscriptionBindingsConflict
		}
		return q.FinishDoubanWishItem(ctx, db.FinishDoubanWishItemParams{ID: id, State: state})
	}, false)
}
func (p *doubanWishPermit) FinishPage(ctx context.Context) error {
	return p.transact(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		raw, e := q.GetDoubanWishPage(ctx, p.lease.ID)
		if e != nil || raw == nil {
			return ports.ErrUnavailable
		}
		var v ports.DoubanWishPage
		if json.Unmarshal(raw, &v) != nil || !wishValidPage(p.work, v) {
			return ports.ErrUnavailable
		}
		pending, e := q.DoubanWishPagePending(ctx, p.lease.ID)
		if e != nil || pending {
			return ports.ErrUnavailable
		}
		if e = q.FinishDoubanWishPage(ctx, p.lease.ID); e != nil {
			return e
		}
		if v.NextStart != nil {
			if e = p.store.nextPage(ctx, tx, p.work.RunID, v.MediaType, *v.NextStart); e != nil {
				return e
			}
		} else if v.MediaType == "movie" && p.work.Config.TVRootID != nil {
			if e = p.store.nextPage(ctx, tx, p.work.RunID, "tv", 0); e != nil {
				return e
			}
		} else {
			if e = q.FinishDoubanWishRun(ctx, p.work.RunID); e != nil {
				return e
			}
		}
		return q.CompleteDoubanWishJob(ctx, p.lease.ID)
	}, true)
}
func (s *DoubanWishSyncStore) RecoverExpired(ctx context.Context) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return ports.ErrUnavailable
	}
	defer wishRollback(tx)
	q := db.New(tx)
	isolated, e := q.GuardDoubanSyncIsolation(ctx)
	if e != nil || isolated {
		return ports.ErrUnavailable
	}
	ids, e := q.LockExpiredDoubanWishJobs(ctx, ports.DoubanWishSyncJobKind)
	if e != nil {
		return ports.ErrUnavailable
	}
	for _, id := range ids {
		if e = q.DeadDoubanWishJob(ctx, id); e != nil {
			return ports.ErrUnavailable
		}
		if e = q.ReviewDoubanWishRun(ctx, id); e != nil {
			return ports.ErrUnavailable
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return ports.ErrUnavailable
	}
	return nil
}

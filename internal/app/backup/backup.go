// Package backup implements offline age-encrypted logical backups.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfile"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCleanup requires the operator to quarantine the explicit target database.
var ErrCleanup = errors.New("restore failed and target cleanup failed")

// Options contain file paths and public recipient information only. ConnectionFile
// is a private, explicit pgx connection string file; IdentityFile is an age identity.
type Options struct{ DataDir, ConnectionFile, Archive, KeyArchive, Recipient, IdentityFile, Target, Migrations string }
type envelope struct {
	ID                uuid.UUID
	Schema            int64
	DumpHash, KeyHash string
	Dump, Key         []byte
	Spool             string `json:"-"`
}

func quote(s string) string {
	return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'"
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func private(path string) ([]byte, error) {
	r, e := rootfile.OpenDirectory(filepath.Dir(path), false)
	if e != nil {
		return nil, e
	}
	defer func() { _ = r.Close() }()
	f, e := rootfile.Open(r, filepath.Base(path), os.O_RDONLY, 0)
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	s, e := f.Stat()
	if e != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || s.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return nil, errors.New("private regular 0600 file required")
	}
	b, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if len(b) > 1<<20 {
		return nil, errors.New("file exceeds 1 MiB limit")
	}
	return b, e
}
func publish(path string, b []byte) error { return publishStream(path, bytes.NewReader(b)) }
func publishStream(path string, b io.Reader) error {
	r, e := rootfile.OpenDirectory(filepath.Dir(path), false)
	if e != nil {
		return e
	}
	defer func() { _ = r.Close() }()
	name := ".backup-" + uuid.NewString()
	f, e := rootfile.Open(r, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer func() { _ = r.Remove(name) }()
	_, e = io.Copy(f, b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = r.Link(name, filepath.Base(path)); e != nil {
		return e
	}
	d, e := r.Open(".")
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func connect(ctx context.Context, path string) (*pgx.Conn, *pgx.ConnConfig, error) {
	b, e := private(path)
	if e != nil {
		return nil, nil, e
	}
	c, e := pgx.ParseConfig(strings.TrimSpace(string(b)))
	if e != nil {
		return nil, nil, errors.New("invalid connection file")
	}
	if strings.ContainsAny(c.User+c.Password, "\r\n") {
		return nil, nil, errors.New("multiline database credentials refused")
	}
	db, e := pgx.ConnectConfig(ctx, c)
	if e != nil {
		return nil, nil, errors.New("database connection failed")
	}
	var v int
	if e = db.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&v); e != nil || v/10000 != 18 {
		_ = db.Close(ctx)
		return nil, nil, errors.New("PostgreSQL 18 required")
	}
	return db, c, nil
}
func pg(ctx context.Context, c *pgx.ConnConfig, command string, input io.Reader, output io.Writer) error {
	dir, e := os.MkdirTemp("/data/temp", "pulse-backup-pg-")
	if e != nil {
		return e
	}
	defer func() { _ = os.RemoveAll(dir) }()
	esc := func(s string) string { return strings.NewReplacer("\\", "\\\\", ":", "\\:").Replace(s) }
	pass := filepath.Join(dir, "pgpass")
	if e = os.WriteFile(pass, []byte("*:*:*:"+esc(c.User)+":"+esc(c.Password)+"\n"), 0600); e != nil {
		return e
	}
	ver, e := exec.CommandContext(ctx, command, "--version").Output()
	if e != nil || !strings.Contains(string(ver), " 18.") {
		return errors.New("PG18 client required")
	}
	args := []string{"--no-password", "--no-owner"}
	if command == "pg_dump" {
		args = append(args, "--format=custom")
	} else {
		args = append(args, "--no-privileges", "--exit-on-error", "--single-transaction", "--dbname=service=backup")
	}
	cmd := exec.CommandContext(ctx, command, args...)
	options, e := connectionOptions(c)
	if e != nil {
		return e
	}
	var service strings.Builder
	service.WriteString("[backup]\n")
	for k, v := range options {
		service.WriteString(k + "=" + v + "\n")
	}
	servicePath := filepath.Join(dir, "service.conf")
	if e = os.WriteFile(servicePath, []byte(service.String()), 0600); e != nil {
		return e
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PGSERVICE=backup", "PGSERVICEFILE=" + servicePath, "PGPASSFILE=" + pass, "TMPDIR=" + dir}
	cmd.Stdin = input
	cmd.Stdout = output
	if e = cmd.Run(); e != nil {
		return errors.New("PostgreSQL client operation failed")
	}
	return nil
}
func Export(ctx context.Context, o Options) error {
	l, e := lockfile.Acquire(o.DataDir)
	if e != nil {
		return e
	}
	defer func() { _ = l.Release() }()
	key, e := private(filepath.Join(o.DataDir, "secret.key"))
	if e != nil || len(key) != 32 {
		return errors.New("original 32-byte master key required")
	}
	db, c, e := connect(ctx, o.ConnectionFile)
	if e != nil {
		return e
	}
	defer func() { _ = db.Close(ctx) }()
	if e = validateKey(ctx, db, key); e != nil {
		return e
	}
	var schema int64
	if e = db.QueryRow(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&schema); e != nil {
		return errors.New("schema read failed")
	}
	dump, e := spool()
	if e != nil {
		return e
	}
	defer func() { _ = dump.Close(); _ = os.Remove(dump.Name()) }()
	e = pg(ctx, c, "pg_dump", nil, dump)
	if e != nil {
		return e
	}
	hash, e := fileDigest(dump)
	if e != nil {
		return e
	}
	v := envelope{ID: uuid.New(), Schema: schema, DumpHash: hash, KeyHash: digest(key), Spool: dump.Name()}
	a, e := spool()
	if e != nil {
		return e
	}
	defer func() { _ = a.Close(); _ = os.Remove(a.Name()) }()
	e = sealStream(a, v, o.Recipient)
	if e != nil {
		return e
	}
	v.Spool = ""
	v.Key = key
	k, e := seal(v, o.Recipient)
	if e != nil {
		return e
	}
	// Publish key first: an interrupted pair can be retried with fresh output names.
	if e = publish(o.KeyArchive, k); e != nil {
		return e
	}
	if _, e = a.Seek(0, io.SeekStart); e != nil {
		return e
	}
	return publishStream(o.Archive, a)
}
func Restore(ctx context.Context, o Options) (err error) {
	if !regexp.MustCompile(`^pulse_restore_[a-z0-9_]{1,40}$`).MatchString(o.Target) {
		return errors.New("explicit new target pulse_restore_<name> required")
	}
	l, e := lockfile.Acquire(o.DataDir)
	if e != nil {
		return e
	}
	defer func() { _ = l.Release() }()
	root, e := rootfile.OpenDirectory(o.DataDir, false)
	if e != nil {
		return e
	}
	defer func() { _ = root.Close() }()
	if _, e = root.Lstat("secret.key"); !errors.Is(e, os.ErrNotExist) {
		return errors.New("target master key already exists or cannot be inspected")
	}
	a, e := unseal(o.Archive, o.IdentityFile)
	if e != nil {
		return e
	}
	defer func() { _ = os.Remove(a.Spool) }()
	k, e := unseal(o.KeyArchive, o.IdentityFile)
	if e != nil {
		return e
	}
	if k.Spool != "" {
		defer func() { _ = os.Remove(k.Spool) }()
	}
	dump, e := os.Open(a.Spool)
	if e != nil {
		return e
	}
	defer func() { _ = dump.Close() }()
	hash, e := fileDigest(dump)
	if e != nil {
		return e
	}
	if _, e = dump.Seek(0, io.SeekStart); e != nil {
		return e
	}
	if a.ID == uuid.Nil || a.ID != k.ID || a.Schema != k.Schema || len(k.Key) != 32 || a.KeyHash != digest(k.Key) || k.KeyHash != a.KeyHash || a.DumpHash != hash || k.DumpHash != a.DumpHash || len(a.Key) != 0 || len(a.Dump) != 0 || len(k.Dump) != 0 || k.Spool != "" {
		return errors.New("archive/master key binding mismatch")
	}
	admin, c, e := connect(ctx, o.ConnectionFile)
	if e != nil {
		return e
	}
	defer func() { _ = admin.Close(ctx) }()
	if c.Database == o.Target {
		return errors.New("target must differ from connection database")
	}
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{o.Target}.Sanitize()+" TEMPLATE template0"); e != nil {
		return errors.New("target creation refused; target must be new")
	}
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, drop := admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{o.Target}.Sanitize()+" WITH (FORCE)")
			if drop != nil {
				err = fmt.Errorf("%w: quarantine database %s", ErrCleanup, o.Target)
			}
		}
	}()
	c, e = targetConfig(c, o.Target)
	if e != nil {
		return errors.New("target configuration failed")
	}
	if e = pg(ctx, c, "pg_restore", dump, io.Discard); e != nil {
		return e
	}
	check, e := pgx.ConnectConfig(ctx, c)
	if e != nil {
		return errors.New("restored database unavailable")
	}
	var restoredSchema int64
	e = check.QueryRow(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&restoredSchema)
	if e == nil {
		e = validateKey(ctx, check, k.Key)
	}
	_ = check.Close(ctx)
	if e != nil || restoredSchema != a.Schema || a.Schema > postgres.CurrentMigrationVersion {
		return errors.New("archive schema binding mismatch")
	}
	if _, e = postgres.Migrate(ctx, c.ConnString(), o.Migrations); e != nil {
		return errors.New("restored schema migration failed")
	}
	pc, e := pgxpool.ParseConfig(c.ConnString())
	if e != nil {
		return errors.New("pool configuration failed")
	}
	pool, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		return errors.New("restored connection failed")
	}
	defer pool.Close()
	if e = postgres.ActivateRestoreIsolation(ctx, pool, a.ID); e != nil {
		return errors.New("restore isolation activation failed")
	}
	return publish(filepath.Join(o.DataDir, "secret.key"), k.Key)
}

// Inspect and Reconcile require the same offline data-directory exclusion as restore.
func Inspect(ctx context.Context, o Options) (postgres.RestoreIsolationStatus, error) {
	var s postgres.RestoreIsolationStatus
	l, e := lockfile.Acquire(o.DataDir)
	if e != nil {
		return s, e
	}
	defer func() { _ = l.Release() }()
	p, e := pool(ctx, o.ConnectionFile)
	if e != nil {
		return s, e
	}
	defer p.Close()
	return postgres.RestoreStatus(ctx, p)
}
func pool(ctx context.Context, path string) (*pgxpool.Pool, error) {
	d, c, e := connect(ctx, path)
	if e != nil {
		return nil, e
	}
	_ = d.Close(ctx)
	p, e := pgxpool.New(ctx, c.ConnString())
	if e != nil {
		return nil, errors.New("pool failed")
	}
	return p, nil
}
func Reconcile(ctx context.Context, o Options, r postgres.RestoreReconciliation) error {
	l, e := lockfile.Acquire(o.DataDir)
	if e != nil {
		return e
	}
	defer func() { _ = l.Release() }()
	p, e := pool(ctx, o.ConnectionFile)
	if e != nil {
		return e
	}
	defer p.Close()
	if e = postgres.ReconcileRestoreIsolation(ctx, p, r); e != nil {
		return errors.New("reconciliation refused: verify restore ID, evidence and outstanding work")
	}
	return nil
}

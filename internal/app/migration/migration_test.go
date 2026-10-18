package migration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/dalamudx/PulseMedia/internal/app/backup"
	cryptoplatform "github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("/data/temp", "pulse-migration-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "migration scratch creation failed")
		os.Exit(1)
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err = os.Setenv(key, dir); err != nil {
			_ = os.RemoveAll(dir)
			os.Exit(1)
		}
	}
	code := m.Run()
	if err = os.RemoveAll(dir); err != nil {
		code = 1
	}
	if _, err = os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		code = 1
	}
	if code == 0 {
		fmt.Println("OWNED_MIGRATION_TEST_TEMP_CLEANUP_VERIFIED")
	}
	os.Exit(code)
}
func put(t *testing.T, path string, b []byte) string {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	return path
}

func TestExplicitConnection(t *testing.T) {
	dir := t.TempDir()
	p := put(t, filepath.Join(dir, "connection"), []byte("postgres://explicit:private@localhost/db?sslmode=disable"))
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGSERVICEFILE", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGSSLPASSWORD", "PGCONNECT_TIMEOUT", "PGTARGETSESSIONATTRS", "PGOPTIONS", "PGAPPNAME", "PGTZ", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGSSLSNI", "PGSSLNEGOTIATION", "PGCHANNELBINDING", "PGREQUIREAUTH"} {
		t.Setenv(key, "invalid-inherited-value")
	}
	dsn, err := connection(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := pgx.ParseConfig(dsn)
	if err != nil || c.Host != "localhost" || c.Port != 5432 || c.User != "explicit" || c.Database != "db" || c.Password != "private" || c.TLSConfig != nil || len(c.Fallbacks) != 0 {
		t.Fatal("explicit connection inherited settings")
	}
	// Absence of a file password means explicitly empty, never PGPASSWORD/pgpass.
	put(t, p, []byte("postgres://explicit@localhost/db?sslmode=disable"))
	dsn, err = connection(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err = pgx.ParseConfig(dsn)
	if err != nil || c.Password != "" {
		t.Fatal("inherited password")
	}
	put(t, p, []byte("postgres://explicit:private@db.example/db?sslmode=verify-full&sslrootcert=system"))
	dsn, err = connection(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err = pgx.ParseConfig(dsn)
	if err != nil || c.TLSConfig == nil || c.TLSConfig.ServerName != "db.example" || len(c.Fallbacks) != 0 {
		t.Fatal("TLS identity not preserved")
	}
	for _, raw := range []string{"", "host=localhost user=explicit dbname=db", "postgres://localhost/db?sslmode=disable", "postgres://explicit@localhost/?sslmode=disable", "postgres://explicit@localhost/db", "postgres://explicit@remote/db?sslmode=disable", "postgres://explicit@localhost/db?sslmode=prefer", "postgres://explicit@localhost/db?sslmode=disable&sslmode=verify-full", "postgres://explicit@localhost/db?sslmode=disable&service=hidden", "postgres://explicit@localhost/db?sslmode=disable&host=localhost,other"} {
		put(t, p, []byte(raw))
		if _, err = connection(p); err == nil {
			t.Fatal("unsafe connection accepted")
		}
	}
	put(t, p, bytes.Repeat([]byte{'x'}, (1<<20)+1))
	if _, err = connection(p); err == nil {
		t.Fatal("oversized file accepted")
	}
	put(t, p, []byte("postgres://explicit@localhost/db?sslmode=disable"))
	t.Setenv("PGSERVICE", "untrusted-service")
	if _, err = connection(p); err == nil {
		t.Fatal("inherited service selection accepted")
	}
	t.Setenv("PGSERVICE", "")
	if err = os.Chmod(p, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err = connection(p); err == nil {
		t.Fatal("unsafe permission accepted")
	}
	if err = os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err = connection(link); err == nil {
		t.Fatal("symlink accepted")
	}
	parent := filepath.Join(dir, "parent")
	if err = os.Symlink(dir, parent); err != nil {
		t.Fatal(err)
	}
	if _, err = connection(filepath.Join(parent, "connection")); err == nil {
		t.Fatal("ancestor symlink accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err = syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = connection(fifo); err == nil {
		t.Fatal("FIFO accepted")
	}
}

func TestMigrationCLI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cluster := pgtest.GetCluster(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "pulsemigrate")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "../../../cmd/pulsemigrate")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build failed: %s", out)
	}
	migrations, err := filepath.Abs("../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	call := func(t *testing.T, success bool, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		out, err := cmd.CombinedOutput()
		if success != (err == nil) {
			t.Fatalf("CLI success=%v expected=%v: %s", err == nil, success, out)
		}
		if strings.Contains(string(out), "CREDENTIAL_SENTINEL") {
			t.Fatal("credentials leaked")
		}
		if !success && strings.Contains(string(out), "migration completed") {
			t.Fatal("misleading success")
		}
		return string(out)
	}
	options := func(t *testing.T, dsn string) (Options, []string) {
		t.Helper()
		state := t.TempDir()
		o := Options{DataDir: state, ConnectionFile: put(t, filepath.Join(state, "connection"), []byte(dsn)), Migrations: migrations}
		return o, []string{"--data-dir", state, "--connection-file", o.ConnectionFile, "--migrations", migrations}
	}
	open := func(t *testing.T, dsn string) *pgx.Conn {
		t.Helper()
		c, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal("fixture connection failed")
		}
		t.Cleanup(func() { _ = c.Close(context.Background()) })
		return c
	}
	version := func(t *testing.T, c *pgx.Conn, expected int64) {
		t.Helper()
		var v int64
		if err := c.QueryRow(ctx, "SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1").Scan(&v); err != nil || v != expected {
			t.Fatalf("expected version %d, got %d", expected, v)
		}
	}
	t.Run("help-and-required-inputs", func(t *testing.T) {
		if out := call(t, true, "--help"); !strings.Contains(out, "--connection-file") || !strings.Contains(out, "--archive") {
			t.Fatal("help incomplete")
		}
		call(t, false)
		call(t, false, "--connection-file", "CREDENTIAL_SENTINEL")
		call(t, false, "--unexpected=CREDENTIAL_SENTINEL")
	})
	t.Run("empty-install-and-lock", func(t *testing.T) {
		dsn := cluster.NewTestDB(t)
		parsed, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		ddl, _ := cluster.CreateRoles(t, strings.TrimPrefix(parsed.Path, "/"))
		o, args := options(t, ddl)
		c := open(t, dsn)
		l, err := lockfile.Acquire(o.DataDir)
		if err != nil {
			t.Fatal(err)
		}
		call(t, false, args...)
		if err = l.Release(); err != nil {
			t.Fatal(err)
		}
		var absent bool
		if err = c.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NULL").Scan(&absent); err != nil || !absent {
			t.Fatal("locked command mutated database")
		}
		out := call(t, true, args...)
		if !strings.Contains(out, "schema 0 -> 83") {
			t.Fatal(out)
		}
		version(t, c, 83)
		if _, err = os.Lstat(filepath.Join(o.DataDir, "secret.key")); !os.IsNotExist(err) {
			t.Fatal("master key created")
		}
		var isolated bool
		if err = c.QueryRow(ctx, "SELECT isolated FROM restore_isolation").Scan(&isolated); err != nil || isolated {
			t.Fatal("fresh isolation incorrect")
		}
	})
	t.Run("unsafe-input-and-nonempty-unversioned", func(t *testing.T) {
		dsn := cluster.NewTestDB(t)
		o, args := options(t, dsn)
		c := open(t, dsn)
		for _, mode := range []os.FileMode{0644, 0400} {
			if err = os.Chmod(o.ConnectionFile, mode); err != nil {
				t.Fatal(err)
			}
			call(t, false, args...)
		}
		if err = os.Chmod(o.ConnectionFile, 0600); err != nil {
			t.Fatal(err)
		}
		original, err := os.ReadFile(o.ConnectionFile)
		if err != nil {
			t.Fatal(err)
		}
		put(t, o.ConnectionFile, []byte("postgres://u:CREDENTIAL_SENTINEL@localhost/db?sslmode=prefer"))
		call(t, false, args...)
		put(t, o.ConnectionFile, original)
		if _, err = c.Exec(ctx, "CREATE TABLE preexisting(value text)"); err != nil {
			t.Fatal(err)
		}
		call(t, false, args...)
		var absent bool
		if err = c.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NULL").Scan(&absent); err != nil || !absent {
			t.Fatal("nonempty database mutated")
		}
	})
	t.Run("schema82-recovery-upgrade-and-isolation", func(t *testing.T) {
		dsn := cluster.NewTestDB(t)
		o, args := options(t, dsn)
		c := open(t, dsn)
		old := t.TempDir()
		entries, err := os.ReadDir(migrations)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".sql") && entry.Name() < "00083" {
				b, e := os.ReadFile(filepath.Join(migrations, entry.Name()))
				if e != nil {
					t.Fatal(e)
				}
				put(t, filepath.Join(old, entry.Name()), b)
			}
		}
		if v, e := postgres.Migrate(ctx, dsn, old); e != nil || v != 82 {
			t.Fatalf("old schema fixture failed version=%d", v)
		}
		key := bytes.Repeat([]byte{42}, 32)
		put(t, filepath.Join(o.DataDir, "secret.key"), key)
		encrypted, e := cryptoplatform.Encrypt(key, []byte("CREDENTIAL_SENTINEL"))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Exec(ctx, "INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'migration fixture','https://example.invalid',$2)", uuid.New(), encrypted); e != nil {
			t.Fatal(e)
		}
		id, e := age.GenerateX25519Identity()
		if e != nil {
			t.Fatal(e)
		}
		o.Archive = filepath.Join(o.DataDir, "dump.age")
		o.KeyArchive = filepath.Join(o.DataDir, "key.age")
		o.IdentityFile = put(t, filepath.Join(o.DataDir, "identity"), []byte(id.String()+"\n"))
		bo := backup.Options{DataDir: o.DataDir, ConnectionFile: o.ConnectionFile, Archive: o.Archive, KeyArchive: o.KeyArchive, Recipient: id.Recipient().String()}
		if e = backup.Export(ctx, bo); e != nil {
			t.Fatalf("real backup export failed: %v", e)
		}
		call(t, false, args...)
		version(t, c, 82)
		recoveryArgs := append(append([]string{}, args...), "--archive", o.Archive, "--key-archive", o.KeyArchive, "--identity-file", o.IdentityFile)
		original, e := os.ReadFile(o.Archive)
		if e != nil {
			t.Fatal(e)
		}
		put(t, o.Archive, original[:len(original)-1])
		call(t, false, recoveryArgs...)
		version(t, c, 82)
		put(t, o.Archive, original)
		put(t, filepath.Join(o.DataDir, "secret.key"), bytes.Repeat([]byte{24}, 32))
		call(t, false, recoveryArgs...)
		version(t, c, 82)
		put(t, filepath.Join(o.DataDir, "secret.key"), key)
		fake := rewriteArchive(t, original, id, func(fields map[string]any, dump *[]byte) {
			*dump = []byte("PGDMP deliberately invalid custom dump")
			fields["DumpHash"] = independentDigest(*dump)
		})
		put(t, o.Archive, fake)
		call(t, false, recoveryArgs...)
		version(t, c, 82)
		put(t, o.Archive, original)
		savedKey, e := os.ReadFile(o.KeyArchive)
		if e != nil {
			t.Fatal(e)
		}
		for _, mutation := range []func(map[string]any, *[]byte){
			func(fields map[string]any, _ *[]byte) { fields["ID"] = uuid.NewString() },
			func(fields map[string]any, _ *[]byte) { fields["Schema"] = 81 },
			func(fields map[string]any, _ *[]byte) { fields["DumpHash"] = strings.Repeat("0", 64) },
		} {
			put(t, o.KeyArchive, rewriteArchive(t, savedKey, id, mutation))
			call(t, false, recoveryArgs...)
			version(t, c, 82)
		}
		put(t, o.KeyArchive, savedKey)
		wrong, e := age.GenerateX25519Identity()
		if e != nil {
			t.Fatal(e)
		}
		put(t, o.IdentityFile, []byte(wrong.String()+"\n"))
		call(t, false, recoveryArgs...)
		version(t, c, 82)
		put(t, o.IdentityFile, []byte(id.String()+"\n"))
		call(t, true, recoveryArgs...)
		version(t, c, 83)
		var stored []byte
		if e = c.QueryRow(ctx, "SELECT api_key_encrypted FROM indexer WHERE name='migration fixture'").Scan(&stored); e != nil || !bytes.Equal(stored, encrypted) {
			t.Fatal("encrypted data changed")
		}
		pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
		if e != nil {
			t.Fatal(e)
		}
		restoreID := uuid.New()
		e = postgres.ActivateRestoreIsolation(ctx, pool, restoreID)
		pool.Close()
		if e != nil {
			t.Fatal(e)
		}
		// Re-export schema83 before a repeat migration. The CLI must preserve both
		// persistent isolation and immutable activation history.
		bo.Archive = filepath.Join(o.DataDir, "isolated.age")
		bo.KeyArchive = filepath.Join(o.DataDir, "isolated-key.age")
		if e = backup.Export(ctx, bo); e != nil {
			t.Fatal(e)
		}
		repeat := append(append([]string{}, args...), "--archive", bo.Archive, "--key-archive", bo.KeyArchive, "--identity-file", o.IdentityFile)
		call(t, true, repeat...)
		var isolated bool
		var got uuid.UUID
		var audits int
		if e = c.QueryRow(ctx, "SELECT isolated,restore_id,(SELECT count(*) FROM restore_isolation_audit) FROM restore_isolation").Scan(&isolated, &got, &audits); e != nil || !isolated || got != restoreID || audits != 1 {
			t.Fatal("restore isolation/history changed")
		}
		b, e := os.ReadFile(filepath.Join(o.DataDir, "secret.key"))
		if e != nil || !bytes.Equal(b, key) {
			t.Fatal("master key changed")
		}
	})
	t.Run("failure-rollback", func(t *testing.T) {
		dsn := cluster.NewTestDB(t)
		_, args := options(t, dsn)
		c := open(t, dsn)
		bad := t.TempDir()
		put(t, filepath.Join(bad, "00001_failure.sql"), []byte("-- +goose Up\nCREATE TABLE must_rollback(id int);\n-- +goose StatementBegin\nDO $$ BEGIN RAISE EXCEPTION 'CREDENTIAL_SENTINEL'; END $$;\n-- +goose StatementEnd\n-- +goose Down\nDROP TABLE must_rollback;\n"))
		args[len(args)-1] = bad
		call(t, false, args...)
		version(t, c, 0)
		var absent bool
		if e := c.QueryRow(ctx, "SELECT to_regclass('must_rollback') IS NULL").Scan(&absent); e != nil || !absent {
			t.Fatal("failed migration did not roll back")
		}
	})
	t.Run("signals-session-loss-and-concurrency", func(t *testing.T) {
		for _, failure := range []string{"signal", "disconnect"} {
			t.Run(failure, func(t *testing.T) {
				dsn := cluster.NewTestDB(t)
				o, args := options(t, dsn)
				c := open(t, dsn)
				held := t.TempDir()
				put(t, filepath.Join(held, "00001_hold.sql"), []byte("-- +goose Up\nCREATE TABLE held_rollback(id int);\nSELECT pg_sleep(60);\n-- +goose Down\nDROP TABLE held_rollback;\n"))
				args[len(args)-1] = held
				cmd := exec.CommandContext(ctx, binary, args...)
				var output bytes.Buffer
				cmd.Stdout = &output
				cmd.Stderr = &output
				if e := cmd.Start(); e != nil {
					t.Fatal(e)
				}
				finished := false
				defer func() {
					if !finished {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				}()
				waitCtx, stop := context.WithTimeout(ctx, 20*time.Second)
				defer stop()
				var pid int32
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					e := c.QueryRow(waitCtx, "SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND application_name='pulsemigrate' AND state='active' AND query LIKE '%pg_sleep(60)%'").Scan(&pid)
					if e == nil {
						break
					}
					if !errors.Is(e, pgx.ErrNoRows) {
						t.Fatal("migration never reached held DDL")
					}
					select {
					case <-waitCtx.Done():
						t.Fatal("held DDL timeout")
					case <-ticker.C:
					}
				}
				// The executing DDL backend itself must own the store's session lock.
				var locks int
				if e := c.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND pid=$1 AND granted AND classid::bigint=$2 AND objid::bigint=$3 AND objsubid=1", pid, postgres.MigrationAdvisoryLockID>>32, postgres.MigrationAdvisoryLockID&0xffffffff).Scan(&locks); e != nil || locks != 1 {
					t.Fatal("DDL backend lacks migration session lock")
				}
				// Same-directory and alternate-directory CLI contenders both fail. The
				// store also rejects an independent migrator with its exact busy contract.
				call(t, false, args...)
				if _, e := postgres.Migrate(ctx, dsn, held); !errors.Is(e, postgres.ErrMigrationBusy) {
					t.Fatal("concurrent store migration did not reject held session")
				}
				_, other := options(t, dsn)
				other[len(other)-1] = held
				call(t, false, other...)
				if failure == "signal" {
					if e := cmd.Process.Signal(syscall.SIGTERM); e != nil {
						t.Fatal(e)
					}
				} else {
					if _, e := c.Exec(ctx, "SELECT pg_terminate_backend($1)", pid); e != nil {
						t.Fatal(e)
					}
				}
				if e := cmd.Wait(); e == nil {
					finished = true
					t.Fatal("interrupted migration returned success")
				}
				finished = true
				if strings.Contains(output.String(), "migration completed") {
					t.Fatal("interrupted migration claimed success")
				}
				version(t, c, 0)
				var absent bool
				if e := c.QueryRow(ctx, "SELECT to_regclass('held_rollback') IS NULL").Scan(&absent); e != nil || !absent {
					t.Fatal("interrupted migration DDL remained")
				}
				l, e := lockfile.Acquire(o.DataDir)
				if e != nil {
					t.Fatal("data-dir lock retained")
				}
				if e = l.Release(); e != nil {
					t.Fatal(e)
				}
				closedCtx, closeCancel := context.WithTimeout(ctx, 5*time.Second)
				defer closeCancel()
				for {
					var connections int
					if e = c.QueryRow(closedCtx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='pulsemigrate'").Scan(&connections); e != nil {
						t.Fatal("could not verify migration session closure")
					}
					if connections == 0 {
						break
					}
					select {
					case <-closedCtx.Done():
						t.Fatal("migration reconnected or retained session")
					case <-ticker.C:
					}
				}
			})
		}
	})
	// Explicit malformed DSN error output also remains closed under URL userinfo.
	t.Run("unavailable-connection-redaction", func(t *testing.T) {
		u, e := url.Parse(cluster.BaseDSN)
		if e != nil {
			t.Fatal(e)
		}
		u.User = url.UserPassword("nonexistent", "CREDENTIAL_SENTINEL")
		_, args := options(t, u.String())
		call(t, false, args...)
	})
}

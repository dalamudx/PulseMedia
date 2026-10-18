package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"filippo.io/age"
	"fmt"
	cryptoplatform "github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealBackupRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cluster := pgtest.GetCluster(t)
	dsn := cluster.NewTestDB(t)
	dir, e := os.MkdirTemp("/data/temp", "pulse-backup-test-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := os.RemoveAll(dir); e != nil {
			t.Error(e)
		}
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Error("owned scratch cleanup not verified")
		}
	}()
	put := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	if e = os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(dest, 0700); e != nil {
		t.Fatal(e)
	}
	key := bytes.Repeat([]byte{42}, 32)
	if e = os.WriteFile(filepath.Join(source, "secret.key"), key, 0600); e != nil {
		t.Fatal(e)
	}
	migrations, e := filepath.Abs("../../../migrations")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = postgres.Migrate(ctx, dsn, migrations); e != nil {
		t.Fatal(e)
	}
	c, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = c.Close(ctx) }()
	if _, e = c.Exec(ctx, "CREATE TABLE backup_probe(value text); INSERT INTO backup_probe VALUES ('preserved')"); e != nil {
		t.Fatal(e)
	}
	encrypted, e := cryptoplatform.Encrypt(key, []byte("private fixture credential"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(ctx, "CREATE TABLE backup_secret(value bytea)"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(ctx, "INSERT INTO backup_secret VALUES($1)", encrypted); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(ctx, "INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'backup fixture','https://example.invalid',$2)", uuid.New(), encrypted); e != nil {
		t.Fatal(e)
	}
	identity, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	o := Options{DataDir: source, ConnectionFile: put("source.conn", []byte(dsn)), Archive: filepath.Join(dir, "dump.age"), KeyArchive: filepath.Join(dir, "key.age"), Recipient: identity.Recipient().String(), IdentityFile: put("identity", []byte(identity.String()+"\n")), Target: "pulse_restore_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Migrations: migrations}
	if e = os.WriteFile(filepath.Join(source, "secret.key"), bytes.Repeat([]byte{99}, 32), 0600); e != nil {
		t.Fatal(e)
	}
	if e = Export(ctx, o); e == nil {
		t.Fatal("wrong source master key accepted")
	}
	if e = os.WriteFile(filepath.Join(source, "secret.key"), key, 0600); e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(dir, "pulsebackup")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../../cmd/pulsebackup")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("CLI build: %s", out)
	}
	cli := func(action string, extra ...string) {
		t.Helper()
		args := []string{action, "--data-dir", o.DataDir, "--connection-file", o.ConnectionFile}
		args = append(args, extra...)
		if out, e := exec.CommandContext(ctx, binary, args...).CombinedOutput(); e != nil {
			t.Fatalf("CLI %s failed: %s", action, out)
		}
	}
	cli("export", "--archive", o.Archive, "--key-archive", o.KeyArchive, "--recipient", o.Recipient)
	if e = Export(ctx, o); e == nil {
		t.Fatal("overwrite accepted")
	}
	for i := 0; i < 6; i++ {
		copy := o
		copy.Archive = filepath.Join(dir, fmt.Sprintf("dump-%d.age", i))
		copy.KeyArchive = filepath.Join(dir, fmt.Sprintf("key-%d.age", i))
		if e = Export(ctx, copy); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = os.Stat(o.Archive); e != nil {
		t.Fatal("old backup removed")
	}
	o.DataDir = dest
	wrong, _ := age.GenerateX25519Identity()
	bad := o
	bad.IdentityFile = put("wrong", []byte(wrong.String()+"\n"))
	if Restore(ctx, bad) == nil {
		t.Fatal("wrong identity accepted")
	}
	original, _ := os.ReadFile(o.Archive)
	for _, b := range [][]byte{original[:len(original)-1], append(append([]byte{}, original[:len(original)-1]...), original[len(original)-1]^1)} {
		bad = o
		bad.Archive = put("bad.age", b)
		if Restore(ctx, bad) == nil {
			t.Fatal("damaged archive accepted")
		}
	}
	k, e := unseal(o.KeyArchive, o.IdentityFile)
	if e != nil {
		t.Fatal(e)
	}
	k.Key[0] ^= 1
	enc, _ := seal(k, o.Recipient)
	bad = o
	bad.KeyArchive = put("mismatch.age", enc)
	if Restore(ctx, bad) == nil {
		t.Fatal("mismatched key accepted")
	}
	// A migration failure must drop its newly created database.
	bad = o
	bad.Migrations = filepath.Join(dir, "missing")
	if Restore(ctx, bad) == nil {
		t.Fatal("missing migrations accepted")
	}
	var exists bool
	if e = c.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", o.Target).Scan(&exists); e != nil {
		t.Fatal(e)
	}
	if exists {
		t.Fatal("failed restore database remains")
	}
	cli("restore", "--archive", o.Archive, "--key-archive", o.KeyArchive, "--identity-file", o.IdentityFile, "--target", o.Target, "--migrations", o.Migrations)
	defer func() {
		if _, e = c.Exec(ctx, "DROP DATABASE "+pgx.Identifier{o.Target}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
	}()
	tc, _ := pgx.ParseConfig(dsn)
	targetDSN := tc.ConnString()
	if strings.HasPrefix(targetDSN, "postgres") {
		idx := strings.Index(targetDSN, "?")
		if idx < 0 {
			idx = len(targetDSN)
		}
		slash := strings.LastIndex(targetDSN[:idx], "/")
		targetDSN = targetDSN[:slash+1] + o.Target + targetDSN[idx:]
	} else {
		targetDSN += " dbname=" + o.Target
	}
	o.ConnectionFile = put("target.conn", []byte(targetDSN))
	s, e := Inspect(ctx, o)
	if e != nil || !s.Isolated || s.RestoreID == nil {
		t.Fatalf("isolation: %v %v", s, e)
	}
	restored, e := pgx.Connect(ctx, targetDSN)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = restored.Close(ctx) }()
	var value string
	if e = restored.QueryRow(ctx, "SELECT value FROM backup_probe").Scan(&value); e != nil || value != "preserved" {
		t.Fatal("data not preserved")
	}
	got, _ := os.ReadFile(filepath.Join(dest, "secret.key"))
	if !bytes.Equal(got, key) {
		t.Fatal("key not preserved")
	}
	var cipher []byte
	if e = restored.QueryRow(ctx, "SELECT value FROM backup_secret").Scan(&cipher); e != nil {
		t.Fatal(e)
	}
	plain, e := cryptoplatform.Decrypt(got, cipher)
	if e != nil || string(plain) != "private fixture credential" {
		t.Fatal("restored credential decryption failed")
	}
	l, e := lockfile.Acquire(dest)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Inspect(ctx, o); e == nil {
		t.Fatal("live app lock ignored")
	}
	if e = l.Release(); e != nil {
		t.Fatal(e)
	}
	r := postgres.RestoreReconciliation{RestoreID: *s.RestoreID, Operator: "offline test", DownloaderEvidence: "private fixture: no external tasks", FilesystemEvidence: "probe and original key verified", OrphanEvidence: "private fixture: no external orphans"}
	p, e := pgxpool.New(ctx, targetDSN)
	if e != nil {
		t.Fatal(e)
	}
	if e = postgres.RequireRestoreReconciled(ctx, p); e == nil {
		t.Fatal("App fence failed")
	}
	p.Close()
	badR := r
	badR.RestoreID = uuid.New()
	if Reconcile(ctx, o, badR) == nil {
		t.Fatal("wrong restore ID accepted")
	}
	jobID := uuid.New()
	if _, e = restored.Exec(ctx, "INSERT INTO job(id,kind,state) VALUES($1,'backup_probe','queued')", jobID); e != nil {
		t.Fatal(e)
	}
	if Reconcile(ctx, o, r) == nil {
		t.Fatal("queued work released")
	}
	if _, e = restored.Exec(ctx, "DELETE FROM job WHERE id=$1", jobID); e != nil {
		t.Fatal(e)
	}
	rb, _ := json.Marshal(r)
	cli("status")
	cli("reconcile", "--evidence-file", put("evidence.json", rb))
	s, e = Inspect(ctx, o)
	if e != nil || s.Isolated {
		t.Fatal("release failed")
	}
	// Existing database and key must remain untouched.
	if Restore(ctx, o) == nil {
		t.Fatal("existing target accepted")
	}
}

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	cryptoplatform "github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestContainerInstallUpgrade is explicitly opt-in: build the final image first,
// then run through scripts/with_podman_test_postgres.sh with
// PULSE_CONTAINER_TEST_IMAGE set. Only the schema82 upgrade seed uses host SQL;
// the empty installation, recovery exports and upgrades use image binaries.
func TestContainerInstallUpgrade(t *testing.T) {
	image := os.Getenv("PULSE_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("explicit locally built final container image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cluster := pgtest.GetCluster(t)
	cfg, err := pgx.ParseConfig(cluster.BaseDSN)
	if err != nil || !filepath.IsAbs(cfg.Host) || cfg.Password != "" || cfg.Port != 5432 {
		t.Fatal("private password-free PG18 Unix-socket fixture required")
	}
	for _, p := range []string{cfg.Host, filepath.Join(cfg.Host, ".s.PGSQL.5432")} {
		st, e := os.Stat(p)
		if e != nil {
			t.Fatal("private socket unavailable")
		}
		path, mode := p, st.Mode().Perm()
		if e = os.Chmod(path, 0777); e != nil {
			t.Fatal("private socket permission setup failed")
		}
		t.Cleanup(func() {
			current, e := os.Stat(path)
			if e != nil || !os.SameFile(st, current) {
				t.Error("private socket identity changed; refusing chmod")
				return
			}
			if e = os.Chmod(path, mode); e != nil {
				t.Error("private socket mode restoration failed")
			}
		})
	}
	inspect := func(format string) string {
		t.Helper()
		b, e := exec.CommandContext(ctx, "podman", "image", "inspect", "--format", format, image).Output()
		if e != nil {
			t.Fatal("final test image inspection failed")
		}
		return strings.TrimSpace(string(b))
	}
	if inspect("{{.Config.User}}") != "10001:10001" {
		t.Fatal("image must default to UID/GID10001")
	}
	arch := inspect("{{.Architecture}}")
	if arch != "amd64" && arch != "arm64" {
		t.Fatal("unexpected image architecture")
	}
	// Pin all invocations to the inspected image even if a local tag is rebuilt.
	imageID := inspect("{{.Id}}")

	for _, scenario := range []string{"empty-install", "schema82-upgrade-and-isolation"} {
		t.Run(scenario, func(t *testing.T) {
			s := newContainerUpgradeSmoke(t, ctx, imageID, arch, cfg.Host, cluster.NewTestDB(t))
			if scenario == "empty-install" {
				var empty bool
				if e := s.db.QueryRow(ctx, "SELECT to_regclass('public.goose_db_version') IS NULL").Scan(&empty); e != nil || !empty {
					t.Fatal("install fixture was already migrated")
				}
				out := s.migrate(true)
				if !strings.Contains(out, fmt.Sprintf("schema 0 -> %d", postgres.CurrentMigrationVersion)) {
					t.Fatal("image CLI did not report a fresh current-schema installation")
				}
				s.version(postgres.CurrentMigrationVersion)
				if _, e := os.Lstat(filepath.Join(s.data, "secret.key")); !os.IsNotExist(e) {
					t.Fatal("installer invented a master key")
				}
				var isolated bool
				if e := s.db.QueryRow(ctx, "SELECT isolated FROM restore_isolation").Scan(&isolated); e != nil || isolated {
					t.Fatal("fresh installation has incorrect isolation state")
				}
				return
			}
			s.verifyUpgrade()
		})
	}
	if !t.Failed() {
		t.Logf("CONTAINER_INSTALL_UPGRADE_PASSED arch=%s image=%s uid=10001 schema=%d empty_image_install=true no_invented_key=true image_backup=true recovery_refusals_unchanged=true credential_preserved=true isolation_history_preserved=true read_only_root=true capabilities=none network=none", arch, imageID, postgres.CurrentMigrationVersion)
	}
}

type containerUpgradeSmoke struct {
	t       *testing.T
	ctx     context.Context
	image   string
	arch    string
	socket  string
	work    string
	data    string
	dsn     string
	db      *pgx.Conn
	secrets [][]byte
	calls   int
}

func newContainerUpgradeSmoke(t *testing.T, ctx context.Context, image, arch, socket, dsn string) *containerUpgradeSmoke {
	t.Helper()
	work, err := os.MkdirTemp("/data/temp", "pulse-image-upgrade-")
	if err != nil {
		t.Fatal("owned scratch creation failed")
	}
	st, err := os.Lstat(work)
	if err != nil {
		t.Fatal("owned scratch inspection failed")
	}
	t.Cleanup(func() {
		current, e := os.Lstat(work)
		if e != nil || !os.SameFile(st, current) || !current.IsDir() {
			t.Error("owned scratch identity changed; refusing removal")
			return
		}
		if e = os.RemoveAll(work); e != nil {
			t.Error("owned upgrade scratch cleanup failed")
		}
		if _, e = os.Lstat(work); !os.IsNotExist(e) {
			t.Error("owned upgrade scratch remains")
		}
	})
	for _, name := range []string{"data", "temp"} {
		path := filepath.Join(work, name)
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal("owned bind directory creation failed")
		}
		if err = os.Chown(path, 10001, 10001); err != nil {
			t.Fatal("container smoke requires ownership-capable host")
		}
	}
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("private upgrade database connection failed")
	}
	t.Cleanup(func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := c.Close(closeCtx); e != nil {
			t.Error("private upgrade connection cleanup failed")
		}
	})
	s := &containerUpgradeSmoke{t: t, ctx: ctx, image: image, arch: arch, socket: socket, work: work, data: filepath.Join(work, "data"), dsn: dsn, db: c}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("private fixture connection invalid")
	}
	u := &url.URL{Scheme: "postgres", User: url.User(cfg.User), Host: "localhost", Path: "/" + cfg.Database}
	u.RawQuery = url.Values{"host": {"/pg"}, "sslmode": {"disable"}}.Encode()
	s.put("connection", []byte(u.String()))
	return s
}

func (s *containerUpgradeSmoke) put(name string, b []byte) {
	s.t.Helper()
	path := filepath.Join(s.data, name)
	if err := os.WriteFile(path, b, 0600); err != nil {
		s.t.Fatal("private fixture write failed")
	}
	if err := os.Chown(path, 10001, 10001); err != nil {
		s.t.Fatal("private fixture ownership failed")
	}
}

func (s *containerUpgradeSmoke) run(success bool, binary string, args ...string) string {
	s.t.Helper()
	s.calls++
	name := fmt.Sprintf("%s-%d", filepath.Base(s.work), s.calls)
	const label = "io.pulsemedia.upgrade-smoke"
	s.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := exec.CommandContext(ctx, "podman", "container", "exists", name).Run()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				s.t.Error("cannot verify owned upgrade container cleanup")
			}
			return
		}
		b, e := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{index .Config.Labels \""+label+"\"}}", name).Output()
		if e != nil || strings.TrimSpace(string(b)) != s.work {
			s.t.Error("upgrade container ownership mismatch; refusing removal")
			return
		}
		if e = exec.CommandContext(ctx, "podman", "rm", "--force", "--volumes", name).Run(); e != nil {
			s.t.Error("owned upgrade container removal failed")
		}
		if e = exec.CommandContext(ctx, "podman", "container", "exists", name).Run(); e == nil {
			s.t.Error("owned upgrade container remains")
		}
	})
	argv := []string{"run", "--rm", "--name", name, "--label", label + "=" + s.work,
		"--platform", "linux/" + s.arch, "--network", "none", "--read-only", "--user", "10001:10001",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--env", "TMPDIR=/data/temp", "--env", "TMP=/data/temp", "--env", "TEMP=/data/temp",
		"--mount", "type=bind,source=" + s.socket + ",destination=/pg",
		"--mount", "type=bind,source=" + s.data + ",destination=/app/data",
		"--mount", "type=bind,source=" + filepath.Join(s.work, "temp") + ",destination=/data/temp",
		"--entrypoint", binary, s.image}
	out, err := exec.CommandContext(s.ctx, "podman", append(argv, args...)...).CombinedOutput()
	for _, secret := range s.secrets {
		if len(secret) > 0 && bytes.Contains(out, secret) {
			s.t.Fatal("container tool leaked private fixture material")
		}
	}
	if success != (err == nil) {
		s.t.Fatalf("image %s success=%v expected=%v (private output suppressed)", filepath.Base(binary), err == nil, success)
	}
	if !success && (!bytes.Contains(out, []byte("existing schema requires authenticated")) || bytes.Contains(out, []byte("migration completed"))) {
		s.t.Fatal("image CLI failed without the expected recovery refusal")
	}
	return string(out)
}

func (s *containerUpgradeSmoke) migrate(success bool, recovery ...string) string {
	s.t.Helper()
	args := []string{"--data-dir", "/app/data", "--connection-file", "/app/data/connection", "--migrations", "/app/migrations"}
	return s.run(success, "/app/pulsemigrate", append(args, recovery...)...)
}

func (s *containerUpgradeSmoke) version(want int64) {
	s.t.Helper()
	var got int64
	if err := s.db.QueryRow(s.ctx, "SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1").Scan(&got); err != nil || got != want {
		s.t.Fatalf("schema version=%d expected=%d", got, want)
	}
}

// Snapshot the original catalog identities/definitions and migration history.
// Refusal checks also compare the exact credential bytes and private key below.
func (s *containerUpgradeSmoke) catalog() string {
	s.t.Helper()
	var snapshot string
	err := s.db.QueryRow(s.ctx, `SELECT jsonb_build_object(
  'objects', (SELECT jsonb_agg(x ORDER BY x.oid) FROM (SELECT c.oid,c.relname,c.relkind,c.relfilenode FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public') x),
  'columns', (SELECT jsonb_agg(x ORDER BY x.attrelid,x.attnum) FROM (SELECT a.attrelid,a.attnum,a.attname,a.atttypid,a.attnotnull,a.attgenerated FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public') x),
  'constraints', (SELECT jsonb_agg(x ORDER BY x.oid) FROM (SELECT c.oid,pg_get_constraintdef(c.oid) AS definition FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='public') x),
  'versions', (SELECT jsonb_agg(v ORDER BY v.id) FROM goose_db_version v)
)::text`).Scan(&snapshot)
	if err != nil {
		s.t.Fatal("catalog snapshot failed")
	}
	return snapshot
}

func (s *containerUpgradeSmoke) verifyUpgrade() {
	s.t.Helper()
	old := filepath.Join(s.work, "schema82")
	if err := os.Mkdir(old, 0700); err != nil {
		s.t.Fatal("old schema directory creation failed")
	}
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		s.t.Fatal("source migrations unavailable")
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") && entry.Name() < "00083" {
			b, e := os.ReadFile(filepath.Join("../../migrations", entry.Name()))
			if e != nil {
				s.t.Fatal("old migration fixture read failed")
			}
			if e = os.WriteFile(filepath.Join(old, entry.Name()), b, 0600); e != nil {
				s.t.Fatal("old migration fixture write failed")
			}
		}
	}
	if v, e := postgres.Migrate(s.ctx, s.dsn, old); e != nil || v != 82 {
		s.t.Fatal("schema82 seed failed")
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		s.t.Fatal("fixture key generation failed")
	}
	credential := []byte("synthetic-upgrade-" + uuid.NewString())
	encrypted, err := cryptoplatform.Encrypt(key, credential)
	if err != nil {
		s.t.Fatal("fixture encryption failed")
	}
	indexerID := uuid.New()
	if _, err = s.db.Exec(s.ctx, "INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'image upgrade fixture','https://example.invalid',$2)", indexerID, encrypted); err != nil {
		s.t.Fatal("encrypted credential seed failed")
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		s.t.Fatal("recovery identity generation failed")
	}
	identityBytes := []byte(identity.String() + "\n")
	s.secrets = [][]byte{key, credential, []byte(identity.String())}
	s.put("secret.key", key)
	s.put("identity", identityBytes)
	preserved := func() {
		s.t.Helper()
		for name, want := range map[string][]byte{"secret.key": key, "identity": identityBytes} {
			path := filepath.Join(s.data, name)
			st, e := os.Lstat(path)
			if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
				s.t.Fatal("private key or identity permissions changed")
			}
			got, e := os.ReadFile(path)
			if e != nil || !bytes.Equal(got, want) {
				s.t.Fatal("original key or recovery identity changed")
			}
		}
		var stored []byte
		if e := s.db.QueryRow(s.ctx, "SELECT api_key_encrypted FROM indexer WHERE id=$1", indexerID).Scan(&stored); e != nil || !bytes.Equal(stored, encrypted) {
			s.t.Fatal("original credential identity or ciphertext changed")
		}
		plain, e := cryptoplatform.Decrypt(key, stored)
		if e != nil || !bytes.Equal(plain, credential) {
			s.t.Fatal("preserved credential no longer decrypts")
		}
	}
	export := func(prefix string) []string {
		s.t.Helper()
		archive, keyArchive := "/app/data/"+prefix+".age", "/app/data/"+prefix+"-key.age"
		s.run(true, "/app/pulsebackup", "export", "--data-dir", "/app/data", "--connection-file", "/app/data/connection", "--archive", archive, "--key-archive", keyArchive, "--recipient", identity.Recipient().String())
		for _, name := range []string{prefix + ".age", prefix + "-key.age"} {
			st, e := os.Lstat(filepath.Join(s.data, name))
			if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() == 0 {
				s.t.Fatal("image recovery export absent or not private")
			}
		}
		return []string{"--archive", archive, "--key-archive", keyArchive, "--identity-file", "/app/data/identity"}
	}
	recovery82 := export("schema82")
	before := s.catalog()
	refusedUnchanged := func(args ...string) {
		s.t.Helper()
		s.migrate(false, args...)
		s.version(82)
		if s.catalog() != before {
			s.t.Fatal("refused upgrade changed original catalog or migration history")
		}
		preserved()
	}
	refusedUnchanged()
	archive, err := os.ReadFile(filepath.Join(s.data, "schema82.age"))
	if err != nil || len(archive) < 2 {
		s.t.Fatal("image export unreadable")
	}
	s.put("schema82.age", archive[:len(archive)-1])
	refusedUnchanged(recovery82...)
	s.put("schema82.age", archive)
	if !strings.Contains(s.migrate(true, recovery82...), fmt.Sprintf("schema 82 -> %d", postgres.CurrentMigrationVersion)) {
		s.t.Fatal("image CLI did not report schema82 upgrade")
	}
	s.version(postgres.CurrentMigrationVersion)
	preserved()

	// Activate a real persistent fence with its required offline directory lock.
	pool, err := postgres.NewPool(s.ctx, s.dsn, postgres.DefaultPoolConfig())
	if err != nil {
		s.t.Fatal("isolation fixture pool failed")
	}
	defer pool.Close()
	lock, err := lockfile.Acquire(s.data)
	if err != nil {
		s.t.Fatal("offline isolation fixture lock failed")
	}
	restoreID := uuid.New()
	err = postgres.ActivateRestoreIsolation(s.ctx, pool, restoreID)
	releaseErr := lock.Release()
	if err != nil || releaseErr != nil {
		s.t.Fatal("persistent isolation activation failed")
	}
	var isolationBefore string
	isolationSnapshot := func() string {
		s.t.Helper()
		var b string
		if e := s.db.QueryRow(s.ctx, "SELECT jsonb_build_object('state',(SELECT to_jsonb(r) FROM restore_isolation r),'audit',(SELECT jsonb_agg(a ORDER BY a.id) FROM restore_isolation_audit a))::text").Scan(&b); e != nil {
			s.t.Fatal("restore isolation history read failed")
		}
		return b
	}
	isolationBefore = isolationSnapshot()
	// The old schema82 recovery cannot authorize a repeat call on the current schema.
	s.migrate(false, recovery82...)
	recoveryCurrent := export("isolated-current")
	if !strings.Contains(s.migrate(true, recoveryCurrent...), fmt.Sprintf("schema %d -> %d", postgres.CurrentMigrationVersion, postgres.CurrentMigrationVersion)) {
		s.t.Fatal("authenticated current-schema image CLI invocation failed")
	}
	s.version(postgres.CurrentMigrationVersion)
	preserved()
	var isolated bool
	var gotID uuid.UUID
	var audits int
	if err = s.db.QueryRow(s.ctx, "SELECT isolated,restore_id,(SELECT count(*) FROM restore_isolation_audit) FROM restore_isolation").Scan(&isolated, &gotID, &audits); err != nil || !isolated || gotID != restoreID || audits != 1 || isolationSnapshot() != isolationBefore {
		s.t.Fatal("image CLI changed persistent restore isolation or immutable history")
	}
}

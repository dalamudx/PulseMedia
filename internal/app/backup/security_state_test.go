package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	cryptoplatform "github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestBackupRestoreSecurityState(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		version int64
	}{
		{"schema126", 126},
		{"schema129", 129},
		{"totp-pending", postgres.CurrentMigrationVersion},
		{"totp-enabled-recovery", postgres.CurrentMigrationVersion},
		{"seerr", postgres.CurrentMigrationVersion},
		{"subtitle", postgres.CurrentMigrationVersion},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			dsn := pgtest.GetCluster(t).NewTestDB(t)
			migrations, err := filepath.Abs("../../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			initialMigrations := migrations
			if fixture.version != postgres.CurrentMigrationVersion {
				initialMigrations = securityBackupMigrations(t, migrations, fixture.version)
			}
			if version, err := postgres.Migrate(ctx, dsn, initialMigrations); err != nil || version != fixture.version {
				t.Fatalf("initial schema %d: %d %v", fixture.version, version, err)
			}
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close(context.Background()) }()
			key := bytes.Repeat([]byte{42}, 32)
			wrongKey := bytes.Repeat([]byte{99}, 32)
			var cipher []byte
			var cipherQuery string
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := conn.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			if !strings.HasPrefix(fixture.name, "schema") {
				user := uuid.New()
				exec(`INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'backup-security','fixture','admin')`, user)
				switch fixture.name {
				case "totp-pending", "totp-enabled-recovery":
					cipher, err = cryptoplatform.NewSettingsCipher(key).Encrypt(bytes.Repeat([]byte{7}, 20))
					if err != nil {
						t.Fatal(err)
					}
					enabled := fixture.name == "totp-enabled-recovery"
					exec(`INSERT INTO account_totp(user_id,enabled,secret_ciphertext,last_counter,challenge_id,expires_at) VALUES($1,$2,$3,123,$4,'2030-01-01T00:00:00Z')`, user, enabled, cipher, uuid.New())
					if enabled {
						secrets := cryptoplatform.AuthenticationSecrets{}
						exec(`INSERT INTO account_totp_recovery(user_id,code_hash,used_at) VALUES($1,$2,NULL),($1,$3,'2026-01-01T00:00:00Z')`, user, secrets.HashToken("pulse.mfa.recovery.v1:UNUSED"), secrets.HashToken("pulse.mfa.recovery.v1:USED"))
					}
					cipherQuery = `SELECT secret_ciphertext FROM account_totp`
				case "seerr":
					cipher, err = cryptoplatform.NewSecretProtector(key).EncryptSecret([]byte("fixture-seerr-credential"))
					if err != nil {
						t.Fatal(err)
					}
					exec(`INSERT INTO seerr_configuration(source_id,enabled,version,owner_id,configured_by,credential_encrypted) VALUES($1,true,1,$2,$2,$3)`, uuid.New(), user, cipher)
					cipherQuery = `SELECT credential_encrypted FROM seerr_configuration`
				case "subtitle":
					cipher, err = cryptoplatform.NewSecretProtector(key).EncryptSecret([]byte("1\n00:00:00,000 --> 00:00:01,000\nfixture subtitle\n"))
					if err != nil {
						t.Fatal(err)
					}
					root, file, query := uuid.New(), uuid.New(), uuid.New()
					exec(`INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/synthetic/backup-security','movie','Backup')`, root)
					exec(`INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'fixture.mkv',4096,1)`, file, root)
					exec(`INSERT INTO job(id,kind,state) VALUES($1,'subtitle_candidate','succeeded')`, query)
					exec(`INSERT INTO subtitle_candidate_query(id,owner_id,file_id,mapping_revision,command_key,outcome,snapshot) VALUES($1,$2,$3,1,'backup-security','candidates','{}')`, query, user, file)
					exec(`INSERT INTO subtitle_candidate(id,query_id,ordinal,content_encrypted) VALUES($1,$2,0,$3)`, uuid.New(), query, cipher)
					cipherQuery = `SELECT content_encrypted FROM subtitle_candidate`
				}
			}
			var oldSecrets int
			if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM indexer)+(SELECT count(*) FROM release_candidate)+(SELECT count(*) FROM download_client)+(SELECT count(*) FROM media_server)+(SELECT count(*) FROM notification_channel)+(SELECT count(*) FROM setting WHERE key='tmdb.api_key')`).Scan(&oldSecrets); err != nil || oldSecrets != 0 {
				t.Fatalf("old secret inventory must be empty: %d %v", oldSecrets, err)
			}
			before := securityBackupState(t, ctx, conn)
			schemaBefore := securityBackupSchema(t, ctx, conn)
			if err := validateKey(ctx, conn, key); err != nil {
				t.Fatal(err)
			}
			if cipherQuery != "" {
				if err := validateKey(ctx, conn, wrongKey); err == nil {
					t.Fatal("wrong key accepted with only new ciphertext")
				}
			}
			dir := t.TempDir()
			put := func(name string, data []byte) string {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "dest")
			for _, path := range []string{source, dest} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			keyPath := filepath.Join(source, "secret.key")
			if err := os.WriteFile(keyPath, key, 0600); err != nil {
				t.Fatal(err)
			}
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			o := Options{DataDir: source, ConnectionFile: put("source.conn", []byte(dsn)), Archive: filepath.Join(dir, "dump.age"), KeyArchive: filepath.Join(dir, "key.age"), Recipient: identity.Recipient().String(), IdentityFile: put("identity", []byte(identity.String())), Target: "pulse_restore_" + strings.ReplaceAll(uuid.NewString(), "-", ""), Migrations: migrations}
			if cipherQuery != "" {
				if err := os.WriteFile(keyPath, wrongKey, 0600); err != nil {
					t.Fatal(err)
				}
				if err := Export(ctx, o); err == nil {
					t.Fatal("export accepted wrong source key")
				}
				for _, path := range []string{o.Archive, o.KeyArchive} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("failed export published an archive", err)
					}
				}
				if err := os.WriteFile(keyPath, key, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Export(ctx, o); err != nil {
				t.Fatal(err)
			}
			o.DataDir = dest
			if cipherQuery != "" {
				// Bind both authenticated manifests to the wrong key so only stored-data validation can reject it.
				a, err := unseal(o.Archive, o.IdentityFile)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.Remove(a.Spool); err != nil {
						t.Error(err)
					}
				}()
				k, err := unseal(o.KeyArchive, o.IdentityFile)
				if err != nil {
					t.Fatal(err)
				}
				a.KeyHash, k.KeyHash, k.Key = digest(wrongKey), digest(wrongKey), wrongKey
				bad := o
				for _, part := range []struct {
					name   string
					value  envelope
					target *string
				}{
					{"wrong-dump.age", a, &bad.Archive}, {"wrong-key.age", k, &bad.KeyArchive},
				} {
					sealed, err := seal(part.value, o.Recipient)
					if err != nil {
						t.Fatal(err)
					}
					*part.target = put(part.name, sealed)
				}
				if err := Restore(ctx, bad); err == nil {
					t.Fatal("restore accepted wrong key with consistent archive bindings")
				}
				var exists bool
				if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, o.Target).Scan(&exists); err != nil || exists {
					t.Fatalf("failed restore database remains: %t %v", exists, err)
				}
				if _, err := os.Stat(filepath.Join(dest, "secret.key")); !os.IsNotExist(err) {
					t.Fatal("failed restore published key", err)
				}
			}
			defer func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := conn.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{o.Target}.Sanitize()+" WITH (FORCE)"); err != nil {
					t.Error(err)
				}
			}()
			if err := Restore(ctx, o); err != nil {
				t.Fatal(err)
			}
			cfg, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			targetDSN := dsn
			if strings.HasPrefix(targetDSN, "postgres") {
				end := strings.Index(targetDSN, "?")
				if end < 0 {
					end = len(targetDSN)
				}
				slash := strings.LastIndex(targetDSN[:end], "/")
				targetDSN = targetDSN[:slash+1] + o.Target + targetDSN[end:]
			} else {
				targetDSN += " dbname=" + o.Target
			}
			cfg.Database = o.Target
			restored, err := pgx.ConnectConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = restored.Close(context.Background()) }()
			if got := securityBackupState(t, ctx, restored); got != before {
				t.Fatal("restored security state changed, including recovery-code consumption")
			}
			if got := securityBackupState(t, ctx, conn); got != before {
				t.Fatal("backup or restore mutated source security state")
			}
			if got := securityBackupSchema(t, ctx, conn); got != schemaBefore {
				t.Fatal("backup or validation mutated source schema")
			}
			if got := securityBackupSchema(t, ctx, restored); fixture.version == postgres.CurrentMigrationVersion && got != schemaBefore {
				i := 0
				for i < len(got) && i < len(schemaBefore) && got[i] == schemaBefore[i] {
					i++
				}
				t.Fatalf("restore changed schema near byte %d: before %q after %q", i, schemaBefore[i:min(i+240, len(schemaBefore))], got[i:min(i+240, len(got))])
			}
			gotKey, err := os.ReadFile(filepath.Join(dest, "secret.key"))
			if err != nil || !bytes.Equal(gotKey, key) {
				t.Fatal("original master key not preserved", err)
			}
			if cipherQuery != "" {
				var got []byte
				if err := restored.QueryRow(ctx, cipherQuery).Scan(&got); err != nil || !bytes.Equal(got, cipher) {
					t.Fatal("ciphertext not preserved", err)
				}
				if _, err := cryptoplatform.Decrypt(gotKey, got); err != nil {
					t.Fatal(err)
				}
			}
			status, err := Inspect(ctx, Options{DataDir: dest, ConnectionFile: put("target.conn", []byte(targetDSN))})
			if err != nil || !status.Isolated || status.RestoreID == nil {
				t.Fatal("restored database not isolated", err)
			}
			var version int64
			if err := restored.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != postgres.CurrentMigrationVersion {
				t.Fatalf("restored schema: %d %v", version, err)
			}
		})
	}
}

func securityBackupMigrations(t *testing.T, source string, version int64) string {
	t.Helper()
	dir := t.TempDir()
	files, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		var n int64
		if _, err := fmt.Sscanf(file.Name(), "%d_", &n); err != nil || n > version || !strings.HasSuffix(file.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func securityBackupState(t *testing.T, ctx context.Context, conn *pgx.Conn) string {
	t.Helper()
	var state strings.Builder
	for _, table := range []string{"account_security_command", "account_totp", "account_totp_recovery", "seerr_configuration", "seerr_ingress_receipt", "subtitle_candidate_query", "subtitle_candidate"} {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			continue
		}
		var value string
		if err := conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		// Empty tables introduced by historical upgrades add no security state.
		if value != "[]" {
			state.WriteString(table + ":" + value + "\n")
		}
	}
	return state.String()
}

func securityBackupSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) string {
	t.Helper()
	var value string
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
	 'columns',(SELECT jsonb_agg(to_jsonb(c)-'table_catalog'-'udt_catalog'-'domain_catalog' ORDER BY table_name,ordinal_position) FROM information_schema.columns c WHERE table_schema='public'),
	 'constraints',(SELECT jsonb_agg(jsonb_build_array(c.conrelid::regclass::text,c.conname,pg_get_constraintdef(c.oid,true)) ORDER BY c.conrelid::regclass::text,c.conname) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='public'),
	 'triggers',(SELECT jsonb_agg(pg_get_triggerdef(t.oid) ORDER BY c.relname,t.tgname) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND NOT t.tgisinternal)
	)::text`).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

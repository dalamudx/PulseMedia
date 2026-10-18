package pgtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	clusterOnce sync.Once
	clusterInst *TestCluster
	clusterErr  error
)

// TestCluster represents an active PostgreSQL 18 cluster for tests.
type TestCluster struct {
	BaseDSN   string
	Host      string
	Port      int
	SuperUser string
	Password  string
}

// GetCluster returns the singleton test cluster, connecting only to the explicitly configured instance.
func GetCluster(t testing.TB) *TestCluster {
	t.Helper()
	clusterOnce.Do(func() {
		clusterInst, clusterErr = initCluster()
	})
	if clusterErr != nil {
		t.Fatalf("failed to initialize test postgres cluster: %v", clusterErr)
	}
	return clusterInst
}

func initCluster() (*TestCluster, error) {
	extURL := strings.TrimSpace(os.Getenv("PULSE_TEST_DATABASE_URL"))
	if extURL == "" {
		return nil, errors.New("PULSE_TEST_DATABASE_URL is required; use task test or an owned PostgreSQL fixture")
	}
	uri, err := url.Parse(extURL)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") || uri.Host == "" {
		return nil, errors.New("test database URL must be a PostgreSQL URL")
	}
	cfg, err := pgx.ParseConfig(extURL)
	if err != nil {
		return nil, errors.New("invalid test database configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", extURL)
	if err != nil {
		return nil, errors.New("invalid test database configuration")
	}
	defer func() { _ = db.Close() }()
	var version int
	if err = db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); err != nil {
		return nil, errors.New("configured test PostgreSQL is unavailable")
	}
	if version/10000 != 18 {
		return nil, errors.New("test database must use PostgreSQL 18")
	}
	return &TestCluster{Host: cfg.Host, Port: int(cfg.Port), SuperUser: cfg.User, Password: cfg.Password, BaseDSN: extURL}, nil
}

// Preserve the configured transport and credentials when selecting an owned DB.
func (c *TestCluster) databaseURL(database, user, password string) string {
	u, e := url.Parse(c.BaseDSN)
	if e != nil {
		panic("invalid test cluster URL")
	}
	u.Path = "/" + database
	u.RawPath = ""
	if user != "" {
		u.User = url.UserPassword(user, password)
		q := u.Query()
		q.Del("user")
		q.Del("password")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// NewTestDB creates an isolated database on the explicitly configured cluster and schedules its deletion.
func (c *TestCluster) NewTestDB(t testing.TB) string {
	t.Helper()
	dbName := fmt.Sprintf("test_%s", uuid.New().String()[:8])

	adminDB, err := sql.Open("pgx", c.BaseDSN)
	if err != nil {
		t.Fatalf("failed to open admin db: %v", err)
	}
	defer func() {
		_ = adminDB.Close()
	}()

	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE DATABASE "%s" OWNER postgres;`, dbName)); err != nil {
		t.Fatalf("failed to create test db %s: %v", dbName, err)
	}

	dbDSN := c.databaseURL(dbName, "", "")

	t.Cleanup(func() {
		// DROP DATABASE cannot run inside the implicit transaction created by a
		// multi-statement query. FORCE is PG18 and applies only to this owned DB.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dropAdmin, err := sql.Open("pgx", c.BaseDSN)
		if err != nil {
			t.Errorf("open test DB cleanup connection: %v", err)
			return
		}
		defer func() { _ = dropAdmin.Close() }()
		if _, err := dropAdmin.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, dbName)); err != nil {
			t.Errorf("drop owned test DB %s: %v", dbName, err)
		}
	})

	return dbDSN
}

// CreateRoles creates separate DDL and DML roles for permission testing.
func (c *TestCluster) CreateRoles(t testing.TB, dbName string) (ddlDSN, dmlDSN string) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	ddlUser := fmt.Sprintf("role_ddl_%s", suffix)
	dmlUser := fmt.Sprintf("role_dml_%s", suffix)
	password := "SecretPass123!"

	dbDSN := c.databaseURL(dbName, "", "")
	db, err := sql.Open("pgx", dbDSN)
	if err != nil {
		t.Fatalf("failed to open db for role setup: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	ctx := context.Background()

	// 1. Create DDL role: can create tables, owns public schema
	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		CREATE USER "%s" WITH PASSWORD '%s';
		GRANT ALL ON SCHEMA public TO "%s";
		ALTER SCHEMA public OWNER TO "%s";
	`, ddlUser, password, ddlUser, ddlUser))
	if err != nil {
		t.Fatalf("failed to create DDL role: %v", err)
	}

	// 2. Create DML role: only SELECT, INSERT, UPDATE, DELETE on existing and future tables
	_, err = db.ExecContext(ctx, fmt.Sprintf(`
		CREATE USER "%s" WITH PASSWORD '%s';
		GRANT CONNECT ON DATABASE "%s" TO "%s";
		GRANT USAGE ON SCHEMA public TO "%s";
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO "%s";
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "%s";
		ALTER DEFAULT PRIVILEGES FOR ROLE "%s" IN SCHEMA public
			GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "%s";
		ALTER DEFAULT PRIVILEGES FOR ROLE "%s" IN SCHEMA public
			GRANT USAGE, SELECT ON SEQUENCES TO "%s";
	`, dmlUser, password, dbName, dmlUser, dmlUser, dmlUser, dmlUser, ddlUser, dmlUser, ddlUser, dmlUser))
	if err != nil {
		t.Fatalf("failed to create DML role: %v", err)
	}

	ddlDSN = c.databaseURL(dbName, ddlUser, password)
	dmlDSN = c.databaseURL(dbName, dmlUser, password)
	return ddlDSN, dmlDSN
}

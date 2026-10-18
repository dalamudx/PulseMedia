package pgtest_test

import (
	"database/sql"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"net/url"
	"strings"
	"testing"
)

func TestNewTestDBCleansOwnedDatabaseWithLiveConnection(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	var name string
	var live *sql.DB
	t.Run("owned", func(t *testing.T) {
		dsn := cluster.NewTestDB(t)
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		name = strings.TrimPrefix(u.Path, "/")
		live, err = sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if err := live.Ping(); err != nil {
			t.Fatal(err)
		}
	})
	defer func() { _ = live.Close() }()
	admin, err := sql.Open("pgx", cluster.BaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	var exists bool
	if err := admin.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists); err != nil || exists {
		t.Fatal("cleanup silently leaked owned test DB", name, err)
	}
}

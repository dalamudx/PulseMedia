package backup

import (
	"context"
	"github.com/jackc/pgx/v5"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTLSClientTransport(t *testing.T) {
	raw := os.Getenv("PULSE_TEST_DATABASE_URL")
	u, e := url.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	if u.Query().Get("sslmode") != "verify-full" {
		t.Skip("TLS fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cases := []struct {
		name   string
		mutate func(*url.URL)
		accept bool
	}{
		{"verify-full", func(*url.URL) {}, true},
		{"wrong-hostname", func(u *url.URL) { u.Host = "127.0.0.1:" + u.Port() }, false},
		{"wrong-CA", func(u *url.URL) {
			q := u.Query()
			q.Set("sslrootcert", os.Getenv("PULSE_BACKUP_WRONG_CA"))
			u.RawQuery = q.Encode()
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := *u
			tc.mutate(&copy)
			cfg, e := pgx.ParseConfig(copy.String())
			if e != nil {
				t.Fatal(e)
			}
			db, e := pgx.ConnectConfig(ctx, cfg)
			if e == nil {
				_ = db.Close(ctx)
			}
			if (e == nil) != tc.accept {
				t.Fatalf("pgx accept=%v expected %v", e == nil, tc.accept)
			}
			if e = pg(ctx, cfg, "pg_dump", nil, io.Discard); (e == nil) != tc.accept {
				t.Fatalf("pg_dump accept=%v expected %v", e == nil, tc.accept)
			}
			target, e := targetConfig(cfg, "postgres")
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(target.ConnString(), "verify-full") {
				t.Fatal("TLS lost in target config")
			}
		})
	}
}

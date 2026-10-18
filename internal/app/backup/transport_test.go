package backup

import (
	"github.com/jackc/pgx/v5"
	"testing"
)

func TestExplicitTransportOptions(t *testing.T) {
	for _, raw := range []string{"postgres://user:example-pass@localhost:5432/source?sslmode=verify-full", "host='localhost' port=5432 user='user' password='example-pass' dbname=source sslmode=verify-full", "postgres://user:example-pass@localhost:5432,example.invalid:5433/source?sslmode=verify-full"} {
		c, e := pgx.ParseConfig(raw)
		if e != nil {
			t.Fatal(e)
		}
		o, e := connectionOptions(c)
		if e != nil {
			t.Fatal(e)
		}
		if o["sslmode"] != "verify-full" || o["password"] != "" {
			t.Fatal("transport downgrade or password exposure")
		}
		target, e := targetConfig(c, "pulse_restore_test")
		if e != nil {
			t.Fatal(e)
		}
		got, e := connectionOptions(target)
		if e != nil {
			t.Fatal(e)
		}
		if got["host"] != o["host"] || got["port"] != o["port"] || got["sslmode"] != o["sslmode"] || got["dbname"] != "pulse_restore_test" {
			t.Fatal("target transport changed")
		}
	}
}

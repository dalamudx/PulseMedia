package pgtest

import (
	"net/url"
	"testing"
)

func TestDatabaseURLPreservesTransport(t *testing.T) {
	c := &TestCluster{BaseDSN: "postgres://postgres:fixture%2Fonly@127.0.0.1:5438/postgres?sslmode=require&application_name=test"}
	for _, role := range []string{"", "role_ddl_fixture"} {
		u, e := url.Parse(c.databaseURL("test_owned", role, "role-only"))
		if e != nil || u.Host != "127.0.0.1:5438" || u.Path != "/test_owned" || u.Query().Get("sslmode") != "require" || u.Query().Get("application_name") != "test" {
			t.Fatal("transport not preserved")
		}
		if role != "" && u.User.Username() != role {
			t.Fatal("role not replaced")
		}
		if role == "" {
			p, _ := u.User.Password()
			if p != "fixture/only" {
				t.Fatal("credential encoding changed")
			}
		}
	}
}

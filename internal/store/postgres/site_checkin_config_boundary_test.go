package postgres_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func TestSiteCheckinConfigTimezonePG(t *testing.T) {
	f := newCheckinFixture(t)
	for version, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York", "Etc/GMT+8"} {
		t.Run(zone, func(t *testing.T) {
			in := f.input()
			in.Timezone = zone
			in.ExpectedVersion = int64(version)
			f.save(t, in, fmt.Sprintf("zone-%d", version))
		})
	}
	version := int64(4)
	t.Run("PRC", func(t *testing.T) {
		var known bool
		if err := f.p.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_timezone_names WHERE name='PRC')`).Scan(&known); err != nil {
			t.Fatal(err)
		}
		in := f.input()
		in.Timezone, in.ExpectedVersion = "PRC", version
		r, err := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, in, "registry-prc")
		if !known {
			checkinErr(t, err, ports.ErrRejected)
			return
		}
		if err != nil || r == nil || r.Config.Timezone != "PRC" {
			t.Fatal("registered name changed", r, err)
		}
		version++
	})
	for _, zone := range []string{"Local", "Factory", "posixrules", "posix/UTC", "right/UTC", "Madeup99/Zone400"} {
		t.Run(zone, func(t *testing.T) {
			in := f.input()
			in.Timezone = zone
			in.ExpectedVersion = version
			_, err := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, in, "invalid")
			checkinErr(t, err, ports.ErrRejected)
		})
	}
	for _, daily := range []string{"24:00", "1:00", "23:60", "12:00:00", " 01:00"} {
		in := f.input()
		in.DailyAt = daily
		_, err := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, in, "badtime")
		checkinErr(t, err, ports.ErrRejected)
	}
}

func TestSiteCheckinConfigCredentialsPG(t *testing.T) {
	for _, mode := range []string{"nil", "wrong", "cookie", "disabled", "stale"} {
		t.Run(mode, func(t *testing.T) {
			f := newCheckinFixture(t)
			switch mode {
			case "nil":
				f.s = postgres.NewSiteCheckinConfigStore(f.p, idgen.New(), nil)
			case "wrong":
				f.s = postgres.NewSiteCheckinConfigStore(f.p, idgen.New(), &checkinSecrets{key: bytes.Repeat([]byte{42}, 32)})
			case "cookie":
				f.exec(t, `UPDATE indexer SET api_key_encrypted='broken' WHERE id=$1`, f.id)
			case "disabled":
				f.exec(t, `INSERT INTO indexer_lifecycle(indexer_id,enabled,deleted_at) VALUES($1,false,clock_timestamp())`, f.id)
			case "stale":
				f.exec(t, `UPDATE indexer SET config_version=2 WHERE id=$1`, f.id)
			}
			_, err := f.s.SaveSiteCheckinConfig(t.Context(), f.a, f.id, f.input(), "failure")
			want := ports.ErrUnavailable
			switch mode {
			case "disabled":
				want = ports.ErrNotFound
			case "stale":
				want = ports.ErrEndpointVersionConflict
			}
			checkinErr(t, err, want)
			f.counts(t, 0)
		})
	}
}

package postgres_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestUntrackedDiscoveryDueInvalidScopesCannotStarveValidPG(t *testing.T) {
	f := newUntrackedDiscoveryFixture(t)
	for i := 1; i <= 65; i++ {
		f.scope = uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
		f.save(t, f.in, "invalid-before-valid")
	}
	f.exec(t, `UPDATE download_client SET remote_path_map='{}' WHERE id=$1`, f.client)
	f.client = uuid.New()
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'valid-later','qbittorrent','https://scope.invalid','fixture','fixture','{"/remote":"/scope-source"}')`, f.client)
	f.in.ClientID = f.client
	if err := f.p.QueryRow(t.Context(), `SELECT updated_at FROM download_client WHERE id=$1`, f.client).Scan(&f.in.ExpectedClientVersion); err != nil {
		t.Fatal(err)
	}
	f.scope = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	f.save(t, f.in, "valid-after-invalid")
	if n, err := f.discovery.QueueDueUntrackedDiscoveries(t.Context(), 64); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if n, err := f.discovery.QueueDueUntrackedDiscoveries(t.Context(), 64); err != nil || n != 1 {
		t.Fatal("later valid scope starved", n, err)
	}
	f.count(t, `SELECT count(*) FROM job WHERE kind='untracked_discovery'`, 1)
	f.count(t, `SELECT count(*) FROM untracked_discovery_state WHERE scope_id=$1 AND requested_sequence=1 AND active_job_id IS NOT NULL`, 1, f.scope)
}

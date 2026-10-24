package download

import (
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestDownloadWatchManifestPaginationPG(t *testing.T) {
	f := newWatchFixture(t)
	ratePublisherExec(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map)
 SELECT gen_random_uuid(),'watch-page-'||n,c.client_type,c.url,c.username,c.password_encrypted,c.remote_path_map
 FROM download_client c CROSS JOIN generate_series(1,65) n WHERE c.id=$1`, f.task.ClientID)
	ratePublisherExec(t, f.pool, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,content_path,progress,total_bytes,downloaded_bytes)
 SELECT gen_random_uuid(),gen_random_uuid(),c.id,$1,$2,'tracking','/remote','/remote/item.mkv',0.1,100,10
 FROM download_client c WHERE c.name LIKE 'watch-page-%'`, f.task.CandidateID, f.task.InfoHash)
	ratePublisherExec(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence)
 SELECT gen_random_uuid(),t.operation_id,d.unit_id,d.root_id,d.actor_type,d.actor_id,d.submission_fence
 FROM download_task t JOIN download_client c ON c.id=t.client_id CROSS JOIN download_requirement d
 WHERE c.name LIKE 'watch-page-%' AND d.operation_id=$1`, f.task.OperationID)
	seen := make(map[string]bool)
	clients := make(map[uuid.UUID]bool)
	cursor := ""
	for n := 0; n < 150; n++ {
		page, e := f.sources.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Cursor: cursor, Limit: 1})
		if e != nil || len(page.Issues) != 0 || len(page.Sources) != 1 {
			t.Fatal("source paging failed", e)
		}
		binding := page.Sources[0].Binding
		if seen[binding.Stamp] {
			t.Fatal("source repeated across cursor")
		}
		seen[binding.Stamp] = true
		clients[binding.ClientID] = true
		if page.NextCursor == "" {
			if !page.Complete {
				t.Fatal("last source page incomplete")
			}
			break
		}
		if page.NextCursor == cursor || page.Complete {
			t.Fatal("source cursor failed to advance")
		}
		cursor = page.NextCursor
	}
	if len(clients) != 66 || len(seen) != 132 {
		t.Fatal("source tail starved", len(clients), len(seen))
	}
}

func TestDownloadWatchTargetPaginationPG(t *testing.T) {
	f := newWatchFixture(t)
	ratePublisherExec(t, f.pool, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,content_path,progress,total_bytes,downloaded_bytes)
 SELECT gen_random_uuid(),gen_random_uuid(),$1,$2,md5(n::text)||'12345678','tracking','/remote','/remote/item.mkv',0.1,100,10
 FROM generate_series(1,66) n`, f.task.ClientID, f.task.CandidateID)
	ratePublisherExec(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence)
 SELECT gen_random_uuid(),t.operation_id,d.unit_id,d.root_id,d.actor_type,d.actor_id,d.submission_fence
 FROM download_task t CROSS JOIN download_requirement d
 WHERE t.client_id=$1 AND t.operation_id<>$2 AND d.operation_id=$2`, f.task.ClientID, f.task.OperationID)
	seen := make(map[uuid.UUID]bool)
	cursor := ""
	for n := 0; n < 20; n++ {
		page, e := f.sources.ResolveDownloadWatchTargets(t.Context(), f.target.Binding, ports.RootChangeHint{RelativePath: ".", Kind: ports.RootChangeRescan}, ports.DownloadWatchTargetQuery{Cursor: cursor, Limit: 5})
		if e != nil || len(page.Targets) == 0 || len(page.Targets) > 5 {
			t.Fatal("target page failed", e)
		}
		for _, target := range page.Targets {
			if seen[target.TaskID] {
				t.Fatal("target repeated")
			}
			seen[target.TaskID] = true
		}
		if page.NextCursor == "" {
			if !page.Complete {
				t.Fatal("last target page incomplete")
			}
			break
		}
		if cursor == page.NextCursor {
			t.Fatal("target cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 67 {
		t.Fatal("target tail starved", len(seen))
	}
	_, e := f.sources.ResolveDownloadWatchTargets(t.Context(), f.target.Binding, ports.RootChangeHint{RelativePath: "other.mkv", Kind: ports.RootChangeRescan}, ports.DownloadWatchTargetQuery{Cursor: cursor, Limit: 5})
	if !errors.Is(e, ports.ErrRejected) {
		t.Fatal("cursor crossed hint scope", e)
	}
}

func TestDownloadWatchHealthRefreshKeepsBindingPG(t *testing.T) {
	f := newWatchFixture(t)
	ratePublisherExec(t, f.pool, `UPDATE library_root SET health_revision=health_revision+1,updated_at=clock_timestamp() WHERE id=$1`, f.root)
	current := f.readTarget(t)
	if current != f.target {
		t.Fatal("routine health refresh invalidated source binding")
	}
}

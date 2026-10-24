package postgres_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func TestAutomaticTVWatchSourcePagingPG(t *testing.T) {
	f := automaticTVSubmissionBase(t, false)
	mapping := make(map[string]string)
	for i := 0; i < 64; i++ {
		mapping[fmt.Sprintf("/remote/%03d", i)] = filepath.Join(f.path, "tv", fmt.Sprintf("%03d", i))
	}
	raw, _ := json.Marshal(mapping)
	execIntent(t, f.pool, `UPDATE download_client SET remote_path_map=$2 WHERE id=$1`, f.client, raw)
	proof := automaticTVSubmissionMetainfo(t, "Trusted.Native.Series.2024.S02E03.mkv")
	r := f.start(t)
	candidate := tvExecutionCandidate(t, f, "paging-submission", "1080p", 5)
	hash := proof.InfoHash()
	candidate.InfoHash = &hash
	tvExecutionPage(t, f, r, candidate)
	finalizer := tvExecutionLease(t, f, r)
	decision, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, finalizer)
	if e != nil || decision.State != "reserved" {
		t.Fatal(decision, e)
	}
	job, e := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "original-sender", Column3: 60000})
	if e != nil {
		t.Fatal(e)
	}
	req := ports.AutomaticTVSendRequest{OperationID: decision.OperationID, Lease: ports.JobLease{ID: job.ID, Owner: "original-sender", Token: job.LeaseToken}}
	store := postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32)))
	submission := automaticTVSubmissionFixture{automaticTVFixture: f, store: store, request: req, command: ports.PrepareAutomaticTVSubmissionCommand{Request: req, TaskID: uuid.New(), Metainfo: proof}}
	observation := automaticTVObservationFromSubmission(t, submission, true)
	watch := automaticTVWatchFromObservation(t, observation)
	before := watch.snapshot(t)
	page, e := watch.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if e != nil || len(page.Sources) != 64 || page.NextCursor == "" || page.Complete {
		t.Fatal("first page", page, e)
	}
	next, e := watch.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Cursor: page.NextCursor})
	if e != nil || len(next.Sources) != 1 || next.NextCursor != "" || !next.Complete {
		t.Fatal("tail", next, e)
	}
	seen := make(map[string]bool)
	for _, source := range append(page.Sources, next.Sources...) {
		if seen[source.Binding.Stamp] {
			t.Fatal("duplicate source")
		}
		seen[source.Binding.Stamp] = true
	}
	if before != watch.snapshot(t) {
		t.Fatal("paging wrote state")
	}
}

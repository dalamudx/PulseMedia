package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectionContractBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	f := setupEpisodeMappingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	q, pool := f.a.Queries(), f.a.DBPool()
	var root uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT root_id FROM subscription WHERE id=$1", f.sub.Id).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1", root); err != nil {
		t.Fatal(err)
	}
	var wanted []uuid.UUID
	for i, unit := range f.units {
		file := uuid.New()
		if _, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: file, RootID: root, RelPath: []string{"S01E01.mkv", "S01E02.mkv"}[i], SizeBytes: 100, MtimeNs: 1}); err != nil {
			t.Fatal(err)
		}
		if err := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: unit}); err != nil {
			t.Fatal(err)
		}
		profile := quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray}
		if i == 0 {
			profile.SourceParserVersion = quality.SourceTokenParserVersion
		}
		raw, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, RootID: pgtype.UUID{Bytes: root, Valid: true}, QualityProfile: raw}); err != nil {
			t.Fatal(err)
		}
		var id uuid.UUID
		if err = pool.QueryRow(ctx, "SELECT id FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2", f.sub.Id, unit).Scan(&id); err != nil {
			t.Fatal(err)
		}
		wanted = append(wanted, id)
	}
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	for i, id := range wanted {
		report, err := evaluator.EvaluateWantedItem(ctx, id, quality.DefaultPolicy())
		if err != nil || report.IsSatisfied != (i == 0) {
			t.Fatal("real fixture ownership", report, err)
		}
	}
	// Old unversioned source cannot gain green authority even if an old writer says so.
	if _, err := pool.Exec(ctx, "UPDATE wanted_item SET is_satisfied=true,current_fact_stage='imported',next_evaluate_at=clock_timestamp()+interval '1 year' WHERE id=$1", wanted[1]); err != nil {
		t.Fatal(err)
	}
	const countsSQL = "SELECT (SELECT count(*) FROM library_file),(SELECT count(*) FROM unit_fact),(SELECT count(*) FROM job),(SELECT count(*) FROM release_candidate),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM acquisition_reservation)"
	var before [6]int
	if err := pool.QueryRow(ctx, countsSQL).Scan(&before[0], &before[1], &before[2], &before[3], &before[4], &before[5]); err != nil {
		t.Fatal(err)
	}
	if before[0] != 2 || before[1] != 2 || before[2] != 0 || before[4] != 0 || before[5] != 0 {
		t.Fatal("unexpected browser setup", before)
	}
	probe := "/fixture-" + uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probe && r.Method == http.MethodPost {
			var action struct {
				Action string `json:"action"`
			}
			if json.NewDecoder(r.Body).Decode(&action) != nil {
				w.WriteHeader(400)
				return
			}
			var err error
			switch action.Action {
			case "legacy-write":
				_, err = pool.Exec(r.Context(), "UPDATE wanted_item SET is_satisfied=is_satisfied,next_evaluate_at=clock_timestamp()+interval '1 year' WHERE id=$1", wanted[0])
			case "recompute":
				_, err = evaluator.EvaluateWantedItem(r.Context(), wanted[0], quality.DefaultPolicy())
			default:
				w.WriteHeader(400)
				return
			}
			if err != nil {
				t.Error(err)
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(204)
			return
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	script, err := filepath.Abs("../../web/tests/projection-contract-browser.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_CONTRACT_UI_BASE="+server.URL, "PULSE_CONTRACT_UI_SUB="+f.sub.Id.String(), "PULSE_CONTRACT_UI_PROBE="+probe)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("projection browser failed: %v %s", err, raw)
	}
	t.Log(string(raw))
	var after [6]int
	if err = pool.QueryRow(ctx, countsSQL).Scan(&after[0], &after[1], &after[2], &after[3], &after[4], &after[5]); err != nil || after != before {
		t.Fatal("browser cache invalidation changed business state", before, after, err)
	}
	var persisted string
	if err = pool.QueryRow(ctx, "SELECT COALESCE(quality_profile->>'source_parser_version','') FROM unit_fact WHERE unit_id=$1", f.units[1]).Scan(&persisted); err != nil || persisted != "" {
		t.Fatal("legacy raw source was re-signed", persisted, err)
	}
}

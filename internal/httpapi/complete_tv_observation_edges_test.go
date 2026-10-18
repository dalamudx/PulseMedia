package httpapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/google/uuid"
)

func TestCompleteTVObservationExtraReleasedProtection(t *testing.T) {
	for _, kind := range []string{"lane", "reservation"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			if kind == "lane" {
				f.exec(t, `INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp,released_at) SELECT $1,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp,clock_timestamp() FROM acquisition_reservation LIMIT 1`, uuid.New())
			} else {
				f.exec(t, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state) SELECT $1,unit_id,root_id,purpose,candidate_id,operation_id,'released' FROM unit_reservation LIMIT 1`, uuid.New())
			}
			before := f.snapshot(t)
			s, e := f.observer.LoadCompleteTVObservation(context.Background(), f.request)
			if s != nil || !errors.Is(e, ports.ErrCompleteTVObservationStale) || f.snapshot(t) != before {
				t.Fatalf("extra historical protection accepted: snapshot=%v error=%v", s, e)
			}
		})
	}
}

func TestCompleteTVObservationTaskTailRollback(t *testing.T) {
	for _, stage := range []string{"update", "audit", "finish"} {
		t.Run(stage, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			switch stage {
			case "update":
				f.exec(t, `CREATE FUNCTION observation_task_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='tracking' THEN NEW.state='unknown'; END IF; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER observation_task_tail BEFORE UPDATE ON download_task FOR EACH ROW EXECUTE FUNCTION observation_task_tail()`)
			case "audit":
				f.exec(t, `CREATE FUNCTION observation_task_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE download_task SET state='unknown'; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER observation_task_tail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION observation_task_tail()`)
			case "finish":
				f.exec(t, `CREATE FUNCTION observation_task_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='complete_tv_coverage_observation' AND NEW.state='succeeded' THEN UPDATE download_task SET state='unknown'; END IF; RETURN NEW; END $$`)
				f.exec(t, `CREATE TRIGGER observation_task_tail BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION observation_task_tail()`)
			}
			f.rejectObservation(t, s, matchingObservation(s), ports.ErrCompleteTVObservationStale)
		})
	}
}

func TestCompleteTVObservationLegacyAddIsolation(t *testing.T) {
	f := newPrepareTVFixture(t)
	var clientID uuid.UUID
	if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT client_id FROM complete_tv_execution_authority WHERE operation_id=$1`, f.c.OperationID).Scan(&clientID); e != nil {
		t.Fatal(e)
	}
	m := download.NewManager(f.f.a.DBPool(), idgen.New(), nil)
	client := &completeTVLegacyObserver{}
	m.RegisterClient(clientID, client)
	before := f.work(t)
	task, e := m.SubmitDownload(context.Background(), f.c.OperationID, clientID, f.candidate, "/downloads")
	if task != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || f.work(t) != before {
		t.Fatalf("generic Add accepted or changed authority: task=%v error=%v", task, e)
	}
}

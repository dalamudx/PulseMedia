package mediasidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// Inventory, physical identities and imported events are SQL PREPARED evidence;
// these tests perform no filesystem publication or provider lookup.
func subtitleFixture(t *testing.T, kind string, sizeBytes ...int64) (fixture, *SubtitleRepository, ports.MediaSidecarSnapshot) {
	t.Helper()
	f := newFixture(t, kind)
	var version int
	if err := f.pool.QueryRow(t.Context(), `SELECT current_setting('server_version_num')::integer`).Scan(&version); err != nil || version != 180006 {
		t.Fatalf("PostgreSQL 18.6 required: version=%d err=%v", version, err)
	}
	size := int64(16384)
	if len(sizeBytes) > 0 {
		size = sizeBytes[0]
	}
	f.exec(t, `UPDATE library_file SET size_bytes=$2,file_fingerprint=$3 WHERE id=$1`, f.file, size, fmt.Sprintf("stat.v1:301:%d:123456789", size))
	// Content changes revoke the old fact; prepare a new import for this size.
	f.exec(t, `INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), f.unit, f.root, f.file)
	f.exec(t, `UPDATE job SET kind='media_subtitle',dedupe_key='media-subtitle:'||$2::uuid::text||':'||(SELECT mapping_revision::text FROM library_file WHERE id=$2) WHERE id=$1`, f.lease.ID, f.file)
	r := NewSubtitleRepository(f.pool, testIDs{})
	s, err := r.LoadSubtitle(t.Context(), f.lease)
	if err != nil {
		t.Fatal(err)
	}
	return f, r, s
}

func subtitleCompletion(s ports.MediaSidecarSnapshot) ports.MediaSubtitleCompletion {
	name, err := media.ChineseSubtitleName(s.FileRelative)
	if err != nil {
		panic(err)
	}
	return ports.MediaSubtitleCompletion{
		Outcome: "observed", Name: name,
		VideoHash:   ports.SubtitleVideoHash{Hash: strings.Repeat("0123456789abcdef0123456789abcdef;", 3) + "0123456789abcdef0123456789abcdef", SizeBytes: s.FileIdentity.Size},
		Publication: ports.SidecarResult{Disposition: ports.SidecarCreated, Published: true, Identity: ports.FileIdentity{Device: s.RootIdentity.Device, Inode: 501, Size: 128, Mtime: 1234}},
	}
}

func assertSubtitleReceipt(t *testing.T, f fixture, c ports.MediaSubtitleCompletion, review bool) {
	t.Helper()
	var state, reason, actor, actorID, action, targetType, targetID string
	var result, audit []byte
	var released, finished bool
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT state,coalesce(error,''),result,lease_owner IS NULL AND lease_until IS NULL,finished_at IS NOT NULL,(SELECT count(*) FROM audit_log) FROM job WHERE id=$1`, f.lease.ID).Scan(&state, &reason, &result, &released, &finished, &count); err != nil {
		t.Fatal(err)
	}
	wantState, wantReason := "succeeded", ""
	if review {
		wantState, wantReason = "dead", "review_required"
	}
	var got ports.MediaSubtitleCompletion
	if json.Unmarshal(result, &got) != nil || !reflect.DeepEqual(got, c) || state != wantState || reason != wantReason || !released || !finished || count != 1 {
		t.Fatalf("original job receipt: state=%s reason=%s released=%t finished=%t audits=%d result=%s", state, reason, released, finished, count, result)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT actor_type,actor_id,action,target_type,target_id,payload FROM audit_log`).Scan(&actor, &actorID, &action, &targetType, &targetID, &audit); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(audit, &got) != nil || !reflect.DeepEqual(got, c) || actor != "system" || actorID != ports.MediaSubtitleJobKind || action != "media.subtitle.completed" || targetType != "job" || targetID != f.lease.ID.String() {
		t.Fatalf("subtitle audit mismatch: %s %s %s %s %s %s", actor, actorID, action, targetType, targetID, audit)
	}
}

func TestMediaSubtitleImportedFactsAndResults(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f, r, s := subtitleFixture(t, kind)
			before := f.graph(t, false)
			if len(s.Binding) != 64 || s.Source != (ports.MediaSidecarPayload{Version: 1, SourceEventID: f.event, FileID: f.file, UnitID: f.unit}) || s.RootID != f.root || s.MediaID != f.media || s.MediaType != kind || s.RootIdentity != (ports.FileIdentity{Device: 42, Inode: 101}) || s.FileIdentity != (ports.FileIdentity{Device: 42, Inode: 301, Size: 16384, Mtime: 123456789}) || s.FileFingerprint != "stat.v1:301:16384:123456789" || s.Title != "电影 & <test>" || s.TMDBID != 6211 || s.Year == nil || *s.Year != 2024 || len(s.Mappings) != 1 || s.Mappings[0].UnitID != f.unit {
				t.Fatalf("imported snapshot: %+v", s)
			}
			if err := r.ValidateSubtitle(t.Context(), f.lease, s); err != nil {
				t.Fatal(err)
			}
			fresh, err := r.LoadSubtitle(t.Context(), f.lease)
			if err != nil || !reflect.DeepEqual(s, fresh) || before != f.graph(t, false) {
				t.Fatal("Load/Validate changed evidence", err)
			}
			f.exec(t, `UPDATE library_root SET scan_token=21,mutation_seq=mutation_seq+1,name='Renamed',updated_at=clock_timestamp()`)
			if err = r.ValidateSubtitle(t.Context(), f.lease, s); err != nil {
				t.Fatal("ordinary scan invalidated authority", err)
			}
		})
	}
	for _, outcome := range []string{"created", "preserved", "not_found", "too_small", "uncertain", "unsafe", "not_published"} {
		t.Run(outcome, func(t *testing.T) {
			size := int64(16384)
			if outcome == "too_small" {
				size = 16383
			}
			f, r, s := subtitleFixture(t, "movie", size)
			c := subtitleCompletion(s)
			review := false
			switch outcome {
			case "preserved":
				c.Publication.Disposition = ports.SidecarPreservedRegular
				c.Publication.Published = false
				c.VideoHash = ports.SubtitleVideoHash{}
			case "not_found":
				c.Outcome = "not_found"
				c.Name = ""
				c.Publication = ports.SidecarResult{}
			case "too_small":
				c = ports.MediaSubtitleCompletion{Outcome: "too_small"}
			case "uncertain":
				c.Publication.Disposition = ports.SidecarUncertain
				review = true
			case "unsafe":
				c.Publication = ports.SidecarResult{Disposition: ports.SidecarUnsafeExisting}
				c.VideoHash = ports.SubtitleVideoHash{}
				review = true
			case "not_published":
				c.Publication = ports.SidecarResult{Disposition: ports.SidecarNotPublished}
				review = true
			}
			before := f.graph(t, true)
			if err := r.CompleteSubtitle(t.Context(), f.lease, s, c); err != nil {
				t.Fatal(err)
			}
			assertSubtitleReceipt(t, f, c, review)
			if before != f.graph(t, true) {
				t.Fatal("completion created business facts, placeholders or inventory")
			}
			all := f.graph(t, false)
			if err := r.CompleteSubtitle(t.Context(), f.lease, s, c); err != ports.ErrMediaSidecarStale {
				t.Fatal("original lease replay", err)
			}
			if all != f.graph(t, false) {
				t.Fatal("replay changed graph")
			}
		})
	}
}

func TestMediaSubtitleAuthorityFences(t *testing.T) {
	for name, sql := range map[string]string{
		"owner":               `UPDATE job SET lease_owner='other'`,
		"token":               `UPDATE job SET lease_token=18`,
		"state":               `UPDATE job SET state='queued'`,
		"expiry":              `UPDATE job SET lease_until=clock_timestamp()-interval '1 second'`,
		"payload_extra":       `UPDATE job SET payload=payload||'{"path":"/forged"}'`,
		"payload_version":     `UPDATE job SET payload=jsonb_set(payload,'{version}','2')`,
		"payload_event":       `UPDATE job SET payload=jsonb_set(payload,'{source_event_id}',to_jsonb(gen_random_uuid()))`,
		"event_kind":          `UPDATE outbox_event SET event_type='other'`,
		"event_aggregate":     `UPDATE outbox_event SET aggregate_id=gen_random_uuid()::text`,
		"event_file":          `UPDATE outbox_event SET payload=jsonb_set(payload,'{file_id}',to_jsonb(gen_random_uuid()))`,
		"event_unit":          `UPDATE outbox_event SET payload=jsonb_set(payload,'{unit_id}',to_jsonb(gen_random_uuid()))`,
		"isolation":           `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid()`,
		"generation":          `UPDATE library_root SET generation=generation+1`,
		"health":              `UPDATE library_root SET health='offline'`,
		"baseline":            `UPDATE library_root SET baseline_completed=false`,
		"baseline_partial":    `UPDATE scan_run SET state='partial'`,
		"baseline_incomplete": `UPDATE scan_finalization SET completed_at=NULL`,
		"baseline_missing":    `DELETE FROM scan_finalization`,
		"root_inode":          `UPDATE scan_finalization SET root_inode=102`,
		"root_device":         `UPDATE scan_finalization SET root_device=43`,
		"root_path":           `UPDATE library_root SET path='/synthetic/replacement'`,
		"root_marker":         `UPDATE library_root SET marker_filename='.other'`,
		"health_revision":     `UPDATE library_root SET health_revision=health_revision+1`,
		"file_path":           `UPDATE library_file SET rel_path='Show/changed.mkv'`,
		"file_retired":        `UPDATE library_file SET retired_at=clock_timestamp()`,
		"file_sequence":       `UPDATE library_file SET last_mutation_seq=last_mutation_seq+1`,
		"file_size":           `UPDATE library_file SET size_bytes=16385`,
		"file_mtime":          `UPDATE library_file SET mtime_ns=mtime_ns+1`,
		"file_inode":          `UPDATE library_file SET file_fingerprint='stat.v1:302:16384:123456789'`,
		"fingerprint_missing": `UPDATE library_file SET file_fingerprint=NULL`,
		"fingerprint_invalid": `UPDATE library_file SET file_fingerprint='stat.v1:0:16384:123456789'`,
		"mapping_revision":    `UPDATE library_file SET mapping_revision=mapping_revision+1`,
		"mapping_retired":     `UPDATE library_file_unit SET retired_at=clock_timestamp()`,
		"fact_revoked":        `UPDATE unit_fact SET revoked_at=clock_timestamp()`,
		"fact_quality":        `UPDATE unit_fact SET quality_profile='{"resolution":"1080p"}'`,
		"unit_retired":        `UPDATE media_unit SET retired=true`,
		"unit_metadata":       `UPDATE media_unit SET metadata_version=metadata_version+1`,
		"unit_facts":          `UPDATE media_unit SET facts_version=facts_version+1`,
		"media_metadata":      `UPDATE media SET metadata_version=metadata_version+1`,
		"title":               `UPDATE media SET title='changed'`,
		"original_title":      `UPDATE media SET original_title='changed'`,
		"year":                `UPDATE media SET year=NULL`,
		"poster":              `UPDATE media_artwork SET poster_path='/changed.jpg'`,
		"identity":            `DELETE FROM media_artwork; UPDATE media_identity SET external_id='6212'`,
	} {
		t.Run(name, func(t *testing.T) {
			f, r, s := subtitleFixture(t, "movie")
			f.exec(t, sql)
			before := f.graph(t, false)
			requireSafe(t, r.ValidateSubtitle(t.Context(), f.lease, s))
			requireSafe(t, r.CompleteSubtitle(t.Context(), f.lease, s, subtitleCompletion(s)))
			if before != f.graph(t, false) {
				t.Fatal("stale authority changed graph")
			}
		})
	}
	t.Run("kind_both_directions", func(t *testing.T) {
		f, r, s := subtitleFixture(t, "movie")
		before := f.graph(t, false)
		_, err := f.repo.Load(t.Context(), f.lease)
		if err != ports.ErrMediaSidecarStale {
			t.Fatal("subtitle lease authorized sidecar Load", err)
		}
		if err = f.repo.Validate(t.Context(), f.lease, s); err != ports.ErrMediaSidecarStale {
			t.Fatal(err)
		}
		if err = f.repo.Complete(t.Context(), f.lease, s, completion(s, false)); err != ports.ErrMediaSidecarStale {
			t.Fatal(err)
		}
		if before != f.graph(t, false) {
			t.Fatal("wrong kind wrote")
		}
		f.exec(t, `UPDATE job SET kind='media_sidecar'`)
		sidecar := f.load(t)
		before = f.graph(t, false)
		_, err = r.LoadSubtitle(t.Context(), f.lease)
		if err != ports.ErrMediaSidecarStale {
			t.Fatal("sidecar lease authorized subtitle Load", err)
		}
		if err = r.ValidateSubtitle(t.Context(), f.lease, sidecar); err != ports.ErrMediaSidecarStale {
			t.Fatal(err)
		}
		if err = r.CompleteSubtitle(t.Context(), f.lease, sidecar, subtitleCompletion(sidecar)); err != ports.ErrMediaSidecarStale {
			t.Fatal(err)
		}
		if before != f.graph(t, false) {
			t.Fatal("wrong kind wrote")
		}
	})
	t.Run("load_requires_imported_facts", func(t *testing.T) {
		f, r, _ := subtitleFixture(t, "movie")
		f.exec(t, `UPDATE unit_fact SET revoked_at=clock_timestamp()`)
		before := f.graph(t, false)
		_, err := r.LoadSubtitle(t.Context(), f.lease)
		requireSafe(t, err)
		if before != f.graph(t, false) {
			t.Fatal("failed Load wrote")
		}
	})
}

func TestMediaSubtitleRejectForgedResults(t *testing.T) {
	f, r, s := subtitleFixture(t, "movie")
	before := f.graph(t, false)
	for name, mutate := range map[string]func(*ports.MediaSubtitleCompletion){
		"empty_outcome":     func(c *ports.MediaSubtitleCompletion) { c.Outcome = "" },
		"unknown_outcome":   func(c *ports.MediaSubtitleCompletion) { c.Outcome = "created" },
		"uppercase_outcome": func(c *ports.MediaSubtitleCompletion) { c.Outcome = "Observed" },
		"empty_hash":        func(c *ports.MediaSubtitleCompletion) { c.VideoHash = ports.SubtitleVideoHash{} },
		"malformed_hash":    func(c *ports.MediaSubtitleCompletion) { c.VideoHash.Hash = "forged" },
		"uppercase_hash":    func(c *ports.MediaSubtitleCompletion) { c.VideoHash.Hash = strings.ToUpper(c.VideoHash.Hash) },
		"hash_separator": func(c *ports.MediaSubtitleCompletion) {
			c.VideoHash.Hash = strings.ReplaceAll(c.VideoHash.Hash, ";", ":")
		},
		"hash_size":           func(c *ports.MediaSubtitleCompletion) { c.VideoHash.SizeBytes++ },
		"hash_below_min":      func(c *ports.MediaSubtitleCompletion) { c.VideoHash.SizeBytes = 16383 },
		"name_empty":          func(c *ports.MediaSubtitleCompletion) { c.Name = "" },
		"name_traversal":      func(c *ports.MediaSubtitleCompletion) { c.Name = "../source.zh-cn.default.srt" },
		"name_basename":       func(c *ports.MediaSubtitleCompletion) { c.Name = "source.zh-cn.default.srt" },
		"name_language":       func(c *ports.MediaSubtitleCompletion) { c.Name = strings.ReplaceAll(c.Name, "zh-cn", "en") },
		"name_suffix":         func(c *ports.MediaSubtitleCompletion) { c.Name += ".exe" },
		"unknown_publication": func(c *ports.MediaSubtitleCompletion) { c.Publication.Disposition = "success" },
		"created_unpublished": func(c *ports.MediaSubtitleCompletion) { c.Publication.Published = false },
		"created_zero":        func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity = ports.FileIdentity{} },
		"foreign_device":      func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity.Device++ },
		"source_inode":        func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity.Inode = s.FileIdentity.Inode },
		"root_inode":          func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity.Inode = s.RootIdentity.Inode },
		"zero_inode":          func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity.Inode = 0 },
		"negative_size":       func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity.Size = -1 },
		"empty_created":       func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity.Size = 0 },
		"oversize":            func(c *ports.MediaSubtitleCompletion) { c.Publication.Identity.Size = ports.MaxSubtitleBytes + 1 },
		"preserved_published": func(c *ports.MediaSubtitleCompletion) { c.Publication.Disposition = ports.SidecarPreservedRegular },
		"preserved_zero": func(c *ports.MediaSubtitleCompletion) {
			c.Publication = ports.SidecarResult{Disposition: ports.SidecarPreservedRegular}
		},
		"uncertain_unpublished": func(c *ports.MediaSubtitleCompletion) {
			c.Publication.Disposition = ports.SidecarUncertain
			c.Publication.Published = false
		},
		"uncertain_hash": func(c *ports.MediaSubtitleCompletion) {
			c.Publication.Disposition = ports.SidecarUncertain
			c.VideoHash = ports.SubtitleVideoHash{}
		},
		"unsafe_published": func(c *ports.MediaSubtitleCompletion) {
			c.Publication = ports.SidecarResult{Disposition: ports.SidecarUnsafeExisting, Published: true}
		},
		"unsafe_identity": func(c *ports.MediaSubtitleCompletion) {
			c.Publication.Disposition = ports.SidecarUnsafeExisting
			c.Publication.Published = false
		},
		"unpublished_identity": func(c *ports.MediaSubtitleCompletion) {
			c.Publication.Disposition = ports.SidecarNotPublished
			c.Publication.Published = false
		},
		"not_found_publication":  func(c *ports.MediaSubtitleCompletion) { c.Outcome = "not_found"; c.Name = "" },
		"not_found_name":         func(c *ports.MediaSubtitleCompletion) { c.Outcome = "not_found"; c.Publication = ports.SidecarResult{} },
		"not_found_hash":         func(c *ports.MediaSubtitleCompletion) { *c = ports.MediaSubtitleCompletion{Outcome: "not_found"} },
		"too_small_large_source": func(c *ports.MediaSubtitleCompletion) { *c = ports.MediaSubtitleCompletion{Outcome: "too_small"} },
	} {
		t.Run(name, func(t *testing.T) {
			c := subtitleCompletion(s)
			mutate(&c)
			if err := r.CompleteSubtitle(t.Context(), f.lease, s, c); err != ports.ErrMediaSidecarResult {
				t.Fatal("forged result accepted", err)
			}
			if before != f.graph(t, false) {
				t.Fatal("forged result changed graph")
			}
		})
	}
	t.Run("too_small_forbids_evidence", func(t *testing.T) {
		f, r, small := subtitleFixture(t, "movie", 0)
		before := f.graph(t, false)
		for _, c := range []ports.MediaSubtitleCompletion{
			{Outcome: "too_small", Name: subtitleCompletion(s).Name},
			{Outcome: "too_small", VideoHash: subtitleCompletion(s).VideoHash},
			{Outcome: "too_small", Publication: ports.SidecarResult{Disposition: ports.SidecarNotPublished}},
			{Outcome: "not_found", VideoHash: ports.SubtitleVideoHash{Hash: subtitleCompletion(s).VideoHash.Hash, SizeBytes: 0}},
		} {
			if err := r.CompleteSubtitle(t.Context(), f.lease, small, c); err != ports.ErrMediaSidecarResult {
				t.Fatal(err)
			}
			if before != f.graph(t, false) {
				t.Fatal("invalid small result wrote")
			}
		}
	})
}

func TestMediaSubtitleRollback(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprintf("deferred_%t", deferred), func(t *testing.T) {
			f, r, s := subtitleFixture(t, "movie")
			table, trigger := "audit_log", `CREATE TRIGGER subtitle_test_fail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION subtitle_test_failure()`
			if deferred {
				table = "job"
				trigger = `CREATE CONSTRAINT TRIGGER subtitle_test_fail AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION subtitle_test_failure()`
			}
			f.exec(t, `CREATE FUNCTION subtitle_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'secret raw path ciphertext'; END $$`)
			f.exec(t, trigger)
			before := f.graph(t, false)
			c := subtitleCompletion(s)
			if err := r.CompleteSubtitle(t.Context(), f.lease, s, c); err != ports.ErrUnavailable {
				t.Fatal("tail failure leaked", err)
			}
			if before != f.graph(t, false) {
				t.Fatal("original job/audit partially committed")
			}
			f.exec(t, "DROP TRIGGER subtitle_test_fail ON "+table)
			if err := r.CompleteSubtitle(t.Context(), f.lease, s, c); err != nil {
				t.Fatal("original lease retry", err)
			}
			assertSubtitleReceipt(t, f, c, false)
		})
	}
}

func TestMediaSubtitleLockRaces(t *testing.T) {
	for _, table := range []string{"job", "library_root", "audit_log"} {
		for _, op := range []string{"Load", "Validate", "Complete"} {
			if table == "audit_log" && op != "Complete" {
				continue
			}
			t.Run("expiry_"+table+"_"+op, func(t *testing.T) {
				f, r, s := subtitleFixture(t, "movie")
				f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '750 milliseconds'`)
				before := f.graph(t, false)
				tx, err := f.pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				lock := "SELECT 1 FROM " + table + " FOR UPDATE"
				if table == "audit_log" {
					lock = "LOCK TABLE audit_log IN ACCESS EXCLUSIVE MODE"
				}
				if _, err = tx.Exec(t.Context(), lock); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
				done := make(chan error, 1)
				go func() {
					var e error
					switch op {
					case "Load":
						_, e = r.LoadSubtitle(ctx, f.lease)
					case "Validate":
						e = r.ValidateSubtitle(ctx, f.lease, s)
					case "Complete":
						e = r.CompleteSubtitle(ctx, f.lease, s, subtitleCompletion(s))
					}
					done <- e
				}()
				defer func() { cancel(); <-done }()
				waitBlocked(t, f, tx.Conn().PgConn().PID())
				if _, err = tx.Exec(t.Context(), `SELECT pg_sleep(greatest(0,extract(epoch FROM (lease_until-clock_timestamp())))+0.05) FROM job WHERE id=$1`, f.lease.ID); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				err = <-done
				done <- err
				if err != ports.ErrMediaSidecarStale {
					t.Fatal("expired post-lock lease accepted", err)
				}
				if before != f.graph(t, false) {
					t.Fatal("expired waiter wrote success/audit")
				}
			})
		}
	}
	for _, op := range []string{"Load", "Validate", "Complete"} {
		t.Run("restore_after_job_lock_"+op, func(t *testing.T) {
			f, r, s := subtitleFixture(t, "movie")
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(t.Context(), `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid()`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			done := make(chan error, 1)
			go func() {
				var e error
				switch op {
				case "Load":
					_, e = r.LoadSubtitle(ctx, f.lease)
				case "Validate":
					e = r.ValidateSubtitle(ctx, f.lease, s)
				case "Complete":
					e = r.CompleteSubtitle(ctx, f.lease, s, subtitleCompletion(s))
				}
				done <- e
			}()
			defer func() { cancel(); <-done }()
			waitBlocked(t, f, tx.Conn().PgConn().PID())
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = <-done
			done <- err
			if err != ports.ErrMediaSidecarStale {
				t.Fatal("restore isolation after lock accepted", err)
			}
			before := f.graph(t, false)
			requireSafe(t, r.CompleteSubtitle(t.Context(), f.lease, s, subtitleCompletion(s)))
			if before != f.graph(t, false) {
				t.Fatal("isolated replay changed graph")
			}
			var audits int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log`).Scan(&audits); err != nil || audits != 0 {
				t.Fatal("isolated waiter audited success", audits, err)
			}
		})
	}
}

func TestMediaSubtitleConcurrentEnqueue(t *testing.T) {
	f, _, s := subtitleFixture(t, "tv")
	// Start with no subtitle job so the unique-key race must elect a winner.
	f.exec(t, `DELETE FROM job WHERE id=$1`, f.lease.ID)
	events := []uuid.UUID{f.event}
	for i := 0; i < 7; i++ {
		event := uuid.New()
		events = append(events, event)
		f.exec(t, `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) SELECT $1,aggregate_type,aggregate_id,event_type,jsonb_set(payload,'{source_event_id}',to_jsonb($1::uuid)),seq+$2 FROM outbox_event WHERE id=$3`, event, i+1, f.event)
	}
	protected := f.graph(t, true)
	enqueue := func(events []uuid.UUID) {
		t.Helper()
		start := make(chan struct{})
		errs := make(chan error, len(events)*2)
		var wg sync.WaitGroup
		for i := 0; i < len(events)*2; i++ {
			event := events[i%len(events)]
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				tx, err := f.pool.Begin(t.Context())
				if err != nil {
					errs <- err
					return
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				q := db.New(tx)
				if _, err = q.LockMediaSubtitleEnqueue(t.Context(), f.file); err == nil {
					err = q.EnqueueMediaSubtitleJob(t.Context(), db.EnqueueMediaSubtitleJobParams{JobID: uuid.New(), UnitID: f.unit, FileID: f.file, SourceEventID: event})
				}
				if err == nil {
					err = tx.Commit(t.Context())
				}
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	assertJobs := func(want int) {
		t.Helper()
		var n, bad int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE kind<>'media_subtitle' OR state<>'queued' OR max_attempts<>1 OR payload->>'file_id'<>$1::uuid::text OR payload->>'unit_id'<>$2::uuid::text OR NOT EXISTS(SELECT 1 FROM outbox_event e WHERE e.id=(job.payload->>'source_event_id')::uuid AND e.event_type='media.imported' AND e.aggregate_type='media' AND e.aggregate_id=$2::uuid::text AND e.payload->>'file_id'=$1::uuid::text AND e.payload->>'unit_id'=$2::uuid::text)) FROM job`, f.file, f.unit).Scan(&n, &bad); err != nil || n != want || bad != 0 {
			t.Fatalf("jobs=%d want=%d invalid=%d err=%v", n, want, bad, err)
		}
	}
	enqueue(events)
	assertJobs(1)
	var key string
	if err := f.pool.QueryRow(t.Context(), `SELECT dedupe_key FROM job`).Scan(&key); err != nil || key != fmt.Sprintf("media-subtitle:%s:%d", f.file, s.FileMappingRevision) {
		t.Fatal("wrong dedupe key", key, err)
	}
	if protected != f.graph(t, true) {
		t.Fatal("enqueue altered imported facts/inventory/events")
	}
	// A real new mapping revision uses only its newly prepared imported event.
	other := uuid.New()
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,'S01E02',1,2)`, other, f.media)
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, f.file, other)
	f.exec(t, `UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=$1`, f.file)
	newEvent := uuid.New()
	f.exec(t, `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) SELECT $1,aggregate_type,aggregate_id,event_type,jsonb_set(payload,'{source_event_id}',to_jsonb($1::uuid)),100 FROM outbox_event WHERE id=$2`, newEvent, f.event)
	protected = f.graph(t, true)
	enqueue([]uuid.UUID{newEvent})
	assertJobs(2)
	var newCount int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job j JOIN library_file f ON f.id=$1 WHERE j.dedupe_key='media-subtitle:'||f.id::text||':'||f.mapping_revision::text AND j.payload->>'source_event_id'=$2::uuid::text`, f.file, newEvent).Scan(&newCount); err != nil || newCount != 1 {
		t.Fatal("new mapping did not admit exactly one new task", newCount, err)
	}
	if protected != f.graph(t, true) {
		t.Fatal("new mapping enqueue altered business graph")
	}
}

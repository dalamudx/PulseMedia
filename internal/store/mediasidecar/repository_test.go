package mediasidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testIDs struct{}

func (testIDs) NewUUID() uuid.UUID    { return uuid.New() }
func (testIDs) NewUUIDString() string { return uuid.NewString() }

type fixture struct {
	pool                                *pgxpool.Pool
	repo                                *Repository
	lease                               ports.JobLease
	root, file, unit, media, event, run uuid.UUID
}

func newFixture(t *testing.T, kind string) fixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	version, err := postgres.Migrate(t.Context(), dsn, "../../../migrations")
	if err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatalf("schema93 required: %d %v", version, err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "Pacific/Honolulu"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	cfg.MaxConns = 6
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := fixture{pool: pool, repo: NewRepository(pool, testIDs{}), lease: ports.JobLease{ID: uuid.New(), Owner: "sidecar-test", Token: 17}, root: uuid.New(), file: uuid.New(), unit: uuid.New(), media: uuid.New(), event: uuid.New(), run: uuid.New()}
	f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'电影 & <test>','Original',2024,$2)`, f.media, kind)
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,$3,'6211')`, uuid.New(), f.media, "tmdb."+kind)
	key := "movie"
	if kind == "tv" {
		key = "S01E01"
	}
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,$3,$4,$5)`, f.unit, f.media, key, numberFor(kind, 1), numberFor(kind, 1))
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,'/synthetic/sidecar',$2,'Library',true)`, f.root, kind)
	f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,'Show/Season 01/source.mkv',4096,123456789,'stat.v1:301:4096:123456789')`, f.file, f.root)
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, f.file, f.unit)
	f.exec(t, `INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), f.unit, f.root, f.file)
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) VALUES($1,$2,1,1,0,'succeeded',clock_timestamp())`, f.run, f.root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,42,101,true,clock_timestamp() FROM library_root WHERE id=$2`, f.run, f.root)
	payload, _ := json.Marshal(ports.MediaSidecarPayload{Version: 1, SourceEventID: f.event, FileID: f.file, UnitID: f.unit})
	f.exec(t, `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES($1,'media',$2,'media.imported',$3,1)`, f.event, f.unit.String(), payload)
	f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,'media_sidecar','running',$2,$3,$4,clock_timestamp()+interval '5 minutes')`, f.lease.ID, payload, f.lease.Owner, f.lease.Token)
	f.exec(t, `INSERT INTO media_artwork(media_id,provider,namespace,external_id,metadata_version,poster_path) SELECT id,'tmdb','tmdb.'||media_type,'6211',metadata_version,'/Trusted_09-poster.jpg' FROM media WHERE id=$1`, f.media)
	return f
}
func numberFor(kind string, n int32) any {
	if kind == "tv" {
		return n
	}
	return nil
}
func (f fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f fixture) load(t *testing.T) ports.MediaSidecarSnapshot {
	t.Helper()
	s, err := f.repo.Load(t.Context(), f.lease)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// All public tables are included, including empty acquisition/inventory tables.
func (f fixture) graph(t *testing.T, protected bool) string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !protected || (name != "job" && name != "audit_log") {
			tables = append(tables, name)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, table := range tables {
		var data string
		sql := `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM ` + pgx.Identifier{table}.Sanitize() + ` t) s`
		if err = f.pool.QueryRow(t.Context(), sql).Scan(&data); err != nil {
			t.Fatal(err)
		}
		out.WriteString(table)
		out.WriteString(data)
	}
	return out.String()
}
func completion(s ports.MediaSidecarSnapshot, poster bool) ports.MediaSidecarCompletion {
	name := "movie.nfo"
	if s.MediaType == "tv" {
		name = "tvshow.nfo"
	}
	c := ports.MediaSidecarCompletion{Artifacts: []ports.MediaSidecarArtifactResult{{Name: name, Result: ports.SidecarResult{Disposition: ports.SidecarCreated, Published: true, Identity: ports.FileIdentity{Device: 42, Inode: 501, Size: 128, Mtime: 1234}}}}}
	if poster {
		c.Artifacts = append(c.Artifacts, ports.MediaSidecarArtifactResult{Name: "poster.jpg", Result: ports.SidecarResult{Disposition: ports.SidecarPreservedRegular, Identity: ports.FileIdentity{Device: 42, Inode: 502, Size: 1024, Mtime: 5678}}})
	}
	return c
}
func requireSafe(t *testing.T, err error) {
	t.Helper()
	if err != ports.ErrMediaSidecarStale && err != ports.ErrMediaSidecarUnavailable && err != ports.ErrMediaSidecarResult && err != ports.ErrUnavailable && err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("unsafe or absent error: %v", err)
	}
}
func TestMovieAndTVSnapshotAndCompletion(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, kind)
			before := f.graph(t, true)
			s := f.load(t)
			if len(s.Binding) != 64 || s.Source.SourceEventID != f.event || s.Source.FileID != f.file || s.Source.UnitID != f.unit || s.RootID != f.root || s.RootIdentity != (ports.FileIdentity{Device: 42, Inode: 101}) || s.FileIdentity != (ports.FileIdentity{Device: 42, Inode: 301, Size: 4096, Mtime: 123456789}) || s.TMDBID != 6211 || s.Title != "电影 & <test>" || s.Year == nil || *s.Year != 2024 || s.MediaID != f.media || s.MediaType != kind || len(s.Mappings) != 1 || s.Mappings[0].UnitID != f.unit || s.PosterPath != "/Trusted_09-poster.jpg" {
				t.Fatalf("bad snapshot: %+v", s)
			}
			if err := f.repo.Validate(t.Context(), f.lease, s); err != nil {
				t.Fatal(err)
			}
			if other := f.load(t); !reflect.DeepEqual(s, other) {
				t.Fatal("non-UTC snapshot unstable")
			}
			c := completion(s, true)
			if err := f.repo.Complete(t.Context(), f.lease, s, c); err != nil {
				t.Fatal(err)
			}
			var state string
			var raw []byte
			var audits int
			if err := f.pool.QueryRow(t.Context(), `SELECT state,result,(SELECT count(*) FROM audit_log) FROM job WHERE id=$1`, f.lease.ID).Scan(&state, &raw, &audits); err != nil {
				t.Fatal(err)
			}
			var got ports.MediaSidecarCompletion
			if json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(c, got) || state != "succeeded" || audits != 1 {
				t.Fatalf("bad completion: %s %s %d", state, raw, audits)
			}
			if before != f.graph(t, true) {
				t.Fatal("protected tables changed")
			}
			graph := f.graph(t, false)
			requireSafe(t, f.repo.Complete(t.Context(), f.lease, s, c))
			if graph != f.graph(t, false) {
				t.Fatal("successful job replayed")
			}
		})
	}
}
func TestLeasePayloadEventAndAuthorityFences(t *testing.T) {
	cases := map[string]string{
		"kind":                 `UPDATE job SET kind='other'`,
		"owner":                `UPDATE job SET lease_owner='other'`,
		"token":                `UPDATE job SET lease_token=18`,
		"state":                `UPDATE job SET state='queued'`,
		"expired":              `UPDATE job SET lease_until=clock_timestamp()-interval '1 second'`,
		"payload_version":      `UPDATE job SET payload=jsonb_set(payload,'{version}','2')`,
		"payload_extra":        `UPDATE job SET payload=payload||'{"path":"/forged"}'`,
		"payload_uuid":         `UPDATE job SET payload=jsonb_set(payload,'{file_id}','"not-uuid"')`,
		"payload_event":        `UPDATE job SET payload=jsonb_set(payload,'{source_event_id}',to_jsonb(gen_random_uuid()))`,
		"event_kind":           `UPDATE outbox_event SET event_type='other'`,
		"event_aggregate":      `UPDATE outbox_event SET aggregate_id=gen_random_uuid()::text`,
		"event_payload":        `UPDATE outbox_event SET payload=jsonb_set(payload,'{file_id}',to_jsonb(gen_random_uuid()))`,
		"isolated":             `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid()`,
		"root_offline":         `UPDATE library_root SET health='offline'`,
		"root_generation":      `UPDATE library_root SET generation=generation+1`,
		"root_no_baseline":     `UPDATE library_root SET baseline_completed=false`,
		"run_partial":          `UPDATE scan_run SET state='partial'`,
		"uncompleted_baseline": `UPDATE scan_finalization SET completed_at=NULL`,
		"no_baseline_identity": `DELETE FROM scan_finalization`,
		"fingerprint_null":     `UPDATE library_file SET file_fingerprint=NULL`,
		"fingerprint_invalid":  `UPDATE library_file SET file_fingerprint='stat.v1:0:4096:123456789'`,
		"fingerprint_overflow": `UPDATE library_file SET file_fingerprint='stat.v1:9223372036854775808:4096:123456789'`,
		"fingerprint_size":     `UPDATE library_file SET file_fingerprint='stat.v1:301:4097:123456789'`,
		"fingerprint_mtime":    `UPDATE library_file SET file_fingerprint='stat.v1:301:4096:123456788'`,
		"file_retired":         `UPDATE library_file SET retired_at=clock_timestamp()`,
		"mapping_retired":      `UPDATE library_file_unit SET retired_at=clock_timestamp()`,
		"fact_revoked":         `UPDATE unit_fact SET revoked_at=clock_timestamp()`,
		"unit_retired":         `UPDATE media_unit SET retired=true`,
		"identity_missing":     `DELETE FROM media_artwork; DELETE FROM media_identity`,
		"identity_nonpositive": `DELETE FROM media_artwork; UPDATE media_identity SET external_id='0'`,
		"identity_overflow":    `DELETE FROM media_artwork; UPDATE media_identity SET external_id='9223372036854775808'`,
		"identity_namespace":   `DELETE FROM media_artwork; UPDATE media_identity SET namespace='tmdb.tv'`,
		"identity_ambiguous":   `INSERT INTO media_identity(id,media_id,namespace,external_id) SELECT gen_random_uuid(),id,'tmdb.movie','6212' FROM media`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "movie")
			s := f.load(t)
			f.exec(t, sql)
			before := f.graph(t, false)
			_, err := f.repo.Load(t.Context(), f.lease)
			requireSafe(t, err)
			requireSafe(t, f.repo.Validate(t.Context(), f.lease, s))
			requireSafe(t, f.repo.Complete(t.Context(), f.lease, s, completion(s, true)))
			if before != f.graph(t, false) {
				t.Fatal("failed authority mutated graph")
			}
		})
	}
}
func TestSnapshotChangesAndOrdinaryScans(t *testing.T) {
	for name, sql := range map[string]string{
		"path":             `UPDATE library_root SET path='/synthetic/replacement'`,
		"marker":           `UPDATE library_root SET marker_filename='.new_marker'`,
		"health_revision":  `UPDATE library_root SET health_revision=health_revision+1`,
		"identity":         `UPDATE scan_finalization SET root_inode=102`,
		"file_path":        `UPDATE library_file SET rel_path='Show/other.mkv'`,
		"file_sequence":    `UPDATE library_file SET last_mutation_seq=last_mutation_seq+1`,
		"mapping_revision": `UPDATE library_file SET mapping_revision=mapping_revision+1`,
		"facts":            `UPDATE unit_fact SET quality_profile='{"resolution":"1080p"}'`,
		"title":            `UPDATE media SET title='changed'`,
		"original":         `UPDATE media SET original_title='changed'`,
		"year":             `UPDATE media SET year=NULL`,
		"media_version":    `UPDATE media SET metadata_version=metadata_version+1`,
		"unit_version":     `UPDATE media_unit SET metadata_version=metadata_version+1`,
		"unit_facts":       `UPDATE media_unit SET facts_version=facts_version+1`,
		"poster":           `UPDATE media_artwork SET poster_path='/changed.jpg'`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "movie")
			s := f.load(t)
			f.exec(t, sql)
			before := f.graph(t, false)
			requireSafe(t, f.repo.Validate(t.Context(), f.lease, s))
			requireSafe(t, f.repo.Complete(t.Context(), f.lease, s, completion(s, false)))
			if before != f.graph(t, false) {
				t.Fatal("stale snapshot wrote")
			}
		})
	}
	t.Run("ordinary_scan", func(t *testing.T) {
		f := newFixture(t, "movie")
		s := f.load(t)
		f.exec(t, `UPDATE library_root SET scan_token = 21,mutation_seq=mutation_seq+1,updated_at=clock_timestamp(),name='Renamed'`)
		if err := f.repo.Validate(t.Context(), f.lease, s); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(s, f.load(t)) {
			t.Fatal("ordinary scan changed binding")
		}
	})
	t.Run("first_baseline", func(t *testing.T) {
		f := newFixture(t, "movie")
		s := f.load(t)
		f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,created_at) VALUES($1,$2,2,1,0,'succeeded',clock_timestamp()+interval '1 minute')`, uuid.New(), f.root)
		f.exec(t, `INSERT INTO scan_finalization SELECT id,root_id,'/synthetic/sidecar','movie','.pulsemedia_root',1,42,999,NULL,true,clock_timestamp() FROM scan_run WHERE scan_token=2`)
		if err := f.repo.Validate(t.Context(), f.lease, s); err != nil {
			t.Fatal(err)
		}
	})
}
func TestCopiedBindingCannotForgeAnyDTOField(t *testing.T) {
	f := newFixture(t, "tv")
	s := f.load(t)
	before := f.graph(t, false)
	v := reflect.ValueOf(s)
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if field.Name == "Binding" {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			fresh := f.load(t)
			value := reflect.ValueOf(&fresh).Elem().Field(i)
			switch value.Kind() {
			case reflect.String:
				value.SetString(value.String() + "forged")
			case reflect.Int64:
				value.SetInt(value.Int() + 1)
			case reflect.Pointer:
				if value.IsNil() {
					t.Fatal("fixture requires nonnil pointer")
				}
				n := reflect.New(value.Type().Elem())
				n.Elem().SetInt(value.Elem().Int() + 1)
				value.Set(n)
			case reflect.Slice:
				value.Set(reflect.Zero(value.Type()))
			default:
				value.Set(reflect.Zero(value.Type()))
			}
			if fresh.Binding != s.Binding {
				t.Fatal("test changed binding")
			}
			if err := f.repo.Validate(t.Context(), f.lease, fresh); !errors.Is(err, ports.ErrMediaSidecarStale) {
				t.Fatal("forged DTO accepted", err)
			}
			requireSafe(t, f.repo.Complete(t.Context(), f.lease, fresh, completion(s, false)))
		})
	}
	if before != f.graph(t, false) {
		t.Fatal("forged DTO changed graph")
	}
	// A caller may mutate pointers without changing later Loads.
	*s.Year = 1900
	*s.Mappings[0].SeasonNumber = 99
	fresh := f.load(t)
	if *fresh.Year != 2024 || *fresh.Mappings[0].SeasonNumber != 1 {
		t.Fatal("DTO aliases trusted state")
	}
}
func TestFullMappingsAndOptionalArtwork(t *testing.T) {
	f := newFixture(t, "tv")
	other := uuid.New()
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,'S01E02',1,2)`, other, f.media)
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, f.file, other)
	s := f.load(t)
	if len(s.Mappings) != 2 {
		t.Fatal("mappings truncated")
	}
	seen := false
	for _, m := range s.Mappings {
		if m.UnitID == other && m.MediaID == f.media && *m.EpisodeNumber == 2 {
			seen = true
		}
	}
	if !seen {
		t.Fatal("second mapping missing")
	}
	for _, sql := range []string{`UPDATE media_artwork SET metadata_version=metadata_version+1`, `DELETE FROM media_artwork`} {
		f.exec(t, sql)
		if fresh := f.load(t); fresh.PosterPath != "" {
			t.Fatal("stale artwork accepted")
		}
	}
	s = f.load(t)
	if err := f.repo.Complete(t.Context(), f.lease, s, completion(s, false)); err != nil {
		t.Fatal("NFO-only degraded completion", err)
	}
}
func TestArtifactValidationLeavesGraphUnchanged(t *testing.T) {
	f := newFixture(t, "movie")
	s := f.load(t)
	before := f.graph(t, false)
	cases := map[string]func(*ports.MediaSidecarCompletion){
		"empty":          func(c *ports.MediaSidecarCompletion) { c.Artifacts = nil },
		"alien":          func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Name = "../movie.nfo" },
		"wrong_type":     func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Name = "tvshow.nfo" },
		"duplicate":      func(c *ports.MediaSidecarCompletion) { c.Artifacts = append(c.Artifacts, c.Artifacts[0]) },
		"poster_only":    func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Name = "poster.jpg" },
		"unknown":        func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Disposition = "success" },
		"created_false":  func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Published = false },
		"created_zero":   func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Identity = ports.FileIdentity{} },
		"foreign_device": func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Identity.Device = 99 },
		"source_inode":   func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Identity.Inode = s.FileIdentity.Inode },
		"root_inode":     func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Identity.Inode = s.RootIdentity.Inode },
		"negative_size":  func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Identity.Size = -1 },
		"oversize":       func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Identity.Size = ports.MaxSidecarBytes + 1 },
		"preserved_published": func(c *ports.MediaSidecarCompletion) {
			c.Artifacts[0].Result.Disposition = ports.SidecarPreservedRegular
		},
		"uncertain_zero": func(c *ports.MediaSidecarCompletion) {
			c.Artifacts[0].Result.Disposition = ports.SidecarUncertain
			c.Artifacts[0].Result.Identity = ports.FileIdentity{}
		},
		"unsafe_published": func(c *ports.MediaSidecarCompletion) { c.Artifacts[0].Result.Disposition = ports.SidecarUnsafeExisting },
		"unpublished_identity": func(c *ports.MediaSidecarCompletion) {
			c.Artifacts[0].Result.Disposition = ports.SidecarNotPublished
			c.Artifacts[0].Result.Published = false
		},
		"same_inode": func(c *ports.MediaSidecarCompletion) {
			c.Artifacts = append(c.Artifacts, ports.MediaSidecarArtifactResult{Name: "poster.jpg", Result: c.Artifacts[0].Result})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := completion(s, false)
			mutate(&c)
			if err := f.repo.Complete(t.Context(), f.lease, s, c); err != ports.ErrMediaSidecarResult {
				t.Fatalf("invalid result accepted: %v", err)
			}
			if before != f.graph(t, false) {
				t.Fatal("invalid result wrote")
			}
		})
	}
}
func TestUncertainAndObservedResults(t *testing.T) {
	for _, disposition := range []ports.SidecarDisposition{ports.SidecarUncertain, ports.SidecarPreservedRegular, ports.SidecarUnsafeExisting, ports.SidecarNotPublished} {
		t.Run(string(disposition), func(t *testing.T) {
			f := newFixture(t, "movie")
			s := f.load(t)
			before := f.graph(t, true)
			c := completion(s, false)
			c.Artifacts[0].Result.Disposition = disposition
			if disposition != ports.SidecarUncertain {
				c.Artifacts[0].Result.Published = false
			}
			if disposition == ports.SidecarUnsafeExisting || disposition == ports.SidecarNotPublished {
				c.Artifacts[0].Result.Identity = ports.FileIdentity{}
			}
			if err := f.repo.Complete(t.Context(), f.lease, s, c); err != nil {
				t.Fatal(err)
			}
			var state, reason string
			if err := f.pool.QueryRow(t.Context(), `SELECT state,coalesce(error,'') FROM job WHERE id=$1`, f.lease.ID).Scan(&state, &reason); err != nil {
				t.Fatal(err)
			}
			if disposition != ports.SidecarPreservedRegular {
				if state != "dead" || reason != "review_required" {
					t.Fatalf("failed or uncertain publication claimed success: %s %s", state, reason)
				}
			} else if state != "succeeded" {
				t.Fatal(state)
			}
			if before != f.graph(t, true) {
				t.Fatal("outcome changed protected tables")
			}
		})
	}
}
func TestAuditAndDeferredTailFailuresRollbackAndRetry(t *testing.T) {
	for _, tail := range []bool{false, true} {
		t.Run(fmt.Sprint(tail), func(t *testing.T) {
			f := newFixture(t, "movie")
			s := f.load(t)
			table := "audit_log"
			trigger := "CREATE TRIGGER sidecar_test_fail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION sidecar_test_failure()"
			if tail {
				table = "job"
				trigger = "CREATE CONSTRAINT TRIGGER sidecar_test_fail AFTER UPDATE ON job DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION sidecar_test_failure()"
			}
			f.exec(t, `CREATE FUNCTION sidecar_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'secret raw path ciphertext'; END $$`)
			f.exec(t, trigger)
			before := f.graph(t, false)
			if err := f.repo.Complete(t.Context(), f.lease, s, completion(s, true)); err != ports.ErrUnavailable {
				t.Fatalf("tail error leaked: %v", err)
			}
			if before != f.graph(t, false) {
				t.Fatal("tail failure partially committed")
			}
			f.exec(t, "DROP TRIGGER sidecar_test_fail ON "+table)
			if err := f.repo.Complete(t.Context(), f.lease, s, completion(s, true)); err != nil {
				t.Fatal("same original lease retry failed", err)
			}
		})
	}
}
func waitBlocked(t *testing.T, f fixture, pid uint32) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var blocked bool
		if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>$1 AND wait_event_type='Lock')`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("repository did not block on lock")
		}
	}
}
func TestLeaseExpiresDuringJobAndBusinessLockWait(t *testing.T) {
	for _, tc := range []struct{ table, op string }{{"job", "Load"}, {"library_root", "Load"}, {"job", "Validate"}, {"library_root", "Validate"}, {"job", "Complete"}, {"library_root", "Complete"}, {"audit_log", "Complete"}} {
		t.Run(tc.table+tc.op, func(t *testing.T) {
			f := newFixture(t, "movie")
			s := f.load(t)
			f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '500 milliseconds'`)
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			before := f.graph(t, false)
			lockSQL := "SELECT 1 FROM " + tc.table + " FOR UPDATE"
			if tc.table == "audit_log" {
				lockSQL = "LOCK TABLE audit_log IN ACCESS EXCLUSIVE MODE"
			}
			if _, err = tx.Exec(t.Context(), lockSQL); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				var e error
				switch tc.op {
				case "Load":
					_, e = f.repo.Load(t.Context(), f.lease)
				case "Validate":
					e = f.repo.Validate(t.Context(), f.lease, s)
				case "Complete":
					e = f.repo.Complete(t.Context(), f.lease, s, completion(s, false))
				}
				done <- e
			}()
			waitBlocked(t, f, tx.Conn().PgConn().PID())
			// Wait on the DB clock, while retaining the row lock which caused the wait.
			if _, err = tx.Exec(t.Context(), `SELECT pg_sleep(0.55)`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if err != ports.ErrMediaSidecarStale {
					t.Fatal("expired wait accepted", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("operation did not finish")
			}
			if before != f.graph(t, false) {
				t.Fatal("expired waiter changed graph")
			}
		})
	}
}
func TestChangesDuringHeldPhysicalWorkAreRejected(t *testing.T) {
	for _, sql := range []string{`UPDATE media SET title='concurrent'`, `UPDATE library_root SET marker_filename='.concurrent'`, `UPDATE library_file SET rel_path='Show/changed.mkv'`, `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid()`} {
		t.Run(sql, func(t *testing.T) {
			f := newFixture(t, "movie")
			s := f.load(t)
			// Simulate a held physical capability: the store must keep no DB locks across I/O.
			done := make(chan error, 1)
			go func() { _, err := f.pool.Exec(t.Context(), sql); done <- err }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Load retained a business lock")
			}
			before := f.graph(t, false)
			requireSafe(t, f.repo.Validate(t.Context(), f.lease, s))
			requireSafe(t, f.repo.Complete(t.Context(), f.lease, s, completion(s, false)))
			if before != f.graph(t, false) {
				t.Fatal("held work stale completion wrote")
			}
		})
	}
}
func TestExpiredRecoveryNeverRequeues(t *testing.T) {
	f := newFixture(t, "movie")
	s := f.load(t)
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second'`)
	n, err := db.New(f.pool).RecoverExpiredMediaSidecarJobs(t.Context())
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var state, reason string
	if err = f.pool.QueryRow(t.Context(), `SELECT state,error FROM job WHERE id=$1`, f.lease.ID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "dead" || reason != "review_required" {
		t.Fatal(state, reason)
	}
	requireSafe(t, f.repo.Complete(t.Context(), f.lease, s, completion(s, false)))
}
func TestContextCancellationAndSafeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := NewRepository(nil, nil)
	_, err := r.Load(ctx, ports.JobLease{})
	if err != context.Canceled {
		t.Fatal(err)
	}
	if err = r.Validate(ctx, ports.JobLease{}, ports.MediaSidecarSnapshot{}); err != context.Canceled {
		t.Fatal(err)
	}
	if safeError(context.Background(), errors.New("private SQL /path secret")) != ports.ErrUnavailable {
		t.Fatal("raw error leaked")
	}
}

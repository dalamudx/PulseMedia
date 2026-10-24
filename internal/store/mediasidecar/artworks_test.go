package mediasidecar

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestArtworkCompletionPG(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, kind)
			s := f.load(t)
			before := f.graph(t, true)
			c := completion(s, true)
			names := []string{"logo.png", "clearart.png", "banner.jpg"}
			if kind == "movie" {
				names = append(names, "disc.png")
			}
			for i, name := range names {
				result := c.Artifacts[0].Result
				result.Identity.Inode = int64(600 + i)
				if i == 1 {
					result.Disposition = ports.SidecarPreservedRegular
					result.Published = false
				}
				c.Artifacts = append(c.Artifacts, ports.MediaSidecarArtifactResult{Name: name, Result: result})
			}
			if err := f.repo.Complete(t.Context(), f.lease, s, c); err != nil {
				t.Fatal(err)
			}
			var state string
			var raw []byte
			var got ports.MediaSidecarCompletion
			if err := f.pool.QueryRow(t.Context(), `SELECT state,result FROM job WHERE id=$1`, f.lease.ID).Scan(&state, &raw); err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal(raw, &got) != nil || state != "succeeded" || !reflect.DeepEqual(got, c) || f.graph(t, true) != before {
				t.Fatal("artwork result or protected graph changed")
			}
		})
	}
}

func TestArtworkInvalidCompletionRollbackPG(t *testing.T) {
	for _, scenario := range []string{"tv_disc", "duplicate", "source_alias", "foreign_device", "tail_failure"} {
		t.Run(scenario, func(t *testing.T) {
			kind := "movie"
			if scenario == "tv_disc" {
				kind = "tv"
			}
			f := newFixture(t, kind)
			s := f.load(t)
			c := completion(s, false)
			art := ports.MediaSidecarArtifactResult{Name: "logo.png", Result: c.Artifacts[0].Result}
			art.Result.Identity.Inode = 600
			switch scenario {
			case "tv_disc":
				art.Name = "disc.png"
			case "source_alias":
				art.Result.Identity = s.FileIdentity
			case "foreign_device":
				art.Result.Identity.Device++
			case "tail_failure":
				if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION fail_art_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN RAISE EXCEPTION 'art result rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_art_result BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION fail_art_result()`); err != nil {
					t.Fatal(err)
				}
			}
			c.Artifacts = append(c.Artifacts, art)
			if scenario == "duplicate" {
				c.Artifacts = append(c.Artifacts, art)
			}
			before := f.graph(t, false)
			requireSafe(t, f.repo.Complete(t.Context(), f.lease, s, c))
			if f.graph(t, false) != before {
				t.Fatal("failed artwork completion changed graph")
			}
		})
	}
}

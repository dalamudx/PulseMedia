package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestUntrackedOrganizationFrozenUnitArrayContract(t *testing.T) {
	row := untrackedOrganizationUnitRow{OperationID: uuid.New(), UnitID: uuid.New(), MediaID: uuid.New(), MetadataVersion: 1, MediaMetadataVersion: 2, UnitKey: "movie", CandidateUnit: ports.UntrackedReviewUnit{UnitKey: "movie"}, MediaIdentity: organizationIdentity{Namespace: "tmdb.movie", ExternalID: "100"}}
	good, err := json.Marshal([]untrackedOrganizationUnitRow{row})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := organizationUnitRows(good)
	if err != nil || !reflect.DeepEqual(actual, []untrackedOrganizationUnitRow{row}) {
		t.Fatalf("valid movie with unknown air date rejected: %v", err)
	}
	original, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(original, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "unknown", "null"} {
		t.Run(name, func(t *testing.T) {
			changed := make(map[string]json.RawMessage, len(fields))
			for key, value := range fields {
				changed[key] = value
			}
			switch name {
			case "missing":
				delete(changed, "media_metadata_version")
			case "unknown":
				changed["extra"] = json.RawMessage(`true`)
			case "null":
				changed["media_identity"] = json.RawMessage(`null`)
			}
			raw, failure := json.Marshal([]map[string]json.RawMessage{changed})
			if failure != nil {
				t.Fatal(failure)
			}
			if _, failure = organizationUnitRows(raw); !errors.Is(failure, ports.ErrUntrackedOrganizationStale) {
				t.Fatalf("malformed row accepted: %v", failure)
			}
		})
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `[null]`, `[{}]`, `[1]`, string(good) + ` []`} {
		if _, failure := organizationUnitRows([]byte(raw)); !errors.Is(failure, ports.ErrUntrackedOrganizationStale) {
			t.Fatalf("invalid array accepted: %v", failure)
		}
	}
}

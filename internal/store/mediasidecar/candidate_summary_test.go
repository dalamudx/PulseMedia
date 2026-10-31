package mediasidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"reflect"
	"strings"
	"testing"
)

type summaryCountingCipher struct {
	ports.SecretCipher
	reads int
}

func (c *summaryCountingCipher) DecryptSecret(body []byte) ([]byte, error) {
	c.reads++
	return c.SecretCipher.DecryptSecret(body)
}

func TestSubtitleCandidatesSummaryCachedReadPG(t *testing.T) {
	f, r, a, snapshot := candidateFixture(t)
	ticket, _, err := r.BeginCandidateQuery(t.Context(), a, f.file, snapshot.FileMappingRevision, "summary")
	if err != nil {
		t.Fatal(err)
	}
	body := "7\n00:00:05,125 --> 00:00:09,999\n中文 https://example.invalid/private-token\n\n2\n00:00:00,000 --> 00:00:08,000\n另一条\n\n"
	out, err := r.FinishCandidateQuery(t.Context(), ticket, "candidates", []ports.SubtitleCandidateContent{{Content: []byte(body)}, {Content: []byte(candidateSRT)}})
	if err != nil || len(out.Candidates) != 2 {
		t.Fatal(out, err)
	}
	want := ports.SubtitleCandidateSummary{CueCount: 2, StartMS: 0, EndMS: 9999}
	if out.Candidates[0].Summary == nil || *out.Candidates[0].Summary != want {
		t.Fatal("wrong summary", out)
	}
	before := f.graph(t, false)
	cached, err := r.ReadCandidates(t.Context(), a, f.file)
	if err != nil || !reflect.DeepEqual(out, cached) {
		t.Fatal("cache read differs", cached, err)
	}
	encoded, err := json.Marshal(cached)
	if err != nil || bytes.Contains(encoded, []byte("private-token")) || bytes.Contains(encoded, []byte("中文")) {
		t.Fatal("unsafe public response", err)
	}
	var encrypted []byte
	if err = f.pool.QueryRow(t.Context(), `SELECT content_encrypted FROM subtitle_candidate WHERE id=$1`, out.Candidates[0].ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("private-token")) {
		t.Fatal("plaintext content stored")
	}
	if f.graph(t, false) != before {
		t.Fatal("summary read wrote business state")
	}
	_, state, content, err := r.BeginCandidateConfirmation(t.Context(), a, f.file, out.Candidates[1].ID, snapshot.FileMappingRevision)
	if err != nil || state.QueryID == nil || string(content) != candidateSRT {
		t.Fatal("summary changed selected content", err)
	}
}

func TestSubtitleCandidatesSummaryAuthorityAndVersionPG(t *testing.T) {
	for _, scenario := range []string{"wrong-key", "old-version", "demoted", "other-owner"} {
		t.Run(scenario, func(t *testing.T) {
			f, r, a, snapshot := candidateFixture(t)
			candidateQuery(t, f, r, a, snapshot, "summary-fence")
			spy := &summaryCountingCipher{SecretCipher: crypto.NewSecretProtector(bytes.Repeat([]byte{15}, 32))}
			r.secrets = spy
			switch scenario {
			case "old-version":
				f.exec(t, `UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=$1`, f.file)
			case "demoted":
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, uuid.MustParse(a.ID))
			case "other-owner":
				other := uuid.New()
				f.exec(t, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,'other-summary-admin','fixture','admin',true)`, other)
				copyActor := *a
				copyActor.ID = other.String()
				a = &copyActor
			}
			before := f.graph(t, false)
			state, err := r.ReadCandidates(t.Context(), a, f.file)
			switch scenario {
			case "wrong-key":
				if !errors.Is(err, ports.ErrUnavailable) || spy.reads != 1 {
					t.Fatal("bad key accepted", err, spy.reads)
				}
			case "demoted":
				if !errors.Is(err, ports.ErrForbidden) || spy.reads != 0 {
					t.Fatal("revocation bypass", err, spy.reads)
				}
			default:
				if err != nil || state.Outcome != "not_queried" || state.QueryID != nil || len(state.Candidates) != 0 || spy.reads != 0 {
					t.Fatal("stale/private cache exposed", state, err, spy.reads)
				}
			}
			if err != nil && strings.Contains(err.Error(), "cipher") {
				t.Fatal("internal cipher error exposed")
			}
			if f.graph(t, false) != before {
				t.Fatal("denial wrote business state")
			}
		})
	}
}

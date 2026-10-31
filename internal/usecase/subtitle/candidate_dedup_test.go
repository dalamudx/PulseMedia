package subtitle

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
	"testing"
)

type resultCandidateProvider []ports.SubtitleCandidateContent

func (p resultCandidateProvider) FindSubtitleCandidates(context.Context, ports.SubtitleVideoHash) ([]ports.SubtitleCandidateContent, error) {
	return p, nil
}

type captureCandidateRepository struct {
	*candidateFakeRepository
	contents []ports.SubtitleCandidateContent
}

func (r *captureCandidateRepository) FinishCandidateQuery(ctx context.Context, t ports.SubtitleCandidateTicket, outcome string, contents []ports.SubtitleCandidateContent) (ports.SubtitleCandidateState, error) {
	r.contents = contents
	return r.candidateFakeRepository.FinishCandidateQuery(ctx, t, outcome, contents)
}

func TestCandidateQueryDeduplicatesNormalizedContent(t *testing.T) {
	for _, scenario := range []string{"equivalent encoding", "different dialogue", "different timing", "invalid candidate", "input limit"} {
		t.Run(scenario, func(t *testing.T) {
			_, base, files, _, actor := candidateUnitFixture()
			repo := &captureCandidateRepository{candidateFakeRepository: base}
			bodies := []string{chineseSRT, chineseSRT}
			want := 1
			outcome := "candidates"
			switch scenario {
			case "equivalent encoding":
				bodies = append(bodies, "\ufeff"+strings.ReplaceAll(strings.Replace(chineseSRT, "1\n", "9\n", 1), "\n", "\r\n"))
			case "different dialogue":
				bodies[1] = strings.Replace(chineseSRT, "你好", "不同的中文字幕", 1)
				want = 2
			case "different timing":
				bodies[1] = strings.ReplaceAll(chineseSRT, "00:00:0", "00:01:0")
				want = 2
			case "invalid candidate":
				bodies[1] = "invalid"
				want = 0
				outcome = "provider_error"
			case "input limit":
				for len(bodies) < 9 {
					bodies = append(bodies, chineseSRT)
				}
				want = 0
				outcome = "provider_error"
			}
			provider := make(resultCandidateProvider, len(bodies))
			for i, b := range bodies {
				provider[i].Content = []byte(b)
			}
			s := NewCandidateService(repo, files, provider)
			out, err := s.Query(t.Context(), actor, base.base.snapshot.Source.FileID, 1, "dedup")
			if err != nil || out.Outcome != outcome || len(repo.contents) != want {
				t.Fatal(out, err, len(repo.contents))
			}
			if want > 0 && string(repo.contents[0].Content) != chineseSRT {
				t.Fatal("first candidate order/content changed")
			}
			if !files.released.Load() {
				t.Fatal("root lock retained")
			}
		})
	}
}

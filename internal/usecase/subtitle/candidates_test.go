package subtitle

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
	"time"
)

type candidateFakeRepository struct {
	base      *fakeRepo
	ticket    ports.SubtitleCandidateTicket
	outcome   string
	permit    bool
	completed *ports.MediaSubtitleCompletion
}

func (r *candidateFakeRepository) ReadCandidates(context.Context, *ports.Actor, uuid.UUID) (ports.SubtitleCandidateState, error) {
	return ports.SubtitleCandidateState{Outcome: r.outcome}, nil
}
func (r *candidateFakeRepository) BeginCandidateQuery(_ context.Context, a *ports.Actor, _ uuid.UUID, _ int64, _ string) (ports.SubtitleCandidateTicket, ports.SubtitleCandidateState, error) {
	r.ticket = ports.SubtitleCandidateTicket{QueryID: uuid.New(), ActorID: uuid.MustParse(a.ID), Snapshot: cloneSnapshot(r.base.snapshot)}
	r.ticket.Lease = ports.JobLease{ID: r.ticket.QueryID, Owner: "test", Token: 1}
	return r.ticket, ports.SubtitleCandidateState{}, nil
}
func (r *candidateFakeRepository) ValidateCandidate(ctx context.Context, t ports.SubtitleCandidateTicket) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return r.base.ValidateSubtitle(ctx, t.Lease, t.Snapshot)
}
func (r *candidateFakeRepository) FinishCandidateQuery(ctx context.Context, t ports.SubtitleCandidateTicket, outcome string, _ []ports.SubtitleCandidateContent) (ports.SubtitleCandidateState, error) {
	if e := r.ValidateCandidate(ctx, t); e != nil {
		return ports.SubtitleCandidateState{}, e
	}
	r.outcome = outcome
	return ports.SubtitleCandidateState{Outcome: outcome}, nil
}
func (r *candidateFakeRepository) BeginCandidateConfirmation(_ context.Context, a *ports.Actor, _ uuid.UUID, _ uuid.UUID, _ int64) (ports.SubtitleCandidateTicket, ports.SubtitleCandidateState, []byte, error) {
	if r.permit {
		return ports.SubtitleCandidateTicket{}, ports.SubtitleCandidateState{Outcome: "confirming"}, nil, nil
	}
	r.permit = true
	r.ticket = ports.SubtitleCandidateTicket{QueryID: uuid.New(), ActorID: uuid.MustParse(a.ID), Snapshot: cloneSnapshot(r.base.snapshot)}
	return r.ticket, ports.SubtitleCandidateState{}, []byte(chineseSRT), nil
}
func (r *candidateFakeRepository) FinishCandidateConfirmation(ctx context.Context, t ports.SubtitleCandidateTicket, c ports.MediaSubtitleCompletion) (ports.SubtitleCandidateState, error) {
	if e := r.ValidateCandidate(ctx, t); e != nil {
		return ports.SubtitleCandidateState{}, e
	}
	r.completed = &c
	r.outcome = "confirmed"
	if c.Publication.Disposition == ports.SidecarPreservedRegular {
		r.outcome = "already_present"
	}
	return ports.SubtitleCandidateState{Outcome: r.outcome}, nil
}

type candidateFakeProvider struct {
	f      *fakeFiles
	error  error
	empty  bool
	calls  int
	onFind func()
}

func (p *candidateFakeProvider) FindSubtitleCandidates(context.Context, ports.SubtitleVideoHash) ([]ports.SubtitleCandidateContent, error) {
	p.calls++
	if !p.f.locked || p.f.released.Load() {
		return nil, ports.ErrFileIdentity
	}
	if p.onFind != nil {
		p.onFind()
	}
	if p.error != nil || p.empty {
		return nil, p.error
	}
	return []ports.SubtitleCandidateContent{{Content: []byte(chineseSRT)}}, nil
}
func candidateUnitFixture() (*CandidateService, *candidateFakeRepository, *fakeFiles, *candidateFakeProvider, *ports.Actor) {
	_, base, f, _ := fixture()
	repo := &candidateFakeRepository{base: base}
	provider := &candidateFakeProvider{f: f}
	return NewCandidateService(repo, f, provider), repo, f, provider, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
}
func TestCandidateQuerySeparatesOutcomesAndRefusesChangedSource(t *testing.T) {
	for _, scenario := range []string{"candidates", "not_found", "provider_error", "already_present", "source_changed", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, r, f, p, a := candidateUnitFixture()
			switch scenario {
			case "not_found":
				p.empty = true
			case "provider_error":
				p.error = ports.ErrTimeout
			case "already_present":
				id := ports.FileIdentity{Device: 1, Inode: 44, Size: 99}
				f.existing = &id
			case "source_changed":
				p.onFind = func() { f.replaceSource = true }
			case "revoked":
				p.onFind = func() { r.base.invalid = true }
			}
			out, e := s.Query(t.Context(), a, r.base.snapshot.Source.FileID, 1, "query")
			if scenario == "source_changed" || scenario == "revoked" {
				if e == nil || r.outcome != "" {
					t.Fatal("changed authority completed", out, e)
				}
			} else if e != nil || out.Outcome != scenario {
				t.Fatal("outcome", out, e)
			}
			if scenario == "already_present" && p.calls != 0 {
				t.Fatal("existing subtitle contacted provider")
			}
			if !f.released.Load() {
				t.Fatal("root slot leaked")
			}
		})
	}
}
func TestCandidateConfirmationExplicitOnceAndPreservesFiles(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{true: "preserved", false: "created"}[existing], func(t *testing.T) {
			s, r, f, _, a := candidateUnitFixture()
			if existing {
				id := ports.FileIdentity{Device: 1, Inode: 33, Size: 67}
				f.existing = &id
			}
			out, e := s.Confirm(t.Context(), a, r.base.snapshot.Source.FileID, uuid.New(), 1)
			if e != nil || r.completed == nil {
				t.Fatal(out, e)
			}
			want := 1
			if existing {
				want = 0
			}
			if f.publishCalls != want {
				t.Fatal("unexpected publication", f.publishCalls)
			}
			_, e = s.Confirm(t.Context(), a, r.base.snapshot.Source.FileID, uuid.New(), 1)
			if e != nil || f.publishCalls != want {
				t.Fatal("replayed publication", e)
			}
		})
	}
}
func TestCandidateCancelledPublicationRetainsPhysicalSlot(t *testing.T) {
	s, r, f, _, a := candidateUnitFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	f.onPublish = func() { close(entered); <-release }
	go func() { _, e := s.Confirm(ctx, a, r.base.snapshot.Source.FileID, uuid.New(), 1); done <- e }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("publication not entered")
	}
	cancel()
	if f.released.Load() {
		t.Error("physical slot released before I/O returned")
	}
	select {
	case <-done:
		t.Error("returned before physical I/O")
	default:
	}
	close(release)
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if !f.released.Load() || r.completed != nil {
		t.Fatal("late completion or leaked slot")
	}
}

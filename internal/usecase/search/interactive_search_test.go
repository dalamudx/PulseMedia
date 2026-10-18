package search_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

type interactiveRepo struct {
	ports.InteractiveSearchRepository
	prepared ports.PreparedInteractiveSearch
	actor    *ports.Actor
	header   *ports.InteractiveSearchHeader
	snapshot *ports.InteractiveSearchSnapshot
	err      error
	hashes   []string
	keys     []string
	actions  []string
	ids      []uuid.UUID
	calls    int
}

func (r *interactiveRepo) CreateInteractiveSearch(_ context.Context, actor *ports.Actor, p ports.PreparedInteractiveSearch, key, hash string) (*ports.InteractiveSearchHeader, error) {
	r.calls++
	r.actor = actor
	r.prepared = p
	r.hashes = append(r.hashes, hash)
	r.keys = append(r.keys, key)
	return r.header, r.err
}
func (r *interactiveRepo) ReadInteractiveSearch(_ context.Context, actor *ports.Actor, id uuid.UUID) (*ports.InteractiveSearchSnapshot, error) {
	r.calls++
	r.actor = actor
	r.ids = append(r.ids, id)
	return r.snapshot, r.err
}
func (r *interactiveRepo) CancelInteractiveSearch(_ context.Context, actor *ports.Actor, id uuid.UUID, key, hash string) (*ports.InteractiveSearchHeader, error) {
	r.calls++
	r.actor = actor
	r.ids = append(r.ids, id)
	r.keys = append(r.keys, key)
	r.hashes = append(r.hashes, hash)
	return r.header, r.err
}
func (r *interactiveRepo) ReadInteractiveSearchReceipt(_ context.Context, actor *ports.Actor, key, action string) (*ports.InteractiveSearchHeader, error) {
	r.calls++
	r.actor = actor
	r.keys = append(r.keys, key)
	r.actions = append(r.actions, action)
	return r.header, r.err
}

type interactiveSecrets struct {
	err     error
	empty   bool
	digests int
	before  func()
}

func (c *interactiveSecrets) EncryptSecret([]byte) ([]byte, error) {
	panic("search input must only use keyed digest")
}
func (c *interactiveSecrets) DigestSecret(raw []byte) (string, error) {
	c.digests++
	if c.before != nil {
		c.before()
	}
	if c.err != nil {
		return "", c.err
	}
	if c.empty {
		return "", nil
	}
	mac := hmac.New(sha256.New, []byte("interactive-search-test-system-key"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func interactiveActor() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "10000000-0000-0000-0000-000000000001", Role: ports.RoleMember}
}
func interactiveHeader() *ports.InteractiveSearchHeader {
	return &ports.InteractiveSearchHeader{ID: uuid.New(), RankingPolicy: quality.DefaultPolicy()}
}

func TestInteractiveSearchCanonicalCommands(t *testing.T) {
	repo := &interactiveRepo{header: interactiveHeader()}
	codec := &interactiveSecrets{}
	svc := search.NewInteractiveSearchService(repo, codec)
	ctx := context.Background()
	actor := interactiveActor()
	media, sub := uuid.New(), uuid.New()
	inputs := []ports.InteractiveSearchInput{
		{Query: "\u2003电影 2026\u00a0", MediaID: &media, SubscriptionID: &sub},
		{Query: "电影 2026", MediaID: &media, SubscriptionID: &sub},
		{Query: "电影 2027", MediaID: &media, SubscriptionID: &sub},
		{Query: "电影 2026", MediaID: &media},
		{Query: "电影 2026", SubscriptionID: &sub},
	}
	for _, input := range inputs {
		if _, err := svc.Create(ctx, actor, input, "create-key.1"); err != nil {
			t.Fatal(err)
		}
	}
	if repo.hashes[0] != repo.hashes[1] {
		t.Fatal("Unicode whitespace changed semantic hash")
	}
	for i := 2; i < len(inputs); i++ {
		if repo.hashes[i] == repo.hashes[0] {
			t.Fatalf("scope/query not bound: %d", i)
		}
	}
	if !reflect.DeepEqual(repo.prepared.RankingPolicy, quality.DefaultPolicy()) {
		t.Fatal("default policy not supplied")
	}
	createHash := repo.hashes[1]
	id := repo.header.ID
	for _, target := range []uuid.UUID{id, id, uuid.New()} {
		if _, err := svc.Cancel(ctx, actor, target, "cancel-key.1"); err != nil {
			t.Fatal(err)
		}
	}
	n := len(repo.hashes)
	if repo.hashes[n-3] != repo.hashes[n-2] || repo.hashes[n-1] == repo.hashes[n-2] || createHash == repo.hashes[n-2] {
		t.Fatal("cancel digest must bind action and target stably")
	}
	if codec.digests != 8 || repo.keys[0] != "create-key.1" {
		t.Fatal("commands bypassed injected digest/key")
	}
	for _, action := range []string{"create", "cancel"} {
		if _, err := svc.Receipt(ctx, actor, "receipt-key", action); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(repo.actions, []string{"create", "cancel"}) {
		t.Fatal(repo.actions)
	}
}

func TestInteractiveSearchPermissionBoundary(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	cases := []struct {
		name  string
		actor *ports.Actor
		want  error
	}{
		{"missing", nil, ports.ErrUnauthorized},
		{"malformed", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleMember, ID: "broken"}, ports.ErrUnauthorized},
		{"zero", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleMember, ID: uuid.Nil.String()}, ports.ErrUnauthorized},
		{"token", &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin, ID: id.String(), Scopes: []string{}}, ports.ErrForbidden},
		{"system", &ports.Actor{Type: ports.ActorTypeSystem, Role: ports.RoleAdmin, ID: id.String()}, ports.ErrForbidden},
		{"service", &ports.Actor{Type: ports.ActorTypeService, Role: ports.RoleAdmin, ID: id.String()}, ports.ErrForbidden},
		{"inactive-role", &ports.Actor{Type: ports.ActorTypeUser, Role: "disabled", ID: id.String()}, ports.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &interactiveRepo{header: interactiveHeader()}
			codec := &interactiveSecrets{}
			svc := search.NewInteractiveSearchService(repo, codec)
			_, a := svc.Create(ctx, tc.actor, ports.InteractiveSearchInput{Query: "q"}, "valid-key")
			_, b := svc.Read(ctx, tc.actor, id)
			_, c := svc.Cancel(ctx, tc.actor, id, "valid-key")
			_, d := svc.Receipt(ctx, tc.actor, "valid-key", "create")
			for _, err := range []error{a, b, c, d} {
				if err != tc.want {
					t.Fatalf("got %v want %v", err, tc.want)
				}
			}
			if repo.calls != 0 || codec.digests != 0 {
				t.Fatal("invalid actor reached dependency")
			}
		})
	}
	for _, role := range []string{ports.RoleMember, ports.RoleAdmin} {
		actor := interactiveActor()
		actor.Role = role
		repo := &interactiveRepo{err: fmt.Errorf("revoked private query: %w", ports.ErrUserDisabled)}
		svc := search.NewInteractiveSearchService(repo, &interactiveSecrets{})
		_, a := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: "q"}, "valid-key")
		_, b := svc.Read(ctx, actor, id)
		_, c := svc.Cancel(ctx, actor, id, "valid-key")
		_, d := svc.Receipt(ctx, actor, "valid-key", "cancel")
		for _, err := range []error{a, b, c, d} {
			if err != ports.ErrUserDisabled {
				t.Fatalf("current DB authority lost: %v", err)
			}
		}
		if repo.calls != 4 {
			t.Fatal("member/admin must reach repository revalidation")
		}
	}
}

func TestInteractiveSearchInputValidation(t *testing.T) {
	ctx := context.Background()
	actor := interactiveActor()
	zero := uuid.Nil
	for _, query := range []string{"", "\u2003", "line\n", "\tq", "a\x00b", "a\u0085b", string([]byte{0xff}), strings.Repeat("界", 257), strings.Repeat("a", 2049)} {
		repo := &interactiveRepo{}
		svc := search.NewInteractiveSearchService(repo, &interactiveSecrets{})
		if _, err := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: query}, "valid-key"); err != ports.ErrRejected || repo.calls != 0 {
			t.Fatalf("query validation %q: %v", query, err)
		}
	}
	for _, input := range []ports.InteractiveSearchInput{{Query: "q", MediaID: &zero}, {Query: "q", SubscriptionID: &zero}} {
		if _, err := search.NewInteractiveSearchService(&interactiveRepo{}, &interactiveSecrets{}).Create(ctx, actor, input, "valid-key"); err != ports.ErrRejected {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"short", strings.Repeat("a", 131), "has space", "bad/key1", "nonascii界", "control\n"} {
		repo := &interactiveRepo{}
		svc := search.NewInteractiveSearchService(repo, &interactiveSecrets{})
		_, a := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: "q"}, key)
		_, b := svc.Cancel(ctx, actor, uuid.New(), key)
		_, c := svc.Receipt(ctx, actor, key, "create")
		if a != ports.ErrRejected || b != ports.ErrRejected || c != ports.ErrRejected || repo.calls != 0 {
			t.Fatal("key accepted", key)
		}
	}
	repo := &interactiveRepo{header: interactiveHeader()}
	svc := search.NewInteractiveSearchService(repo, &interactiveSecrets{})
	for _, query := range []string{"q", strings.Repeat("界", 256)} {
		for _, key := range []string{"aA0-_.:z", strings.Repeat("a", 130)} {
			if _, err := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: query}, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := svc.Read(ctx, actor, uuid.Nil); err != ports.ErrRejected {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, actor, uuid.Nil, "valid-key"); err != ports.ErrRejected {
		t.Fatal(err)
	}
	if _, err := svc.Receipt(ctx, actor, "valid-key", "delete"); err != ports.ErrRejected {
		t.Fatal(err)
	}
}

func TestInteractiveSearchErrorSuppression(t *testing.T) {
	ctx := context.Background()
	actor := interactiveActor()
	id := uuid.New()
	for _, codec := range []*interactiveSecrets{{err: errors.New("codec https://private/passkey query")}, {empty: true}} {
		repo := &interactiveRepo{header: interactiveHeader()}
		svc := search.NewInteractiveSearchService(repo, codec)
		_, a := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: "private query"}, "valid-key")
		_, b := svc.Cancel(ctx, actor, id, "valid-key")
		if a != ports.ErrUnavailable || b != ports.ErrUnavailable || repo.calls != 0 {
			t.Fatal("codec detail escaped", a, b)
		}
	}
	for _, want := range []error{ports.ErrInteractiveSearchConflict, ports.ErrCommandConflict, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrUnavailable, context.Canceled, context.DeadlineExceeded} {
		repo := &interactiveRepo{err: fmt.Errorf("secret URL query: %w", want)}
		svc := search.NewInteractiveSearchService(repo, &interactiveSecrets{})
		_, a := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: "q"}, "valid-key")
		_, b := svc.Read(ctx, actor, id)
		_, c := svc.Cancel(ctx, actor, id, "valid-key")
		_, d := svc.Receipt(ctx, actor, "valid-key", "cancel")
		for _, err := range []error{a, b, c, d} {
			if err != want {
				t.Fatalf("unsafe/wrong error %v want %v", err, want)
			}
		}
	}
	for _, repo := range []*interactiveRepo{{}, {err: errors.New("raw URL private query")}} {
		svc := search.NewInteractiveSearchService(repo, &interactiveSecrets{})
		_, a := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: "q"}, "valid-key")
		_, b := svc.Read(ctx, actor, id)
		_, c := svc.Cancel(ctx, actor, id, "valid-key")
		_, d := svc.Receipt(ctx, actor, "valid-key", "cancel")
		for _, err := range []error{a, b, c, d} {
			if err != ports.ErrUnavailable {
				t.Fatal("nil/raw result", err)
			}
		}
	}
	var svc *search.InteractiveSearchService
	if _, err := svc.Read(ctx, actor, id); err != ports.ErrUnavailable {
		t.Fatal(err)
	}
}

func TestInteractiveSearchCloneIsolation(t *testing.T) {
	ctx := context.Background()
	actor := interactiveActor()
	media, sub := uuid.New(), uuid.New()
	originalMedia, originalSub := media, sub
	now := time.Now()
	info := "hash"
	header := interactiveHeader()
	header.MediaID = &originalMedia
	header.SubscriptionID = &originalSub
	header.CancelledAt = &now
	repo := &interactiveRepo{header: header}
	codec := &interactiveSecrets{before: func() { media = uuid.New(); sub = uuid.New(); actor.Role = "changed" }}
	svc := search.NewInteractiveSearchService(repo, codec)
	result, err := svc.Create(ctx, actor, ports.InteractiveSearchInput{Query: "q", MediaID: &media, SubscriptionID: &sub}, "valid-key")
	if err != nil {
		t.Fatal(err)
	}
	if *repo.prepared.MediaID != originalMedia || *repo.prepared.SubscriptionID != originalSub || repo.actor.Role != ports.RoleMember {
		t.Fatal("caller mutation changed prepared command")
	}
	*repo.prepared.MediaID = uuid.Nil
	repo.actor.Role = "repo-changed"
	if media == uuid.Nil || actor.Role == "repo-changed" {
		t.Fatal("repo mutated caller")
	}
	*result.MediaID = uuid.Nil
	*result.SubscriptionID = uuid.Nil
	*result.CancelledAt = time.Time{}
	result.RankingPolicy.ResolutionOrder[quality.Res2160p] = 1
	result.RankingPolicy.SourceOrder[quality.SourceRemux] = 1
	if *header.MediaID == uuid.Nil || *header.SubscriptionID == uuid.Nil || header.CancelledAt.IsZero() || header.RankingPolicy.ResolutionOrder[quality.Res2160p] != 400 || header.RankingPolicy.SourceOrder[quality.SourceRemux] != 50 {
		t.Fatal("header aliases repository")
	}
	repo.snapshot = &ports.InteractiveSearchSnapshot{Header: *header, State: "running", Sources: []ports.InteractiveSearchSource{{State: "running", QueryIDs: []uuid.UUID{uuid.New()}}}, Candidates: []ports.InteractiveSearchCandidate{{Candidate: ports.SearchCandidate{ID: uuid.New(), LinkEncrypted: []byte{1}, QualityProfile: []byte(`{}`), InfoHash: &info, PublishDate: &now, MatchedMediaID: &originalMedia, MatchedUnitID: &originalSub}}}}
	actor = interactiveActor()
	out, err := svc.Read(ctx, actor, header.ID)
	if err != nil {
		t.Fatal(err)
	}
	out.Sources[0].State = "mutated"
	out.Sources[0].QueryIDs[0] = uuid.Nil
	out.Candidates[0].Candidate.LinkEncrypted[0] = 9
	out.Candidates[0].Candidate.QualityProfile[0] = 'x'
	*out.Candidates[0].Candidate.InfoHash = "changed"
	*out.Candidates[0].Candidate.PublishDate = time.Time{}
	*out.Candidates[0].Candidate.MatchedMediaID = uuid.Nil
	*out.Candidates[0].Candidate.MatchedUnitID = uuid.Nil
	out.Header.RankingPolicy.SourceOrder[quality.SourceRemux] = 2
	stored := repo.snapshot
	if stored.Sources[0].State != "running" || stored.Sources[0].QueryIDs[0] == uuid.Nil || stored.Candidates[0].Candidate.LinkEncrypted[0] != 1 || string(stored.Candidates[0].Candidate.QualityProfile) != "{}" || info != "hash" || now.IsZero() || originalMedia == uuid.Nil || originalSub == uuid.Nil || stored.Header.RankingPolicy.SourceOrder[quality.SourceRemux] != 50 {
		t.Fatal("snapshot aliases repository")
	}
	for _, action := range []string{"create", "cancel"} {
		out, err := svc.Receipt(ctx, actor, "valid-key", action)
		if err != nil {
			t.Fatal(err)
		}
		*out.MediaID = uuid.Nil
		if *header.MediaID == uuid.Nil {
			t.Fatal("receipt aliases header")
		}
	}
	outCancel, err := svc.Cancel(ctx, actor, header.ID, "valid-key")
	if err != nil {
		t.Fatal(err)
	}
	*outCancel.SubscriptionID = uuid.Nil
	if *header.SubscriptionID == uuid.Nil {
		t.Fatal("cancel aliases header")
	}
}

func TestInteractiveSearchReadStatesAndGlobalCap(t *testing.T) {
	ctx := context.Background()
	actor := interactiveActor()
	header := interactiveHeader()
	for _, state := range []string{"running", "failed", "complete", "cancelled"} {
		snapshot := &ports.InteractiveSearchSnapshot{Header: *header, State: state, Sources: []ports.InteractiveSearchSource{{State: "failed", Reason: "unavailable", Partial: true}}}
		svc := search.NewInteractiveSearchService(&interactiveRepo{snapshot: snapshot}, nil)
		out, err := svc.Read(ctx, actor, header.ID)
		if err != nil {
			t.Fatal(err)
		}
		if out.State != state || !reflect.DeepEqual(out.Sources, snapshot.Sources) || out.TotalCandidates != 0 || out.Truncated || out.StatisticsMode != "latest_persisted" {
			t.Fatalf("empty state misrepresented: %+v", out)
		}
	}
	for _, count := range []int{500, 501, 650} {
		snapshot := &ports.InteractiveSearchSnapshot{Header: *header, State: "running"}
		for i := 0; i < count; i++ {
			snapshot.Candidates = append(snapshot.Candidates, ports.InteractiveSearchCandidate{Candidate: ports.SearchCandidate{ID: uuid.New(), Seeders: int32(i)}})
		}
		svc := search.NewInteractiveSearchService(&interactiveRepo{snapshot: snapshot}, nil)
		out, err := svc.Read(ctx, actor, header.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Candidates) != 500 || out.TotalCandidates != count || out.Truncated != (count > 500) || out.Partial != (count > 500) || out.Candidates[0].Candidate.Seeders != int32(count-1) || out.Candidates[499].Candidate.Seeders != int32(count-500) {
			t.Fatalf("cap before global sort: %+v", out)
		}
		if snapshot.Candidates[0].Candidate.Seeders != 0 || snapshot.Partial {
			t.Fatal("read mutated source snapshot")
		}
	}
	repo := &interactiveRepo{snapshot: &ports.InteractiveSearchSnapshot{Header: *header, Partial: true}}
	out, err := search.NewInteractiveSearchService(repo, nil).Read(ctx, actor, header.ID)
	if err != nil || !out.Partial {
		t.Fatal("persisted partial lost", err)
	}
	repo.snapshot.Header.RankingPolicy.Revision = 0
	if _, err := search.NewInteractiveSearchService(repo, nil).Read(ctx, actor, header.ID); err != ports.ErrUnavailable {
		t.Fatal("invalid frozen policy must fail safely", err)
	}
}

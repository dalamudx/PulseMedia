package search_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

type managementCall struct {
	mutation  ports.IndexerMutation
	key, hash string
}
type managementRepo struct {
	calls   []managementCall
	lists   int
	err     error
	views   []ports.IndexerManagementView
	actor   *ports.Actor
	receipt *ports.IndexerManagementReceipt
}

func (r *managementRepo) MutateIndexer(_ context.Context, actor *ports.Actor, mutation ports.IndexerMutation, key, hash string) (*ports.IndexerManagementReceipt, error) {
	r.actor = actor
	r.calls = append(r.calls, managementCall{mutation, key, hash})
	if r.err != nil {
		return nil, r.err
	}
	if r.receipt != nil {
		return r.receipt, nil
	}
	return &ports.IndexerManagementReceipt{Action: mutation.Action, Deleted: mutation.Action == ports.IndexerDelete, Indexer: ports.IndexerManagementView{ID: mutation.IndexerID, Name: mutation.Name, URL: mutation.Configuration.DisplayURL}}, nil
}
func (r *managementRepo) ListManagedIndexers(_ context.Context, actor *ports.Actor) ([]ports.IndexerManagementView, error) {
	r.lists++
	r.actor = actor
	return r.views, r.err
}
func managementActor() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: "260b6959-e847-4f16-87df-55cc86c5a156", Role: ports.RoleAdmin}
}
func managementCodec() *crypto.SecretProtector {
	return crypto.NewSecretProtector(bytes.Repeat([]byte{0x31}, 32))
}
func managementString(value string) *string { return &value }
func managementInput() ports.IndexerManagementInput {
	return ports.IndexerManagementInput{Name: "站点", Protocol: ports.IndexerProtocolTorznab, URL: managementString("https://private:password@localhost:8443/secret/path?passkey=private-key"), Credential: managementString(" session=private-cookie "), Categories: []int{2000, 5000}}
}

func TestIndexerManagementCreateEnvelopeAndRetry(t *testing.T) {
	ctx := context.Background()
	repo := &managementRepo{}
	codec := managementCodec()
	service := search.NewIndexerManagementService(repo, codec)
	input := managementInput()
	actor := managementActor()
	for range 2 {
		if _, err := service.Create(ctx, actor, input, "create-key-01"); err != nil {
			t.Fatal(err)
		}
	}
	first, second := repo.calls[0], repo.calls[1]
	if first.hash == "" || first.hash != second.hash || first.key != "create-key-01" || first.mutation.IndexerID != uuid.Nil || repo.actor != actor {
		t.Fatal("create did not preserve stable command identity")
	}
	if bytes.Equal(first.mutation.Configuration.URLCiphertext, second.mutation.Configuration.URLCiphertext) || bytes.Equal(first.mutation.Configuration.CredentialCiphertext, second.mutation.Configuration.CredentialCiphertext) {
		t.Fatal("expected randomized system envelopes")
	}
	for _, call := range repo.calls {
		c := call.mutation.Configuration
		if call.mutation.Action != ports.IndexerCreate || call.mutation.PreserveURL || call.mutation.PreserveCredential || c.DisplayURL != "https://localhost:8443" || c.Protocol != input.Protocol {
			t.Fatal("incorrect create configuration")
		}
		for _, pair := range []struct {
			cipher []byte
			plain  string
		}{{c.URLCiphertext, *input.URL}, {c.CredentialCiphertext, *input.Credential}} {
			plain, err := codec.DecryptSecret(pair.cipher)
			if err != nil || string(plain) != pair.plain {
				t.Fatal("secret lost exact bytes")
			}
		}
	}
	input.Categories[0] = 1234
	if first.mutation.Configuration.Categories[0] != 2000 {
		t.Fatal("configuration aliases input categories")
	}
	first.mutation.Configuration.Categories[1] = 4321
	if input.Categories[1] != 5000 || second.mutation.Configuration.Categories[1] != 5000 {
		t.Fatal("categories alias another invocation")
	}
	input = managementInput()
	input.Credential = nil
	input.Categories = []int{}
	if _, err := service.Create(ctx, actor, input, "public-feed-key"); err != nil {
		t.Fatal(err)
	}
	plain, err := codec.DecryptSecret(repo.calls[2].mutation.Configuration.CredentialCiphertext)
	if err != nil || len(plain) != 0 || repo.calls[2].mutation.PreserveCredential {
		t.Fatal("missing create credential must encrypt empty bytes")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "URL") || strings.Contains(string(raw), "Credential") {
		t.Fatal("input JSON exposes secrets")
	}
}

func TestIndexerManagementUpdateIntentAndHash(t *testing.T) {
	ctx := context.Background()
	repo := &managementRepo{}
	codec := managementCodec()
	service := search.NewIndexerManagementService(repo, codec)
	actor := managementActor()
	id := uuid.MustParse("4d90bce7-7c8a-4484-9cda-1cedcab4f597")
	base := managementInput()
	base.ExpectedVersion = 7
	base.URL = nil
	base.Credential = nil
	if _, err := service.Update(ctx, actor, id, base, "update-key-01"); err != nil {
		t.Fatal(err)
	}
	preserved := repo.calls[0].mutation
	if !preserved.PreserveURL || !preserved.PreserveCredential || len(preserved.Configuration.URLCiphertext) != 0 || len(preserved.Configuration.CredentialCiphertext) != 0 || preserved.Configuration.DisplayURL != "" {
		t.Fatal("preserve sent replacement secret data")
	}
	changes := []struct {
		name   string
		change func(*ports.IndexerManagementInput)
		target uuid.UUID
	}{
		{"retry", func(*ports.IndexerManagementInput) {}, id},
		{"clear credential", func(i *ports.IndexerManagementInput) { i.Credential = managementString("") }, id},
		{"replace URL", func(i *ports.IndexerManagementInput) { i.URL = managementString("http://localhost/exact?secret=yes") }, id},
		{"protocol", func(i *ports.IndexerManagementInput) { i.Protocol = ports.IndexerProtocolRSS }, id},
		{"version", func(i *ports.IndexerManagementInput) { i.ExpectedVersion++ }, id},
		{"name", func(i *ports.IndexerManagementInput) { i.Name = "Another" }, id},
		{"categories", func(i *ports.IndexerManagementInput) { i.Categories = []int{} }, id},
		{"category order", func(i *ports.IndexerManagementInput) { i.Categories = []int{5000, 2000} }, id},
		{"target", func(*ports.IndexerManagementInput) {}, uuid.MustParse("4d90bce7-7c8a-4484-9cda-1cedcab4f598")},
	}
	hashes := map[string]bool{repo.calls[0].hash: true}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			tc.change(&input)
			if _, err := service.Update(ctx, actor, tc.target, input, "update-key-01"); err != nil {
				t.Fatal(err)
			}
			call := repo.calls[len(repo.calls)-1]
			if tc.name == "retry" {
				if call.hash != repo.calls[0].hash {
					t.Fatal("retry changed semantic hash")
				}
				return
			}
			if hashes[call.hash] {
				t.Fatal("distinct semantic intent reused hash")
			}
			hashes[call.hash] = true
			if tc.name == "clear credential" {
				plain, err := codec.DecryptSecret(call.mutation.Configuration.CredentialCiphertext)
				if err != nil || len(plain) != 0 || call.mutation.PreserveCredential {
					t.Fatal("clear must replace with encrypted empty bytes")
				}
			}
			if tc.name == "replace URL" {
				plain, err := codec.DecryptSecret(call.mutation.Configuration.URLCiphertext)
				if err != nil || string(plain) != *input.URL || call.mutation.PreserveURL {
					t.Fatal("replacement URL was altered")
				}
			}
		})
	}
	// Delete binds the action and version while carrying no configuration secrets.
	for _, version := range []int64{7, 7, 8} {
		if _, err := service.Delete(ctx, actor, id, version, "update-key-01"); err != nil {
			t.Fatal(err)
		}
	}
	n := len(repo.calls)
	a, b, c := repo.calls[n-3], repo.calls[n-2], repo.calls[n-1]
	if a.hash != b.hash || a.hash == c.hash || hashes[a.hash] || a.mutation.Action != ports.IndexerDelete || !reflect.DeepEqual(a.mutation.Configuration, ports.PreparedIndexerConfiguration{}) || a.mutation.Name != "" || a.mutation.PreserveURL || a.mutation.PreserveCredential {
		t.Fatal("delete command contains unrelated input or unstable identity")
	}
}

func TestIndexerManagementRejectsBeforeRepository(t *testing.T) {
	ctx := context.Background()
	id := uuid.MustParse("4d90bce7-7c8a-4484-9cda-1cedcab4f597")
	for _, actor := range []*ports.Actor{nil, {Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleMember}, {Type: ports.ActorTypeSystem, Role: ports.RoleAdmin}, {Type: ports.ActorTypeService, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleAdmin, Scopes: []string{"*"}}, {Type: ports.ActorTypeUser, ID: "", Role: ports.RoleAdmin}} {
		repo := &managementRepo{}
		s := search.NewIndexerManagementService(repo, managementCodec())
		_, e1 := s.Create(ctx, actor, managementInput(), "valid-key")
		_, e2 := s.Update(ctx, actor, id, managementInput(), "valid-key")
		_, e3 := s.Delete(ctx, actor, id, 1, "valid-key")
		_, e4 := s.List(ctx, actor)
		for _, err := range []error{e1, e2, e3, e4} {
			if !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) {
				t.Fatal("unauthorized request passed preliminary boundary")
			}
		}
		if len(repo.calls) != 0 || repo.lists != 0 {
			t.Fatal("unauthorized request reached repository")
		}
	}
	invalid := []struct {
		name   string
		change func(*ports.IndexerManagementInput)
	}{
		{"missing URL", func(i *ports.IndexerManagementInput) { i.URL = nil }},
		{"empty URL", func(i *ports.IndexerManagementInput) { i.URL = managementString("") }},
		{"unsafe URL", func(i *ports.IndexerManagementInput) { i.URL = managementString("https://localhost/a?key=%0a") }},
		{"protocol", func(i *ports.IndexerManagementInput) { i.Protocol = "auto" }},
		{"name empty", func(i *ports.IndexerManagementInput) { i.Name = "" }},
		{"name padding", func(i *ports.IndexerManagementInput) { i.Name = " padded " }},
		{"name control", func(i *ports.IndexerManagementInput) { i.Name = "bad\nname" }},
		{"name UTF8", func(i *ports.IndexerManagementInput) { i.Name = string([]byte{0xff}) }},
		{"name bound", func(i *ports.IndexerManagementInput) { i.Name = strings.Repeat("站", 257) }},
		{"categories absent", func(i *ports.IndexerManagementInput) { i.Categories = nil }},
		{"categories duplicate", func(i *ports.IndexerManagementInput) { i.Categories = []int{2000, 2000} }},
		{"category negative", func(i *ports.IndexerManagementInput) { i.Categories = []int{-1} }},
		{"credential control", func(i *ports.IndexerManagementInput) { i.Credential = managementString("bad\nsecret") }},
		{"create version", func(i *ports.IndexerManagementInput) { i.ExpectedVersion = 1 }},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			repo := &managementRepo{}
			input := managementInput()
			tc.change(&input)
			_, err := search.NewIndexerManagementService(repo, managementCodec()).Create(ctx, managementActor(), input, "valid-key")
			if err != ports.ErrRejected || len(repo.calls) != 0 {
				t.Fatal("invalid create reached repository")
			}
		})
	}
	repo := &managementRepo{}
	s := search.NewIndexerManagementService(repo, managementCodec())
	for _, key := range []string{"", "short", " padded-key", "embedded key", "bad\nkey", "unicode-站点", strings.Repeat("a", 131), "bad/key?x"} {
		input := managementInput()
		if _, err := s.Create(ctx, managementActor(), input, key); err != ports.ErrRejected {
			t.Fatal("invalid create key accepted")
		}
		input.ExpectedVersion = 1
		if _, err := s.Update(ctx, managementActor(), id, input, key); err != ports.ErrRejected {
			t.Fatal("invalid update key accepted")
		}
		if _, err := s.Delete(ctx, managementActor(), id, 1, key); err != ports.ErrRejected {
			t.Fatal("invalid delete key accepted")
		}
	}
	for _, target := range []uuid.UUID{uuid.Nil, id} {
		for _, version := range []int64{-1, 0, 1} {
			if target != uuid.Nil && version == 1 {
				continue
			}
			input := managementInput()
			input.ExpectedVersion = version
			if _, err := s.Update(ctx, managementActor(), target, input, "valid-key"); err != ports.ErrRejected {
				t.Fatal("invalid update fence accepted")
			}
			if _, err := s.Delete(ctx, managementActor(), target, version, "valid-key"); err != ports.ErrRejected {
				t.Fatal("invalid delete fence accepted")
			}
		}
	}
	if len(repo.calls) != 0 {
		t.Fatal("invalid command reached repository")
	}
	input := managementInput()
	input.Name = strings.Repeat("站", 256)
	if _, err := s.Create(ctx, managementActor(), input, strings.Repeat("a", 130)); err != nil {
		t.Fatal("valid upper boundary rejected")
	}
}

type managementFailingCodec struct{ encrypt, digest bool }

func (c managementFailingCodec) EncryptSecret(plain []byte) ([]byte, error) {
	if c.encrypt {
		return nil, errors.New("private codec secret")
	}
	return managementCodec().EncryptSecret(plain)
}
func (c managementFailingCodec) DigestSecret(plain []byte) (string, error) {
	if c.digest {
		return "", errors.New("private digest secret")
	}
	return managementCodec().DigestSecret(plain)
}

func TestIndexerManagementSafeFailuresAndRead(t *testing.T) {
	ctx := context.Background()
	actor := managementActor()
	for _, codec := range []ports.SecretProtector{nil, managementFailingCodec{encrypt: true}, managementFailingCodec{digest: true}, crypto.NewSecretProtector([]byte("invalid-key"))} {
		repo := &managementRepo{}
		_, err := search.NewIndexerManagementService(repo, codec).Create(ctx, actor, managementInput(), "valid-key")
		if err != ports.ErrUnavailable || len(repo.calls) != 0 {
			t.Fatal("codec failure leaked or reached repository")
		}
	}
	for _, s := range []*search.IndexerManagementService{nil, search.NewIndexerManagementService(nil, managementCodec())} {
		if _, err := s.Create(ctx, actor, managementInput(), "valid-key"); err != ports.ErrUnavailable {
			t.Fatal("missing dependency not unavailable")
		}
	}
	for _, failure := range []error{errors.New("postgres DSN and secret"), fmt.Errorf("private context: %w", ports.ErrIndexerCommandConflict), fmt.Errorf("private context: %w", ports.ErrForbidden), context.Canceled} {
		repo := &managementRepo{err: failure}
		s := search.NewIndexerManagementService(repo, managementCodec())
		_, err := s.Create(ctx, actor, managementInput(), "valid-key")
		want := ports.ErrUnavailable
		for _, safe := range []error{ports.ErrIndexerCommandConflict, ports.ErrForbidden, context.Canceled} {
			if errors.Is(failure, safe) {
				want = safe
			}
		}
		if err != want {
			t.Fatal("repository error did not map to safe sentinel")
		}
		if _, err = s.List(ctx, actor); err != want {
			t.Fatal("list error did not map to safe sentinel")
		}
	}
	receipt := &ports.IndexerManagementReceipt{Action: ports.IndexerCreate, Indexer: ports.IndexerManagementView{ID: uuid.MustParse("4d90bce7-7c8a-4484-9cda-1cedcab4f597"), ConfigVersion: 11}}
	repo := &managementRepo{receipt: receipt}
	s := search.NewIndexerManagementService(repo, nil)
	views, err := s.List(ctx, actor)
	if err != nil || views == nil || len(views) != 0 || repo.actor != actor {
		t.Fatal("list requires unnecessary crypto or lost empty array")
	}
	repo.views = []ports.IndexerManagementView{receipt.Indexer}
	views, err = s.List(ctx, actor)
	if err != nil || !reflect.DeepEqual(views, repo.views) {
		t.Fatal("list changed repository view")
	}
	got, err := search.NewIndexerManagementService(repo, managementCodec()).Create(ctx, actor, managementInput(), "valid-key")
	if err != nil || got != receipt {
		t.Fatal("service did not return authoritative receipt")
	}
}

type managementCallbackCodec struct {
	callback  func()
	canonical []byte
}

func (c *managementCallbackCodec) EncryptSecret(plain []byte) ([]byte, error) {
	if c.callback != nil {
		c.callback()
		c.callback = nil
	}
	return managementCodec().EncryptSecret(plain)
}
func (c *managementCallbackCodec) DigestSecret(plain []byte) (string, error) {
	c.canonical = bytes.Clone(plain)
	return managementCodec().DigestSecret(plain)
}
func TestIndexerManagementFreezesInputBeforeCodec(t *testing.T) {
	input := managementInput()
	originalURL, originalCredential := *input.URL, *input.Credential
	codec := &managementCallbackCodec{callback: func() {
		*input.URL = "http://changed.invalid"
		*input.Credential = "changed"
		input.Categories[0] = 1234
	}}
	repo := &managementRepo{}
	_, err := search.NewIndexerManagementService(repo, codec).Create(context.Background(), managementActor(), input, "valid-key")
	if err != nil {
		t.Fatal(err)
	}
	call := repo.calls[0]
	plain, err := managementCodec().DecryptSecret(call.mutation.Configuration.CredentialCiphertext)
	if err != nil || string(plain) != originalCredential || call.mutation.Configuration.Categories[0] != 2000 {
		t.Fatal("injected codec mutation changed frozen input")
	}
	var canonical struct {
		Contract        string
		URL, Credential *string
		Categories      []int
	}
	if err := json.Unmarshal(codec.canonical, &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical.Contract != "indexer-management.v1" || canonical.URL == nil || *canonical.URL != originalURL || canonical.Credential == nil || *canonical.Credential != originalCredential || canonical.Categories[0] != 2000 {
		t.Fatal("digest omitted secret intent or used mutated input")
	}
	expected, err := managementCodec().DigestSecret(codec.canonical)
	if err != nil || expected != call.hash {
		t.Fatal("request hash did not use system keyed digest")
	}
}

func TestIndexerManagementListTemplates(t *testing.T) {
	service := search.NewIndexerManagementService(nil, nil)

	// 1. Unauthorized
	if _, err := service.ListTemplates(t.Context(), nil); !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}

	// 2. Token caller rejected
	tokenActor := &ports.Actor{ID: "00000000-0000-0000-0000-000000000001", Type: ports.ActorTypeUser, Role: ports.RoleAdmin, Scopes: []string{}}
	if _, err := service.ListTemplates(t.Context(), tokenActor); !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for token actor, got %v", err)
	}

	// 3. Non-user actor rejected
	sysActor := &ports.Actor{ID: "00000000-0000-0000-0000-000000000001", Type: ports.ActorTypeSystem, Role: ports.RoleAdmin}
	if _, err := service.ListTemplates(t.Context(), sysActor); !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for system actor, got %v", err)
	}

	// 4. Unknown role rejected
	guestActor := &ports.Actor{ID: "00000000-0000-0000-0000-000000000001", Type: ports.ActorTypeUser, Role: "guest"}
	if _, err := service.ListTemplates(t.Context(), guestActor); !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for guest role, got %v", err)
	}

	// 5. Admin succeeds
	adminActor := &ports.Actor{ID: "00000000-0000-0000-0000-000000000001", Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	templates, err := service.ListTemplates(t.Context(), adminActor)
	if err != nil {
		t.Fatalf("admin list failed: %v", err)
	}
	if len(templates) < 20 {
		t.Fatalf("expected >= 20 templates, got %d", len(templates))
	}

	// Verify Prowlarr template structure
	var foundProwlarr bool
	for _, tmpl := range templates {
		if tmpl.ID == "prowlarr" {
			foundProwlarr = true
			if tmpl.Protocol != ports.IndexerProtocolTorznab {
				t.Errorf("prowlarr protocol: expected torznab, got %s", tmpl.Protocol)
			}
			if len(tmpl.RecommendedCategories) == 0 {
				t.Errorf("prowlarr recommended categories empty")
			}
			if len(tmpl.CategoryMappings) == 0 {
				t.Errorf("prowlarr category mappings empty")
			}
		}
	}
	if !foundProwlarr {
		t.Error("prowlarr template not found in output")
	}

	// 6. Member succeeds
	memberActor := &ports.Actor{ID: "00000000-0000-0000-0000-000000000002", Type: ports.ActorTypeUser, Role: ports.RoleMember}
	memberTemplates, err := service.ListTemplates(t.Context(), memberActor)
	if err != nil {
		t.Fatalf("member list failed: %v", err)
	}
	if len(memberTemplates) != len(templates) {
		t.Fatalf("member count mismatch: got %d, want %d", len(memberTemplates), len(templates))
	}
}

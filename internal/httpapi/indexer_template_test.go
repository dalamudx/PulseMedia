package httpapi

import (
	"context"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestListIndexerTemplatesHTTP(t *testing.T) {
	handler := NewServerHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	// 1. Service uninitialized -> 403
	resp, err := handler.ListIndexerTemplates(t.Context(), ListIndexerTemplatesRequestObject{})
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if _, ok := resp.(ListIndexerTemplates403ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 403 for uninitialized service, got %T", resp)
	}

	// Inject indexer management service
	svc := usecaseSearch.NewIndexerManagementService(nil, nil)
	handler.SetIndexerManagementServices(svc, nil)

	// 2. Unauthenticated -> 401
	resp, err = handler.ListIndexerTemplates(t.Context(), ListIndexerTemplatesRequestObject{})
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if _, ok := resp.(ListIndexerTemplates401ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 401 for unauthenticated request, got %T", resp)
	}

	// 3. Token actor -> 403
	tokenCtx := context.WithValue(t.Context(), actorKey, &ports.Actor{
		ID:     "00000000-0000-0000-0000-000000000001",
		Type:   ports.ActorTypeUser,
		Role:   ports.RoleAdmin,
		Scopes: []string{},
	})
	resp, err = handler.ListIndexerTemplates(tokenCtx, ListIndexerTemplatesRequestObject{})
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if _, ok := resp.(ListIndexerTemplates403ApplicationProblemPlusJSONResponse); !ok {
		t.Fatalf("expected 403 for token actor, got %T", resp)
	}

	// 4. Member actor -> 200 with 24 templates
	memberCtx := context.WithValue(t.Context(), actorKey, &ports.Actor{
		ID:   "00000000-0000-0000-0000-000000000002",
		Type: ports.ActorTypeUser,
		Role: ports.RoleMember,
	})
	resp, err = handler.ListIndexerTemplates(memberCtx, ListIndexerTemplatesRequestObject{})
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	memberResult, ok := resp.(ListIndexerTemplates200JSONResponse)
	if !ok {
		t.Fatalf("expected 200 for member actor, got %T", resp)
	}
	if len(memberResult.Templates) < 20 {
		t.Fatalf("expected >= 20 templates, got %d", len(memberResult.Templates))
	}

	// 5. Admin actor -> 200 with complete details
	adminCtx := context.WithValue(t.Context(), actorKey, &ports.Actor{
		ID:   "00000000-0000-0000-0000-000000000001",
		Type: ports.ActorTypeUser,
		Role: ports.RoleAdmin,
	})
	resp, err = handler.ListIndexerTemplates(adminCtx, ListIndexerTemplatesRequestObject{})
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	adminResult, ok := resp.(ListIndexerTemplates200JSONResponse)
	if !ok {
		t.Fatalf("expected 200 for admin actor, got %T", resp)
	}
	if len(adminResult.Templates) != len(memberResult.Templates) {
		t.Fatalf("template count mismatch between admin and member")
	}

	expectedIDs := []string{
		"prowlarr", "jackett", "mteam", "hdsky", "chdbits", "ttg", "hdchina",
		"ourbits", "opencd", "springwood", "hdhome", "pterclub", "soulvoice",
		"audiences", "haidan", "lemonhd", "keepfrds", "discfan", "carpt",
		"redleaves", "iptorrents", "torrentleech", "filelist", "1337x",
	}

	templateMap := make(map[string]IndexerTemplate, len(adminResult.Templates))
	for _, tmpl := range adminResult.Templates {
		templateMap[tmpl.Id] = tmpl
		if tmpl.Name == "" || tmpl.Domain == "" || tmpl.DefaultUrl == "" || tmpl.Description == "" || tmpl.CredentialHelp == "" {
			t.Errorf("template %s has empty text field", tmpl.Id)
		}
		if len(tmpl.RecommendedCategories) == 0 {
			t.Errorf("template %s has empty recommended categories", tmpl.Id)
		}
		if len(tmpl.CategoryMappings) == 0 {
			t.Errorf("template %s has empty category mappings", tmpl.Id)
		}
	}

	for _, id := range expectedIDs {
		if _, exists := templateMap[id]; !exists {
			t.Errorf("missing expected builtin template: %s", id)
		}
	}
}

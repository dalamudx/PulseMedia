package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestM7IndexerTemplatesHTTPPG(t *testing.T) {
	f := organizationSetup(t)
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })

	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	adminPass := fmt.Sprintf("Admin-%s-123!", t.Name())
	admin, _, err := authService.SetupAdmin(t.Context(), "tmpl-admin", adminPass)
	if err != nil {
		t.Fatal(err)
	}

	masterKey := bytes.Repeat([]byte{0x42}, 32)
	protector := crypto.NewSecretProtector(masterKey)
	indexerStore := postgres.NewIndexerManagementStore(f.pool, f.gen, protector)
	managementSvc := usecaseSearch.NewIndexerManagementService(indexerStore, protector)
	receiptSvc := usecaseSearch.NewIndexerManagementReceiptService(indexerStore)

	handler := httpapi.NewServerHandler(a, authService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetIndexerManagementServices(managementSvc, receiptSvc)
	router := httpapi.NewRouter(a, handler, authService, nil)

	// Admin login
	adminLoginBody, _ := json.Marshal(map[string]string{"username": "tmpl-admin", "password": adminPass})
	adminLoginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(adminLoginBody)))
	adminLoginReq.Header.Set("Content-Type", "application/json")
	adminLoginReq.Host, adminLoginReq.RemoteAddr = "localhost", "127.0.0.1:1234"
	adminLoginRec := httptest.NewRecorder()
	router.ServeHTTP(adminLoginRec, adminLoginReq)
	if adminLoginRec.Code != http.StatusOK || len(adminLoginRec.Result().Cookies()) == 0 {
		t.Fatalf("admin login failed: %d", adminLoginRec.Code)
	}
	adminCookie := adminLoginRec.Result().Cookies()[0]

	getTemplates := func(cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/indexer-templates", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// 1. Anonymous -> 401
	anonResp := getTemplates(nil)
	if anonResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous, got %d", anonResp.Code)
	}

	// 2. Admin -> 200 with 24 templates
	adminResp := getTemplates(adminCookie)
	if adminResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d: %s", adminResp.Code, adminResp.Body.String())
	}
	var templateList struct {
		Templates []struct {
			ID                    string `json:"id"`
			Name                  string `json:"name"`
			Domain                string `json:"domain"`
			Protocol              string `json:"protocol"`
			DefaultURL            string `json:"default_url"`
			RecommendedCategories []int  `json:"recommended_categories"`
			CategoryMappings      []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"category_mappings"`
			Description    string `json:"description"`
			CredentialHelp string `json:"credential_help"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(adminResp.Body.Bytes(), &templateList); err != nil {
		t.Fatalf("failed to decode templates response: %v", err)
	}
	if len(templateList.Templates) < 20 {
		t.Fatalf("expected >= 20 templates, got %d", len(templateList.Templates))
	}

	templateMap := make(map[string]any)
	for _, t := range templateList.Templates {
		templateMap[t.ID] = t
	}

	// Check Prowlarr template
	prowlarrRaw, ok := templateMap["prowlarr"]
	if !ok {
		t.Fatal("missing prowlarr template")
	}
	prowlarr := prowlarrRaw.(struct {
		ID                    string `json:"id"`
		Name                  string `json:"name"`
		Domain                string `json:"domain"`
		Protocol              string `json:"protocol"`
		DefaultURL            string `json:"default_url"`
		RecommendedCategories []int  `json:"recommended_categories"`
		CategoryMappings      []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"category_mappings"`
		Description    string `json:"description"`
		CredentialHelp string `json:"credential_help"`
	})
	if prowlarr.Protocol != "torznab" || len(prowlarr.RecommendedCategories) == 0 {
		t.Fatalf("invalid prowlarr template: %+v", prowlarr)
	}

	// Check M-Team template
	mteamRaw, ok := templateMap["mteam"]
	if !ok {
		t.Fatal("missing mteam template")
	}
	mteam := mteamRaw.(struct {
		ID                    string `json:"id"`
		Name                  string `json:"name"`
		Domain                string `json:"domain"`
		Protocol              string `json:"protocol"`
		DefaultURL            string `json:"default_url"`
		RecommendedCategories []int  `json:"recommended_categories"`
		CategoryMappings      []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"category_mappings"`
		Description    string `json:"description"`
		CredentialHelp string `json:"credential_help"`
	})
	if mteam.Protocol != "nexusphp" || len(mteam.RecommendedCategories) == 0 {
		t.Fatalf("invalid mteam template: %+v", mteam)
	}

	// 3. Create indexer using Prowlarr template autofill
	createProwlarrBody, _ := json.Marshal(map[string]any{
		"name":       prowlarr.Name,
		"protocol":   prowlarr.Protocol,
		"url":        prowlarr.DefaultURL,
		"api_key":    "{{SECRET_5z9x6l1d}}",
		"categories": prowlarr.RecommendedCategories,
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/indexers", strings.NewReader(string(createProwlarrBody)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Idempotency-Key", "prowlarr-autofill-key")
	createReq.Host = "localhost"
	createReq.Header.Set("Origin", "http://localhost")
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create indexer via prowlarr template failed: %d: %s", createRec.Code, createRec.Body.String())
	}

	// 4. Create indexer using M-Team template autofill
	createMTeamBody, _ := json.Marshal(map[string]any{
		"name":       mteam.Name,
		"protocol":   mteam.Protocol,
		"url":        mteam.DefaultURL,
		"api_key":    "fake-cookie=token",
		"categories": mteam.RecommendedCategories,
	})
	createMReq := httptest.NewRequest(http.MethodPost, "/api/v1/indexers", strings.NewReader(string(createMTeamBody)))
	createMReq.Header.Set("Content-Type", "application/json")
	createMReq.Header.Set("Idempotency-Key", "mteam-autofill-key")
	createMReq.Host = "localhost"
	createMReq.Header.Set("Origin", "http://localhost")
	createMReq.AddCookie(adminCookie)
	createMRec := httptest.NewRecorder()
	router.ServeHTTP(createMRec, createMReq)
	if createMRec.Code != http.StatusCreated {
		t.Fatalf("create indexer via mteam template failed: %d: %s", createMRec.Code, createMRec.Body.String())
	}

	// 5. Verify created indexers in database
	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM indexer WHERE name IN ($1, $2)", prowlarr.Name, mteam.Name).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected 2 indexers in DB, got %d (err: %v)", count, err)
	}

	// 6. Member can read templates
	if _, err := f.pool.Exec(t.Context(), "UPDATE app_user SET role='member' WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	memberResp := getTemplates(adminCookie)
	if memberResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for member, got %d", memberResp.Code)
	}
}

package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/web"
)

// NewRouter constructs the complete HTTP handler hierarchy with middlewares, OpenAPI endpoints, SSE, and embedded SPA.
func NewRouter(a AppContext, server StrictServerInterface, authSvc *auth.Service, sseHub *SSEHub) http.Handler {
	mux := http.NewServeMux()
	if target, ok := server.(*ServerHandler); ok {
		target.activityHub = sseHub
		target.logHub = sseHub
	}

	// Never use the generated default callbacks: both strict handler errors and
	// parameter parsing may contain raw DB/adapter errors, URLs or credentials.
	badRequest := func(w http.ResponseWriter, r *http.Request, _ error) {
		WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context()))
	}
	strictHandler := NewStrictHandlerWithOptions(server, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest,
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed", GetRequestID(r.Context()))
		},
	})
	HandlerWithOptions(strictHandler, StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: badRequest, Middlewares: []MiddlewareFunc{strictSiteCheckinConfigJSON, strictSiteAccountStatsReadJSON, strictSiteAccountStatsConfigJSON, strictDoubanSyncConfigJSON, strictMediaScrapeJSON, strictSubscriptionCreationJSON, strictInteractiveSearchJSON, strictEndpointConfigurationJSON, strictIndexerManagementJSON, strictLibraryReuseJSON, strictUpgradeOrganizationJSON, strictUpgradeCommandsJSON, strictUpgradePreviewJSON, strictCompleteTVOrganizationJSON, strictTVSelectionJSON, strictCompleteTVAcquisitionJSON, strictEpisodeAcquisitionJSON, strictTVCoverageReviewJSON, strictTransferRecoveryJSON, strictTransferRevisionJSON}})

	// 2. Mount Server-Sent Events endpoint
	if sseHub != nil {
		mux.Handle("/api/v1/events", sseHub)
	}

	// 3. Setup is only available locally, or with an explicit one-time deployment token.
	cookieMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/status" {
			w.Header().Set("Cache-Control", "no-store")
			actor := GetActor(r.Context())
			if actor == nil {
				WriteJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", GetRequestID(r.Context()))
				return
			}
		}
		if r.URL.Path == "/api/v1/auth/setup" && r.Method == http.MethodPost {
			configured := os.Getenv("PULSE_SETUP_TOKEN")
			if (configured != "" && (len(r.Header.Values("X-Setup-Token")) != 1 || subtle.ConstantTimeCompare([]byte(configured), []byte(r.Header.Get("X-Setup-Token"))) != 1)) ||
				(configured == "" && !localSetupRequest(r)) {
				WriteJSONError(w, http.StatusForbidden, "SETUP_RESTRICTED", "setup requires local access or a deployment token", GetRequestID(r.Context()))
				return
			}
		}
		if (r.URL.Path == "/api/v1/auth/setup" || r.URL.Path == "/api/v1/auth/login") && r.Method == http.MethodPost {
			rec := &authResponseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
			mux.ServeHTTP(rec, r)

			if rec.statusCode == http.StatusOK {
				var authResp AuthResponse
				if err := json.Unmarshal(rec.body.Bytes(), &authResp); err == nil && authResp.Token != "" {
					http.SetCookie(w, &http.Cookie{
						Name:     "pulsemedia_session",
						Value:    authResp.Token,
						Path:     "/",
						HttpOnly: true,
						Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
						SameSite: http.SameSiteStrictMode,
						MaxAge:   7 * 24 * 3600,
					})
				}
			}
			rec.flush()
			return
		}

		if r.URL.Path == "/api/v1/auth/logout" && r.Method == http.MethodPost {
			rec := &authResponseRecorder{ResponseWriter: w, body: &bytes.Buffer{}}
			mux.ServeHTTP(rec, r)
			if rec.statusCode == http.StatusNoContent {
				http.SetCookie(w, &http.Cookie{
					Name:     "pulsemedia_session",
					Value:    "",
					Path:     "/",
					HttpOnly: true,
					Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
					SameSite: http.SameSiteStrictMode,
					MaxAge:   -1,
				})
			}
			rec.flush()
			return
		}

		mux.ServeHTTP(w, r)
	})

	// 4. Fallback for unmatched /api/... request: ALWAYS return JSON 404, NEVER HTML SPA
	apiNotFound := func(w http.ResponseWriter, r *http.Request) {
		WriteJSONError(w, http.StatusNotFound, "NOT_FOUND", "endpoint not found", GetRequestID(r.Context()))
	}
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/", apiNotFound)
	// Explicit non-GET health routes must not recurse through the SPA fallback.
	for _, path := range []string{"/healthz", "/readyz"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", "GET, HEAD")
			WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", GetRequestID(r.Context()))
		})
	}

	// 5. SPA Handler for non-API frontend paths
	distFS := web.Dist()
	fileServer := http.FileServer(distFS)

	spaHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strict invariant: API paths never fall back to SPA HTML
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			cookieMux.ServeHTTP(w, r)
			return
		}

		// Try serving static asset if it exists in dist
		cleanPath := strings.TrimPrefix(r.URL.Path, "/")
		if cleanPath != "" {
			if f, err := distFS.Open(cleanPath); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Non-API routes fall back to index.html
		if indexFile, err := distFS.Open("index.html"); err == nil {
			_ = indexFile.Close()
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}

		cookieMux.ServeHTTP(w, r)
	})

	// Global middleware chain: RequestID -> Recovery -> Logging -> Idempotency -> Auth
	chain := RequestIDMiddleware(a.IDGen())(
		ProblemResponseMiddleware()(RecoveryMiddleware(a.Logger())(
			LoggingMiddleware(a.Logger())(
				IdempotencyMiddleware()(
					authAdmissionMiddleware(a.Clock())(AuthMiddleware(authSvc)(APITokenScopeMiddleware()(CSRFMiddleware()(strictTokenManagementJSON(activityChanges(sseHub, spaHandler)))))),
				),
			),
		)),
	)

	return siteCheckinConfigNoStore(siteAccountStatsReadNoStore(siteAccountStatsConfigNoStore(doubanSyncConfigNoStore(chain))))
}

type authResponseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
}

func (r *authResponseRecorder) WriteHeader(code int) {
	r.statusCode = code
}

func (r *authResponseRecorder) Write(b []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *authResponseRecorder) flush() {
	r.ResponseWriter.WriteHeader(r.statusCode)
	_, _ = io.Copy(r.ResponseWriter, r.body)
}

func (r *authResponseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// The direct local setup path must not be reachable through a rebound DNS host.
func localSetupRequest(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(peer)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return false
	}
	authority, err := url.Parse("//" + r.Host)
	if err != nil || authority.User != nil || authority.Path != "" || authority.RawQuery != "" || authority.Fragment != "" {
		return false
	}
	host := strings.TrimSuffix(authority.Hostname(), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip = net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

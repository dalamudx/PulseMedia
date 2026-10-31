package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	actorKey     contextKey = "actor"
	rawTokenKey  contextKey = "raw_token"
	idempKeyKey  contextKey = "idempotency_key"
)

// GetRequestID returns the request ID from the context, or empty string.
func GetRequestID(ctx context.Context) string {
	if val, ok := ctx.Value(requestIDKey).(string); ok {
		return val
	}
	return ""
}

// GetActor returns the authenticated Actor from the context, or nil.
func GetActor(ctx context.Context) *ports.Actor {
	if val, ok := ctx.Value(actorKey).(*ports.Actor); ok {
		return val
	}
	return nil
}

// GetRawToken returns the raw token string from the context, or empty.
func GetRawToken(ctx context.Context) string {
	if val, ok := ctx.Value(rawTokenKey).(string); ok {
		return val
	}
	return ""
}

// GetIdempotencyKey returns the idempotency key from the context, or empty.
func GetIdempotencyKey(ctx context.Context) string {
	if val, ok := ctx.Value(idempKeyKey).(string); ok {
		return val
	}
	return ""
}

// validRequestID limits client-controlled correlation data to a bounded ASCII token.
func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '.' || c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}

// RequestIDMiddleware injects a request ID into the request context and response header.
func RequestIDMiddleware(idgen ports.IDGen) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get("X-Request-ID")
			if len(r.Header.Values("X-Request-ID")) != 1 || !validRequestID(reqID) {
				reqID = ""
			}
			if reqID == "" && idgen != nil {
				reqID = idgen.NewUUIDString()
			}
			if !validRequestID(reqID) {
				reqID = "req-" + fmt.Sprintf("%d", time.Now().UnixNano())
			}

			w.Header().Set("X-Request-ID", reqID)
			ctx := context.WithValue(r.Context(), requestIDKey, reqID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// IdempotencyMiddleware extracts Idempotency-Key header and puts it in context.
func IdempotencyMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if idemp := r.Header.Get("Idempotency-Key"); idemp != "" {
				ctx := context.WithValue(r.Context(), idempKeyKey, idemp)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRFMiddleware protects cookie-authenticated writes and browser bootstrap requests.
// Bootstrap can arrive before any cookie exists, including a browser-simple text body.
func CSRFMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				bootstrap := r.URL.Path == "/api/v1/auth/setup" || r.URL.Path == "/api/v1/auth/login"
				hasOrigin := len(r.Header.Values("Origin")) > 0
				_, cookieErr := r.Cookie("pulsemedia_session")
				cookieWrite := cookieErr == nil && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
				if (bootstrap && hasOrigin || cookieWrite) && !sameRequestOrigin(r) {
					WriteJSONError(w, http.StatusForbidden, "CSRF_REJECTED", "same-origin request required", GetRequestID(r.Context()))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func sameRequestOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || strings.Contains(origins[0], "#") || origin.Opaque != "" ||
		(origin.Scheme != "http" && origin.Scheme != "https") || (r.TLS != nil && origin.Scheme != "https") {
		return false
	}
	authority, err := url.Parse("//" + r.Host)
	if err != nil || authority.User != nil || authority.Path != "" || authority.RawQuery != "" || authority.Fragment != "" || origin.Hostname() == "" ||
		!strings.EqualFold(origin.Hostname(), authority.Hostname()) {
		return false
	}
	originPort, requestPort := origin.Port(), authority.Port()
	defaultPort := "80"
	if origin.Scheme == "https" {
		defaultPort = "443"
	}
	if originPort == "" {
		originPort = defaultPort
	}
	if requestPort == "" {
		requestPort = defaultPort
	}
	return originPort == requestPort
}

// AuthMiddleware resolves bearer tokens and session cookies into an authenticated Actor.
func AuthMiddleware(authSvc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			} else if cookie, err := r.Cookie("pulsemedia_session"); err == nil {
				token = cookie.Value
			}

			if token != "" && authSvc != nil {
				actor, err := authSvc.AuthenticateToken(r.Context(), token)
				if err != nil && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrUserDisabled) && !errors.Is(err, ports.ErrTokenExpired) && !errors.Is(err, ports.ErrTokenRevoked) {
					WriteJSONError(w, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "authentication temporarily unavailable", GetRequestID(r.Context()))
					return
				}
				if err == nil && actor != nil {
					ctx := context.WithValue(r.Context(), actorKey, actor)
					ctx = context.WithValue(ctx, rawTokenKey, token)
					r = r.WithContext(ctx)
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	written    int64
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	if r.statusCode != 0 {
		return
	}
	// Informational responses do not commit the final status (except protocol upgrade).
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(statusCode)
		return
	}
	r.ResponseWriter.WriteHeader(statusCode)
	r.statusCode = statusCode
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.statusCode == 0 {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
func (r *responseRecorder) FlushError() error {
	if r.statusCode == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(r.ResponseWriter).Flush()
}
func (r *responseRecorder) Flush() { _ = r.FlushError() }

// LoggingMiddleware records bounded route templates, never raw paths or query values.
// The optional resolver must return a registered route pattern, not request data.
func LoggingMiddleware(logger *slog.Logger, route ...func(*http.Request) string) func(http.Handler) http.Handler {
	return requestDiagnostics(logger, nil, route...)
}

func requestDiagnostics(logger *slog.Logger, metrics *requestMetrics, route ...func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			pattern := "unmatched"
			if len(route) == 1 {
				if resolved := route[0](r); resolved != "" {
					pattern = resolved
				}
			}
			method := r.Method
			switch method {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
			default:
				method = "OTHER"
			}
			rec := &responseRecorder{ResponseWriter: w}
			if metrics != nil {
				metrics.mu.Lock()
				metrics.inFlight++
				metrics.mu.Unlock()
				defer func() {
					status := rec.statusCode
					if status == 0 {
						status = http.StatusOK
					}
					metrics.observe(pattern, method, status, time.Since(start).Seconds(), rec.written)
				}()
			}
			next.ServeHTTP(rec, r)
			status := rec.statusCode
			if status == 0 {
				status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http request served",
				slog.String("method", method),
				slog.String("path", pattern),
				slog.Int("status", status),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", GetRequestID(r.Context())),
				slog.Int64("bytes", rec.written),
			)
		})
	}
}

// RecoveryMiddleware recovers from panics, logs the stack, and writes a clean 500 error.
func RecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					reqID := GetRequestID(r.Context())
					stack := string(debug.Stack())

					logger.ErrorContext(r.Context(), "http panic recovered",
						slog.String("error", "handler panic"),
						slog.String("request_id", reqID),
						slog.String("stack", stack),
					)

					WriteJSONError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", reqID)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// WriteJSONError writes an RFC 9457 problem with stable legacy extensions.
func WriteJSONError(w http.ResponseWriter, status int, code, message, reqID string) {
	writeProblem(w, status, problemResponse(status, code, message, reqID))
}

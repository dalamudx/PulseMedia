package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
)

const maxProblemBody = 64 << 10

func problemResponse(status int, code, message, requestID string) ErrorResponse {
	return ErrorResponse{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: message, Code: code, Message: message, RequestId: requestID}
}
func writeProblem(w http.ResponseWriter, status int, body ErrorResponse) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Del("Content-Length")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ProblemResponseMiddleware adapts generated/custom errors at the HTTP edge.
// Successful responses, including SSE, are never buffered.
func ProblemResponseMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := &problemWriter{ResponseWriter: w}
			var out http.ResponseWriter = p
			if _, ok := w.(http.Flusher); ok {
				out = &flushingProblemWriter{p: p, r: r}
			}
			next.ServeHTTP(out, r)
			p.finish(r)
		})
	}
}

type problemWriter struct {
	http.ResponseWriter
	status    int
	body      bytes.Buffer
	oversized bool
	finalized bool
}

func (w *problemWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *problemWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	if status < 400 {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *problemWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status < 400 {
		return w.ResponseWriter.Write(b)
	}
	if !w.finalized && !w.oversized {
		if w.body.Len()+len(b) > maxProblemBody {
			w.oversized = true
			w.body.Reset()
		} else {
			_, _ = w.body.Write(b)
		}
	}
	return len(b), nil
}
func (w *problemWriter) finish(r *http.Request) {
	if w.finalized || w.status < 400 {
		return
	}
	w.finalized = true
	// A readiness failure remains a typed health report.
	if r.URL.Path == "/readyz" && w.status == http.StatusServiceUnavailable && !w.oversized {
		var health struct {
			Ready  *bool           `json:"ready"`
			Checks map[string]bool `json:"checks"`
		}
		if json.Unmarshal(w.body.Bytes(), &health) == nil && health.Ready != nil && health.Checks != nil {
			w.ResponseWriter.WriteHeader(w.status)
			if r.Method != http.MethodHead {
				_, _ = w.ResponseWriter.Write(w.body.Bytes())
			}
			return
		}
	}
	body := problemResponse(w.status, "HTTP_ERROR", http.StatusText(w.status), GetRequestID(r.Context()))
	var legacy ErrorResponse
	if !w.oversized && json.Unmarshal(w.body.Bytes(), &legacy) == nil && legacy.Code != "" && legacy.Message != "" {
		body = problemResponse(w.status, legacy.Code, legacy.Message, GetRequestID(r.Context()))
		body.Details = legacy.Details
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Del("Content-Length")
		w.ResponseWriter.WriteHeader(w.status)
		return
	}
	writeProblem(w.ResponseWriter, w.status, body)
}

type flushingProblemWriter struct {
	p *problemWriter
	r *http.Request
}

func (w *flushingProblemWriter) Header() http.Header         { return w.p.Header() }
func (w *flushingProblemWriter) WriteHeader(status int)      { w.p.WriteHeader(status) }
func (w *flushingProblemWriter) Write(b []byte) (int, error) { return w.p.Write(b) }
func (w *flushingProblemWriter) Unwrap() http.ResponseWriter { return w.p.ResponseWriter }
func (w *flushingProblemWriter) Flush() {
	if w.p.status == 0 {
		w.p.WriteHeader(http.StatusOK)
	}
	w.p.finish(w.r)
	w.p.ResponseWriter.(http.Flusher).Flush()
}

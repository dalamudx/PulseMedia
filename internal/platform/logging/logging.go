package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

var (
	sensitiveKeyPattern    = regexp.MustCompile(`(?i)(password|token|secret|api[_-]?key|passkey|cookie|auth|dsn)`)
	credentialQueryPattern = regexp.MustCompile(`([?&;])([^=?&#;\s]+)=([^?&#;\s]*)`)
	userinfoPattern        = regexp.MustCompile(`(://[^:]+:)[^@]+(@)`)
)

// RedactValue masks strings if they look like credentials, URLs with passwords, or query params with tokens.
func RedactValue(val any) any {
	str, ok := val.(string)
	if !ok {
		return val
	}
	return RedactString(str)
}

// RedactString redacts sensitive information within a string.
func RedactString(s string) string {
	if s == "" {
		return ""
	}
	s = userinfoPattern.ReplaceAllString(s, `${1}***${2}`)
	// Match each parameter in context instead of parsing the entire log message
	// as a URL. Malformed paths must not disable credential redaction.
	return credentialQueryPattern.ReplaceAllStringFunc(s, func(parameter string) string {
		parts := credentialQueryPattern.FindStringSubmatch(parameter)
		key, err := url.QueryUnescape(parts[2])
		if err != nil || !sensitiveKeyPattern.MatchString(key) {
			return parameter
		}
		return parts[1] + parts[2] + "=***"
	})
}

// RedactingHandler is an slog.Handler that masks sensitive keys and values.
type RedactingHandler struct {
	next slog.Handler
}

func NewRedactingHandler(next slog.Handler) *RedactingHandler {
	return &RedactingHandler{next: next}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	newRecord := slog.NewRecord(record.Time, record.Level, RedactString(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		newRecord.AddAttrs(h.sanitizeAttr(attr))
		return true
	})
	return h.next.Handle(ctx, newRecord)
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	sanitized := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		sanitized[i] = h.sanitizeAttr(a)
	}
	return &RedactingHandler{next: h.next.WithAttrs(sanitized)}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{next: h.next.WithGroup(name)}
}

func (h *RedactingHandler) sanitizeAttr(attr slog.Attr) slog.Attr {
	return h.sanitizeAttrDepth(attr, 0)
}

func (h *RedactingHandler) sanitizeAttrDepth(attr slog.Attr, depth int) slog.Attr {
	if depth >= 16 {
		return slog.String(attr.Key, "***")
	}
	if sensitiveKeyPattern.MatchString(attr.Key) {
		return slog.String(attr.Key, "***")
	}

	attr.Value = attr.Value.Resolve()
	switch attr.Value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, RedactString(attr.Value.String()))
	case slog.KindGroup:
		attrs := attr.Value.Group()
		sanitized := make([]slog.Attr, len(attrs))
		for i, a := range attrs {
			sanitized[i] = h.sanitizeAttrDepth(a, depth+1)
		}
		return slog.Group(attr.Key, anySliceToAny(sanitized)...)
	case slog.KindAny:
		switch value := attr.Value.Any().(type) {
		case error:
			return slog.String(attr.Key, RedactString(fmt.Sprint(value)))
		case map[string]any:
			children := make([]slog.Attr, 0, len(value))
			for key, child := range value {
				children = append(children, h.sanitizeAttrDepth(slog.Any(key, child), depth+1))
			}
			return slog.Group(attr.Key, anySliceToAny(children)...)
		case map[string]string:
			children := make([]slog.Attr, 0, len(value))
			for key, child := range value {
				children = append(children, h.sanitizeAttrDepth(slog.String(key, child), depth+1))
			}
			return slog.Group(attr.Key, anySliceToAny(children)...)
		default:
			// Opaque objects have no reviewed field contract. Callers can expose
			// safe fields with LogValuer or explicit slog attributes.
			return slog.String(attr.Key, "***")
		}
	default:
		return attr
	}
}

func anySliceToAny(attrs []slog.Attr) []any {
	out := make([]any, len(attrs))
	for i, a := range attrs {
		out[i] = a
	}
	return out
}

// NewLogger creates a new configured slog.Logger with sensitive field redaction.
func NewLogger(w io.Writer, levelStr string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(levelStr) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}
	baseHandler := slog.NewJSONHandler(w, opts)
	return slog.New(NewRedactingHandler(baseHandler))
}

// teeHandler forwards each record to a primary handler and an optional sink.
// It is installed inside the redacting handler so the sink only ever observes
// sanitized records.
type teeHandler struct {
	primary slog.Handler
	sink    func(slog.Level, string)
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.primary.Enabled(ctx, level)
}

func (h *teeHandler) Handle(ctx context.Context, record slog.Record) error {
	if h.sink != nil {
		h.sink(record.Level, RedactString(formatRecord(record)))
	}
	return h.primary.Handle(ctx, record)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{primary: h.primary.WithAttrs(attrs), sink: h.sink}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{primary: h.primary.WithGroup(name), sink: h.sink}
}

// formatRecord renders one record as a single line: message plus attributes.
func formatRecord(record slog.Record) string {
	var b strings.Builder
	b.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(attr.Key)
		b.WriteByte('=')
		b.WriteString(attr.Value.Resolve().String())
		return true
	})
	return b.String()
}

// NewLoggerWithSink creates the process logger and mirrors every accepted
// record, already redacted, into sink for user-visible live logs.
func NewLoggerWithSink(w io.Writer, levelStr string, sink func(slog.Level, string)) *slog.Logger {
	return WithSink(NewLogger(w, levelStr), sink)
}

// WithSink mirrors accepted records into sink. The sink always receives a
// redacted single-line rendering, whatever handler the logger already has.
func WithSink(logger *slog.Logger, sink func(slog.Level, string)) *slog.Logger {
	if logger == nil || sink == nil {
		return logger
	}
	return slog.New(&teeHandler{primary: logger.Handler(), sink: sink})
}

// Package observability configures structured logging and Prometheus metrics.
package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// secretKeys are attribute names whose values are never written to logs,
// whatever the caller passed. The list is deliberately broad.
var secretKeys = []string{"password", "passwd", "secret", "token", "api_key", "apikey", "authorization", "cookie", "master_key", "recovery_code"}

// NewLogger builds a slog.Logger at the given level and format ("text" or
// "json") that redacts any attribute whose key looks like a secret.
func NewLogger(w io.Writer, level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl, ReplaceAttr: redact}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

func redact(_ []string, a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	for _, s := range secretKeys {
		if strings.Contains(k, s) {
			return slog.String(a.Key, "[redacted]")
		}
	}
	return a
}

type ctxKey struct{}

// WithLogger stores a request-scoped logger in ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// Logger returns the request-scoped logger or the default logger.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

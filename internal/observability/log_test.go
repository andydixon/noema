package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf, "info", "json")
	l.Info("x", "api_key", "sk-123", "Password", "hunter2", "user", "andy")
	out := buf.String()
	if strings.Contains(out, "sk-123") || strings.Contains(out, "hunter2") || !strings.Contains(out, "andy") {
		t.Fatalf("redaction wrong: %s", out)
	}
}

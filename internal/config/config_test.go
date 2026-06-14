package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestPrecedenceDefaultsFileEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.WriteFile(p, []byte(`{"http_addr":"1.2.3.4:1","log_level":"debug","database_url":"postgres://u:p@h/db","session_idle_ttl":"1h","session_absolute_ttl":"2h"}`), 0o600)
	c, err := Load(p, env(map[string]string{"NOEMA_LOG_LEVEL": "warn", "NOEMA_MAX_WORKERS": "3"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != "1.2.3.4:1" || c.LogLevel != "warn" || c.MaxWorkers != 3 || c.SessionIdleTTL != time.Hour || c.Env != "development" {
		t.Fatalf("unexpected %+v", c)
	}
}

func TestValidationAndRedaction(t *testing.T) {
	if _, err := Load("", env(map[string]string{})); err == nil || !strings.Contains(err.Error(), "database_url") {
		t.Fatalf("expected database_url error, got %v", err)
	}
	if _, err := Load("", env(map[string]string{"NOEMA_DATABASE_URL": "postgres://x", "NOEMA_MAX_WORKERS": "abc"})); err == nil {
		t.Fatal("expected parse error")
	}
	c, err := Load("", env(map[string]string{"NOEMA_DATABASE_URL": "postgres://u:secret@h/db", "NOEMA_MASTER_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Redacted()
	if strings.Contains(r.DatabaseURL, "secret") || r.MasterKey != "[set]" {
		t.Fatalf("redaction failed: %+v", r)
	}
}

func TestUnknownFileKeyRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"databse_url":"x"}`), 0o600)
	if _, err := Load(p, env(nil)); err == nil {
		t.Fatal("expected unknown field error")
	}
}

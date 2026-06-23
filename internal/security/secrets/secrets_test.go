package secrets

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferences(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	r, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Encrypt("sk-secret")
	if err != nil || !strings.HasPrefix(ref, "enc:") || strings.Contains(ref, "sk-secret") {
		t.Fatalf("encrypt: %v %s", err, ref)
	}
	if v, _ := r.Resolve(ref); v != "sk-secret" {
		t.Fatalf("round trip failed: %q", v)
	}
	other, _ := New(base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210")))
	if _, err := other.Resolve(ref); err == nil {
		t.Fatal("decryption with wrong key succeeded")
	}
	none, _ := New("")
	if _, err := none.Resolve(ref); err != ErrNoMasterKey {
		t.Fatalf("expected ErrNoMasterKey, got %v", err)
	}
	if _, err := none.Encrypt("x"); err != ErrNoMasterKey {
		t.Fatal("encrypt without key should fail")
	}
	t.Setenv("NOEMA_TEST_SECRET", "from-env")
	if v, _ := r.Resolve("env:NOEMA_TEST_SECRET"); v != "from-env" {
		t.Fatal("env reference failed")
	}
	if _, err := r.Resolve("env:NOEMA_DEFINITELY_UNSET"); err == nil {
		t.Fatal("unset env should error")
	}
	p := filepath.Join(t.TempDir(), "s")
	os.WriteFile(p, []byte("from-file\n"), 0o600)
	if v, _ := r.Resolve("file:" + p); v != "from-file" {
		t.Fatal("file reference failed")
	}
	if _, err := r.Resolve("plaintext-key"); err == nil {
		t.Fatal("plaintext must be rejected")
	}
	if Describe(ref) != "encrypted in database" || Describe("env:X") != "environment variable X" {
		t.Fatal("describe wrong")
	}
	if _, err := New("short"); err == nil {
		t.Fatal("short key accepted")
	}
}

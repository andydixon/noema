package httpclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPrivateRanges(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.5.5", "169.254.169.254", "::1", "fd00::1", "::ffff:10.0.0.1", "0.0.0.0"} {
		if !IsPrivate(netip.MustParseAddr(s)) {
			t.Errorf("%s should be private", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111", "93.184.216.34"} {
		if IsPrivate(netip.MustParseAddr(s)) {
			t.Errorf("%s should be public", s)
		}
	}
}

func TestClientRefusesLoopbackUnlessAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/redir", http.StatusFound)
			return
		}
		w.Write([]byte(strings.Repeat("x", 100))) //nolint:errcheck
	}))
	defer srv.Close()
	strict := New(Options{Timeout: 2 * time.Second})
	_, err := strict.Get(srv.URL)
	if err == nil || !errors.Is(errors.Unwrap(errors.Unwrap(err)), ErrPrivateAddress) && !strings.Contains(err.Error(), "private") {
		t.Fatalf("loopback should be refused by default: %v", err)
	}
	lax := New(Options{Timeout: 2 * time.Second, AllowPrivate: true})
	res, err := lax.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAll(res.Body, 50); err == nil {
		t.Fatal("body cap not enforced")
	}
	res.Body.Close()
	if _, err := lax.Get(srv.URL + "/redir"); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("redirect loop should be bounded: %v", err)
	}
}

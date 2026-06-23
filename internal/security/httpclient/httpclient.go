// Package httpclient builds outbound HTTP clients hardened against SSRF
// (T6): private, loopback, link-local and metadata addresses are refused at
// dial time (so redirects are covered too), redirects are bounded, and
// response bodies are capped.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Options configure a client.
type Options struct {
	Timeout      time.Duration
	AllowPrivate bool  // permit RFC1918/loopback targets (local Ollama, self-hosted services)
	MaxBody      int64 // response body cap for ReadAll; 0 = 8 MiB
	MaxRedirects int   // 0 = 5
}

// ErrPrivateAddress is returned when a dial would reach a blocked range.
var ErrPrivateAddress = errors.New("httpclient: destination address is private or reserved")

var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "::ffff:0:0/96"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsPrivate reports whether addr falls in a blocked range.
func IsPrivate(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range blocked {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// New builds a hardened client. The check happens after DNS resolution and
// on every connection, so DNS rebinding and redirects to internal hosts are
// both refused.
func New(o Options) *http.Client {
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = 5
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("httpclient: no addresses for %s", host)
			}
			var lastErr error
			for _, ip := range ips {
				if !o.AllowPrivate && IsPrivate(ip) {
					lastErr = fmt.Errorf("%w: %s resolves to %s", ErrPrivateAddress, host, ip)
					continue
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	max := o.MaxRedirects
	return &http.Client{Timeout: o.Timeout, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return fmt.Errorf("httpclient: stopped after %d redirects", max)
		}
		return nil
	}}
}

// ReadAll reads a response body up to max bytes (0 = 8 MiB) and errors if
// the body is larger, so a hostile server cannot exhaust memory.
func ReadAll(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = 8 << 20
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("httpclient: response exceeds %d bytes", max)
	}
	return b, nil
}

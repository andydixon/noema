// Package browser provides read-only web capabilities that behave like the
// reading half of a browser: open a page, read its text, list its links.
// Pages are fetched through the SSRF-safe client, scripts never run, and
// everything a page says arrives as low-trust observation data. The policy
// target is the page's host, so operators can allow or deny by domain.
//
// Writing operations (click, fill, submit, download) are designed in
// ADR-0011 but deliberately not registered: they need a sandboxed rendering
// engine and per-domain write policy that this release does not ship.
package browser

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/security/httpclient"
	"github.com/andydixon/noema/pkg/events"
)

// Options configure the browser capabilities.
type Options struct {
	// Client overrides the HTTP client (tests). Nil uses the SSRF-safe client.
	Client   *http.Client
	MaxBytes int64 // default 512 KiB
}

// Host returns the lower-cased host of an http(s) URL parameter.
func Host(params map[string]any) (string, error) {
	raw, _ := params["url"].(string)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("url must be an absolute http(s) URL")
	}
	if u.User != nil {
		return "", errors.New("credentials in URLs are not accepted")
	}
	return strings.ToLower(u.Hostname()), nil
}

// Capabilities returns browser.navigate, browser.read and browser.links.
func Capabilities(o Options) []capabilities.Capability {
	if o.MaxBytes <= 0 {
		o.MaxBytes = 512 << 10
	}
	param := map[string]capabilities.Param{"url": {Type: "url", Required: true, Description: "page to open", MaxLen: 2000}}
	common := func(name, desc string, exec capabilities.Executor) capabilities.Capability {
		return capabilities.Capability{Name: name, Description: desc, Risk: capabilities.RiskMedium, TargetKind: "domain", Access: capabilities.AccessRead, Idempotent: true, Timeout: 25 * time.Second,
			SideEffects: "one GET request to the page's server, which may log it; no scripts run and nothing is submitted", DefaultEffect: capabilities.Ask,
			Params: param, TargetFrom: Host, OutputTrust: events.TrustLow, Estimate: capabilities.Cost{WallSeconds: 2, Network: 1}, Executor: exec}
	}
	return []capabilities.Capability{
		common("browser.navigate", "Open a page and report its final address, status, title and link count.", func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
			p, err := o.load(ctx, inv)
			if err != nil {
				return capabilities.Result{}, err
			}
			return capabilities.Result{Summary: fmt.Sprintf("HTTP %d %s: %q (%d links)", p.status, p.final, p.title, len(p.links)),
				Data: map[string]any{"status": p.status, "final_url": p.final, "title": p.title, "content_type": p.contentType, "links": len(p.links), "bytes": p.bytes}}, nil
		}),
		common("browser.read", "Open a page and return its readable text with scripts, styles and markup removed.", func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
			p, err := o.load(ctx, inv)
			if err != nil {
				return capabilities.Result{}, err
			}
			return capabilities.Result{Summary: fmt.Sprintf("read %q from %s (%d characters)", p.title, p.final, len(p.text)), Output: p.text,
				Data: map[string]any{"status": p.status, "final_url": p.final, "title": p.title}}, nil
		}),
		common("browser.links", "Open a page and list its links as absolute URLs with their text.", func(ctx context.Context, inv capabilities.Invocation) (capabilities.Result, error) {
			p, err := o.load(ctx, inv)
			if err != nil {
				return capabilities.Result{}, err
			}
			var b strings.Builder
			for _, l := range p.links {
				fmt.Fprintf(&b, "%s\t%s\n", l.URL, l.Text)
			}
			return capabilities.Result{Summary: fmt.Sprintf("%d links on %s", len(p.links), p.final), Output: b.String(), Data: map[string]any{"final_url": p.final, "links": p.links}}, nil
		}),
	}
}

// Link is one anchor on a page.
type Link struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}

type page struct {
	status             int
	final, contentType string
	title, text        string
	links              []Link
	bytes              int
}

func (o Options) load(ctx context.Context, inv capabilities.Invocation) (page, error) {
	raw, _ := inv.Params["url"].(string)
	client := o.Client
	if client == nil {
		client = httpclient.New(httpclient.Options{Timeout: 20 * time.Second, MaxBody: o.MaxBytes})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return page{}, err
	}
	req.Header.Set("User-Agent", "noema/1 (+capability browser; read-only)")
	req.Header.Set("Accept", "text/html, text/plain;q=0.8")
	res, err := client.Do(req)
	if err != nil {
		return page{}, err
	}
	defer res.Body.Close()
	body, err := httpclient.ReadAll(res.Body, o.MaxBytes)
	if err != nil {
		return page{}, err
	}
	p := page{status: res.StatusCode, final: res.Request.URL.String(), contentType: res.Header.Get("Content-Type"), bytes: len(body)}
	// A redirect must not leave the policy target: the host that was
	// authorised is the host that is read.
	if want, _ := Host(inv.Params); !strings.EqualFold(res.Request.URL.Hostname(), want) {
		return page{}, fmt.Errorf("redirected from %s to %s; open the new address as its own request", want, res.Request.URL.Hostname())
	}
	doc := string(body)
	if !strings.Contains(p.contentType, "html") {
		p.text = doc
		return p, nil
	}
	p.title, p.text, p.links = Extract(doc, res.Request.URL)
	return p, nil
}

var (
	dropRe  = regexp.MustCompile(`(?is)<(script|style|noscript|template|iframe|svg)\b.*?</(script|style|noscript|template|iframe|svg)\s*>`)
	titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	anchorR = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>(.*?)</a>`)
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	blockRe = regexp.MustCompile(`(?i)</?(p|div|br|li|h[1-6]|tr|section|article|header|footer)\b[^>]*>`)
)

// Extract returns a page's title, readable text and absolute links. Only
// http(s) links are kept; javascript:, data: and similar are dropped.
func Extract(doc string, base *url.URL) (string, string, []Link) {
	doc = dropRe.ReplaceAllString(doc, " ")
	title := ""
	if m := titleRe.FindStringSubmatch(doc); m != nil {
		title = clean(tagRe.ReplaceAllString(m[1], " "))
	}
	var links []Link
	seen := map[string]bool{}
	for _, m := range anchorR.FindAllStringSubmatch(doc, 500) {
		href := html.UnescapeString(m[1] + m[2] + m[3])
		u, err := base.Parse(strings.TrimSpace(href))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		u.Fragment = ""
		if seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		links = append(links, Link{URL: u.String(), Text: clean(tagRe.ReplaceAllString(m[4], " "))})
	}
	text := blockRe.ReplaceAllString(doc, "\n")
	text = tagRe.ReplaceAllString(text, " ")
	var lines []string
	for _, l := range strings.Split(html.UnescapeString(text), "\n") {
		if l = clean(l); l != "" {
			lines = append(lines, l)
		}
	}
	return title, strings.Join(lines, "\n"), links
}

func clean(s string) string { return strings.Join(strings.Fields(html.UnescapeString(s)), " ") }

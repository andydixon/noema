package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andydixon/noema/pkg/events"
)

// Webhook signing: the sender computes HMAC-SHA256 over "<timestamp>.<body>"
// with the shared secret and sends X-Noema-Timestamp (unix seconds) and
// X-Noema-Signature: sha256=<hex>. Timestamps older than five minutes and
// replayed signatures are rejected (threat T22).

// Signature errors.
var (
	ErrBadSignature = errors.New("webhook: invalid signature")
	ErrStale        = errors.New("webhook: timestamp outside tolerance")
	ErrReplay       = errors.New("webhook: signature already seen")
)

// Sign computes the signature header value for a body and timestamp.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks timestamp tolerance and the HMAC in constant time.
func Verify(secret string, tsHeader, sigHeader string, body []byte, now time.Time) error {
	ts, err := strconv.ParseInt(strings.TrimSpace(tsHeader), 10, 64)
	if err != nil {
		return ErrStale
	}
	if d := now.Unix() - ts; d > 300 || d < -300 {
		return ErrStale
	}
	want := Sign(secret, ts, body)
	if !hmac.Equal([]byte(strings.TrimSpace(sigHeader)), []byte(want)) {
		return ErrBadSignature
	}
	return nil
}

// ReplayCache remembers recent signatures for the tolerance window.
type ReplayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReplayCache creates a cache.
func NewReplayCache() *ReplayCache { return &ReplayCache{seen: map[string]time.Time{}} }

// Check records sig and reports whether it was already present.
func (c *ReplayCache) Check(sig string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, t := range c.seen {
		if now.Sub(t) > 10*time.Minute {
			delete(c.seen, k)
		}
	}
	if _, dup := c.seen[sig]; dup {
		return true
	}
	c.seen[sig] = now
	return false
}

// WebhookBody is the accepted payload. Unknown JSON is stringified as text.
type WebhookBody struct {
	Title     string  `json:"title"`
	Text      string  `json:"text"`
	Reference string  `json:"reference"`
	Salience  float64 `json:"salience"`
}

// Ingest turns a verified webhook body into an observation.
func (m *Manager) Ingest(ctx context.Context, in Integration, body []byte) (events.Event, error) {
	if len(body) > 256<<10 {
		return events.Event{}, fmt.Errorf("%w: body too large", ErrInvalid)
	}
	var wb WebhookBody
	if err := json.Unmarshal(body, &wb); err != nil || strings.TrimSpace(wb.Text) == "" {
		wb = WebhookBody{Title: "webhook payload", Text: string(body)}
	}
	if wb.Salience <= 0 || wb.Salience > 1 {
		wb.Salience = 0.5
	}
	sink := &Sink{Events: m.Events, Store: m.Store, Graph: m.Graph, Log: m.Log, Mind: in.MindID, Integ: in, run: Run{IntegrationID: in.ID, StartedAt: time.Now()}}
	summary := wb.Text
	if wb.Title != "" {
		summary = wb.Title + ": " + wb.Text
	}
	ev, err := sink.Observe(ctx, Observation{Summary: summary, Text: wb.Text, Reference: wb.Reference, ContentType: "text/plain", Salience: wb.Salience, Metadata: map[string]any{"title": wb.Title, "method": "webhook"}})
	if err != nil {
		return events.Event{}, err
	}
	sink.run.Items = 1
	m.Store.RecordRun(ctx, in.ID, sink.run) //nolint:errcheck
	return ev, nil
}

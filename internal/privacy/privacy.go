// Package privacy is the audited path for making sensitive content
// irrecoverable without breaking the history it belongs to (ADR-0009).
//
// A redaction keeps the row, its identifiers, its links, its type and its
// timestamps, and replaces the content columns with a fixed marker. What was
// removed is attested by a SHA-256 of the removed content in the ledger, so
// a later reader can establish that an event existed, when, of what kind,
// what depended on it, that it was redacted, by whom and under which policy,
// while the content itself is gone from the live database and from stored
// snapshot archives. Backups and exports made before the redaction are
// outside this system's reach and are documented as such.
package privacy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/audit"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Kind of row a redaction targets.
type Kind string

// Kinds.
const (
	KindEvent     Kind = "event"
	KindMemory    Kind = "memory"
	KindMessage   Kind = "message"
	KindLLMCall   Kind = "llm_call"
	KindBelief    Kind = "belief"
	KindAssertion Kind = "assertion"
)

// Marker replaces removed content everywhere.
const Marker = "[redacted]"

// Errors.
var (
	ErrInvalid         = errors.New("privacy: invalid request")
	ErrAlreadyRedacted = errors.New("privacy: already redacted")
)

// Request describes one redaction.
type Request struct {
	MindID  ids.ID
	Kind    Kind
	ID      ids.ID
	Reason  string
	Policy  string
	Cascade bool // also redact rows derived from the target
	By      string
}

// Redaction is one ledger row.
type Redaction struct {
	ID          ids.ID    `json:"id"`
	MindID      ids.ID    `json:"mind_id"`
	Kind        Kind      `json:"target_kind"`
	TargetID    ids.ID    `json:"target_id"`
	Reason      string    `json:"reason"`
	Policy      string    `json:"policy"`
	ContentHash string    `json:"content_hash"`
	RequestedBy string    `json:"requested_by"`
	CascadeOf   ids.ID    `json:"cascade_of,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Service performs redactions.
type Service struct {
	DB        *pgxpool.Pool
	Events    *evstore.Store
	Audit     *audit.Logger
	Snapshots *snapshots.Store
	Log       *slog.Logger
}

// Redact makes the target's content irrecoverable and returns the ledger
// row plus any cascaded rows. Everything runs in one transaction; stored
// snapshot archives are scrubbed afterwards and the outcome is audited.
func (s *Service) Redact(ctx context.Context, req Request) (Redaction, []Redaction, error) {
	if req.MindID.IsZero() || req.ID.IsZero() || strings.TrimSpace(req.Reason) == "" || !validKind(req.Kind) {
		return Redaction{}, nil, fmt.Errorf("%w: mind, kind, id and reason are required", ErrInvalid)
	}
	var primary Redaction
	var cascaded []Redaction
	err := storage.Tx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		primary, err = s.redactOne(ctx, tx, req, ids.Nil)
		if err != nil {
			return err
		}
		if !req.Cascade {
			return nil
		}
		deps := s.dependants(ctx, tx, req.MindID, req.Kind, req.ID)
		// Events that quote a redacted row (the perception of a message, the
		// encoding of an episode, the creation of a belief) are redacted too,
		// one level out from the target and from each dependant.
		deps = append(deps, s.referencingEvents(ctx, tx, req.MindID, req.Kind, req.ID)...)
		seen := map[dep]bool{{req.Kind, req.ID}: true}
		for i := 0; i < len(deps); i++ {
			d := deps[i]
			if seen[d] {
				continue
			}
			seen[d] = true
			r, err := s.redactOne(ctx, tx, Request{MindID: req.MindID, Kind: d.kind, ID: d.id, Reason: req.Reason, Policy: req.Policy, By: req.By}, primary.ID)
			if errors.Is(err, ErrAlreadyRedacted) {
				continue
			}
			if err != nil {
				return err
			}
			cascaded = append(cascaded, r)
			if d.kind != KindEvent {
				deps = append(deps, s.referencingEvents(ctx, tx, req.MindID, d.kind, d.id)...)
			}
		}
		return nil
	})
	if err != nil {
		return primary, cascaded, err
	}
	// Stored archives hold copies of memories, messages and beliefs; scrub them.
	if s.Snapshots != nil {
		for _, r := range append([]Redaction{primary}, cascaded...) {
			if n, err := s.Snapshots.Scrub(ctx, r.MindID, string(r.Kind), r.TargetID); err != nil {
				s.Log.Warn("snapshot scrub failed", "redaction", r.ID, "err", err)
			} else if n > 0 {
				s.Log.Info("snapshot archives scrubbed", "redaction", r.ID, "archives", n)
			}
		}
	}
	if s.Audit != nil {
		s.Audit.Log(ctx, "privacy.redact", string(req.Kind), req.ID.String(), map[string]any{"redaction_id": primary.ID, "reason": req.Reason, "policy": req.Policy, "cascaded": len(cascaded), "content_hash": primary.ContentHash})
	}
	return primary, cascaded, nil
}

func (s *Service) redactOne(ctx context.Context, tx pgx.Tx, req Request, cascadeOf ids.ID) (Redaction, error) {
	content, mind, err := s.content(ctx, tx, req.Kind, req.ID)
	if err != nil {
		return Redaction{}, err
	}
	if mind != req.MindID {
		return Redaction{}, fmt.Errorf("%w: %s %s does not belong to this mind", ErrInvalid, req.Kind, req.ID)
	}
	if content == nil {
		return Redaction{}, ErrAlreadyRedacted
	}
	sum := sha256.Sum256(content)
	r := Redaction{ID: ids.New(), MindID: req.MindID, Kind: req.Kind, TargetID: req.ID, Reason: req.Reason, Policy: req.Policy, ContentHash: "sha256:" + hex.EncodeToString(sum[:]), RequestedBy: req.By, CascadeOf: cascadeOf, CreatedAt: time.Now()}
	if _, err := tx.Exec(ctx, `INSERT INTO redactions (id, mind_id, target_kind, target_id, reason, policy, content_hash, requested_by, cascade_of, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		r.ID, r.MindID, string(r.Kind), r.TargetID, r.Reason, r.Policy, r.ContentHash, r.RequestedBy, nullID(cascadeOf), r.CreatedAt); err != nil {
		if strings.Contains(err.Error(), "redactions_target_kind_target_id_key") {
			return Redaction{}, ErrAlreadyRedacted
		}
		return Redaction{}, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT noema_redact($1, $2, $3)`, string(req.Kind), req.ID, r.ID).Scan(&n); err != nil {
		return Redaction{}, err
	}
	if n == 0 {
		return Redaction{}, ErrAlreadyRedacted
	}
	// The history records that a redaction happened, never what was removed.
	if s.Events != nil {
		if _, err := s.Events.AppendIn(ctx, tx, events.New{MindID: req.MindID, Type: events.Redaction, Source: "privacy", Summary: fmt.Sprintf("%s %s redacted under %s", req.Kind, req.ID.String()[len(req.ID.String())-8:], firstNonEmpty(req.Policy, "operator request")),
			Salience: 0.2, Confidence: 1, Evidence: []events.EvidenceRef{{Kind: string(req.Kind), ID: string(req.ID), Note: "redacted"}},
			Metadata: map[string]any{"redaction_id": r.ID, "target_kind": string(req.Kind), "target_id": req.ID, "policy": req.Policy, "content_hash": r.ContentHash, "cascade_of": cascadeOf},
			Payload:  map[string]any{"redaction_id": r.ID, "reason": req.Reason}}); err != nil {
			return Redaction{}, err
		}
	}
	return r, nil
}

// content returns the JSON of the columns a redaction will remove (nil when
// already redacted) and the row's mind, for the hash and the scoping check.
func (s *Service) content(ctx context.Context, q storage.Querier, kind Kind, id ids.ID) ([]byte, ids.ID, error) {
	var mind ids.ID
	var already *time.Time
	var raw []byte
	var err error
	switch kind {
	case KindEvent:
		err = q.QueryRow(ctx, `SELECT mind_id, redacted_at, jsonb_build_object('summary', summary, 'payload', payload, 'metadata', metadata) FROM cognitive_events WHERE id=$1`, id).Scan(&mind, &already, &raw)
	case KindMemory:
		err = q.QueryRow(ctx, `SELECT mind_id, redacted_at, jsonb_build_object('content', content, 'detail', detail) FROM memories WHERE id=$1`, id).Scan(&mind, &already, &raw)
	case KindMessage:
		err = q.QueryRow(ctx, `SELECT mind_id, redacted_at, jsonb_build_object('content', content, 'intent', intent) FROM messages WHERE id=$1`, id).Scan(&mind, &already, &raw)
	case KindLLMCall:
		err = q.QueryRow(ctx, `SELECT coalesce(mind_id, '00000000-0000-0000-0000-000000000000'::uuid), redacted_at, jsonb_build_object('request', request, 'response', response) FROM llm_calls WHERE id=$1`, id).Scan(&mind, &already, &raw)
	case KindBelief:
		err = q.QueryRow(ctx, `SELECT mind_id, redacted_at, jsonb_build_object('proposition', proposition, 'detail', detail) FROM beliefs WHERE id=$1`, id).Scan(&mind, &already, &raw)
	case KindAssertion:
		err = q.QueryRow(ctx, `SELECT mind_id, redacted_at, jsonb_build_object('proposition', proposition, 'context', context) FROM assertions WHERE id=$1`, id).Scan(&mind, &already, &raw)
	}
	if err != nil {
		return nil, ids.Nil, storage.Translate(err)
	}
	if already != nil {
		return nil, mind, nil
	}
	return raw, mind, nil
}

type dep struct {
	kind Kind
	id   ids.ID
}

// dependants lists rows whose content derives from the target: episodes
// encoded from an event, the message it carried, assertions made in it,
// beliefs whose evidence roots at it, and the model calls that worded the
// messages. Cascade is one level from an event; deeper structure keeps its
// links but not the content.
func (s *Service) dependants(ctx context.Context, q storage.Querier, mind ids.ID, kind Kind, id ids.ID) []dep {
	var out []dep
	collect := func(k Kind, sql string, args ...any) {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var x ids.ID
			if rows.Scan(&x) == nil && !x.IsZero() {
				out = append(out, dep{k, x})
			}
		}
	}
	switch kind {
	case KindEvent:
		collect(KindMemory, `SELECT id FROM memories WHERE mind_id=$1 AND origin_event=$2 AND redacted_at IS NULL`, mind, id)
		collect(KindMessage, `SELECT id FROM messages WHERE mind_id=$1 AND event_id=$2 AND redacted_at IS NULL`, mind, id)
		collect(KindAssertion, `SELECT id FROM assertions WHERE mind_id=$1 AND source_event=$2 AND redacted_at IS NULL`, mind, id)
		collect(KindBelief, `SELECT DISTINCT belief_id FROM belief_evidence WHERE mind_id=$1 AND $2::uuid = ANY(root_ids)`, mind, id)
		collect(KindLLMCall, `SELECT llm_call_id FROM messages WHERE mind_id=$1 AND (event_id=$2 OR reply_to IN (SELECT id FROM messages WHERE event_id=$2)) AND llm_call_id IS NOT NULL`, mind, id)
		collect(KindMessage, `SELECT id FROM messages WHERE mind_id=$1 AND reply_to IN (SELECT id FROM messages WHERE event_id=$2) AND redacted_at IS NULL`, mind, id)
	case KindMessage:
		collect(KindLLMCall, `SELECT llm_call_id FROM messages WHERE mind_id=$1 AND id=$2 AND llm_call_id IS NOT NULL`, mind, id)
	case KindBelief:
		collect(KindAssertion, `SELECT id FROM assertions WHERE mind_id=$1 AND belief_id=$2 AND redacted_at IS NULL`, mind, id)
	}
	return out
}

// referencingEvents finds events that quote a row: direct children of an
// event, and events whose evidence or metadata point at the row. Redaction
// events are never included, so the record of redaction survives.
func (s *Service) referencingEvents(ctx context.Context, q storage.Querier, mind ids.ID, kind Kind, id ids.ID) []dep {
	var out []dep
	ref := fmt.Sprintf(`[{"kind":%q,"id":%q}]`, string(kind), id.String())
	metaKey := string(kind) + "_id"
	rows, err := q.Query(ctx, `SELECT id FROM cognitive_events WHERE mind_id=$1 AND redacted_at IS NULL AND type <> 'redaction'
		AND (evidence @> $2::jsonb OR metadata->>$3 = $4 OR ($5 AND (causation_id=$6 OR $6 = ANY(parent_ids)))) LIMIT 500`, mind, ref, metaKey, id.String(), kind == KindEvent, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var x ids.ID
		if rows.Scan(&x) == nil && x != id {
			out = append(out, dep{KindEvent, x})
		}
	}
	return out
}

// List returns the ledger for a mind, newest first.
func (s *Service) List(ctx context.Context, mind ids.ID, limit int) ([]Redaction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT id, mind_id, target_kind, target_id, reason, policy, content_hash, requested_by, cascade_of, created_at FROM redactions WHERE mind_id=$1 ORDER BY created_at DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Redaction
	for rows.Next() {
		var r Redaction
		var kind string
		var cascade *string
		if err := rows.Scan(&r.ID, &r.MindID, &kind, &r.TargetID, &r.Reason, &r.Policy, &r.ContentHash, &r.RequestedBy, &cascade, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Kind = Kind(kind)
		if cascade != nil {
			r.CascadeOf = ids.ID(*cascade)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func validKind(k Kind) bool {
	switch k {
	case KindEvent, KindMemory, KindMessage, KindLLMCall, KindBelief, KindAssertion:
		return true
	}
	return false
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Attest recomputes nothing: redacted content is gone. It exposes the hash
// so an operator holding an external copy can prove what was removed.
func Attest(r Redaction) string { return r.ContentHash }

var _ = json.Marshal

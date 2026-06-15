// Package audit records administrative actions in the append-only audit_log.
// Every mutation performed by an operator through the UI, API or CLI goes
// through Log so that the Audit page is complete.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Entry is one audit record.
type Entry struct {
	ID         ids.ID          `json:"id"`
	Seq        int64           `json:"seq"`
	At         string          `json:"at"`
	ActorKind  string          `json:"actor_kind"`
	ActorID    string          `json:"actor_id"`
	ActorName  string          `json:"actor_name"`
	Action     string          `json:"action"`
	TargetKind string          `json:"target_kind"`
	TargetID   string          `json:"target_id"`
	Details    json.RawMessage `json:"details"`
	IP         string          `json:"ip"`
}

// Logger writes audit entries.
type Logger struct {
	db  storage.Querier
	log *slog.Logger
}

// New creates an audit logger.
func New(db storage.Querier, log *slog.Logger) *Logger { return &Logger{db: db, log: log} }

// Log records an action. The actor is taken from the request principal when
// present, otherwise the action is attributed to the system. Failures are
// logged but never block the underlying operation. Details must not contain
// secrets; callers pass structured, already-redacted values.
func (l *Logger) Log(ctx context.Context, action, targetKind, targetID string, details any) {
	l.LogAs(ctx, "", "", action, targetKind, targetID, details)
}

// LogAs records an action on behalf of a named actor kind ("mind" for
// cognitive actions surfaced to the audit trail) when no principal applies.
func (l *Logger) LogAs(ctx context.Context, actorKind, actorID, action, targetKind, targetID string, details any) {
	kind, id, name, ip := "system", "", "", ""
	if p, ok := auth.PrincipalFrom(ctx); ok {
		kind, id, name = "user", p.User.ID.String(), p.User.Username
	}
	if actorKind != "" {
		kind, id = actorKind, actorID
	}
	if v, ok := ctx.Value(ipKey{}).(string); ok {
		ip = v
	}
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		raw = []byte(`{"marshal_error":true}`)
	}
	_, err = l.db.Exec(ctx, `INSERT INTO audit_log (id, actor_kind, actor_id, actor_name, action, target_kind, target_id, details, ip)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), kind, id, name, action, targetKind, targetID, raw, ip)
	if err != nil {
		l.log.Error("audit write failed", "action", action, "err", err)
	}
}

type ipKey struct{}

// WithIP attaches the client address so entries record where actions came from.
func WithIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ipKey{}, ip)
}

// List returns entries newest first, paginated by seq cursor (0 = latest).
func (l *Logger) List(ctx context.Context, beforeSeq int64, limit int, targetKind, targetID string) ([]Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := l.db.Query(ctx, `SELECT id, seq, at::text, actor_kind, actor_id, actor_name, action, target_kind, target_id, details, ip
		FROM audit_log WHERE ($1 = 0 OR seq < $1) AND ($3 = '' OR target_kind = $3) AND ($4 = '' OR target_id = $4)
		ORDER BY seq DESC LIMIT $2`, beforeSeq, limit, targetKind, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Seq, &e.At, &e.ActorKind, &e.ActorID, &e.ActorName, &e.Action, &e.TargetKind, &e.TargetID, &e.Details, &e.IP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

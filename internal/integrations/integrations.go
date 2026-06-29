// Package integrations connects minds to external sources: a watched research
// folder, git repositories, Prometheus-style metrics and signed webhooks.
// Everything ingested is hostile data: it arrives as observations tagged with
// source, trust, method and reference, never as instructions (spec §25–28,
// threats T2, T3, T6, T7, T22, T23).
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Kind of integration.
type Kind string

// Kinds.
const (
	ResearchFolder Kind = "research_folder"
	Git            Kind = "git"
	Prometheus     Kind = "prometheus"
	Webhook        Kind = "webhook"
)

// AllKinds lists kinds.
var AllKinds = []Kind{ResearchFolder, Git, Prometheus, Webhook}

// Valid reports whether k is known.
func (k Kind) Valid() bool {
	for _, x := range AllKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Integration is a configured source.
type Integration struct {
	ID            ids.ID         `json:"id"`
	MindID        ids.ID         `json:"mind_id"`
	Kind          Kind           `json:"kind"`
	Name          string         `json:"name"`
	Config        map[string]any `json:"config"`
	SecretRef     string         `json:"-"`
	SecretSet     bool           `json:"secret_set"`
	Trust         events.Trust   `json:"trust"`
	Enabled       bool           `json:"enabled"`
	Status        string         `json:"status"`
	State         map[string]any `json:"state"`
	LastRunAt     *time.Time     `json:"last_run_at,omitempty"`
	LastOKAt      *time.Time     `json:"last_ok_at,omitempty"`
	LastError     string         `json:"last_error"`
	IngestedCount int            `json:"ingested_count"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// Interval returns the polling interval from config (default 5 minutes, min 30s).
func (i Integration) Interval() time.Duration {
	if s, ok := i.Config["interval_seconds"].(float64); ok && s >= 30 {
		return time.Duration(s) * time.Second
	}
	return 5 * time.Minute
}

// Run is one poll.
type Run struct {
	ID            ids.ID          `json:"id"`
	IntegrationID ids.ID          `json:"integration_id"`
	StartedAt     time.Time       `json:"started_at"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
	Items         int             `json:"items"`
	Observations  int             `json:"observations"`
	Errors        int             `json:"errors"`
	Notes         json.RawMessage `json:"notes"`
	Error         string          `json:"error"`
}

// Document is an ingested item.
type Document struct {
	ID             ids.ID    `json:"id"`
	MindID         ids.ID    `json:"mind_id"`
	IntegrationID  ids.ID    `json:"integration_id"`
	Fingerprint    string    `json:"fingerprint"`
	Reference      string    `json:"reference"`
	Title          string    `json:"title"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	ExtractedChars int       `json:"extracted_chars"`
	Excerpt        string    `json:"excerpt"`
	Status         string    `json:"status"`
	Error          string    `json:"error"`
	EventID        ids.ID    `json:"event_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ErrInvalid marks bad configuration.
var ErrInvalid = errors.New("integrations: invalid")

// Store persists integrations.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

const cols = `id, mind_id, kind, name, config, secret_ref, trust, enabled, status, state, last_run_at, last_ok_at, last_error, ingested_count, created_at, updated_at`

func scan(row pgx.Row) (Integration, error) {
	var i Integration
	var kind, trust string
	var cfg, st []byte
	if err := row.Scan(&i.ID, &i.MindID, &kind, &i.Name, &cfg, &i.SecretRef, &trust, &i.Enabled, &i.Status, &st, &i.LastRunAt, &i.LastOKAt, &i.LastError, &i.IngestedCount, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return i, storage.Translate(err)
	}
	i.Kind, i.Trust, i.SecretSet = Kind(kind), events.Trust(trust), i.SecretRef != ""
	json.Unmarshal(cfg, &i.Config) //nolint:errcheck
	json.Unmarshal(st, &i.State)   //nolint:errcheck
	if i.Config == nil {
		i.Config = map[string]any{}
	}
	if i.State == nil {
		i.State = map[string]any{}
	}
	return i, nil
}

// Input is the editable part.
type Input struct {
	MindID    ids.ID
	Kind      Kind
	Name      string
	Config    map[string]any
	SecretRef string // "" keeps existing on update
	Trust     events.Trust
	Enabled   bool
}

func (in *Input) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 64 || in.MindID.IsZero() {
		return fmt.Errorf("%w: mind and name required", ErrInvalid)
	}
	if !in.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, in.Kind)
	}
	if in.Config == nil {
		in.Config = map[string]any{}
	}
	for k := range in.Config {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "secret") || strings.Contains(lk, "password") || strings.Contains(lk, "token") || strings.Contains(lk, "api_key") {
			return fmt.Errorf("%w: put credentials in the secret reference, not in config (%s)", ErrInvalid, k)
		}
	}
	if !in.Trust.Valid() || in.Trust == events.TrustOperator || in.Trust == events.TrustUser || in.Trust == events.TrustSystem {
		in.Trust = events.TrustSource
		if in.Kind == ResearchFolder {
			in.Trust = events.TrustLow
		}
	}
	return validateKindConfig(in.Kind, in.Config)
}

// Create inserts an integration.
func (s *Store) Create(ctx context.Context, in Input) (Integration, error) {
	if err := in.validate(); err != nil {
		return Integration{}, err
	}
	cfg, _ := json.Marshal(in.Config)
	id := ids.New()
	_, err := s.db.Exec(ctx, `INSERT INTO integrations (id, mind_id, kind, name, config, secret_ref, trust, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, in.MindID, string(in.Kind), in.Name, cfg, in.SecretRef, string(in.Trust), in.Enabled)
	if err != nil {
		return Integration{}, storage.Translate(err)
	}
	return s.Get(ctx, id)
}

// Update edits an integration; an empty SecretRef keeps the stored one.
func (s *Store) Update(ctx context.Context, id ids.ID, in Input) (Integration, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Integration{}, err
	}
	in.MindID, in.Kind = cur.MindID, cur.Kind
	if err := in.validate(); err != nil {
		return Integration{}, err
	}
	cfg, _ := json.Marshal(in.Config)
	status := cur.Status
	if !in.Enabled {
		status = "disabled"
	} else if status == "disabled" {
		status = "new"
	}
	_, err = s.db.Exec(ctx, `UPDATE integrations SET name=$2, config=$3, secret_ref=CASE WHEN $4='' THEN secret_ref ELSE $4 END, trust=$5, enabled=$6, status=$7, updated_at=now() WHERE id=$1`,
		id, in.Name, cfg, in.SecretRef, string(in.Trust), in.Enabled, status)
	if err != nil {
		return Integration{}, storage.Translate(err)
	}
	return s.Get(ctx, id)
}

// Delete removes an integration and its runs/documents.
func (s *Store) Delete(ctx context.Context, id ids.ID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM integrations WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// Get fetches one.
func (s *Store) Get(ctx context.Context, id ids.ID) (Integration, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM integrations WHERE id=$1`, id))
}

// List returns integrations, optionally for one mind.
func (s *Store) List(ctx context.Context, mind ids.ID) ([]Integration, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM integrations WHERE $1::uuid IS NULL OR mind_id=$1 ORDER BY kind, lower(name)`, nullID(mind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Integration
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SetState persists poller bookkeeping.
func (s *Store) SetState(ctx context.Context, id ids.ID, state map[string]any) error {
	raw, _ := json.Marshal(state)
	_, err := s.db.Exec(ctx, `UPDATE integrations SET state=$2, updated_at=now() WHERE id=$1`, id, raw)
	return err
}

// RecordRun stores a finished run and updates health fields.
func (s *Store) RecordRun(ctx context.Context, id ids.ID, r Run) error {
	notes := r.Notes
	if len(notes) == 0 {
		notes = []byte("[]")
	}
	now := time.Now()
	if _, err := s.db.Exec(ctx, `INSERT INTO integration_runs (id, integration_id, started_at, finished_at, items, observations, errors, notes, error) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		ids.New(), id, r.StartedAt, now, r.Items, r.Observations, r.Errors, notes, r.Error); err != nil {
		return err
	}
	status := "ok"
	if r.Error != "" {
		status = "error"
	}
	_, err := s.db.Exec(ctx, `UPDATE integrations SET last_run_at=$2, last_ok_at=CASE WHEN $3='' THEN $2 ELSE last_ok_at END, last_error=$3, status=$4, ingested_count=ingested_count+$5, updated_at=now() WHERE id=$1`, id, now, r.Error, status, r.Observations)
	return err
}

// MarkStale flags enabled integrations whose last success is older than three intervals.
func (s *Store) MarkStale(ctx context.Context) error {
	list, err := s.List(ctx, ids.Nil)
	if err != nil {
		return err
	}
	for _, i := range list {
		if !i.Enabled || i.Kind == Webhook || i.LastOKAt == nil || i.Status != "ok" {
			continue
		}
		if time.Since(*i.LastOKAt) > 3*i.Interval() {
			s.db.Exec(ctx, `UPDATE integrations SET status='stale' WHERE id=$1 AND status='ok'`, i.ID) //nolint:errcheck
		}
	}
	return nil
}

// Runs lists recent runs.
func (s *Store) Runs(ctx context.Context, id ids.ID, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `SELECT id, integration_id, started_at, finished_at, items, observations, errors, notes, error FROM integration_runs WHERE integration_id=$1 ORDER BY started_at DESC LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.IntegrationID, &r.StartedAt, &r.FinishedAt, &r.Items, &r.Observations, &r.Errors, &r.Notes, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordDocument stores an ingested document row; duplicates by fingerprint
// are reported with ok=false and the existing row.
func (s *Store) RecordDocument(ctx context.Context, d Document) (Document, bool, error) {
	if d.ID.IsZero() {
		d.ID = ids.New()
	}
	d.CreatedAt = time.Now()
	var ev *string
	if !d.EventID.IsZero() {
		e := string(d.EventID)
		ev = &e
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO ingested_documents (id, mind_id, integration_id, fingerprint, reference, title, content_type, size_bytes, extracted_chars, excerpt, status, error, event_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (mind_id, fingerprint) DO NOTHING`,
		d.ID, d.MindID, d.IntegrationID, d.Fingerprint, d.Reference, d.Title, d.ContentType, d.SizeBytes, d.ExtractedChars, d.Excerpt, d.Status, d.Error, ev)
	if err != nil {
		return d, false, err
	}
	if tag.RowsAffected() == 0 {
		return d, false, nil
	}
	return d, true, nil
}

// HasFingerprint reports whether a document was already ingested for a mind.
func (s *Store) HasFingerprint(ctx context.Context, mind ids.ID, fp string) bool {
	var n int
	s.db.QueryRow(ctx, `SELECT count(*) FROM ingested_documents WHERE mind_id=$1 AND fingerprint=$2`, mind, fp).Scan(&n) //nolint:errcheck
	return n > 0
}

// Documents lists recent documents for an integration.
func (s *Store) Documents(ctx context.Context, id ids.ID, limit int) ([]Document, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT id, mind_id, integration_id, fingerprint, reference, title, content_type, size_bytes, extracted_chars, excerpt, status, error, event_id, created_at FROM ingested_documents WHERE integration_id=$1 ORDER BY created_at DESC LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		var ev *string
		if err := rows.Scan(&d.ID, &d.MindID, &d.IntegrationID, &d.Fingerprint, &d.Reference, &d.Title, &d.ContentType, &d.SizeBytes, &d.ExtractedChars, &d.Excerpt, &d.Status, &d.Error, &ev, &d.CreatedAt); err != nil {
			return nil, err
		}
		if ev != nil {
			d.EventID = ids.ID(*ev)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

// validateKindConfig checks the per-kind configuration keys.
func validateKindConfig(kind Kind, cfg map[string]any) error {
	str := func(k string) string { s, _ := cfg[k].(string); return strings.TrimSpace(s) }
	switch kind {
	case ResearchFolder:
		if str("path") == "" {
			return fmt.Errorf("%w: research_folder needs a path", ErrInvalid)
		}
	case Git:
		if str("url") == "" {
			return fmt.Errorf("%w: git needs a url (or local path)", ErrInvalid)
		}
	case Prometheus:
		u := str("endpoint")
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("%w: prometheus needs an http(s) endpoint", ErrInvalid)
		}
		qs, _ := cfg["queries"].([]any)
		if len(qs) == 0 {
			return fmt.Errorf("%w: prometheus needs at least one query", ErrInvalid)
		}
		for _, q := range qs {
			m, ok := q.(map[string]any)
			if !ok || strings.TrimSpace(fmt.Sprint(m["expr"])) == "" || strings.TrimSpace(fmt.Sprint(m["name"])) == "" {
				return fmt.Errorf("%w: each query needs name and expr", ErrInvalid)
			}
		}
	case Webhook:
		// nothing required; the secret reference is strongly recommended
	}
	return nil
}

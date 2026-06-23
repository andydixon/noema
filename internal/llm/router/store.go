package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Provider is a configured endpoint. APIKeyRef is a secret reference; the
// struct never carries plaintext.
type Provider struct {
	ID           ids.ID            `json:"id"`
	Name         string            `json:"name"`
	Kind         llm.ProviderKind  `json:"kind"`
	Endpoint     string            `json:"endpoint"`
	APIKeyRef    string            `json:"-"`
	KeyDescribed string            `json:"api_key"` // human description only
	AllowPrivate bool              `json:"allow_private"`
	Headers      map[string]string `json:"headers"`
	Timeout      time.Duration     `json:"timeout"`
	Enabled      bool              `json:"enabled"`
	LastTestAt   *time.Time        `json:"last_test_at,omitempty"`
	LastTestOK   *bool             `json:"last_test_ok,omitempty"`
	LastTestMsg  string            `json:"last_test_msg"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// Model is a named model on a provider.
type Model struct {
	ID             ids.ID         `json:"id"`
	ProviderID     ids.ID         `json:"provider_id"`
	Provider       string         `json:"provider"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"` // chat | embedding
	ContextWindow  int            `json:"context_window"`
	Dims           int            `json:"dims"`
	InputCostPerM  float64        `json:"input_cost_per_m"`
	OutputCostPerM float64        `json:"output_cost_per_m"`
	Params         map[string]any `json:"params"`
	Enabled        bool           `json:"enabled"`
}

// RoleAssignment maps a role to a primary model and fallbacks.
type RoleAssignment struct {
	Role        llm.Role `json:"role"`
	ModelID     *ids.ID  `json:"model_id,omitempty"`
	FallbackIDs []ids.ID `json:"fallback_ids"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	UpdatedBy   string   `json:"updated_by"`
}

// Call is a recorded model invocation.
type Call struct {
	ID            ids.ID          `json:"id"`
	MindID        ids.ID          `json:"mind_id,omitempty"`
	CorrelationID ids.ID          `json:"correlation_id,omitempty"`
	Role          llm.Role        `json:"role"`
	Purpose       string          `json:"purpose"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	PromptName    string          `json:"prompt_name"`
	PromptVersion int             `json:"prompt_version"`
	RequestHash   string          `json:"request_hash"`
	Request       json.RawMessage `json:"request"`
	Response      json.RawMessage `json:"response,omitempty"`
	Error         string          `json:"error,omitempty"`
	PromptTokens  int             `json:"prompt_tokens"`
	OutputTokens  int             `json:"output_tokens"`
	Cost          float64         `json:"cost"`
	LatencyMS     int             `json:"latency_ms"`
	Seed          *int64          `json:"seed,omitempty"`
	Replayed      bool            `json:"replayed"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ErrInvalid marks bad configuration input.
var ErrInvalid = errors.New("llm: invalid")

// Store persists LLM configuration and calls.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ---- providers ----

const provCols = `id, name, kind, endpoint, api_key_ref, allow_private, headers, timeout_ms, enabled, last_test_at, last_test_ok, last_test_msg, created_at, updated_at`

func scanProvider(row pgx.Row) (Provider, error) {
	var p Provider
	var kind string
	var headers []byte
	var timeoutMS int
	if err := row.Scan(&p.ID, &p.Name, &kind, &p.Endpoint, &p.APIKeyRef, &p.AllowPrivate, &headers, &timeoutMS, &p.Enabled, &p.LastTestAt, &p.LastTestOK, &p.LastTestMsg, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, storage.Translate(err)
	}
	p.Kind = llm.ProviderKind(kind)
	p.Timeout = time.Duration(timeoutMS) * time.Millisecond
	json.Unmarshal(headers, &p.Headers) //nolint:errcheck
	if p.Headers == nil {
		p.Headers = map[string]string{}
	}
	return p, nil
}

// ProviderInput is the editable part of a provider.
type ProviderInput struct {
	Name         string
	Kind         llm.ProviderKind
	Endpoint     string
	APIKeyRef    string // "" keeps the existing reference on update
	AllowPrivate bool
	Headers      map[string]string
	Timeout      time.Duration
	Enabled      bool
}

func (in *ProviderInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 64 {
		return fmt.Errorf("%w: name required (max 64)", ErrInvalid)
	}
	if !in.Kind.Valid() {
		return fmt.Errorf("%w: unknown provider kind %q", ErrInvalid, in.Kind)
	}
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	if in.Endpoint == "" {
		in.Endpoint = in.Kind.DefaultEndpoint()
	}
	if !strings.HasPrefix(in.Endpoint, "http://") && !strings.HasPrefix(in.Endpoint, "https://") {
		return fmt.Errorf("%w: endpoint must be an http(s) URL", ErrInvalid)
	}
	if in.Timeout <= 0 {
		in.Timeout = 60 * time.Second
	}
	if in.Headers == nil {
		in.Headers = map[string]string{}
	}
	for k := range in.Headers {
		if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "x-api-key") {
			return fmt.Errorf("%w: put credentials in the API key field, not headers", ErrInvalid)
		}
	}
	return nil
}

// CreateProvider adds a provider.
func (s *Store) CreateProvider(ctx context.Context, in ProviderInput) (Provider, error) {
	if err := in.validate(); err != nil {
		return Provider{}, err
	}
	id := ids.New()
	headers, _ := json.Marshal(in.Headers)
	_, err := s.db.Exec(ctx, `INSERT INTO llm_providers (id, name, kind, endpoint, api_key_ref, allow_private, headers, timeout_ms, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, in.Name, string(in.Kind), in.Endpoint, in.APIKeyRef, in.AllowPrivate, headers, in.Timeout.Milliseconds(), in.Enabled)
	if err != nil {
		return Provider{}, storage.Translate(err)
	}
	return s.GetProvider(ctx, id)
}

// UpdateProvider edits a provider; an empty APIKeyRef keeps the stored one.
func (s *Store) UpdateProvider(ctx context.Context, id ids.ID, in ProviderInput) (Provider, error) {
	if err := in.validate(); err != nil {
		return Provider{}, err
	}
	headers, _ := json.Marshal(in.Headers)
	tag, err := s.db.Exec(ctx, `UPDATE llm_providers SET name=$2, kind=$3, endpoint=$4, api_key_ref=CASE WHEN $5='' THEN api_key_ref ELSE $5 END, allow_private=$6, headers=$7, timeout_ms=$8, enabled=$9, updated_at=now() WHERE id=$1`,
		id, in.Name, string(in.Kind), in.Endpoint, in.APIKeyRef, in.AllowPrivate, headers, in.Timeout.Milliseconds(), in.Enabled)
	if err != nil {
		return Provider{}, storage.Translate(err)
	}
	if tag.RowsAffected() == 0 {
		return Provider{}, storage.ErrNotFound
	}
	return s.GetProvider(ctx, id)
}

// ClearProviderKey removes the stored key reference.
func (s *Store) ClearProviderKey(ctx context.Context, id ids.ID) error {
	_, err := s.db.Exec(ctx, `UPDATE llm_providers SET api_key_ref='', updated_at=now() WHERE id=$1`, id)
	return err
}

// DeleteProvider removes a provider and its models (calls keep provider name).
func (s *Store) DeleteProvider(ctx context.Context, id ids.ID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM llm_providers WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// RecordProviderTest stores the outcome of a connectivity test.
func (s *Store) RecordProviderTest(ctx context.Context, id ids.ID, ok bool, msg string) error {
	_, err := s.db.Exec(ctx, `UPDATE llm_providers SET last_test_at=now(), last_test_ok=$2, last_test_msg=$3 WHERE id=$1`, id, ok, msg)
	return err
}

// GetProvider fetches one provider.
func (s *Store) GetProvider(ctx context.Context, id ids.ID) (Provider, error) {
	return scanProvider(s.db.QueryRow(ctx, `SELECT `+provCols+` FROM llm_providers WHERE id=$1`, id))
}

// ListProviders lists providers by name.
func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.Query(ctx, `SELECT `+provCols+` FROM llm_providers ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- models ----

const modelCols = `m.id, m.provider_id, p.name, m.name, m.kind, m.context_window, m.dims, m.input_cost_per_m, m.output_cost_per_m, m.params, m.enabled`

func scanModel(row pgx.Row) (Model, error) {
	var m Model
	var params []byte
	if err := row.Scan(&m.ID, &m.ProviderID, &m.Provider, &m.Name, &m.Kind, &m.ContextWindow, &m.Dims, &m.InputCostPerM, &m.OutputCostPerM, &params, &m.Enabled); err != nil {
		return m, storage.Translate(err)
	}
	json.Unmarshal(params, &m.Params) //nolint:errcheck
	if m.Params == nil {
		m.Params = map[string]any{}
	}
	return m, nil
}

// ModelInput is the editable part of a model.
type ModelInput struct {
	ProviderID     ids.ID
	Name           string
	Kind           string
	ContextWindow  int
	Dims           int
	InputCostPerM  float64
	OutputCostPerM float64
	Params         map[string]any
	Enabled        bool
}

// CreateModel adds a model to a provider.
func (s *Store) CreateModel(ctx context.Context, in ModelInput) (Model, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.ProviderID.IsZero() {
		return Model{}, fmt.Errorf("%w: provider and model name required", ErrInvalid)
	}
	if in.Kind != "chat" && in.Kind != "embedding" {
		in.Kind = "chat"
	}
	if in.Params == nil {
		in.Params = map[string]any{}
	}
	params, _ := json.Marshal(in.Params)
	id := ids.New()
	_, err := s.db.Exec(ctx, `INSERT INTO llm_models (id, provider_id, name, kind, context_window, dims, input_cost_per_m, output_cost_per_m, params, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, in.ProviderID, in.Name, in.Kind, in.ContextWindow, in.Dims, in.InputCostPerM, in.OutputCostPerM, params, in.Enabled)
	if err != nil {
		return Model{}, storage.Translate(err)
	}
	return s.GetModel(ctx, id)
}

// UpdateModel edits a model.
func (s *Store) UpdateModel(ctx context.Context, id ids.ID, in ModelInput) (Model, error) {
	params, _ := json.Marshal(in.Params)
	tag, err := s.db.Exec(ctx, `UPDATE llm_models SET name=$2, kind=$3, context_window=$4, dims=$5, input_cost_per_m=$6, output_cost_per_m=$7, params=$8, enabled=$9 WHERE id=$1`,
		id, strings.TrimSpace(in.Name), in.Kind, in.ContextWindow, in.Dims, in.InputCostPerM, in.OutputCostPerM, params, in.Enabled)
	if err != nil {
		return Model{}, storage.Translate(err)
	}
	if tag.RowsAffected() == 0 {
		return Model{}, storage.ErrNotFound
	}
	return s.GetModel(ctx, id)
}

// DeleteModel removes a model.
func (s *Store) DeleteModel(ctx context.Context, id ids.ID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM llm_models WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// GetModel fetches a model with its provider name.
func (s *Store) GetModel(ctx context.Context, id ids.ID) (Model, error) {
	return scanModel(s.db.QueryRow(ctx, `SELECT `+modelCols+` FROM llm_models m JOIN llm_providers p ON p.id=m.provider_id WHERE m.id=$1`, id))
}

// ListModels lists all models.
func (s *Store) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := s.db.Query(ctx, `SELECT `+modelCols+` FROM llm_models m JOIN llm_providers p ON p.id=m.provider_id ORDER BY lower(p.name), lower(m.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- roles ----

// ListRoles returns assignments for every known role (unassigned roles have nil ModelID).
func (s *Store) ListRoles(ctx context.Context) ([]RoleAssignment, error) {
	rows, err := s.db.Query(ctx, `SELECT role, model_id, fallback_ids, temperature, max_tokens, updated_by FROM llm_roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byRole := map[llm.Role]RoleAssignment{}
	for rows.Next() {
		var a RoleAssignment
		var role string
		var fallbacks []string
		if err := rows.Scan(&role, &a.ModelID, &fallbacks, &a.Temperature, &a.MaxTokens, &a.UpdatedBy); err != nil {
			return nil, err
		}
		a.Role = llm.Role(role)
		for _, f := range fallbacks {
			a.FallbackIDs = append(a.FallbackIDs, ids.ID(f))
		}
		byRole[a.Role] = a
	}
	out := make([]RoleAssignment, 0, len(llm.Roles))
	for _, r := range llm.Roles {
		a, ok := byRole[r.Role]
		if !ok {
			a = RoleAssignment{Role: r.Role}
		}
		if a.FallbackIDs == nil {
			a.FallbackIDs = []ids.ID{}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetRole assigns a role.
func (s *Store) SetRole(ctx context.Context, a RoleAssignment) error {
	known := false
	for _, r := range llm.Roles {
		if r.Role == a.Role {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("%w: unknown role %q", ErrInvalid, a.Role)
	}
	fallbacks := make([]string, 0, len(a.FallbackIDs))
	for _, f := range a.FallbackIDs {
		fallbacks = append(fallbacks, string(f))
	}
	_, err := s.db.Exec(ctx, `INSERT INTO llm_roles (role, model_id, fallback_ids, temperature, max_tokens, updated_by) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (role) DO UPDATE SET model_id=excluded.model_id, fallback_ids=excluded.fallback_ids, temperature=excluded.temperature, max_tokens=excluded.max_tokens, updated_by=excluded.updated_by, updated_at=now()`,
		string(a.Role), a.ModelID, fallbacks, a.Temperature, a.MaxTokens, a.UpdatedBy)
	return err
}

// ---- calls ----

// RecordCall stores a call.
func (s *Store) RecordCall(ctx context.Context, c Call) error {
	if c.ID.IsZero() {
		c.ID = ids.New()
	}
	_, err := s.db.Exec(ctx, `INSERT INTO llm_calls (id, mind_id, correlation_id, role, purpose, provider, model, prompt_name, prompt_version, request_hash, request, response, error, prompt_tokens, output_tokens, cost, latency_ms, seed, replayed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		c.ID, nullID(c.MindID), nullID(c.CorrelationID), string(c.Role), c.Purpose, c.Provider, c.Model, c.PromptName, c.PromptVersion, c.RequestHash, c.Request, nullRaw(c.Response), c.Error, c.PromptTokens, c.OutputTokens, c.Cost, c.LatencyMS, c.Seed, c.Replayed)
	return err
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

func nullRaw(r json.RawMessage) *json.RawMessage {
	if len(r) == 0 {
		return nil
	}
	return &r
}

// CallFilter selects calls.
type CallFilter struct {
	MindID        ids.ID
	CorrelationID ids.ID
	Role          llm.Role
	ErrorsOnly    bool
	Limit         int
}

// ListCalls returns calls newest first.
func (s *Store) ListCalls(ctx context.Context, f CallFilter) ([]Call, error) {
	where := []string{"TRUE"}
	args := []any{}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if !f.MindID.IsZero() {
		where = append(where, "mind_id = "+arg(f.MindID))
	}
	if !f.CorrelationID.IsZero() {
		where = append(where, "correlation_id = "+arg(f.CorrelationID))
	}
	if f.Role != "" {
		where = append(where, "role = "+arg(string(f.Role)))
	}
	if f.ErrorsOnly {
		where = append(where, "error <> ''")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT id, mind_id, correlation_id, role, purpose, provider, model, prompt_name, prompt_version, request_hash, request, response, error, prompt_tokens, output_tokens, cost, latency_ms, seed, replayed, created_at
		FROM llm_calls WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT `+arg(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Call
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCall(row pgx.Row) (Call, error) {
	var c Call
	var mind, corr *string
	var role string
	var resp *json.RawMessage
	if err := row.Scan(&c.ID, &mind, &corr, &role, &c.Purpose, &c.Provider, &c.Model, &c.PromptName, &c.PromptVersion, &c.RequestHash, &c.Request, &resp, &c.Error, &c.PromptTokens, &c.OutputTokens, &c.Cost, &c.LatencyMS, &c.Seed, &c.Replayed, &c.CreatedAt); err != nil {
		return c, storage.Translate(err)
	}
	c.Role = llm.Role(role)
	if mind != nil {
		c.MindID = ids.ID(*mind)
	}
	if corr != nil {
		c.CorrelationID = ids.ID(*corr)
	}
	if resp != nil {
		c.Response = *resp
	}
	return c, nil
}

// GetCall fetches a call.
func (s *Store) GetCall(ctx context.Context, id ids.ID) (Call, error) {
	return scanCall(s.db.QueryRow(ctx, `SELECT id, mind_id, correlation_id, role, purpose, provider, model, prompt_name, prompt_version, request_hash, request, response, error, prompt_tokens, output_tokens, cost, latency_ms, seed, replayed, created_at FROM llm_calls WHERE id=$1`, id))
}

// FindRecorded returns the most recent successful call with the same request
// hash, for exact replay.
func (s *Store) FindRecorded(ctx context.Context, hash string) (Call, error) {
	return scanCall(s.db.QueryRow(ctx, `SELECT id, mind_id, correlation_id, role, purpose, provider, model, prompt_name, prompt_version, request_hash, request, response, error, prompt_tokens, output_tokens, cost, latency_ms, seed, replayed, created_at
		FROM llm_calls WHERE request_hash=$1 AND error='' AND NOT replayed ORDER BY created_at DESC LIMIT 1`, hash))
}

// UsageRow is aggregated usage.
type UsageRow struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Role         string  `json:"role"`
	Calls        int64   `json:"calls"`
	Errors       int64   `json:"errors"`
	PromptTokens int64   `json:"prompt_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Cost         float64 `json:"cost"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

// Usage aggregates calls since a time, grouped by provider, model and role.
func (s *Store) Usage(ctx context.Context, since time.Time, mind ids.ID) ([]UsageRow, error) {
	rows, err := s.db.Query(ctx, `SELECT provider, model, role, count(*), count(*) FILTER (WHERE error <> ''), coalesce(sum(prompt_tokens),0), coalesce(sum(output_tokens),0), coalesce(sum(cost),0), coalesce(avg(latency_ms),0)
		FROM llm_calls WHERE created_at >= $1 AND ($2::uuid IS NULL OR mind_id = $2) GROUP BY provider, model, role ORDER BY sum(prompt_tokens)+sum(output_tokens) DESC`, since, nullID(mind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.Provider, &u.Model, &u.Role, &u.Calls, &u.Errors, &u.PromptTokens, &u.OutputTokens, &u.Cost, &u.AvgLatencyMS); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// TokensSince sums tokens used since a time, optionally for one mind. Used
// for budget enforcement.
func (s *Store) TokensSince(ctx context.Context, since time.Time, mind ids.ID) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT coalesce(sum(prompt_tokens + output_tokens),0) FROM llm_calls WHERE created_at >= $1 AND NOT replayed AND ($2::uuid IS NULL OR mind_id = $2)`, since, nullID(mind)).Scan(&n)
	return n, err
}

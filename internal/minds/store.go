package minds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Status of a mind.
type Status string

// Mind statuses. Archived minds keep all history but never run.
const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusArchived Status = "archived"
)

// Mind is the identity row plus its current personality and configuration.
type Mind struct {
	ID                 ids.ID          `json:"id"`
	Slug               string          `json:"slug"`
	DisplayName        string          `json:"display_name"`
	Status             Status          `json:"status"`
	Config             CognitiveConfig `json:"config"`
	Notes              string          `json:"notes"`
	PersonalityVersion int             `json:"personality_version"`
	Personality        Personality     `json:"personality"`
	TemplateID         *ids.ID         `json:"template_id,omitempty"`
	ForkedFromMind     *ids.ID         `json:"forked_from_mind,omitempty"`
	ForkedFromSnapshot *ids.ID         `json:"forked_from_snapshot,omitempty"`
	ClonedFromMind     *ids.ID         `json:"cloned_from_mind,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// Version is one personality revision.
type Version struct {
	ID          ids.ID         `json:"id"`
	MindID      ids.ID         `json:"mind_id"`
	Version     int            `json:"version"`
	Personality Personality    `json:"personality"`
	TemplateID  *ids.ID        `json:"template_id,omitempty"`
	Overrides   map[string]any `json:"overrides"`
	ChangeNote  string         `json:"change_note"`
	CreatedBy   string         `json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
}

// Template is a reusable personality.
type Template struct {
	ID          ids.ID      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Personality Personality `json:"personality"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// Store persists minds.
type Store struct {
	db *pgxpool.Pool
}

// NewStore creates the store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ErrInvalid marks caller input problems.
var ErrInvalid = errors.New("invalid")

// CreateParams describes a new mind.
type CreateParams struct {
	Slug        string
	DisplayName string
	Personality *Personality // nil → DefaultPersonality
	TemplateID  *ids.ID      // start from a template (Personality then acts as overrides)
	Config      *CognitiveConfig
	Notes       string
	CreatedBy   string
	// Research allows ablations and workspace overrides; only snapshot
	// restores (forks, imports of research forks) set it.
	Research bool
}

// Create inserts a mind with personality version 1 and an initial self-model.
func (s *Store) Create(ctx context.Context, p CreateParams) (Mind, error) {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	if !slugRe.MatchString(p.Slug) {
		return Mind{}, fmt.Errorf("%w: slug must be lowercase letters, digits and hyphens", ErrInvalid)
	}
	if p.DisplayName == "" {
		p.DisplayName = p.Slug
	}
	pers := DefaultPersonality(p.DisplayName)
	var overrides = map[string]any{}
	if p.TemplateID != nil {
		t, err := s.GetTemplate(ctx, *p.TemplateID)
		if err != nil {
			return Mind{}, fmt.Errorf("template: %w", err)
		}
		pers = t.Personality
		pers.Identity.Name, pers.Identity.DisplayName = p.DisplayName, p.DisplayName
		if p.Personality != nil {
			overrides = Overrides(t.Personality, *p.Personality)
			pers = ApplyOverrides(t.Personality, overrides)
		} else {
			overrides = Overrides(t.Personality, pers)
		}
	} else if p.Personality != nil {
		pers = *p.Personality
	}
	pers.Normalise()
	if err := pers.Validate(); err != nil {
		return Mind{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	cfg := DefaultConfig()
	if p.Config != nil {
		cfg = *p.Config
	}
	cfg.Normalise()
	if cfg.ResearchSettings() && !p.Research {
		return Mind{}, fmt.Errorf("%w: ablations and workspace overrides are only allowed on research forks, never on an original mind", ErrInvalid)
	}
	if err := cfg.ValidateResearch(); err != nil {
		return Mind{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	m := Mind{ID: ids.New(), Slug: p.Slug, DisplayName: p.DisplayName, Status: StatusActive, Config: cfg, Notes: p.Notes,
		PersonalityVersion: 1, Personality: pers, TemplateID: p.TemplateID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	err := storage.Tx(ctx, s.db, func(tx pgx.Tx) error {
		cfgJSON, _ := json.Marshal(cfg)
		if _, err := tx.Exec(ctx, `INSERT INTO minds (id, slug, display_name, status, config, notes, personality_version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,1,$7,$7)`, m.ID, m.Slug, m.DisplayName, string(m.Status), cfgJSON, m.Notes, m.CreatedAt); err != nil {
			return storage.Translate(err)
		}
		if err := insertVersion(ctx, tx, m.ID, 1, pers, p.TemplateID, overrides, "created", p.CreatedBy); err != nil {
			return err
		}
		sm := NewSelfModel(pers)
		smJSON, _ := json.Marshal(sm)
		_, err := tx.Exec(ctx, `INSERT INTO self_models (mind_id, model) VALUES ($1,$2)`, m.ID, smJSON)
		return err
	})
	return m, err
}

func insertVersion(ctx context.Context, q storage.Querier, mindID ids.ID, version int, p Personality, templateID *ids.ID, overrides map[string]any, note, by string) error {
	pj, _ := json.Marshal(p)
	oj, _ := json.Marshal(overrides)
	if overrides == nil {
		oj = []byte("{}")
	}
	_, err := q.Exec(ctx, `INSERT INTO personality_versions (id, mind_id, version, personality, template_id, overrides, change_note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), mindID, version, pj, templateID, oj, note, by)
	return err
}

const mindCols = `m.id, m.slug, m.display_name, m.status, m.config, m.notes, m.personality_version, m.forked_from_mind, m.forked_from_snapshot, m.cloned_from_mind, m.created_at, m.updated_at, v.personality, v.template_id`
const mindFrom = ` FROM minds m JOIN personality_versions v ON v.mind_id = m.id AND v.version = m.personality_version `

func scanMind(row pgx.Row) (Mind, error) {
	var m Mind
	var status string
	var cfg, pers []byte
	err := row.Scan(&m.ID, &m.Slug, &m.DisplayName, &status, &cfg, &m.Notes, &m.PersonalityVersion, &m.ForkedFromMind, &m.ForkedFromSnapshot, &m.ClonedFromMind, &m.CreatedAt, &m.UpdatedAt, &pers, &m.TemplateID)
	if err != nil {
		return Mind{}, storage.Translate(err)
	}
	m.Status = Status(status)
	json.Unmarshal(cfg, &m.Config)       //nolint:errcheck
	json.Unmarshal(pers, &m.Personality) //nolint:errcheck
	m.Config.Normalise()
	return m, nil
}

// Get fetches a mind by id.
func (s *Store) Get(ctx context.Context, id ids.ID) (Mind, error) {
	return scanMind(s.db.QueryRow(ctx, `SELECT `+mindCols+mindFrom+`WHERE m.id=$1`, id))
}

// Resolve accepts a slug or an id.
func (s *Store) Resolve(ctx context.Context, ref string) (Mind, error) {
	if id, err := ids.Parse(ref); err == nil {
		return s.Get(ctx, id)
	}
	return scanMind(s.db.QueryRow(ctx, `SELECT `+mindCols+mindFrom+`WHERE lower(m.slug)=lower($1)`, ref))
}

// List returns minds, optionally including archived ones.
func (s *Store) List(ctx context.Context, includeArchived bool) ([]Mind, error) {
	rows, err := s.db.Query(ctx, `SELECT `+mindCols+mindFrom+`WHERE ($1 OR m.status <> 'archived') ORDER BY m.created_at`, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mind
	for rows.Next() {
		m, err := scanMind(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateParams are the mutable identity fields.
type UpdateParams struct {
	DisplayName *string
	Notes       *string
	Status      *Status
	Config      *CognitiveConfig
}

// Update changes identity fields and configuration.
func (s *Store) Update(ctx context.Context, id ids.ID, p UpdateParams) (Mind, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return Mind{}, err
	}
	if p.DisplayName != nil && strings.TrimSpace(*p.DisplayName) != "" {
		m.DisplayName = strings.TrimSpace(*p.DisplayName)
	}
	if p.Notes != nil {
		m.Notes = *p.Notes
	}
	if p.Status != nil {
		switch *p.Status {
		case StatusActive, StatusDisabled, StatusArchived:
			m.Status = *p.Status
		default:
			return Mind{}, fmt.Errorf("%w: unknown status", ErrInvalid)
		}
	}
	if p.Config != nil {
		c := *p.Config
		c.Normalise()
		if c.ResearchSettings() && m.ForkedFromSnapshot == nil {
			return Mind{}, fmt.Errorf("%w: ablations and workspace overrides are only allowed on forks, never on an original mind", ErrInvalid)
		}
		if err := c.ValidateResearch(); err != nil {
			return Mind{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		m.Config = c
	}
	cfgJSON, _ := json.Marshal(m.Config)
	_, err = s.db.Exec(ctx, `UPDATE minds SET display_name=$2, notes=$3, status=$4, config=$5, updated_at=now() WHERE id=$1`,
		id, m.DisplayName, m.Notes, string(m.Status), cfgJSON)
	if err != nil {
		return Mind{}, err
	}
	return s.Get(ctx, id)
}

// SavePersonality appends a new version. If the mind follows a template, the
// overrides relative to that template are recorded alongside.
func (s *Store) SavePersonality(ctx context.Context, id ids.ID, p Personality, note, by string) (Version, error) {
	p.Normalise()
	if err := p.Validate(); err != nil {
		return Version{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	m, err := s.Get(ctx, id)
	if err != nil {
		return Version{}, err
	}
	overrides := map[string]any{}
	if m.TemplateID != nil {
		if t, err := s.GetTemplate(ctx, *m.TemplateID); err == nil {
			overrides = Overrides(t.Personality, p)
		}
	}
	return s.appendVersion(ctx, m, p, m.TemplateID, overrides, note, by)
}

func (s *Store) appendVersion(ctx context.Context, m Mind, p Personality, templateID *ids.ID, overrides map[string]any, note, by string) (Version, error) {
	next := m.PersonalityVersion + 1
	err := storage.Tx(ctx, s.db, func(tx pgx.Tx) error {
		if err := insertVersion(ctx, tx, m.ID, next, p, templateID, overrides, note, by); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE minds SET personality_version=$2, display_name=$3, updated_at=now() WHERE id=$1`, m.ID, next, p.Identity.DisplayName); err != nil {
			return err
		}
		// Keep the self-model's identity in step.
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT model FROM self_models WHERE mind_id=$1 FOR UPDATE`, m.ID).Scan(&raw); err == nil {
			var sm SelfModel
			json.Unmarshal(raw, &sm) //nolint:errcheck
			sm.Refresh(p)
			smJSON, _ := json.Marshal(sm)
			tx.Exec(ctx, `UPDATE self_models SET model=$2, updated_at=now() WHERE mind_id=$1`, m.ID, smJSON) //nolint:errcheck
		}
		return nil
	})
	if err != nil {
		return Version{}, err
	}
	return s.GetVersion(ctx, m.ID, next)
}

// ApplyTemplate re-bases a mind on a template, preserving its recorded
// overrides (spec §4: assign templates without losing mind-specific overrides).
func (s *Store) ApplyTemplate(ctx context.Context, id ids.ID, templateID *ids.ID, by string) (Version, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return Version{}, err
	}
	cur, err := s.GetVersion(ctx, id, m.PersonalityVersion)
	if err != nil {
		return Version{}, err
	}
	overrides := cur.Overrides
	if m.TemplateID == nil {
		// Not previously templated: only identity fields are considered mind-specific.
		overrides = map[string]any{"identity": toMap(m.Personality)["identity"]}
	}
	if templateID == nil {
		return s.appendVersion(ctx, m, m.Personality, nil, map[string]any{}, "detached from template", by)
	}
	t, err := s.GetTemplate(ctx, *templateID)
	if err != nil {
		return Version{}, fmt.Errorf("template: %w", err)
	}
	p := ApplyOverrides(t.Personality, overrides)
	if p.Identity.Name == "" {
		p.Identity.Name, p.Identity.DisplayName = m.Personality.Identity.Name, m.Personality.Identity.DisplayName
	}
	return s.appendVersion(ctx, m, p, templateID, overrides, "applied template "+t.Name, by)
}

// Restore makes an older version current by appending a copy of it.
func (s *Store) Restore(ctx context.Context, id ids.ID, version int, by string) (Version, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return Version{}, err
	}
	old, err := s.GetVersion(ctx, id, version)
	if err != nil {
		return Version{}, err
	}
	return s.appendVersion(ctx, m, old.Personality, old.TemplateID, old.Overrides, fmt.Sprintf("restored version %d", version), by)
}

// GetVersion fetches one personality version.
func (s *Store) GetVersion(ctx context.Context, id ids.ID, version int) (Version, error) {
	return scanVersion(s.db.QueryRow(ctx, `SELECT id, mind_id, version, personality, template_id, overrides, change_note, created_by, created_at FROM personality_versions WHERE mind_id=$1 AND version=$2`, id, version))
}

func scanVersion(row pgx.Row) (Version, error) {
	var v Version
	var pers, ov []byte
	if err := row.Scan(&v.ID, &v.MindID, &v.Version, &pers, &v.TemplateID, &ov, &v.ChangeNote, &v.CreatedBy, &v.CreatedAt); err != nil {
		return Version{}, storage.Translate(err)
	}
	json.Unmarshal(pers, &v.Personality) //nolint:errcheck
	json.Unmarshal(ov, &v.Overrides)     //nolint:errcheck
	if v.Overrides == nil {
		v.Overrides = map[string]any{}
	}
	return v, nil
}

// ListVersions returns all versions newest first.
func (s *Store) ListVersions(ctx context.Context, id ids.ID) ([]Version, error) {
	rows, err := s.db.Query(ctx, `SELECT id, mind_id, version, personality, template_id, overrides, change_note, created_by, created_at FROM personality_versions WHERE mind_id=$1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Clone creates a new mind with a copy of the source's current personality,
// template link and configuration. Memories and history are not copied:
// cloning is "same character, fresh experience"; forking from a snapshot
// (phase 15) copies state.
func (s *Store) Clone(ctx context.Context, sourceID ids.ID, slug, displayName, by string) (Mind, error) {
	src, err := s.Get(ctx, sourceID)
	if err != nil {
		return Mind{}, err
	}
	p := src.Personality
	if displayName == "" {
		displayName = slug
	}
	p.Identity.Name, p.Identity.DisplayName = displayName, displayName
	cfg := src.Config
	m, err := s.Create(ctx, CreateParams{Slug: slug, DisplayName: displayName, Personality: &p, TemplateID: src.TemplateID, Config: &cfg, Notes: "cloned from " + src.Slug, CreatedBy: by})
	if err != nil {
		return Mind{}, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE minds SET cloned_from_mind=$2 WHERE id=$1`, m.ID, src.ID); err != nil {
		return Mind{}, err
	}
	return s.Get(ctx, m.ID)
}

// GetSelfModel returns the self-model.
func (s *Store) GetSelfModel(ctx context.Context, id ids.ID) (SelfModel, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT model FROM self_models WHERE mind_id=$1`, id).Scan(&raw); err != nil {
		return SelfModel{}, storage.Translate(err)
	}
	var sm SelfModel
	return sm, json.Unmarshal(raw, &sm)
}

// SaveSelfModel replaces the self-model. Callers emit the self_model_updated event.
func (s *Store) SaveSelfModel(ctx context.Context, id ids.ID, sm SelfModel) error {
	sm.UpdatedAt = time.Now()
	raw, _ := json.Marshal(sm)
	_, err := s.db.Exec(ctx, `INSERT INTO self_models (mind_id, model) VALUES ($1,$2) ON CONFLICT (mind_id) DO UPDATE SET model=excluded.model, updated_at=now()`, id, raw)
	return err
}

// ---- templates ----

// CreateTemplate stores a template.
func (s *Store) CreateTemplate(ctx context.Context, name, description string, p Personality) (Template, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return Template{}, fmt.Errorf("%w: name required (max 80 chars)", ErrInvalid)
	}
	p.Normalise()
	if p.Identity.Name == "" {
		p.Identity.Name, p.Identity.DisplayName = name, name
	}
	if err := p.Validate(); err != nil {
		return Template{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	t := Template{ID: ids.New(), Name: name, Description: description, Personality: p, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	pj, _ := json.Marshal(p)
	_, err := s.db.Exec(ctx, `INSERT INTO personality_templates (id, name, description, personality, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$5)`, t.ID, t.Name, t.Description, pj, t.CreatedAt)
	return t, storage.Translate(err)
}

// UpdateTemplate replaces a template's content. Minds using it are not changed
// until an operator re-applies it (explicit, audited).
func (s *Store) UpdateTemplate(ctx context.Context, id ids.ID, name, description string, p Personality) (Template, error) {
	p.Normalise()
	if err := p.Validate(); err != nil {
		return Template{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	pj, _ := json.Marshal(p)
	tag, err := s.db.Exec(ctx, `UPDATE personality_templates SET name=$2, description=$3, personality=$4, updated_at=now() WHERE id=$1`, id, strings.TrimSpace(name), description, pj)
	if err != nil {
		return Template{}, storage.Translate(err)
	}
	if tag.RowsAffected() == 0 {
		return Template{}, storage.ErrNotFound
	}
	return s.GetTemplate(ctx, id)
}

// DeleteTemplate removes a template. Versions keep a dangling reference set to
// NULL by the database; their stored effective personality is unaffected.
func (s *Store) DeleteTemplate(ctx context.Context, id ids.ID) error {
	return storage.Tx(ctx, s.db, func(tx pgx.Tx) error {
		// personality_versions is append-only, so the FK cannot be nulled; instead
		// refuse deletion while any mind's *current* version depends on it.
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM minds m JOIN personality_versions v ON v.mind_id=m.id AND v.version=m.personality_version WHERE v.template_id=$1`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: template is in use by %d mind(s); detach them first", ErrInvalid, n)
		}
		var hist int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM personality_versions WHERE template_id=$1`, id).Scan(&hist); err != nil {
			return err
		}
		if hist > 0 {
			return fmt.Errorf("%w: template is referenced by personality history and cannot be deleted; rename it instead", ErrInvalid)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM personality_templates WHERE id=$1`, id)
		if err == nil && tag.RowsAffected() == 0 {
			return storage.ErrNotFound
		}
		return err
	})
}

// GetTemplate fetches a template.
func (s *Store) GetTemplate(ctx context.Context, id ids.ID) (Template, error) {
	return scanTemplate(s.db.QueryRow(ctx, `SELECT id, name, description, personality, created_at, updated_at FROM personality_templates WHERE id=$1`, id))
}

func scanTemplate(row pgx.Row) (Template, error) {
	var t Template
	var pj []byte
	if err := row.Scan(&t.ID, &t.Name, &t.Description, &pj, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Template{}, storage.Translate(err)
	}
	json.Unmarshal(pj, &t.Personality) //nolint:errcheck
	return t, nil
}

// ListTemplates returns all templates by name.
func (s *Store) ListTemplates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, description, personality, created_at, updated_at FROM personality_templates ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

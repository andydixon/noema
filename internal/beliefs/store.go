package beliefs

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
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Belief is a proposition with derived confidence and status.
type Belief struct {
	ID            ids.ID             `json:"id"`
	MindID        ids.ID             `json:"mind_id"`
	Proposition   string             `json:"proposition"`
	Subject       string             `json:"subject"`
	Epistemic     concepts.Epistemic `json:"epistemic"`
	Status        Status             `json:"status"`
	Confidence    float64            `json:"confidence"`
	Prior         float64            `json:"prior"`
	Version       int                `json:"version"`
	Source        string             `json:"source"`
	Detail        map[string]any     `json:"detail"`
	OriginEvent   ids.ID             `json:"origin_event,omitempty"`
	SupersededBy  ids.ID             `json:"superseded_by,omitempty"`
	PinnedStatus  *Status            `json:"pinned_status,omitempty"`
	FirstCreated  time.Time          `json:"first_created"`
	LastEvaluated time.Time          `json:"last_evaluated"`
	Evaluations   int                `json:"evaluations"`
	// Temporal scope and validity (see temporal.go). ValidUntil is set when a
	// later observation superseded a transient state; the belief then says
	// what was true, and when.
	TemporalScope string     `json:"temporal_scope"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	ValidFrom     *time.Time `json:"valid_from,omitempty"`
	ValidUntil    *time.Time `json:"valid_until,omitempty"`
	RedactedAt    *time.Time `json:"redacted_at,omitempty"`
}

// EvidenceRow is stored evidence.
type EvidenceRow struct {
	ID        ids.ID `json:"id"`
	Evidence  `json:",inline"`
	Note      string    `json:"note"`
	EventID   ids.ID    `json:"event_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Alternative is a competing explanation kept alongside a belief.
type Alternative struct {
	ID          ids.ID    `json:"id"`
	Proposition string    `json:"proposition"`
	Confidence  float64   `json:"confidence"`
	Note        string    `json:"note"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// Version is one entry in the append-only history.
type Version struct {
	Version     int       `json:"version"`
	Status      Status    `json:"status"`
	Confidence  float64   `json:"confidence"`
	Proposition string    `json:"proposition"`
	Reason      string    `json:"reason"`
	Breakdown   Breakdown `json:"breakdown"`
	EventID     ids.ID    `json:"event_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Link relates two beliefs.
type Link struct {
	ID         ids.ID    `json:"id"`
	FromID     ids.ID    `json:"from_id"`
	ToID       ids.ID    `json:"to_id"`
	Relation   string    `json:"relation"`
	Confidence float64   `json:"confidence"`
	Other      *Belief   `json:"other,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Correction is an operator statement about the mind's content.
type Correction struct {
	ID         ids.ID    `json:"id"`
	Kind       string    `json:"kind"`
	TargetKind string    `json:"target_kind"`
	TargetID   string    `json:"target_id"`
	Statement  string    `json:"statement"`
	Reason     string    `json:"reason"`
	CreatedBy  string    `json:"created_by"`
	EventID    ids.ID    `json:"event_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ErrInvalid marks bad input; ErrBlocked means a do-not-infer correction applies.
var (
	ErrInvalid = errors.New("invalid belief")
	ErrBlocked = errors.New("blocked by operator correction")
)

// Emitter appends events inside the caller's transaction and returns them.
// The brain cycle and the API each provide one so provenance events are
// written atomically with belief rows.
type Emitter func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error)

// Store persists beliefs.
type Store struct {
	db *pgxpool.Pool
}

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// New describes a belief to create.
type New struct {
	MindID       ids.ID
	Proposition  string
	Subject      string
	Epistemic    concepts.Epistemic
	Source       string
	Evidence     []EvidenceInput
	Alternatives []string
	Detail       map[string]any
	OriginEvent  ids.ID
	Correlation  ids.ID
	Causation    ids.ID
	// ObservedAt is when the state described was observed (defaults to now);
	// TemporalScope overrides the deterministic classifier when the producer knows.
	ObservedAt    *time.Time
	TemporalScope string
}

// EvidenceInput is evidence supplied by callers.
type EvidenceInput struct {
	Direction   string
	Kind        string
	RefID       string
	Source      string
	Weight      float64
	Credibility float64
	Note        string
	// Provenance (see Evidence). Family defaults to Source; Derivation
	// defaults to direct. Producers that know their roots must say so.
	Derivation string
	Family     string
	RootIDs    []string
}

// Result is a belief with its computed breakdown and the events written.
type Result struct {
	Belief    Belief
	Breakdown Breakdown
	Events    []events.Event
}

// Create stores a belief, its evidence, alternatives and first version, emits
// belief_created/hypothesis_created, and runs contradiction detection.
func (s *Store) Create(ctx context.Context, q storage.Querier, emit Emitter, n New) (Result, error) {
	n.Proposition = strings.TrimSpace(n.Proposition)
	if n.MindID.IsZero() || n.Proposition == "" || len(n.Proposition) > 2000 {
		return Result{}, fmt.Errorf("%w: proposition required (max 2000 chars)", ErrInvalid)
	}
	if !n.Epistemic.Valid() {
		n.Epistemic = concepts.Hypothesis
	}
	if blocked, c := s.blocked(ctx, q, n.MindID, n.Proposition); blocked {
		return Result{}, fmt.Errorf("%w: %s", ErrBlocked, c.Statement)
	}
	var ev []Evidence
	var notes []string
	for _, e := range n.Evidence {
		ne := normalise(e)
		ne, note := s.applyReliability(ctx, q, n.MindID, strings.TrimSpace(n.Subject), ne, e.Note)
		ev = append(ev, ne)
		notes = append(notes, note)
	}
	prior := PriorFor(n.Epistemic)
	bd := Compute(prior, ev)
	st := Derive(bd, n.Epistemic)
	observed := time.Now()
	if n.ObservedAt != nil {
		observed = *n.ObservedAt
	}
	scope := n.TemporalScope
	if scope == "" {
		scope = ClassifyScope(n.Proposition)
	}
	b := Belief{ID: ids.New(), MindID: n.MindID, Proposition: n.Proposition, Subject: strings.TrimSpace(n.Subject), Epistemic: n.Epistemic, Status: st, Confidence: bd.Confidence,
		Prior: prior, Version: 1, Source: n.Source, Detail: n.Detail, OriginEvent: n.OriginEvent, FirstCreated: time.Now(), LastEvaluated: time.Now(), Evaluations: 1,
		TemporalScope: scope, ObservedAt: &observed}
	if scope == ScopeTransient {
		b.ValidFrom = &observed
	}
	if b.Detail == nil {
		b.Detail = map[string]any{}
	}
	detail, _ := json.Marshal(b.Detail)
	if _, err := q.Exec(ctx, `INSERT INTO beliefs (id, mind_id, proposition, subject, epistemic, status, confidence, prior, version, source, detail, origin_event, temporal_scope, observed_at, valid_from)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,$9,$10,$11,$12,$13,$14)`, b.ID, b.MindID, b.Proposition, b.Subject, string(b.Epistemic), string(b.Status), b.Confidence, b.Prior, b.Source, detail, nullID(b.OriginEvent), b.TemporalScope, b.ObservedAt, b.ValidFrom); err != nil {
		return Result{}, err
	}
	typ := events.BeliefCreated
	if n.Epistemic == concepts.Hypothesis {
		typ = events.HypothesisCreated
	}
	refs := []events.EvidenceRef{{Kind: "belief", ID: string(b.ID)}}
	for _, e := range n.Evidence {
		if e.RefID != "" {
			refs = append(refs, events.EvidenceRef{Kind: e.Kind, ID: e.RefID, Note: e.Direction + ": " + e.Note})
		}
	}
	created, err := emit(ctx, q, events.New{MindID: b.MindID, Type: typ, Source: n.Source, Summary: fmt.Sprintf("%s (%.2f, %s): %s", typ, b.Confidence, b.Status, b.Proposition),
		Salience: 0.5 + 0.3*b.Confidence, Confidence: b.Confidence, CausationID: n.Causation, CorrelationID: n.Correlation, Evidence: refs,
		Metadata: map[string]any{"belief_id": string(b.ID), "status": b.Status, "subject": b.Subject}, Payload: map[string]any{"belief_id": b.ID, "proposition": b.Proposition, "status": b.Status, "confidence": b.Confidence, "breakdown": bd, "alternatives": n.Alternatives}})
	if err != nil {
		return Result{}, err
	}
	res := Result{Belief: b, Breakdown: bd, Events: []events.Event{created}}
	for i, e := range ev {
		if err := s.insertEvidence(ctx, q, b, e, notes[i], created.ID); err != nil {
			return Result{}, err
		}
	}
	for _, alt := range n.Alternatives {
		if alt = strings.TrimSpace(alt); alt != "" {
			if _, err := q.Exec(ctx, `INSERT INTO belief_alternatives (id, belief_id, mind_id, proposition, confidence) VALUES ($1,$2,$3,$4,$5)`, ids.New(), b.ID, b.MindID, alt, 0.2); err != nil {
				return Result{}, err
			}
		}
	}
	if err := s.insertVersion(ctx, q, b, bd, "created", created.ID); err != nil {
		return Result{}, err
	}
	contra, err := s.detectContradictions(ctx, q, emit, b, created.ID)
	if err != nil {
		return Result{}, err
	}
	res.Events = append(res.Events, contra...)
	return res, nil
}

func normalise(e EvidenceInput) Evidence {
	if e.Direction != "against" {
		e.Direction = "for"
	}
	if e.Weight <= 0 || e.Weight > 1 {
		e.Weight = 0.5
	}
	if e.Credibility <= 0 || e.Credibility > 1 {
		e.Credibility = 0.5
	}
	if e.Kind == "" {
		e.Kind = "note"
	}
	if e.Derivation == "" {
		e.Derivation = DerivationDirect
	}
	if e.Family == "" {
		e.Family = e.Source
	}
	return Evidence{Direction: e.Direction, Kind: e.Kind, RefID: e.RefID, Source: e.Source, Weight: e.Weight, Credibility: e.Credibility,
		Derivation: e.Derivation, Family: e.Family, RootIDs: e.RootIDs, ProvenanceStatus: "recorded"}
}

func (s *Store) insertEvidence(ctx context.Context, q storage.Querier, b Belief, e Evidence, note string, eventID ids.ID) error {
	if e.Derivation == "" {
		e.Derivation = DerivationDirect
	}
	if e.Family == "" {
		e.Family = FamilyOf(e)
	}
	roots := e.RootIDs
	if roots == nil {
		roots = []string{}
	}
	_, err := q.Exec(ctx, `INSERT INTO belief_evidence (id, belief_id, mind_id, direction, kind, ref_id, source, weight, credibility, note, event_id, derivation, family, root_ids, provenance_status) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::uuid[],'recorded')`,
		ids.New(), b.ID, b.MindID, e.Direction, e.Kind, e.RefID, e.Source, e.Weight, e.Credibility, note, nullID(eventID), e.Derivation, e.Family, roots)
	return err
}

func (s *Store) insertVersion(ctx context.Context, q storage.Querier, b Belief, bd Breakdown, reason string, eventID ids.ID) error {
	raw, _ := json.Marshal(bd)
	_, err := q.Exec(ctx, `INSERT INTO belief_versions (id, belief_id, mind_id, version, status, confidence, proposition, reason, breakdown, event_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		ids.New(), b.ID, b.MindID, b.Version, string(b.Status), b.Confidence, b.Proposition, reason, raw, nullID(eventID))
	return err
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

const cols = `b.id, b.mind_id, b.proposition, b.subject, b.epistemic, b.status, b.confidence, b.prior, b.version, b.source, b.detail, b.origin_event, b.superseded_by, b.pinned_status, b.first_created, b.last_evaluated, b.evaluations, b.temporal_scope, b.observed_at, b.valid_from, b.valid_until, b.redacted_at`

func scan(row pgx.Row) (Belief, error) {
	var b Belief
	var epi, status string
	var detail []byte
	var origin, superseded, pinned *string
	if err := row.Scan(&b.ID, &b.MindID, &b.Proposition, &b.Subject, &epi, &status, &b.Confidence, &b.Prior, &b.Version, &b.Source, &detail, &origin, &superseded, &pinned, &b.FirstCreated, &b.LastEvaluated, &b.Evaluations, &b.TemporalScope, &b.ObservedAt, &b.ValidFrom, &b.ValidUntil, &b.RedactedAt); err != nil {
		return b, storage.Translate(err)
	}
	b.Epistemic, b.Status = concepts.Epistemic(epi), Status(status)
	json.Unmarshal(detail, &b.Detail) //nolint:errcheck
	if b.Detail == nil {
		b.Detail = map[string]any{}
	}
	if origin != nil {
		b.OriginEvent = ids.ID(*origin)
	}
	if superseded != nil {
		b.SupersededBy = ids.ID(*superseded)
	}
	if pinned != nil {
		st := Status(*pinned)
		b.PinnedStatus = &st
	}
	return b, nil
}

// Get fetches a belief scoped to a mind. A nil q reads from the pool.
func (s *Store) Get(ctx context.Context, q storage.Querier, mind, id ids.ID) (Belief, error) {
	if q == nil {
		q = s.db
	}
	return scan(q.QueryRow(ctx, `SELECT `+cols+` FROM beliefs b WHERE b.id=$1 AND b.mind_id=$2`, id, mind))
}

// Filter selects beliefs.
type Filter struct {
	Statuses      []Status
	MinConfidence float64
	MaxConfidence float64
	Subject       string
	Search        string
	Limit         int
}

// List returns beliefs by confidence descending.
func (s *Store) List(ctx context.Context, mind ids.ID, f Filter) ([]Belief, error) {
	where := []string{"b.mind_id = $1"}
	args := []any{mind}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if len(f.Statuses) > 0 {
		ss := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			ss[i] = string(st)
		}
		where = append(where, "b.status = ANY("+arg(ss)+")")
	} else {
		where = append(where, "b.status <> 'superseded'")
	}
	if f.MinConfidence > 0 {
		where = append(where, "b.confidence >= "+arg(f.MinConfidence))
	}
	if f.MaxConfidence > 0 {
		where = append(where, "b.confidence <= "+arg(f.MaxConfidence))
	}
	if f.Subject != "" {
		where = append(where, "b.subject = "+arg(f.Subject))
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		where = append(where, "b.search @@ websearch_to_tsquery('english', "+arg(q)+")")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM beliefs b WHERE `+strings.Join(where, " AND ")+` ORDER BY b.confidence DESC, b.last_evaluated DESC LIMIT `+arg(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Belief
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// EvidenceFor returns all evidence rows of a belief. A nil q reads from the pool.
func (s *Store) EvidenceFor(ctx context.Context, q storage.Querier, mind, id ids.ID) ([]EvidenceRow, error) {
	if q == nil {
		q = s.db
	}
	rows, err := q.Query(ctx, `SELECT id, direction, kind, ref_id, source, weight, credibility, retracted, note, event_id, created_at, derivation, family, root_ids::text[], provenance_status FROM belief_evidence WHERE belief_id=$1 AND mind_id=$2 ORDER BY created_at`, id, mind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceRow
	for rows.Next() {
		var r EvidenceRow
		var evID *string
		if err := rows.Scan(&r.ID, &r.Direction, &r.Kind, &r.RefID, &r.Source, &r.Weight, &r.Credibility, &r.Retracted, &r.Note, &evID, &r.CreatedAt, &r.Derivation, &r.Family, &r.RootIDs, &r.ProvenanceStatus); err != nil {
			return nil, err
		}
		if evID != nil {
			r.EventID = ids.ID(*evID)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddEvidence attaches evidence, recomputes confidence and status, appends a
// version when anything changed and emits the matching event.
func (s *Store) AddEvidence(ctx context.Context, q storage.Querier, emit Emitter, mind, id ids.ID, in EvidenceInput, reason string, causation, correlation ids.ID) (Result, error) {
	b, err := s.Get(ctx, q, mind, id)
	if err != nil {
		return Result{}, err
	}
	if b.Status == StatusSuperseded {
		return Result{}, fmt.Errorf("%w: belief is superseded", ErrInvalid)
	}
	e := normalise(in)
	e, note := s.applyReliability(ctx, q, mind, b.Subject, e, in.Note)
	if err := s.insertEvidence(ctx, q, b, e, note, ids.Nil); err != nil {
		return Result{}, err
	}
	return s.reevaluate(ctx, q, emit, b, reason, causation, correlation, &e, in)
}

// Reevaluate recomputes a belief from its stored evidence (used after
// retractions and corrections).
func (s *Store) Reevaluate(ctx context.Context, q storage.Querier, emit Emitter, mind, id ids.ID, reason string) (Result, error) {
	b, err := s.Get(ctx, q, mind, id)
	if err != nil {
		return Result{}, err
	}
	return s.reevaluate(ctx, q, emit, b, reason, ids.Nil, ids.Nil, nil, EvidenceInput{})
}

func (s *Store) reevaluate(ctx context.Context, q storage.Querier, emit Emitter, b Belief, reason string, causation, correlation ids.ID, added *Evidence, in EvidenceInput) (Result, error) {
	rows, err := s.EvidenceFor(ctx, q, b.MindID, b.ID)
	if err != nil {
		return Result{}, err
	}
	ev := make([]Evidence, 0, len(rows))
	for _, r := range rows {
		ev = append(ev, r.Evidence)
	}
	bd := Compute(b.Prior, ev)
	st := Derive(bd, b.Epistemic)
	if b.PinnedStatus != nil {
		st = *b.PinnedStatus
	}
	old := b
	b.Confidence, b.Status, b.LastEvaluated, b.Evaluations = bd.Confidence, st, time.Now(), b.Evaluations+1
	changed := old.Confidence != b.Confidence || old.Status != b.Status
	res := Result{Belief: b, Breakdown: bd}
	typ := events.BeliefUpdated
	summary := fmt.Sprintf("belief re-evaluated (%.2f → %.2f, %s): %s", old.Confidence, b.Confidence, b.Status, b.Proposition)
	if added != nil {
		if added.Direction == "for" {
			typ = events.HypothesisSupported
			if b.Epistemic != concepts.Hypothesis {
				typ = events.BeliefUpdated
			}
			summary = fmt.Sprintf("support added (%.2f → %.2f): %s", old.Confidence, b.Confidence, b.Proposition)
		} else {
			typ = events.HypothesisWeakened
			if b.Epistemic != concepts.Hypothesis {
				typ = events.BeliefChallenged
			}
			summary = fmt.Sprintf("counter-evidence added (%.2f → %.2f): %s", old.Confidence, b.Confidence, b.Proposition)
		}
	}
	refs := []events.EvidenceRef{{Kind: "belief", ID: string(b.ID)}}
	if added != nil && added.RefID != "" {
		refs = append(refs, events.EvidenceRef{Kind: added.Kind, ID: added.RefID, Note: added.Direction + ": " + in.Note})
	}
	ev1, err := emit(ctx, q, events.New{MindID: b.MindID, Type: typ, Source: firstNonEmpty(in.Source, "beliefs"), Summary: summary, Salience: 0.3 + 0.4*absf(b.Confidence-old.Confidence)*4, Confidence: b.Confidence,
		CausationID: causation, CorrelationID: correlation, Evidence: refs,
		Metadata: map[string]any{"belief_id": string(b.ID), "status": b.Status, "from": old.Confidence, "to": b.Confidence},
		Payload:  map[string]any{"belief_id": b.ID, "proposition": b.Proposition, "from": map[string]any{"confidence": old.Confidence, "status": old.Status}, "to": map[string]any{"confidence": b.Confidence, "status": b.Status}, "breakdown": bd, "reason": reason}})
	if err != nil {
		return Result{}, err
	}
	res.Events = append(res.Events, ev1)
	if changed {
		b.Version++
		res.Belief = b
		if err := s.insertVersion(ctx, q, b, bd, reason, ev1.ID); err != nil {
			return Result{}, err
		}
	}
	if _, err := q.Exec(ctx, `UPDATE beliefs SET confidence=$3, status=$4, version=$5, last_evaluated=now(), evaluations=evaluations+1 WHERE id=$1 AND mind_id=$2`, b.ID, b.MindID, b.Confidence, string(b.Status), b.Version); err != nil {
		return Result{}, err
	}
	if changed && added != nil && added.Direction == "for" {
		contra, err := s.detectContradictions(ctx, q, emit, b, ev1.ID)
		if err != nil {
			return Result{}, err
		}
		res.Events = append(res.Events, contra...)
	}
	return res, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// detectContradictions compares b against the mind's active beliefs. A
// contradiction links both, emits an event and adds mutual counter-evidence
// so both confidences reflect the conflict until it is resolved.
func (s *Store) detectContradictions(ctx context.Context, q storage.Querier, emit Emitter, b Belief, causation ids.ID) ([]events.Event, error) {
	rows, err := q.Query(ctx, `SELECT `+cols+` FROM beliefs b WHERE b.mind_id=$1 AND b.id<>$2 AND b.status NOT IN ('rejected','superseded') AND ($3 = '' OR b.subject = $3 OR b.subject = '') LIMIT 500`, b.MindID, b.ID, b.Subject)
	if err != nil {
		return nil, err
	}
	var others []Belief
	for rows.Next() {
		o, err := scan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		others = append(others, o)
	}
	rows.Close()
	var out []events.Event
	for _, o := range others {
		ok, sim := Contradicts(b.Proposition, o.Proposition)
		if !ok {
			continue
		}
		var exists bool
		q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM belief_links WHERE relation='contradicts' AND ((from_id=$1 AND to_id=$2) OR (from_id=$2 AND to_id=$1)))`, b.ID, o.ID).Scan(&exists) //nolint:errcheck
		if exists {
			continue
		}
		if StateChange(o, b) {
			// A later observation of a transient state: the older belief was
			// true of its time and is closed, not contested and not weakened.
			ev, err := s.closeState(ctx, q, emit, o, b, sim, causation)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
			continue
		}
		ev, err := emit(ctx, q, events.New{MindID: b.MindID, Type: events.ContradictionDetected, Source: "beliefs.contradiction", Summary: fmt.Sprintf("contradiction: %q vs %q", trunc(b.Proposition, 60), trunc(o.Proposition, 60)),
			Salience: 0.8, Confidence: sim, CausationID: causation,
			Evidence: []events.EvidenceRef{{Kind: "belief", ID: string(b.ID)}, {Kind: "belief", ID: string(o.ID)}},
			Metadata: map[string]any{"belief_id": string(b.ID), "other_id": string(o.ID), "similarity": sim},
			Payload:  map[string]any{"a": map[string]any{"id": b.ID, "proposition": b.Proposition, "confidence": b.Confidence}, "b": map[string]any{"id": o.ID, "proposition": o.Proposition, "confidence": o.Confidence}, "similarity": sim, "method": "negation+jaccard"}})
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
		if _, err := q.Exec(ctx, `INSERT INTO belief_links (id, mind_id, from_id, to_id, relation, confidence, event_id) VALUES ($1,$2,$3,$4,'contradicts',$5,$6) ON CONFLICT DO NOTHING`, ids.New(), b.MindID, b.ID, o.ID, sim, ev.ID); err != nil {
			return nil, err
		}
		// Each belief becomes counter-evidence for the other, weighted by the other's confidence.
		if err := s.insertEvidence(ctx, q, b, Evidence{Direction: "against", Kind: "belief", RefID: string(o.ID), Source: "contradiction", Weight: 0.5, Credibility: o.Confidence, Derivation: DerivationInference, Family: "belief:" + string(o.ID)}, "contradicts: "+trunc(o.Proposition, 120), ev.ID); err != nil {
			return nil, err
		}
		if err := s.insertEvidence(ctx, q, o, Evidence{Direction: "against", Kind: "belief", RefID: string(b.ID), Source: "contradiction", Weight: 0.5, Credibility: b.Confidence, Derivation: DerivationInference, Family: "belief:" + string(b.ID)}, "contradicts: "+trunc(b.Proposition, 120), ev.ID); err != nil {
			return nil, err
		}
		for _, x := range []Belief{b, o} {
			rows, err := s.EvidenceFor(ctx, q, x.MindID, x.ID)
			if err != nil {
				return nil, err
			}
			var evs []Evidence
			for _, r := range rows {
				evs = append(evs, r.Evidence)
			}
			bd := Compute(x.Prior, evs)
			st := Derive(bd, x.Epistemic)
			if x.PinnedStatus != nil {
				st = *x.PinnedStatus
			}
			if bd.Confidence != x.Confidence || st != x.Status {
				x.Confidence, x.Status, x.Version = bd.Confidence, st, x.Version+1
				if err := s.insertVersion(ctx, q, x, bd, "contradiction detected", ev.ID); err != nil {
					return nil, err
				}
				if _, err := q.Exec(ctx, `UPDATE beliefs SET confidence=$3, status=$4, version=$5, last_evaluated=now() WHERE id=$1 AND mind_id=$2`, x.ID, x.MindID, x.Confidence, string(x.Status), x.Version); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

// Retract is the operator saying "this belief is wrong". Status is pinned to
// rejected, a correction is recorded, and optionally a do-not-infer rule.
func (s *Store) Retract(ctx context.Context, emit Emitter, mind, id ids.ID, reason, by string, doNotInfer bool) (Result, error) {
	var res Result
	err := storage.Tx(ctx, s.db, func(tx pgx.Tx) error {
		b, err := s.Get(ctx, tx, mind, id)
		if err != nil {
			return err
		}
		// An operator saying "wrong" is an outcome for the sources that backed
		// it, in this belief's domain only: nobody's standing elsewhere moves.
		if err := s.RecordBeliefOutcome(ctx, tx, mind, id, false); err != nil {
			return err
		}
		ev, err := emit(ctx, tx, events.New{MindID: mind, Type: events.BeliefRetracted, Source: "operator", Summary: fmt.Sprintf("operator %s rejected belief: %s (%s)", by, b.Proposition, reason),
			Salience: 0.7, Confidence: 1, Evidence: []events.EvidenceRef{{Kind: "belief", ID: string(b.ID)}},
			Metadata: map[string]any{"belief_id": string(b.ID), "actor": by, "do_not_infer": doNotInfer}, Payload: map[string]any{"belief_id": b.ID, "proposition": b.Proposition, "reason": reason, "previous": map[string]any{"confidence": b.Confidence, "status": b.Status}}})
		if err != nil {
			return err
		}
		if err := s.insertEvidence(ctx, tx, b, Evidence{Direction: "against", Kind: "operator", RefID: string(ev.ID), Source: "operator", Weight: 1, Credibility: 0.95}, reason, ev.ID); err != nil {
			return err
		}
		rej := StatusRejected
		b.PinnedStatus, b.Status, b.Version = &rej, rej, b.Version+1
		rows, _ := s.EvidenceFor(ctx, tx, mind, id)
		var evs []Evidence
		for _, r := range rows {
			evs = append(evs, r.Evidence)
		}
		bd := Compute(b.Prior, evs)
		b.Confidence = bd.Confidence
		if err := s.insertVersion(ctx, tx, b, bd, "operator correction: "+reason, ev.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE beliefs SET status='rejected', pinned_status='rejected', confidence=$3, version=$4, last_evaluated=now() WHERE id=$1 AND mind_id=$2`, id, mind, b.Confidence, b.Version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO corrections (id, mind_id, kind, target_kind, target_id, statement, reason, created_by, event_id) VALUES ($1,$2,'belief_wrong','belief',$3,$4,$5,$6,$7)`, ids.New(), mind, id, b.Proposition, reason, by, ev.ID); err != nil {
			return err
		}
		if doNotInfer {
			if _, err := tx.Exec(ctx, `INSERT INTO corrections (id, mind_id, kind, target_kind, target_id, statement, reason, created_by, event_id) VALUES ($1,$2,'do_not_infer','belief',$3,$4,$5,$6,$7)`, ids.New(), mind, id, b.Proposition, reason, by, ev.ID); err != nil {
				return err
			}
		}
		res = Result{Belief: b, Breakdown: bd, Events: []events.Event{ev}}
		return nil
	})
	return res, err
}

// AddCorrection records a free-standing correction such as "do not infer X".
func (s *Store) AddCorrection(ctx context.Context, q storage.Querier, mind ids.ID, c Correction) (Correction, error) {
	c.ID, c.CreatedAt = ids.New(), time.Now()
	_, err := q.Exec(ctx, `INSERT INTO corrections (id, mind_id, kind, target_kind, target_id, statement, reason, created_by, event_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		c.ID, mind, c.Kind, c.TargetKind, c.TargetID, c.Statement, c.Reason, c.CreatedBy, nullID(c.EventID))
	return c, err
}

// Corrections lists corrections for a mind, newest first.
func (s *Store) Corrections(ctx context.Context, mind ids.ID, kind string) ([]Correction, error) {
	rows, err := s.db.Query(ctx, `SELECT id, kind, target_kind, target_id, statement, reason, created_by, event_id, created_at FROM corrections WHERE mind_id=$1 AND ($2='' OR kind=$2) ORDER BY created_at DESC LIMIT 500`, mind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Correction
	for rows.Next() {
		var c Correction
		var ev *string
		if err := rows.Scan(&c.ID, &c.Kind, &c.TargetKind, &c.TargetID, &c.Statement, &c.Reason, &c.CreatedBy, &ev, &c.CreatedAt); err != nil {
			return nil, err
		}
		if ev != nil {
			c.EventID = ids.ID(*ev)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// blocked checks do-not-infer corrections against a proposition.
func (s *Store) blocked(ctx context.Context, q storage.Querier, mind ids.ID, proposition string) (bool, Correction) {
	rows, err := q.Query(ctx, `SELECT id, statement, reason FROM corrections WHERE mind_id=$1 AND kind='do_not_infer'`, mind)
	if err != nil {
		return false, Correction{}
	}
	defer rows.Close()
	pt, _ := Polarity(proposition)
	for rows.Next() {
		var c Correction
		if err := rows.Scan(&c.ID, &c.Statement, &c.Reason); err != nil {
			return false, Correction{}
		}
		ct, _ := Polarity(c.Statement)
		if Jaccard(pt, ct) >= 0.7 {
			return true, c
		}
	}
	return false, Correction{}
}

// Versions returns the append-only history, newest first.
func (s *Store) Versions(ctx context.Context, mind, id ids.ID) ([]Version, error) {
	rows, err := s.db.Query(ctx, `SELECT version, status, confidence, proposition, reason, breakdown, event_id, created_at FROM belief_versions WHERE belief_id=$1 AND mind_id=$2 ORDER BY version DESC`, id, mind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var st string
		var bd []byte
		var ev *string
		if err := rows.Scan(&v.Version, &st, &v.Confidence, &v.Proposition, &v.Reason, &bd, &ev, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Status = Status(st)
		json.Unmarshal(bd, &v.Breakdown) //nolint:errcheck
		if ev != nil {
			v.EventID = ids.ID(*ev)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Alternatives lists competing explanations.
func (s *Store) Alternatives(ctx context.Context, mind, id ids.ID) ([]Alternative, error) {
	rows, err := s.db.Query(ctx, `SELECT id, proposition, confidence, note, status, created_at FROM belief_alternatives WHERE belief_id=$1 AND mind_id=$2 ORDER BY created_at`, id, mind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alternative
	for rows.Next() {
		var a Alternative
		if err := rows.Scan(&a.ID, &a.Proposition, &a.Confidence, &a.Note, &a.Status, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddAlternative records another candidate explanation.
func (s *Store) AddAlternative(ctx context.Context, mind, id ids.ID, proposition, note string) (Alternative, error) {
	proposition = strings.TrimSpace(proposition)
	if proposition == "" {
		return Alternative{}, fmt.Errorf("%w: proposition required", ErrInvalid)
	}
	a := Alternative{ID: ids.New(), Proposition: proposition, Confidence: 0.2, Note: note, Status: "open", CreatedAt: time.Now()}
	tag, err := s.db.Exec(ctx, `INSERT INTO belief_alternatives (id, belief_id, mind_id, proposition, confidence, note) SELECT $1,$2,$3,$4,$5,$6 WHERE EXISTS (SELECT 1 FROM beliefs WHERE id=$2 AND mind_id=$3)`, a.ID, id, mind, a.Proposition, a.Confidence, a.Note)
	if err != nil {
		return Alternative{}, err
	}
	if tag.RowsAffected() == 0 {
		return Alternative{}, storage.ErrNotFound
	}
	return a, nil
}

// Links returns contradiction and support links with the other belief loaded.
func (s *Store) Links(ctx context.Context, mind, id ids.ID) ([]Link, error) {
	rows, err := s.db.Query(ctx, `SELECT l.id, l.from_id, l.to_id, l.relation, l.confidence, l.created_at, `+cols+` FROM belief_links l
		JOIN beliefs b ON b.id = CASE WHEN l.from_id=$1 THEN l.to_id ELSE l.from_id END WHERE l.mind_id=$2 AND (l.from_id=$1 OR l.to_id=$1) ORDER BY l.created_at`, id, mind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var other Belief
		var epi, status string
		var detail []byte
		var origin, superseded, pinned *string
		if err := rows.Scan(&l.ID, &l.FromID, &l.ToID, &l.Relation, &l.Confidence, &l.CreatedAt,
			&other.ID, &other.MindID, &other.Proposition, &other.Subject, &epi, &status, &other.Confidence, &other.Prior, &other.Version, &other.Source, &detail, &origin, &superseded, &pinned, &other.FirstCreated, &other.LastEvaluated, &other.Evaluations, &other.TemporalScope, &other.ObservedAt, &other.ValidFrom, &other.ValidUntil, &other.RedactedAt); err != nil {
			return nil, err
		}
		other.Epistemic, other.Status = concepts.Epistemic(epi), Status(status)
		l.Other = &other
		out = append(out, l)
	}
	return out, rows.Err()
}

// RetractEvidence marks a piece of evidence as not counting (memory retracted etc.).
func (s *Store) RetractEvidence(ctx context.Context, q storage.Querier, mind ids.ID, kind, refID string) ([]ids.ID, error) {
	rows, err := q.Query(ctx, `UPDATE belief_evidence SET retracted=true WHERE mind_id=$1 AND kind=$2 AND ref_id=$3 AND NOT retracted RETURNING belief_id`, mind, kind, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.ID
	for rows.Next() {
		var id ids.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Stats summarises beliefs for the overview.
type Stats struct {
	Total    int64            `json:"total"`
	ByStatus map[string]int64 `json:"by_status"`
}

// Stats counts beliefs by status.
func (s *Store) Stats(ctx context.Context, mind ids.ID) (Stats, error) {
	st := Stats{ByStatus: map[string]int64{}}
	rows, err := s.db.Query(ctx, `SELECT status, count(*) FROM beliefs WHERE mind_id=$1 GROUP BY status`, mind)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return st, err
		}
		st.ByStatus[k] = n
		st.Total += n
	}
	return st, rows.Err()
}

// Contested returns beliefs currently in conflict, for attention and reflection.
func (s *Store) Contested(ctx context.Context, mind ids.ID, limit int) ([]Belief, error) {
	return s.List(ctx, mind, Filter{Statuses: []Status{StatusContested}, Limit: limit})
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// Search returns live beliefs matching any of the words in text, best match
// first. Used to answer "what do you believe about X".
func (s *Store) Search(ctx context.Context, mind ids.ID, text string, limit int) ([]Belief, error) {
	toks, _ := Polarity(text)
	if len(toks) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	q := strings.Join(toks, " | ")
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM beliefs b WHERE b.mind_id=$1 AND b.status NOT IN ('superseded') AND b.search @@ to_tsquery('english', $2)
		ORDER BY ts_rank_cd(b.search, to_tsquery('english', $2)) DESC, b.confidence DESC LIMIT $3`, mind, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Belief
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Change describes a belief whose status or confidence moved.
type Change struct {
	Belief     Belief    `json:"belief"`
	FromStatus Status    `json:"from_status"`
	FromConf   float64   `json:"from_confidence"`
	Reason     string    `json:"reason"`
	At         time.Time `json:"at"`
}

// RecentChanges lists beliefs whose latest version differs in status from an
// earlier version within the window ("what have you changed your mind about").
func (s *Store) RecentChanges(ctx context.Context, mind ids.ID, since time.Time, limit int) ([]Change, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.Query(ctx, `WITH latest AS (
			SELECT DISTINCT ON (belief_id) belief_id, status, confidence, reason, created_at FROM belief_versions WHERE mind_id=$1 AND created_at >= $2 ORDER BY belief_id, version DESC),
		first AS (
			SELECT DISTINCT ON (belief_id) belief_id, status, confidence FROM belief_versions WHERE mind_id=$1 ORDER BY belief_id, version ASC)
		SELECT `+cols+`, f.status, f.confidence, l.reason, l.created_at FROM latest l JOIN first f ON f.belief_id=l.belief_id JOIN beliefs b ON b.id=l.belief_id
		WHERE l.status <> f.status ORDER BY l.created_at DESC LIMIT $3`, mind, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var epi, status, fromStatus string
		var detail []byte
		var origin, superseded, pinned *string
		b := &c.Belief
		if err := rows.Scan(&b.ID, &b.MindID, &b.Proposition, &b.Subject, &epi, &status, &b.Confidence, &b.Prior, &b.Version, &b.Source, &detail, &origin, &superseded, &pinned, &b.FirstCreated, &b.LastEvaluated, &b.Evaluations, &b.TemporalScope, &b.ObservedAt, &b.ValidFrom, &b.ValidUntil, &b.RedactedAt, &fromStatus, &c.FromConf, &c.Reason, &c.At); err != nil {
			return nil, err
		}
		b.Epistemic, b.Status, c.FromStatus = concepts.Epistemic(epi), Status(status), Status(fromStatus)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Uncertain returns beliefs in the middle band or contested, least certain first.
func (s *Store) Uncertain(ctx context.Context, mind ids.ID, limit int) ([]Belief, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM beliefs b WHERE b.mind_id=$1 AND b.status NOT IN ('rejected','superseded') AND (b.status='contested' OR (b.confidence BETWEEN 0.3 AND 0.7))
		ORDER BY abs(b.confidence-0.5) ASC, b.last_evaluated DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Belief
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// closeState ends the validity of an older transient belief when a newer
// observation shows the state changed. Its score is untouched; its status
// becomes superseded with valid_until set, and a version and an event
// record why. "The door was open at 10:00" remains true.
func (s *Store) closeState(ctx context.Context, q storage.Querier, emit Emitter, older, newer Belief, sim float64, causation ids.ID) (events.Event, error) {
	until := *newer.ObservedAt
	ev, err := emit(ctx, q, events.New{MindID: older.MindID, Type: events.BeliefUpdated, Source: "beliefs.temporal", Summary: fmt.Sprintf("state changed: %q held until %s; now %q", trunc(older.Proposition, 60), until.Format("15:04:05"), trunc(newer.Proposition, 60)),
		Salience: 0.5, Confidence: sim, CausationID: causation,
		Evidence: []events.EvidenceRef{{Kind: "belief", ID: string(older.ID), Note: "closed"}, {Kind: "belief", ID: string(newer.ID), Note: "current"}},
		Metadata: map[string]any{"belief_id": string(older.ID), "superseded_by": string(newer.ID), "valid_until": until, "reason": "state changed"},
		Payload:  map[string]any{"older": map[string]any{"id": older.ID, "proposition": older.Proposition, "valid_from": older.ValidFrom, "valid_until": until}, "newer": map[string]any{"id": newer.ID, "proposition": newer.Proposition, "observed_at": newer.ObservedAt}, "method": "temporal supersession"}})
	if err != nil {
		return events.Event{}, err
	}
	if _, err := q.Exec(ctx, `UPDATE beliefs SET status='superseded', superseded_by=$3, valid_until=$4, version=version+1, last_evaluated=now(), evaluations=evaluations+1 WHERE id=$1 AND mind_id=$2`, older.ID, older.MindID, newer.ID, until); err != nil {
		return events.Event{}, err
	}
	older.Version++
	older.Status = StatusSuperseded
	if err := s.insertVersion(ctx, q, older, Breakdown{Prior: older.Prior, Confidence: older.Confidence, Meaning: ScoreMeaning, Notes: []string{"state changed; score unchanged, validity closed"}}, "state changed: superseded by a later observation", ev.ID); err != nil {
		return events.Event{}, err
	}
	if _, err := q.Exec(ctx, `INSERT INTO belief_links (id, mind_id, from_id, to_id, relation, confidence, event_id) VALUES ($1,$2,$3,$4,'supersedes',$5,$6) ON CONFLICT DO NOTHING`, ids.New(), older.MindID, newer.ID, older.ID, sim, ev.ID); err != nil {
		return events.Event{}, err
	}
	return ev, nil
}

// History lists superseded beliefs about a topic with their validity, so a
// mind can say what was true and when.
func (s *Store) History(ctx context.Context, mind ids.ID, text string, limit int) ([]Belief, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	toks, _ := Polarity(text)
	if len(toks) == 0 {
		return nil, nil
	}
	q := strings.Join(toks, " | ")
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM beliefs b WHERE b.mind_id=$1 AND b.status='superseded' AND b.valid_until IS NOT NULL AND b.search @@ to_tsquery('english', $2) ORDER BY b.valid_until DESC LIMIT $3`, mind, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Belief
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

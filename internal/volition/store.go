package volition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Store persists volitional state. Every change goes through a Recorder so
// its event and, where a choice was made, its decision row are written in
// the same transaction.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// DB exposes the pool for callers that record outside a transaction.
func (s *Store) DB() *pgxpool.Pool { return s.db }

// ErrInvalid marks bad input; ErrClosed marks a transition from a closed state.
var (
	ErrInvalid = errors.New("volition: invalid")
	ErrClosed  = errors.New("volition: intention is closed")
)

// DirectEmitter appends and publishes immediately. Use it only with the pool,
// never inside a transaction (subscribers must not see uncommitted events).
func DirectEmitter(es *evstore.Store) Emitter {
	return func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		evs, err := es.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		es.Publish(evs...)
		return evs[0], nil
	}
}

// Direct returns a recorder on the pool that publishes as it goes.
func (s *Store) Direct(es *evstore.Store, source string) Recorder {
	return Recorder{Q: s.db, Emit: DirectEmitter(es), Source: source}
}

func (r Recorder) emit(ctx context.Context, mind ids.ID, typ events.Type, summary string, salience float64, evidence []events.EvidenceRef, meta map[string]any, payload any) (ids.ID, error) {
	if r.Emit == nil {
		return ids.Nil, nil
	}
	ev, err := r.Emit(ctx, r.Q, events.New{MindID: mind, Type: typ, Source: firstNonEmpty(r.Source, "volition"), Summary: summary, Salience: salience, Confidence: 1,
		CausationID: r.Cause, CorrelationID: r.Correlation, Evidence: evidence, Metadata: meta, Payload: payload, OccurredAt: r.Now})
	if err != nil {
		return ids.Nil, err
	}
	return ev.ID, nil
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	v := string(id)
	return &v
}

func idStrings(in []ids.ID) []string {
	out := make([]string, 0, len(in))
	for _, id := range in {
		if !id.IsZero() {
			out = append(out, string(id))
		}
	}
	return out
}

func toIDs(in []string) []ids.ID {
	out := make([]ids.ID, len(in))
	for i, s := range in {
		out[i] = ids.ID(s)
	}
	return out
}

func setID(dst *ids.ID, src *string) {
	if src != nil {
		*dst = ids.ID(*src)
	}
}

func jsonOr(v any, empty string) []byte {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(empty)
	}
	return b
}

// ---- wants ----

const wantCols = `id, mind_id, subject, description, origin_type, origin_event, origin_ref, strength, valence, urgency, status, drives, value_keys, goal_id, commitment_id, proposal, terms, reinforcements, suppressions, last_reinforced_at, resolution, resolved_at, created_at, updated_at`

func scanWant(row pgx.Row) (Want, error) {
	var w Want
	var origin, status string
	var originEv, goal, commitment *string
	var proposal, terms []byte
	if err := row.Scan(&w.ID, &w.MindID, &w.Subject, &w.Description, &origin, &originEv, &w.OriginRef, &w.Strength, &w.Valence, &w.Urgency, &status, &w.Drives, &w.Values, &goal, &commitment, &proposal, &terms,
		&w.Reinforcements, &w.Suppressions, &w.LastReinforcedAt, &w.Resolution, &w.ResolvedAt, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return w, storage.Translate(err)
	}
	w.Origin, w.Status = Origin(origin), WantStatus(status)
	setID(&w.OriginEvent, originEv)
	setID(&w.GoalID, goal)
	setID(&w.CommitmentID, commitment)
	if len(proposal) > 0 && string(proposal) != "null" {
		w.Proposal = &Proposal{}
		json.Unmarshal(proposal, w.Proposal) //nolint:errcheck
	}
	json.Unmarshal(terms, &w.Terms) //nolint:errcheck
	return w, nil
}

// UpsertWant creates a want from a candidate or, when an active want with
// the same subject exists, reinforces it: identical wants refresh rather
// than accumulate. It returns the want and whether it was new.
func (s *Store) UpsertWant(ctx context.Context, r Recorder, mind ids.ID, c Candidate) (Want, bool, error) {
	now := r.now()
	c.Subject, c.Description = strings.TrimSpace(c.Subject), strings.TrimSpace(c.Description)
	if mind.IsZero() || c.Subject == "" || c.Description == "" || len(c.Subject) > 300 {
		return Want{}, false, fmt.Errorf("%w: want needs a subject (≤300 chars) and a description", ErrInvalid)
	}
	c.Strength, c.Urgency = clamp01(c.Strength), clamp01(c.Urgency)
	if c.Valence != "avoid" {
		c.Valence = "approach"
	}
	existing, err := scanWant(r.Q.QueryRow(ctx, `SELECT `+wantCols+` FROM wants WHERE mind_id=$1 AND subject=$2 AND status='active'`, mind, c.Subject))
	switch {
	case err == nil:
		eff := existing.Effective(now)
		next := round3(clamp01(maxf(eff, c.Strength) + 0.1*(1-maxf(eff, c.Strength))))
		urgency := maxf(existing.Urgency, c.Urgency)
		if _, err := r.Q.Exec(ctx, `UPDATE wants SET strength=$2, urgency=$3, reinforcements=reinforcements+1, last_reinforced_at=$4, updated_at=$4 WHERE id=$1`, existing.ID, next, urgency, now); err != nil {
			return Want{}, false, err
		}
		if next-eff >= 0.05 {
			if _, err := r.emit(ctx, mind, events.WantReinforced, fmt.Sprintf("want reinforced (%.2f → %.2f): %s", eff, next, existing.Description), 0.2,
				[]events.EvidenceRef{{Kind: "want", ID: string(existing.ID)}}, map[string]any{"want_id": string(existing.ID), "from": eff, "to": next, "origin_event": string(c.OriginEvent)}, map[string]any{"candidate_terms": c.Terms}); err != nil {
				return Want{}, false, err
			}
		}
		existing.Strength, existing.Urgency, existing.Reinforcements, existing.LastReinforcedAt = next, urgency, existing.Reinforcements+1, now
		return existing, false, nil
	case !errors.Is(err, storage.ErrNotFound):
		return Want{}, false, err
	}
	w := Want{ID: ids.New(), MindID: mind, Subject: c.Subject, Description: trunc(c.Description, 500), Origin: c.Origin, OriginEvent: c.OriginEvent, OriginRef: c.OriginRef, Strength: c.Strength, Valence: c.Valence, Urgency: c.Urgency,
		Status: WantActive, Drives: nonNil(c.Drives), Values: nonNil(c.Values), GoalID: c.GoalID, CommitmentID: c.CommitmentID, Proposal: c.Proposal, Terms: c.Terms, LastReinforcedAt: now, CreatedAt: now, UpdatedAt: now}
	if w.Terms == nil {
		w.Terms = map[string]float64{}
	}
	var proposal []byte
	if w.Proposal != nil {
		proposal, _ = json.Marshal(w.Proposal)
	}
	if _, err := r.Q.Exec(ctx, `INSERT INTO wants (id, mind_id, subject, description, origin_type, origin_event, origin_ref, strength, valence, urgency, status, drives, value_keys, goal_id, commitment_id, proposal, terms, last_reinforced_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'active',$11,$12,$13,$14,$15,$16,$17,$17,$17)`,
		w.ID, mind, w.Subject, w.Description, string(w.Origin), nullID(w.OriginEvent), w.OriginRef, w.Strength, w.Valence, w.Urgency, w.Drives, w.Values, nullID(w.GoalID), nullID(w.CommitmentID), proposal, jsonOr(w.Terms, "{}"), now); err != nil {
		return Want{}, false, storage.Translate(err)
	}
	evidence := []events.EvidenceRef{{Kind: "want", ID: string(w.ID)}}
	if !w.OriginEvent.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "event", ID: string(w.OriginEvent), Note: "origin"})
	}
	if !w.GoalID.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "goal", ID: string(w.GoalID)})
	}
	if _, err := r.emit(ctx, mind, events.WantCreated, fmt.Sprintf("wants (%.2f, %s): %s", w.Strength, w.Origin, w.Description), 0.25+0.3*w.Strength, evidence,
		map[string]any{"want_id": string(w.ID), "origin": string(w.Origin), "subject": w.Subject, "valence": w.Valence, "strength": w.Strength}, w); err != nil {
		return Want{}, false, err
	}
	return w, true, nil
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// Wants lists a mind's wants by status (empty = all), newest first.
func (s *Store) Wants(ctx context.Context, q storage.Querier, mind ids.ID, statuses []WantStatus, limit int) ([]Want, error) {
	if q == nil {
		q = s.db
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	st := make([]string, len(statuses))
	for i, x := range statuses {
		st[i] = string(x)
	}
	rows, err := q.Query(ctx, `SELECT `+wantCols+` FROM wants WHERE mind_id=$1 AND (cardinality($2::text[])=0 OR status=ANY($2)) ORDER BY created_at DESC LIMIT $3`, mind, st, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Want
	for rows.Next() {
		w, err := scanWant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWant fetches one want of a mind.
func (s *Store) GetWant(ctx context.Context, q storage.Querier, mind, id ids.ID) (Want, error) {
	if q == nil {
		q = s.db
	}
	return scanWant(q.QueryRow(ctx, `SELECT `+wantCols+` FROM wants WHERE id=$1 AND mind_id=$2`, id, mind))
}

// ResolveWant closes an active want (satisfied, abandoned, expired, superseded).
func (s *Store) ResolveWant(ctx context.Context, r Recorder, mind, id ids.ID, status WantStatus, resolution string) error {
	if status == WantActive {
		return fmt.Errorf("%w: resolving to active", ErrInvalid)
	}
	now := r.now()
	tag, err := r.Q.Exec(ctx, `UPDATE wants SET status=$3, resolution=$4, resolved_at=$5, updated_at=$5 WHERE id=$1 AND mind_id=$2 AND status='active'`, id, mind, string(status), trunc(resolution, 500), now)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return nil
}

// SuppressWant records that an active want was not pursued, and why. The
// want stays active and represented; its suppression count rises.
func (s *Store) SuppressWant(ctx context.Context, r Recorder, w Want, reason string, commitments []ids.ID) error {
	if _, err := r.Q.Exec(ctx, `UPDATE wants SET suppressions=suppressions+1, updated_at=$2 WHERE id=$1`, w.ID, r.now()); err != nil {
		return err
	}
	evidence := []events.EvidenceRef{{Kind: "want", ID: string(w.ID)}}
	for _, c := range commitments {
		evidence = append(evidence, events.EvidenceRef{Kind: "commitment", ID: string(c)})
	}
	_, err := r.emit(ctx, w.MindID, events.WantSuppressed, "want not pursued: "+w.Description+" — "+reason, 0.35, evidence,
		map[string]any{"want_id": string(w.ID), "suppressions": w.Suppressions + 1, "commitments": idStrings(commitments)}, map[string]any{"reason": reason})
	return err
}

// ExpireWants closes active wants whose strength decayed below the floor
// and, beyond the cap, the weakest ones. It returns how many were closed.
func (s *Store) ExpireWants(ctx context.Context, r Recorder, mind ids.ID, maxActive int) (int, error) {
	if maxActive <= 0 {
		maxActive = 40
	}
	now := r.now()
	ws, err := s.Wants(ctx, r.Q, mind, []WantStatus{WantActive}, 1000)
	if err != nil {
		return 0, err
	}
	SortWants(ws, now)
	n := 0
	for i, w := range ws {
		why := ""
		switch {
		case w.Effective(now) < 0.1:
			why = "faded: not reinforced for " + now.Sub(w.LastReinforcedAt).Truncate(time.Minute).String()
		case i >= maxActive:
			why = "displaced by stronger wants (more than " + fmt.Sprint(maxActive) + " active)"
		}
		if why == "" {
			continue
		}
		if err := s.ResolveWant(ctx, r, mind, w.ID, WantExpired, why); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ---- conflicts ----

const conflictCols = `id, mind_id, want_a, want_b, kind, status, preferred, resolution, rationale, detected_event, resolved_at, created_at`

func scanConflict(row pgx.Row) (Conflict, error) {
	var c Conflict
	var pref, det *string
	var rat []byte
	if err := row.Scan(&c.ID, &c.MindID, &c.WantA, &c.WantB, &c.Kind, &c.Status, &pref, &c.Resolution, &rat, &det, &c.ResolvedAt, &c.CreatedAt); err != nil {
		return c, storage.Translate(err)
	}
	setID(&c.Preferred, pref)
	setID(&c.DetectedEvent, det)
	json.Unmarshal(rat, &c.Rationale) //nolint:errcheck
	return c, nil
}

// RecordConflict stores a resolved conflict between two wants once per open
// pair, with the decision and a want_conflict_detected event. Neither want is
// discarded. It returns the conflict and whether it was new.
func (s *Store) RecordConflict(ctx context.Context, r Recorder, a, b Want, kind string, res Resolution) (Conflict, bool, error) {
	existing, err := scanConflict(r.Q.QueryRow(ctx, `SELECT `+conflictCols+` FROM want_conflicts WHERE mind_id=$1 AND status='open' AND least(want_a,want_b)=least($2::uuid,$3::uuid) AND greatest(want_a,want_b)=greatest($2::uuid,$3::uuid)`, a.MindID, a.ID, b.ID))
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return Conflict{}, false, err
	}
	now := r.now()
	c := Conflict{ID: ids.New(), MindID: a.MindID, WantA: a.ID, WantB: b.ID, Kind: kind, Status: "open", Preferred: res.Preferred, Resolution: res.Outcome, Rationale: res.Rationale, CreatedAt: now}
	preferred, other := a, b
	if res.Preferred == b.ID {
		preferred, other = b, a
	}
	summary := fmt.Sprintf("wants conflict (%s): %q vs %q — %s %q", kind, a.Description, b.Description, map[string]string{"preferred": "preferred", "preferred_with_constraint": "preferred, with constraint,", "deferred": "deferred in favour of"}[res.Outcome], preferred.Description)
	evID, err := r.emit(ctx, a.MindID, events.WantConflictDetected, summary, 0.5, []events.EvidenceRef{{Kind: "want", ID: string(a.ID)}, {Kind: "want", ID: string(b.ID)}},
		map[string]any{"conflict_id": string(c.ID), "want_a": string(a.ID), "want_b": string(b.ID), "preferred": string(res.Preferred), "outcome": res.Outcome, "kind": kind}, c)
	if err != nil {
		return Conflict{}, false, err
	}
	c.DetectedEvent = evID
	if _, err := r.Q.Exec(ctx, `INSERT INTO want_conflicts (id, mind_id, want_a, want_b, kind, status, preferred, resolution, rationale, detected_event, created_at) VALUES ($1,$2,$3,$4,$5,'open',$6,$7,$8,$9,$10)`,
		c.ID, c.MindID, a.ID, b.ID, kind, nullID(c.Preferred), c.Resolution, jsonOr(c.Rationale, "{}"), nullID(evID), now); err != nil {
		return Conflict{}, false, storage.Translate(err)
	}
	rule, _ := res.Rationale["rule"].(string)
	if _, err := s.RecordDecision(ctx, r.Q, Decision{MindID: a.MindID, Kind: "conflict_resolution", Outcome: res.Outcome, Reason: kind, Rationale: rule, WantID: preferred.ID, ConflictID: c.ID, EventID: evID,
		Inputs: map[string]any{"preferred": preferred.ID, "other": other.ID, "rationale": res.Rationale}, CreatedAt: now}); err != nil {
		return Conflict{}, false, err
	}
	return c, true, nil
}

// SettleConflicts marks open conflicts involving a closed want as dissolved.
func (s *Store) SettleConflicts(ctx context.Context, q storage.Querier, mind ids.ID, now time.Time) error {
	_, err := q.Exec(ctx, `UPDATE want_conflicts c SET status='dissolved', resolved_at=$2 WHERE c.mind_id=$1 AND c.status='open'
		AND EXISTS (SELECT 1 FROM wants w WHERE w.id IN (c.want_a, c.want_b) AND w.status <> 'active')`, mind, now)
	return err
}

// Conflicts lists a mind's want conflicts (status empty = all), newest first.
func (s *Store) Conflicts(ctx context.Context, mind ids.ID, status string, limit int) ([]Conflict, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+conflictCols+` FROM want_conflicts WHERE mind_id=$1 AND ($2='' OR status=$2) ORDER BY created_at DESC LIMIT $3`, mind, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conflict
	for rows.Next() {
		c, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- commitments ----

// NewCommitment describes a commitment to create.
type NewCommitment struct {
	MindID      ids.ID
	Subject     string
	Description string
	Kind        CommitmentKind
	Polarity    string
	Matcher     Matcher
	Unless      string
	Beneficiary string
	Origin      string
	OriginEvent ids.ID
	OriginRef   string
	Strength    float64
	Priority    float64
	Conditions  string
	DueAt       *time.Time
	ExpiresAt   *time.Time
	Revocable   *bool
	GoalID      ids.ID
	IntentionID ids.ID
	Evidence    []events.EvidenceRef
	CreatedBy   string
}

const commitmentCols = `id, mind_id, subject, description, kind, polarity, matcher, unless, beneficiary, origin_type, origin_event, origin_ref, strength, priority, conditions, due_at, expires_at, revocable, status, superseded_by, goal_id, intention_id, evidence, due_notified_at, created_by, status_reason, closed_at, created_at, updated_at`

func scanCommitment(row pgx.Row) (Commitment, error) {
	var c Commitment
	var kind string
	var matcher, evidence []byte
	var originEv, sup, goal, intention *string
	if err := row.Scan(&c.ID, &c.MindID, &c.Subject, &c.Description, &kind, &c.Polarity, &matcher, &c.Unless, &c.Beneficiary, &c.Origin, &originEv, &c.OriginRef, &c.Strength, &c.Priority, &c.Conditions,
		&c.DueAt, &c.ExpiresAt, &c.Revocable, &c.Status, &sup, &goal, &intention, &evidence, &c.DueNotifiedAt, &c.CreatedBy, &c.StatusReason, &c.ClosedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, storage.Translate(err)
	}
	c.Kind = CommitmentKind(kind)
	setID(&c.OriginEvent, originEv)
	setID(&c.SupersededBy, sup)
	setID(&c.GoalID, goal)
	setID(&c.IntentionID, intention)
	json.Unmarshal(matcher, &c.Matcher)   //nolint:errcheck
	json.Unmarshal(evidence, &c.Evidence) //nolint:errcheck
	if c.Evidence == nil {
		c.Evidence = []events.EvidenceRef{}
	}
	return c, nil
}

// CreateCommitment validates and stores a commitment. A new commitment with
// the same subject, kind and polarity supersedes the older one, which is kept
// as history.
func (s *Store) CreateCommitment(ctx context.Context, r Recorder, n NewCommitment) (Commitment, error) {
	now := r.now()
	n.Subject, n.Description = strings.TrimSpace(n.Subject), strings.TrimSpace(n.Description)
	if n.MindID.IsZero() || n.Description == "" || len(n.Description) > 1000 || len(n.Subject) > 300 {
		return Commitment{}, fmt.Errorf("%w: a commitment needs a description (≤1000 chars)", ErrInvalid)
	}
	if n.Subject == "" {
		n.Subject = strings.ToLower(trunc(n.Description, 120))
	}
	if !ValidKind(n.Kind) {
		return Commitment{}, fmt.Errorf("%w: unknown commitment kind %q", ErrInvalid, n.Kind)
	}
	if n.Polarity != "do" && n.Polarity != "refrain" {
		return Commitment{}, fmt.Errorf("%w: polarity must be do or refrain", ErrInvalid)
	}
	if n.Unless != "" && (n.Unless != "approval" || n.Polarity != "refrain") {
		return Commitment{}, fmt.Errorf("%w: only refrain commitments may have unless=approval", ErrInvalid)
	}
	if n.Matcher.Access != "" && n.Matcher.Access != "read" && n.Matcher.Access != "write" {
		return Commitment{}, fmt.Errorf("%w: matcher access must be read, write or empty", ErrInvalid)
	}
	if n.Matcher.MinRisk != "" && riskLevel[n.Matcher.MinRisk] == 0 {
		return Commitment{}, fmt.Errorf("%w: unknown min_risk", ErrInvalid)
	}
	if n.Strength <= 0 || n.Strength > 1 {
		n.Strength = 0.8
	}
	if n.Priority <= 0 || n.Priority > 1 {
		n.Priority = 0.5
	}
	revocable := true
	if n.Revocable != nil {
		revocable = *n.Revocable
	}
	if n.Origin == "" {
		n.Origin = "operator"
	}
	if n.Evidence == nil {
		n.Evidence = []events.EvidenceRef{}
	}
	c := Commitment{ID: ids.New(), MindID: n.MindID, Subject: n.Subject, Description: n.Description, Kind: n.Kind, Polarity: n.Polarity, Matcher: n.Matcher, Unless: n.Unless, Beneficiary: n.Beneficiary, Origin: n.Origin,
		OriginEvent: n.OriginEvent, OriginRef: n.OriginRef, Strength: round3(n.Strength), Priority: round3(n.Priority), Conditions: n.Conditions, DueAt: n.DueAt, ExpiresAt: n.ExpiresAt, Revocable: revocable, Status: "active",
		GoalID: n.GoalID, IntentionID: n.IntentionID, Evidence: n.Evidence, CreatedBy: n.CreatedBy, CreatedAt: now, UpdatedAt: now}
	if _, err := r.Q.Exec(ctx, `INSERT INTO commitments (id, mind_id, subject, description, kind, polarity, matcher, unless, beneficiary, origin_type, origin_event, origin_ref, strength, priority, conditions, due_at, expires_at, revocable, status, goal_id, intention_id, evidence, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,'active',$19,$20,$21,$22,$23,$23)`,
		c.ID, c.MindID, c.Subject, c.Description, string(c.Kind), c.Polarity, jsonOr(c.Matcher, "{}"), c.Unless, c.Beneficiary, c.Origin, nullID(c.OriginEvent), c.OriginRef, c.Strength, c.Priority, c.Conditions, c.DueAt, c.ExpiresAt, c.Revocable,
		nullID(c.GoalID), nullID(c.IntentionID), jsonOr(c.Evidence, "[]"), c.CreatedBy, now); err != nil {
		return Commitment{}, storage.Translate(err)
	}
	tag, err := r.Q.Exec(ctx, `UPDATE commitments SET status='superseded', superseded_by=$1, status_reason='superseded by a newer commitment on the same subject', closed_at=$2, updated_at=$2
		WHERE mind_id=$3 AND id<>$1 AND status='active' AND lower(subject)=lower($4) AND kind=$5 AND polarity=$6`, c.ID, now, c.MindID, c.Subject, string(c.Kind), c.Polarity)
	if err != nil {
		return Commitment{}, err
	}
	verb := map[string]string{"do": "committed to", "refrain": "committed not to"}[c.Polarity]
	summary := fmt.Sprintf("%s: %s (%s, weight %.2f)", verb, c.Description, c.Kind, c.Weight())
	if tag.RowsAffected() > 0 {
		summary += fmt.Sprintf("; supersedes %d earlier commitment(s)", tag.RowsAffected())
	}
	if _, err := r.emit(ctx, c.MindID, events.CommitmentCreated, summary, 0.4+0.3*c.Weight(), append([]events.EvidenceRef{{Kind: "commitment", ID: string(c.ID)}}, c.Evidence...),
		map[string]any{"commitment_id": string(c.ID), "kind": string(c.Kind), "polarity": c.Polarity, "unless": c.Unless, "created_by": c.CreatedBy, "superseded": tag.RowsAffected()}, c); err != nil {
		return Commitment{}, err
	}
	return c, nil
}

// Commitments lists a mind's commitments by status (empty = all).
func (s *Store) Commitments(ctx context.Context, q storage.Querier, mind ids.ID, statuses []string, limit int) ([]Commitment, error) {
	if q == nil {
		q = s.db
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := q.Query(ctx, `SELECT `+commitmentCols+` FROM commitments WHERE mind_id=$1 AND (cardinality($2::text[])=0 OR status=ANY($2)) ORDER BY created_at DESC LIMIT $3`, mind, nonNil(statuses), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Commitment
	for rows.Next() {
		c, err := scanCommitment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCommitment fetches one commitment of a mind.
func (s *Store) GetCommitment(ctx context.Context, q storage.Querier, mind, id ids.ID) (Commitment, error) {
	if q == nil {
		q = s.db
	}
	return scanCommitment(q.QueryRow(ctx, `SELECT `+commitmentCols+` FROM commitments WHERE id=$1 AND mind_id=$2`, id, mind))
}

// ErrNotRevocable is returned when cognition tries to revoke a commitment
// that only an operator may revoke.
var ErrNotRevocable = errors.New("volition: this commitment can only be revoked by an operator")

// CloseCommitment ends an active commitment as fulfilled, broken, revoked or
// expired. Non-revocable commitments can be revoked only by an operator.
func (s *Store) CloseCommitment(ctx context.Context, r Recorder, mind, id ids.ID, status, reason string, byOperator bool) (Commitment, error) {
	typ := map[string]events.Type{"fulfilled": events.CommitmentFulfilled, "broken": events.CommitmentBroken, "revoked": events.CommitmentRevoked, "expired": events.CommitmentRevoked}[status]
	if typ == "" {
		return Commitment{}, fmt.Errorf("%w: status must be fulfilled, broken, revoked or expired", ErrInvalid)
	}
	c, err := s.GetCommitment(ctx, r.Q, mind, id)
	if err != nil {
		return c, err
	}
	if c.Status != "active" {
		return c, fmt.Errorf("%w: commitment is %s", ErrInvalid, c.Status)
	}
	if status == "revoked" && !c.Revocable && !byOperator {
		return c, ErrNotRevocable
	}
	now := r.now()
	if _, err := r.Q.Exec(ctx, `UPDATE commitments SET status=$3, status_reason=$4, closed_at=$5, updated_at=$5 WHERE id=$1 AND mind_id=$2 AND status='active'`, id, mind, status, trunc(reason, 500), now); err != nil {
		return c, err
	}
	c.Status, c.StatusReason, c.ClosedAt = status, reason, &now
	if _, err := r.emit(ctx, mind, typ, fmt.Sprintf("commitment %s: %s — %s", status, c.Description, reason), map[string]float64{"broken": 0.7}[status]+0.3, []events.EvidenceRef{{Kind: "commitment", ID: string(id)}},
		map[string]any{"commitment_id": string(id), "status": status}, map[string]any{"reason": reason}); err != nil {
		return c, err
	}
	return c, nil
}

// MarkDue records that a commitment entered its due window and emits commitment_due once.
func (s *Store) MarkDue(ctx context.Context, r Recorder, c Commitment) error {
	now := r.now()
	tag, err := r.Q.Exec(ctx, `UPDATE commitments SET due_notified_at=$2, updated_at=$2 WHERE id=$1 AND due_notified_at IS NULL`, c.ID, now)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = r.emit(ctx, c.MindID, events.CommitmentDue, fmt.Sprintf("commitment due %s: %s", c.DueAt.Format(time.RFC3339), c.Description), 0.6, []events.EvidenceRef{{Kind: "commitment", ID: string(c.ID)}},
		map[string]any{"commitment_id": string(c.ID), "due_at": c.DueAt}, nil)
	return err
}

// ---- intentions ----

const intentionCols = `id, mind_id, parent_id, description, desired_outcome, kind, source_wants, goal_id, commitment_id, belief_ids, expected_result, confidence, priority, status, blocked_kind, blocked_reason, required_capabilities, require_approval, proposal, plan_id, superseded_by, formed_event, last_event, formation, reconsiderations, status_reason, closed_at, created_at, updated_at`

func scanIntention(row pgx.Row) (Intention, error) {
	var it Intention
	var parent, goal, commitment, plan, sup, formed, last *string
	var kind, status string
	var wants, beliefs []string
	var proposal, formation []byte
	if err := row.Scan(&it.ID, &it.MindID, &parent, &it.Description, &it.DesiredOutcome, &kind, &wants, &goal, &commitment, &beliefs, &it.ExpectedResult, &it.Confidence, &it.Priority, &status, &it.BlockedKind, &it.BlockedReason,
		&it.RequiredCapabilities, &it.RequireApproval, &proposal, &plan, &sup, &formed, &last, &formation, &it.Reconsiderations, &it.StatusReason, &it.ClosedAt, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return it, storage.Translate(err)
	}
	it.Kind, it.Status = IntentionKind(kind), IntentionStatus(status)
	it.SourceWants, it.BeliefIDs = toIDs(wants), toIDs(beliefs)
	for _, x := range []struct {
		dst *ids.ID
		src *string
	}{{&it.ParentID, parent}, {&it.GoalID, goal}, {&it.CommitmentID, commitment}, {&it.PlanID, plan}, {&it.SupersededBy, sup}, {&it.FormedEvent, formed}, {&it.LastEvent, last}} {
		setID(x.dst, x.src)
	}
	if len(proposal) > 0 && string(proposal) != "null" {
		it.Proposal = &Proposal{}
		json.Unmarshal(proposal, it.Proposal) //nolint:errcheck
	}
	json.Unmarshal(formation, &it.Formation) //nolint:errcheck
	return it, nil
}

// CreateIntention stores a formed intention and emits intention_formed with
// its provenance: source wants, goal, commitment, beliefs and the formation
// terms.
func (s *Store) CreateIntention(ctx context.Context, r Recorder, it Intention) (Intention, error) {
	now := r.now()
	it.Description = strings.TrimSpace(it.Description)
	if it.MindID.IsZero() || it.Description == "" {
		return Intention{}, fmt.Errorf("%w: intention needs a mind and a description", ErrInvalid)
	}
	if it.Kind == "" {
		it.Kind = IntentPursue
	}
	if it.Status == "" {
		it.Status = IntentionActive
	}
	it.ID, it.CreatedAt, it.UpdatedAt = ids.New(), now, now
	it.Confidence, it.Priority = clamp01(it.Confidence), clamp01(it.Priority)
	if it.Formation == nil {
		it.Formation = map[string]any{}
	}
	if it.RequiredCapabilities == nil {
		it.RequiredCapabilities = []string{}
	}
	evidence := []events.EvidenceRef{{Kind: "intention", ID: string(it.ID)}}
	for _, w := range it.SourceWants {
		evidence = append(evidence, events.EvidenceRef{Kind: "want", ID: string(w), Note: "source want"})
	}
	if !it.GoalID.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "goal", ID: string(it.GoalID)})
	}
	if !it.CommitmentID.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "commitment", ID: string(it.CommitmentID)})
	}
	for _, b := range it.BeliefIDs {
		evidence = append(evidence, events.EvidenceRef{Kind: "belief", ID: string(b), Note: "supporting belief"})
	}
	if !it.ParentID.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "intention", ID: string(it.ParentID), Note: "parent intention"})
	}
	summary := "intends to " + it.Description
	if it.Status == IntentionBlocked {
		summary += " (blocked: " + it.BlockedReason + ")"
	}
	evID, err := r.emit(ctx, it.MindID, events.IntentionFormed, summary, 0.4+0.4*it.Priority, evidence,
		map[string]any{"intention_id": string(it.ID), "kind": string(it.Kind), "priority": it.Priority, "parent_id": string(it.ParentID), "require_approval": it.RequireApproval, "want_ids": idStrings(it.SourceWants)}, it)
	if err != nil {
		return Intention{}, err
	}
	it.FormedEvent, it.LastEvent = evID, evID
	var proposal []byte
	if it.Proposal != nil {
		proposal, _ = json.Marshal(it.Proposal)
	}
	if _, err := r.Q.Exec(ctx, `INSERT INTO intentions (id, mind_id, parent_id, description, desired_outcome, kind, source_wants, goal_id, commitment_id, belief_ids, expected_result, confidence, priority, status, blocked_kind, blocked_reason,
		required_capabilities, require_approval, proposal, plan_id, formed_event, last_event, formation, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$21,$22,$23,$23)`,
		it.ID, it.MindID, nullID(it.ParentID), trunc(it.Description, 500), trunc(it.DesiredOutcome, 500), string(it.Kind), idStrings(it.SourceWants), nullID(it.GoalID), nullID(it.CommitmentID), idStrings(it.BeliefIDs), trunc(it.ExpectedResult, 500),
		it.Confidence, it.Priority, string(it.Status), it.BlockedKind, it.BlockedReason, it.RequiredCapabilities, it.RequireApproval, proposal, nullID(it.PlanID), nullID(evID), jsonOr(it.Formation, "{}"), now); err != nil {
		return Intention{}, storage.Translate(err)
	}
	return it, nil
}

// Intentions lists a mind's intentions by status (empty = all).
func (s *Store) Intentions(ctx context.Context, q storage.Querier, mind ids.ID, statuses []IntentionStatus, limit int) ([]Intention, error) {
	if q == nil {
		q = s.db
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	st := make([]string, len(statuses))
	for i, x := range statuses {
		st[i] = string(x)
	}
	rows, err := q.Query(ctx, `SELECT `+intentionCols+` FROM intentions WHERE mind_id=$1 AND (cardinality($2::text[])=0 OR status=ANY($2)) ORDER BY created_at DESC LIMIT $3`, mind, st, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Intention
	for rows.Next() {
		it, err := scanIntention(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Open lists live intentions (proposed, active, suspended, blocked).
func (s *Store) Open(ctx context.Context, q storage.Querier, mind ids.ID) ([]Intention, error) {
	return s.Intentions(ctx, q, mind, []IntentionStatus{IntentionProposed, IntentionActive, IntentionSuspended, IntentionBlocked}, 500)
}

// GetIntention fetches one intention of a mind.
func (s *Store) GetIntention(ctx context.Context, q storage.Querier, mind, id ids.ID) (Intention, error) {
	if q == nil {
		q = s.db
	}
	return scanIntention(q.QueryRow(ctx, `SELECT `+intentionCols+` FROM intentions WHERE id=$1 AND mind_id=$2`, id, mind))
}

// GetIntentionAny fetches an intention by id alone (forensic lookups).
func (s *Store) GetIntentionAny(ctx context.Context, id ids.ID) (Intention, error) {
	return scanIntention(s.db.QueryRow(ctx, `SELECT `+intentionCols+` FROM intentions WHERE id=$1`, id))
}

// SetPlan links an intention to its current plan.
func (s *Store) SetPlan(ctx context.Context, q storage.Querier, mind, id, plan ids.ID) error {
	_, err := q.Exec(ctx, `UPDATE intentions SET plan_id=$3, updated_at=now() WHERE id=$1 AND mind_id=$2`, id, mind, nullID(plan))
	return err
}

// Apply records a reconsideration outcome as a status transition with its
// event and decision. History is never rewritten: the earlier state stays in
// the event log and the decision ledger.
func (s *Store) Apply(ctx context.Context, r Recorder, mind, id ids.ID, rec Reconsideration, t Trigger) (Intention, error) {
	it, err := s.GetIntention(ctx, r.Q, mind, id)
	if err != nil {
		return it, err
	}
	return s.applyFrom(ctx, r, it, rec, t)
}

// applyFrom applies a reconsideration to the intention as the caller read
// it. The status change is a compare-and-set against that status: two
// reconsiderations racing on one intention (the executor and the event bus,
// say) cannot both apply, and a closed intention is never reopened. A copy
// the database has moved past gets ErrClosed and writes nothing.
func (s *Store) applyFrom(ctx context.Context, r Recorder, it Intention, rec Reconsideration, t Trigger) (Intention, error) {
	mind, id := it.MindID, it.ID
	if !it.Status.Open() {
		return it, fmt.Errorf("%w (%s)", ErrClosed, it.Status)
	}
	now := r.now()
	next, typ := it.Status, events.IntentionReconsidered
	blockedKind, blockedReason := it.BlockedKind, it.BlockedReason
	requireApproval, kind := it.RequireApproval, it.Kind
	var closed *time.Time
	switch rec.Outcome {
	case Keep:
		return it, nil
	case Block:
		next, typ, blockedKind, blockedReason = IntentionBlocked, events.IntentionBlocked, firstNonEmpty(rec.BlockedKind, BlockPrereq), rec.Reason
	case Suspend:
		next, typ = IntentionSuspended, events.IntentionSuspended
		if rec.BlockedKind != "" {
			blockedKind, blockedReason = rec.BlockedKind, rec.Reason
		}
	case Resume:
		next, typ, blockedKind, blockedReason = IntentionActive, events.IntentionResumed, "", ""
	case Abandon:
		next, typ, closed = IntentionAbandoned, events.IntentionAbandoned, &now
	case Fulfil:
		next, typ, closed, blockedKind, blockedReason = IntentionFulfilled, events.IntentionFulfilled, &now, "", ""
	case Replan:
		next, blockedKind, blockedReason = IntentionActive, "", ""
	case Tighten:
		requireApproval, kind = true, IntentRequestApproval
	default:
		return it, fmt.Errorf("%w: unknown outcome %q", ErrInvalid, rec.Outcome)
	}
	verb := map[events.Type]string{events.IntentionBlocked: "blocked", events.IntentionSuspended: "suspended", events.IntentionResumed: "resumed", events.IntentionAbandoned: "abandoned", events.IntentionFulfilled: "fulfilled", events.IntentionReconsidered: "reconsidering (" + rec.Outcome + ")"}[typ]
	evidence := []events.EvidenceRef{{Kind: "intention", ID: string(it.ID)}}
	if !t.EventID.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "event", ID: string(t.EventID), Note: "trigger: " + t.Kind})
	}
	// The transition is written before its event so that a lost race
	// (zero rows) records nothing at all.
	tag, err := r.Q.Exec(ctx, `UPDATE intentions SET status=$3, blocked_kind=$4, blocked_reason=$5, require_approval=$6, kind=$7, reconsiderations=reconsiderations+1, status_reason=$8, closed_at=coalesce($9, closed_at), updated_at=$10 WHERE id=$1 AND mind_id=$2 AND status=$11`,
		id, mind, string(next), blockedKind, trunc(blockedReason, 500), requireApproval, string(kind), trunc(rec.Reason, 500), closed, now, string(it.Status))
	if err != nil {
		return it, err
	}
	if tag.RowsAffected() == 0 {
		cur, gerr := s.GetIntention(ctx, r.Q, mind, id)
		if gerr != nil {
			return it, gerr
		}
		return cur, fmt.Errorf("%w: it moved from %s to %s meanwhile", ErrClosed, it.Status, cur.Status)
	}
	rr := r
	if !t.EventID.IsZero() {
		rr.Cause = t.EventID
	}
	evID, err := rr.emit(ctx, mind, typ, fmt.Sprintf("intention %s: %s — %s", verb, it.Description, rec.Reason), 0.45, evidence,
		map[string]any{"intention_id": string(it.ID), "from": string(it.Status), "to": string(next), "outcome": rec.Outcome, "trigger": t.Kind, "blocked_kind": blockedKind, "plan_id": string(it.PlanID)}, map[string]any{"reason": rec.Reason, "trigger": t})
	if err != nil {
		return it, err
	}
	if _, err := r.Q.Exec(ctx, `UPDATE intentions SET last_event=$3 WHERE id=$1 AND mind_id=$2`, id, mind, nullID(evID)); err != nil {
		return it, err
	}
	if _, err := s.RecordDecision(ctx, r.Q, Decision{MindID: mind, Kind: "reconsideration", Outcome: rec.Outcome, Reason: t.Kind, Rationale: rec.Reason, IntentionID: id, EventID: evID, CreatedAt: now,
		Inputs: map[string]any{"from": it.Status, "to": next, "trigger": t}}); err != nil {
		return it, err
	}
	if next == IntentionFulfilled {
		for _, w := range it.SourceWants {
			if err := s.ResolveWant(ctx, r, mind, w, WantSatisfied, "intention fulfilled: "+it.Description); err != nil {
				return it, err
			}
		}
		if !it.CommitmentID.IsZero() {
			if _, err := s.CloseCommitment(ctx, r, mind, it.CommitmentID, "fulfilled", "intention fulfilled: "+it.Description, false); err != nil && !errors.Is(err, ErrInvalid) {
				return it, err
			}
		}
	}
	return s.GetIntention(ctx, r.Q, mind, id)
}

// Supersede closes an intention in favour of another.
func (s *Store) Supersede(ctx context.Context, r Recorder, mind, old, by ids.ID, reason string) error {
	it, err := s.GetIntention(ctx, r.Q, mind, old)
	if err != nil {
		return err
	}
	if !it.Status.Open() {
		return nil
	}
	now := r.now()
	tag, err := r.Q.Exec(ctx, `UPDATE intentions SET status='superseded', superseded_by=$3, status_reason=$4, closed_at=$5, updated_at=$5 WHERE id=$1 AND mind_id=$2 AND status=$6`, old, mind, by, reason, now, string(it.Status))
	if err != nil || tag.RowsAffected() == 0 {
		return err // moved on meanwhile: whoever closed it recorded why
	}
	evID, err := r.emit(ctx, mind, events.IntentionSuperseded, "intention superseded: "+it.Description+" — "+reason, 0.4, []events.EvidenceRef{{Kind: "intention", ID: string(old)}, {Kind: "intention", ID: string(by), Note: "replacement"}},
		map[string]any{"intention_id": string(old), "superseded_by": string(by)}, map[string]any{"reason": reason})
	if err != nil {
		return err
	}
	_, err = r.Q.Exec(ctx, `UPDATE intentions SET last_event=$3 WHERE id=$1 AND mind_id=$2`, old, mind, nullID(evID))
	return err
}

// ---- decisions ----

// RecordDecision appends a decision.
func (s *Store) RecordDecision(ctx context.Context, q storage.Querier, d Decision) (Decision, error) {
	if q == nil {
		q = s.db
	}
	d.ID = ids.New()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	if d.Terms == nil {
		d.Terms = map[string]float64{}
	}
	if d.Inputs == nil {
		d.Inputs = map[string]any{}
	}
	_, err := q.Exec(ctx, `INSERT INTO volition_decisions (id, mind_id, kind, outcome, reason, rationale, want_id, intention_id, commitment_id, conflict_id, terms, inputs, event_id, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		d.ID, d.MindID, d.Kind, d.Outcome, d.Reason, trunc(d.Rationale, 2000), nullID(d.WantID), nullID(d.IntentionID), nullID(d.CommitmentID), nullID(d.ConflictID), jsonOr(d.Terms, "{}"), jsonOr(d.Inputs, "{}"), nullID(d.EventID), d.CreatedAt)
	return d, err
}

// DecisionFilter narrows a decision listing.
type DecisionFilter struct {
	WantID       ids.ID
	IntentionID  ids.ID
	CommitmentID ids.ID
	Kind         string
	Limit        int
}

// Decisions lists decisions, newest first.
func (s *Store) Decisions(ctx context.Context, mind ids.ID, f DecisionFilter) ([]Decision, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	rows, err := s.db.Query(ctx, `SELECT id, mind_id, kind, outcome, reason, rationale, want_id, intention_id, commitment_id, conflict_id, terms, inputs, event_id, created_at FROM volition_decisions
		WHERE mind_id=$1 AND ($2::uuid IS NULL OR want_id=$2) AND ($3::uuid IS NULL OR intention_id=$3) AND ($4::uuid IS NULL OR commitment_id=$4) AND ($5='' OR kind=$5) ORDER BY created_at DESC LIMIT $6`,
		mind, nullID(f.WantID), nullID(f.IntentionID), nullID(f.CommitmentID), f.Kind, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		var d Decision
		var want, intention, commitment, conflict, ev *string
		var terms, inputs []byte
		if err := rows.Scan(&d.ID, &d.MindID, &d.Kind, &d.Outcome, &d.Reason, &d.Rationale, &want, &intention, &commitment, &conflict, &terms, &inputs, &ev, &d.CreatedAt); err != nil {
			return nil, err
		}
		setID(&d.WantID, want)
		setID(&d.IntentionID, intention)
		setID(&d.CommitmentID, commitment)
		setID(&d.ConflictID, conflict)
		setID(&d.EventID, ev)
		json.Unmarshal(terms, &d.Terms)   //nolint:errcheck
		json.Unmarshal(inputs, &d.Inputs) //nolint:errcheck
		out = append(out, d)
	}
	return out, rows.Err()
}

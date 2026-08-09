// Package predictions stores explicit expectations with resolution windows
// and machine-checkable criteria, resolves them deterministically, records
// prediction errors and keeps an append-only calibration history (spec §11).
package predictions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Status of a prediction.
type Status string

// Statuses.
const (
	StatusOpen      Status = "open"
	StatusConfirmed Status = "confirmed"
	StatusRefuted   Status = "refuted"
	StatusExpired   Status = "expired"
	StatusWithdrawn Status = "withdrawn"
)

// Criteria says how a prediction is judged.
//
//	event_match: terms must (expect=occurs) or must not (expect=absent) appear in an
//	             event summary of the given types within the window.
//	belief:      the referenced belief's confidence reaches min_confidence by the window end.
//	operator:    only an operator can resolve it.
type Criteria struct {
	Kind          string   `json:"kind"`
	Expect        string   `json:"expect,omitempty"` // occurs | absent
	Terms         []string `json:"terms,omitempty"`
	MinTerms      int      `json:"min_terms,omitempty"` // how many terms must match (default: all, max 3)
	Types         []string `json:"types,omitempty"`
	BeliefID      ids.ID   `json:"belief_id,omitempty"`
	MinConfidence float64  `json:"min_confidence,omitempty"`
}

// Prediction is one expectation.
type Prediction struct {
	ID          ids.ID          `json:"id"`
	MindID      ids.ID          `json:"mind_id"`
	Statement   string          `json:"statement"`
	Subject     string          `json:"subject"`
	Confidence  float64         `json:"confidence"`
	WindowStart time.Time       `json:"window_start"`
	WindowEnd   time.Time       `json:"window_end"`
	Criteria    Criteria        `json:"criteria"`
	Status      Status          `json:"status"`
	Source      string          `json:"source"`
	OriginEvent ids.ID          `json:"origin_event,omitempty"`
	BeliefID    ids.ID          `json:"belief_id,omitempty"`
	ResolvedAt  *time.Time      `json:"resolved_at,omitempty"`
	Resolution  json.RawMessage `json:"resolution,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Result is one append-only outcome row.
type Result struct {
	ID           ids.ID          `json:"id"`
	PredictionID ids.ID          `json:"prediction_id"`
	Outcome      Status          `json:"outcome"`
	Confidence   float64         `json:"confidence"`
	Error        float64         `json:"error"`
	Evidence     json.RawMessage `json:"evidence"`
	EventID      ids.ID          `json:"event_id,omitempty"`
	ResolvedAt   time.Time       `json:"resolved_at"`
}

// ErrInvalid marks bad input.
var ErrInvalid = errors.New("predictions: invalid")

// Store persists predictions.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// New describes a prediction to create.
type New struct {
	MindID      ids.ID
	Statement   string
	Subject     string
	Confidence  float64
	WindowStart time.Time
	WindowEnd   time.Time
	Criteria    Criteria
	Source      string
	OriginEvent ids.ID
	BeliefID    ids.ID
}

func (n *New) validate() error {
	n.Statement = strings.TrimSpace(n.Statement)
	if n.MindID.IsZero() || n.Statement == "" || len(n.Statement) > 1000 {
		return fmt.Errorf("%w: statement required (max 1000 chars)", ErrInvalid)
	}
	if n.Confidence <= 0 || n.Confidence >= 1 {
		return fmt.Errorf("%w: confidence must be strictly between 0 and 1", ErrInvalid)
	}
	if n.WindowStart.IsZero() {
		n.WindowStart = time.Now()
	}
	if !n.WindowEnd.After(n.WindowStart) {
		return fmt.Errorf("%w: window_end must be after window_start", ErrInvalid)
	}
	switch n.Criteria.Kind {
	case "event_match":
		if n.Criteria.Expect != "occurs" && n.Criteria.Expect != "absent" {
			n.Criteria.Expect = "occurs"
		}
		var terms []string
		for _, t := range n.Criteria.Terms {
			if t = strings.ToLower(strings.TrimSpace(t)); len(t) >= 2 {
				terms = append(terms, t)
			}
		}
		if len(terms) == 0 {
			return fmt.Errorf("%w: event_match needs terms", ErrInvalid)
		}
		n.Criteria.Terms = terms
		if n.Criteria.MinTerms <= 0 || n.Criteria.MinTerms > len(terms) {
			n.Criteria.MinTerms = min(len(terms), 3)
		}
	case "belief":
		if n.Criteria.BeliefID.IsZero() || n.Criteria.MinConfidence <= 0 {
			return fmt.Errorf("%w: belief criteria need belief_id and min_confidence", ErrInvalid)
		}
	case "operator", "":
		n.Criteria.Kind = "operator"
	default:
		return fmt.Errorf("%w: unknown criteria kind", ErrInvalid)
	}
	return nil
}

// Create inserts a prediction.
func (s *Store) Create(ctx context.Context, q storage.Querier, n New) (Prediction, error) {
	if err := n.validate(); err != nil {
		return Prediction{}, err
	}
	crit, _ := json.Marshal(n.Criteria)
	p := Prediction{ID: ids.New(), MindID: n.MindID, Statement: n.Statement, Subject: n.Subject, Confidence: n.Confidence, WindowStart: n.WindowStart, WindowEnd: n.WindowEnd, Criteria: n.Criteria,
		Status: StatusOpen, Source: n.Source, OriginEvent: n.OriginEvent, BeliefID: n.BeliefID, CreatedAt: time.Now()}
	_, err := q.Exec(ctx, `INSERT INTO predictions (id, mind_id, statement, subject, confidence, window_start, window_end, criteria, status, source, origin_event, belief_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'open',$9,$10,$11)`, p.ID, p.MindID, p.Statement, p.Subject, p.Confidence, p.WindowStart, p.WindowEnd, crit, p.Source, nullID(p.OriginEvent), nullID(p.BeliefID))
	if err != nil {
		return Prediction{}, err
	}
	return p, nil
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

const cols = `id, mind_id, statement, subject, confidence, window_start, window_end, criteria, status, source, origin_event, belief_id, resolved_at, resolution, created_at`

func scan(row pgx.Row) (Prediction, error) {
	var p Prediction
	var crit []byte
	var status string
	var origin, belief *string
	var res *json.RawMessage
	if err := row.Scan(&p.ID, &p.MindID, &p.Statement, &p.Subject, &p.Confidence, &p.WindowStart, &p.WindowEnd, &crit, &status, &p.Source, &origin, &belief, &p.ResolvedAt, &res, &p.CreatedAt); err != nil {
		return p, storage.Translate(err)
	}
	p.Status = Status(status)
	json.Unmarshal(crit, &p.Criteria) //nolint:errcheck
	if origin != nil {
		p.OriginEvent = ids.ID(*origin)
	}
	if belief != nil {
		p.BeliefID = ids.ID(*belief)
	}
	if res != nil {
		p.Resolution = *res
	}
	return p, nil
}

// Get fetches a prediction scoped to a mind.
func (s *Store) Get(ctx context.Context, mind, id ids.ID) (Prediction, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM predictions WHERE id=$1 AND mind_id=$2`, id, mind))
}

// List returns predictions, newest first, optionally by status.
func (s *Store) List(ctx context.Context, mind ids.ID, statuses []Status, limit int) ([]Prediction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ss := make([]string, len(statuses))
	for i, st := range statuses {
		ss[i] = string(st)
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM predictions WHERE mind_id=$1 AND (cardinality($2::text[])=0 OR status = ANY($2)) ORDER BY created_at DESC LIMIT $3`, mind, ss, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prediction
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Open returns open predictions for a mind (all minds when mind is Nil).
func (s *Store) Open(ctx context.Context, mind ids.ID) ([]Prediction, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM predictions WHERE status='open' AND ($1::uuid IS NULL OR mind_id=$1) ORDER BY window_end LIMIT 1000`, nullID(mind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prediction
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Matches reports whether an event satisfies event_match terms.
func (c Criteria) Matches(ev events.Event) bool {
	if c.Kind != "event_match" {
		return false
	}
	if len(c.Types) > 0 {
		ok := false
		for _, t := range c.Types {
			if events.Type(t) == ev.Type {
				ok = true
			}
		}
		if !ok {
			return false
		}
	} else if ev.Type != events.Observation && ev.Type != events.Perception && ev.Type != events.ToolExecuted && ev.Type != events.ToolFailed {
		return false
	}
	text := strings.ToLower(ev.Summary)
	if raw := string(ev.Payload); len(raw) < 4000 {
		text += " " + strings.ToLower(raw)
	}
	n := 0
	for _, t := range c.Terms {
		if strings.Contains(text, t) {
			n++
		}
	}
	return n >= c.MinTerms
}

// Outcome is the deterministic verdict for a prediction.
type Outcome struct {
	Status   Status
	Evidence []events.EvidenceRef
	Reason   string
}

// Judge decides a prediction against events in its window and the current
// time. It returns ok=false when the prediction cannot be judged yet.
func Judge(p Prediction, now time.Time, window []events.Event, beliefConfidence *float64) (Outcome, bool) {
	switch p.Criteria.Kind {
	case "event_match":
		for _, ev := range window {
			if p.Criteria.Matches(ev) {
				refs := []events.EvidenceRef{{Kind: "event", ID: string(ev.ID), Note: "matched terms"}}
				if p.Criteria.Expect == "absent" {
					return Outcome{Status: StatusRefuted, Evidence: refs, Reason: "an event matching the terms occurred: " + ev.Summary}, true
				}
				return Outcome{Status: StatusConfirmed, Evidence: refs, Reason: "matching event observed: " + ev.Summary}, true
			}
		}
		if now.After(p.WindowEnd) {
			if p.Criteria.Expect == "absent" {
				return Outcome{Status: StatusConfirmed, Reason: "window closed with no matching event"}, true
			}
			return Outcome{Status: StatusRefuted, Reason: "window closed without a matching event"}, true
		}
		return Outcome{}, false
	case "belief":
		if beliefConfidence != nil && *beliefConfidence >= p.Criteria.MinConfidence {
			return Outcome{Status: StatusConfirmed, Evidence: []events.EvidenceRef{{Kind: "belief", ID: string(p.Criteria.BeliefID)}}, Reason: fmt.Sprintf("belief reached %.2f", *beliefConfidence)}, true
		}
		if now.After(p.WindowEnd) {
			return Outcome{Status: StatusRefuted, Evidence: []events.EvidenceRef{{Kind: "belief", ID: string(p.Criteria.BeliefID)}}, Reason: "window closed below the confidence threshold"}, true
		}
		return Outcome{}, false
	default:
		if now.After(p.WindowEnd.Add(7 * 24 * time.Hour)) {
			return Outcome{Status: StatusExpired, Reason: "operator did not resolve it within a week of the window"}, true
		}
		return Outcome{}, false
	}
}

// Resolve records an outcome: status, resolution, append-only result row.
// Error is |truth − confidence| where truth is 1 for confirmed, 0 for refuted.
func (s *Store) Resolve(ctx context.Context, q storage.Querier, p Prediction, o Outcome, eventID ids.ID) (Result, error) {
	if p.Status != StatusOpen {
		return Result{}, fmt.Errorf("%w: prediction already %s", ErrInvalid, p.Status)
	}
	truth := -1.0
	switch o.Status {
	case StatusConfirmed:
		truth = 1
	case StatusRefuted:
		truth = 0
	}
	errv := 0.0
	if truth >= 0 {
		errv = math.Abs(truth - p.Confidence)
	}
	evJSON, _ := json.Marshal(o.Evidence)
	if o.Evidence == nil {
		evJSON = []byte("[]")
	}
	resJSON, _ := json.Marshal(map[string]any{"reason": o.Reason, "evidence": o.Evidence, "error": errv})
	tag, err := q.Exec(ctx, `UPDATE predictions SET status=$3, resolved_at=now(), resolution=$4 WHERE id=$1 AND mind_id=$2 AND status='open'`, p.ID, p.MindID, string(o.Status), resJSON)
	if err != nil {
		return Result{}, err
	}
	if tag.RowsAffected() == 0 {
		return Result{}, fmt.Errorf("%w: already resolved", ErrInvalid)
	}
	r := Result{ID: ids.New(), PredictionID: p.ID, Outcome: o.Status, Confidence: p.Confidence, Error: errv, Evidence: evJSON, EventID: eventID, ResolvedAt: time.Now()}
	_, err = q.Exec(ctx, `INSERT INTO prediction_results (id, prediction_id, mind_id, outcome, confidence, error, evidence, event_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		r.ID, r.PredictionID, p.MindID, string(r.Outcome), r.Confidence, r.Error, evJSON, nullID(eventID))
	return r, err
}

// Bucket is one calibration bin.
type Bucket struct {
	Low, High float64 `json:"-"`
	Label     string  `json:"label"`
	Count     int     `json:"count"`
	Confirmed int     `json:"confirmed"`
	MeanConf  float64 `json:"mean_confidence"`
	HitRate   float64 `json:"hit_rate"`
}

// Calibration summarises resolved predictions.
type Calibration struct {
	Total         int      `json:"total"`
	Confirmed     int      `json:"confirmed"`
	Refuted       int      `json:"refuted"`
	Expired       int      `json:"expired"`
	Accuracy      float64  `json:"accuracy"` // confirmed / (confirmed+refuted)
	Brier         float64  `json:"brier"`    // mean squared error over decided predictions
	Buckets       []Bucket `json:"buckets"`
	Overconfident bool     `json:"overconfident"` // mean confidence exceeds hit rate by > 0.15
}

// Calibrate computes accuracy, Brier score and confidence buckets since a time.
func (s *Store) Calibrate(ctx context.Context, mind ids.ID, since time.Time) (Calibration, error) {
	rows, err := s.db.Query(ctx, `SELECT outcome, confidence FROM prediction_results WHERE mind_id=$1 AND resolved_at >= $2`, mind, since)
	if err != nil {
		return Calibration{}, err
	}
	defer rows.Close()
	c := Calibration{}
	edges := []float64{0, 0.2, 0.4, 0.6, 0.8, 1.01}
	for i := 0; i+1 < len(edges); i++ {
		c.Buckets = append(c.Buckets, Bucket{Low: edges[i], High: edges[i+1], Label: fmt.Sprintf("%.0f–%.0f%%", edges[i]*100, math.Min(edges[i+1], 1)*100)})
	}
	var sumSq, sumConf float64
	for rows.Next() {
		var outcome string
		var conf float64
		if err := rows.Scan(&outcome, &conf); err != nil {
			return c, err
		}
		c.Total++
		switch Status(outcome) {
		case StatusConfirmed:
			c.Confirmed++
			sumSq += (1 - conf) * (1 - conf)
		case StatusRefuted:
			c.Refuted++
			sumSq += conf * conf
		default:
			c.Expired++
			continue
		}
		sumConf += conf
		for i := range c.Buckets {
			b := &c.Buckets[i]
			if conf >= b.Low && conf < b.High {
				b.Count++
				b.MeanConf += conf
				if Status(outcome) == StatusConfirmed {
					b.Confirmed++
				}
			}
		}
	}
	decided := c.Confirmed + c.Refuted
	if decided > 0 {
		c.Accuracy = float64(c.Confirmed) / float64(decided)
		c.Brier = sumSq / float64(decided)
		c.Overconfident = sumConf/float64(decided)-c.Accuracy > 0.15 && decided >= 5
	}
	for i := range c.Buckets {
		b := &c.Buckets[i]
		if b.Count > 0 {
			b.MeanConf /= float64(b.Count)
			b.HitRate = float64(b.Confirmed) / float64(b.Count)
		}
	}
	sort.SliceStable(c.Buckets, func(i, j int) bool { return c.Buckets[i].Low < c.Buckets[j].Low })
	return c, rows.Err()
}

// Stats counts predictions by status.
func (s *Store) Stats(ctx context.Context, mind ids.ID) (map[string]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT status, count(*) FROM predictions WHERE mind_id=$1 GROUP BY status`, mind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// CalibratedScore reads a belief score against this mind's whole prediction
// history. It is the only empirical calibration Noema offers, and it says
// so when the sample is too small.
func (s *Store) CalibratedScore(ctx context.Context, mind ids.ID, score float64) beliefs.Calibrated {
	c, err := s.Calibrate(ctx, mind, time.Time{})
	if err != nil {
		return beliefs.CalibrateWith(score, nil)
	}
	var buckets []beliefs.CalibrationBucket
	for _, b := range c.Buckets {
		buckets = append(buckets, beliefs.CalibrationBucket{Low: b.Low, High: b.High, Decided: b.Count, HitRate: b.HitRate})
	}
	return beliefs.CalibrateWith(score, buckets)
}

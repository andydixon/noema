package beliefs

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// SourceReliability is a source's recorded track record in one domain.
// The score is a Laplace estimate (correct+1)/(correct+wrong+2), so a
// source with no history sits at 0.5 and is left alone.
type SourceReliability struct {
	Source  string  `json:"source"`
	Domain  string  `json:"domain"`
	Correct int     `json:"correct"`
	Wrong   int     `json:"wrong"`
	Score   float64 `json:"score"`
	Applied bool    `json:"applied"` // enough history to adjust credibility
}

// MinReliabilityObservations is the fewest outcomes in a domain before a
// source's record adjusts the credibility of its evidence there.
const MinReliabilityObservations = 3

func (r SourceReliability) score() float64 {
	return float64(r.Correct+1) / float64(r.Correct+r.Wrong+2)
}

// Adjust scales a trust-level credibility by the source's record in the
// domain: neutral (0.5) leaves it alone, a perfect record raises it by
// half, a hopeless one halves it. Applied only with enough history.
func (r SourceReliability) Adjust(credibility float64) float64 {
	if !r.Applied {
		return credibility
	}
	return math.Min(1, math.Max(0.05, credibility*(0.5+r.Score)))
}

// RecordOutcome adds one correct or wrong outcome for source in domain.
func (s *Store) RecordOutcome(ctx context.Context, q storage.Querier, mind ids.ID, source, domain string, correct bool) error {
	if source == "" {
		return nil
	}
	if domain == "" {
		domain = "*"
	}
	// Two literal statements rather than an assembled one: no identifier is
	// ever built from a value, however constrained.
	stmt := `INSERT INTO source_reliability (mind_id, source, domain, wrong) VALUES ($1,$2,$3,1)
		ON CONFLICT (mind_id, source, domain) DO UPDATE SET wrong = source_reliability.wrong + 1, updated_at = now()`
	if correct {
		stmt = `INSERT INTO source_reliability (mind_id, source, domain, correct) VALUES ($1,$2,$3,1)
		ON CONFLICT (mind_id, source, domain) DO UPDATE SET correct = source_reliability.correct + 1, updated_at = now()`
	}
	_, err := q.Exec(ctx, stmt, mind, source, domain)
	return err
}

// Reliability returns the record for a source in a domain.
func (s *Store) Reliability(ctx context.Context, q storage.Querier, mind ids.ID, source, domain string) (SourceReliability, error) {
	if q == nil {
		q = s.db
	}
	if domain == "" {
		domain = "*"
	}
	r := SourceReliability{Source: source, Domain: domain}
	err := q.QueryRow(ctx, `SELECT correct, wrong FROM source_reliability WHERE mind_id=$1 AND source=$2 AND domain=$3`, mind, source, domain).Scan(&r.Correct, &r.Wrong)
	if err != nil && !isNoRows(err) {
		return r, err
	}
	r.Score = round4(r.score())
	r.Applied = r.Correct+r.Wrong >= MinReliabilityObservations
	return r, nil
}

// ReliabilityFor lists the records of every family cited by a belief in its
// domain, for display.
func (s *Store) ReliabilityFor(ctx context.Context, mind ids.ID, b Belief, rows []EvidenceRow) []SourceReliability {
	seen := map[string]bool{}
	var out []SourceReliability
	for _, e := range rows {
		fam := FamilyOf(e.Evidence)
		if fam == "" || seen[fam] {
			continue
		}
		seen[fam] = true
		if r, err := s.Reliability(ctx, nil, mind, fam, b.Subject); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// applyReliability adjusts the credibility of new evidence by the source's
// record in the belief's domain, noting the adjustment on the row so the
// arithmetic stays inspectable. Trust-level credibility is never changed.
func (s *Store) applyReliability(ctx context.Context, q storage.Querier, mind ids.ID, domain string, e Evidence, note string) (Evidence, string) {
	fam := FamilyOf(e)
	if fam == "" || e.Direction != "for" {
		return e, note
	}
	r, err := s.Reliability(ctx, q, mind, fam, domain)
	if err != nil || !r.Applied {
		return e, note
	}
	before := e.Credibility
	e.Credibility = round4(r.Adjust(e.Credibility))
	if e.Credibility != before {
		note += fmt.Sprintf(" [credibility %.2f→%.2f: %s has been right %d and wrong %d time(s) about %s]", before, e.Credibility, fam, r.Correct, r.Wrong, domain)
	}
	return e, note
}

// RecordBeliefOutcome records an outcome for every primary supporting family
// of a belief, in the belief's domain only.
func (s *Store) RecordBeliefOutcome(ctx context.Context, q storage.Querier, mind, beliefID ids.ID, correct bool) error {
	b, err := s.Get(ctx, q, mind, beliefID)
	if err != nil {
		return err
	}
	rows, err := s.EvidenceFor(ctx, q, mind, beliefID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range rows {
		if e.Retracted || e.Direction != "for" || Derived(e.Derivation) {
			continue
		}
		fam := FamilyOf(e.Evidence)
		if fam == "" || seen[fam] || fam == "prediction:"+"" {
			continue
		}
		seen[fam] = true
		if err := s.RecordOutcome(ctx, q, mind, fam, b.Subject, correct); err != nil {
			return err
		}
	}
	return nil
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

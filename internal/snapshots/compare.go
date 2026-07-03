package snapshots

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/pkg/ids"
)

// Comparison is a deterministic side-by-side of two minds.
type Comparison struct {
	A, B        minds.Mind     `json:"-"`
	Stats       []StatRow      `json:"stats"`
	Beliefs     []BeliefDiff   `json:"beliefs"`
	Personality []minds.Change `json:"personality"`
	Config      []ConfigDiff   `json:"config"`
	Events      []StatRow      `json:"events"`
	Summary     map[string]int `json:"summary"`
	Notes       []string       `json:"notes"`
}

// StatRow is one compared number.
type StatRow struct {
	Name string  `json:"name"`
	A    float64 `json:"a"`
	B    float64 `json:"b"`
}

// BeliefDiff compares beliefs by normalised proposition.
type BeliefDiff struct {
	Proposition string  `json:"proposition"`
	InA         bool    `json:"in_a"`
	InB         bool    `json:"in_b"`
	ConfA       float64 `json:"conf_a"`
	ConfB       float64 `json:"conf_b"`
	StatusA     string  `json:"status_a"`
	StatusB     string  `json:"status_b"`
	Delta       float64 `json:"delta"`
}

// ConfigDiff is one differing configuration field.
type ConfigDiff struct {
	Field string `json:"field"`
	A     string `json:"a"`
	B     string `json:"b"`
}

// Compare computes counts, belief differences, personality and config diffs.
func (s *Store) Compare(ctx context.Context, a, b minds.Mind) (Comparison, error) {
	c := Comparison{A: a, B: b, Summary: map[string]int{}}
	count := func(q string, id ids.ID) float64 {
		var n float64
		s.DB.QueryRow(ctx, q, id).Scan(&n) //nolint:errcheck
		return n
	}
	stats := []struct{ name, q string }{
		{"memories (active)", `SELECT count(*) FROM memories WHERE mind_id=$1 AND status='active'`},
		{"beliefs (not rejected)", `SELECT count(*) FROM beliefs WHERE mind_id=$1 AND status NOT IN ('rejected','superseded')`},
		{"beliefs contested", `SELECT count(*) FROM beliefs WHERE mind_id=$1 AND status='contested'`},
		{"mean belief confidence", `SELECT coalesce(avg(confidence),0) FROM beliefs WHERE mind_id=$1 AND status NOT IN ('rejected','superseded')`},
		{"goals active", `SELECT count(*) FROM goals WHERE mind_id=$1 AND status='active'`},
		{"predictions resolved", `SELECT count(*) FROM predictions WHERE mind_id=$1 AND resolved_at IS NOT NULL`},
		{"entities", `SELECT count(*) FROM entities WHERE mind_id=$1 AND merged_into IS NULL`},
		{"events", `SELECT count(*) FROM cognitive_events WHERE mind_id=$1`},
		{"llm calls", `SELECT count(*) FROM llm_calls WHERE mind_id=$1`},
	}
	for _, st := range stats {
		c.Stats = append(c.Stats, StatRow{Name: st.name, A: round(count(st.q, a.ID)), B: round(count(st.q, b.ID))})
	}
	// Event mix by type.
	mix := func(id ids.ID) map[string]float64 {
		out := map[string]float64{}
		rows, err := s.DB.Query(ctx, `SELECT type, count(*) FROM cognitive_events WHERE mind_id=$1 GROUP BY type`, id)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var t string
			var n float64
			if rows.Scan(&t, &n) == nil {
				out[t] = n
			}
		}
		return out
	}
	ma, mb := mix(a.ID), mix(b.ID)
	types := map[string]bool{}
	for t := range ma {
		types[t] = true
	}
	for t := range mb {
		types[t] = true
	}
	for t := range types {
		c.Events = append(c.Events, StatRow{Name: t, A: ma[t], B: mb[t]})
	}
	sort.Slice(c.Events, func(i, j int) bool { return c.Events[i].Name < c.Events[j].Name })
	// Beliefs by normalised proposition.
	type bl struct {
		conf   float64
		status string
	}
	load := func(id ids.ID) map[string]bl {
		out := map[string]bl{}
		rows, err := s.DB.Query(ctx, `SELECT proposition, confidence, status FROM beliefs WHERE mind_id=$1 AND status NOT IN ('superseded')`, id)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var p, st string
			var cf float64
			if rows.Scan(&p, &cf, &st) == nil {
				out[norm(p)] = bl{cf, st}
			}
		}
		return out
	}
	ba, bb := load(a.ID), load(b.ID)
	keys := map[string]bool{}
	for k := range ba {
		keys[k] = true
	}
	for k := range bb {
		keys[k] = true
	}
	for k := range keys {
		x, inA := ba[k]
		y, inB := bb[k]
		d := BeliefDiff{Proposition: k, InA: inA, InB: inB, ConfA: x.conf, ConfB: y.conf, StatusA: x.status, StatusB: y.status}
		switch {
		case inA && inB:
			d.Delta = round(y.conf - x.conf)
			if math.Abs(d.Delta) < 0.005 && x.status == y.status {
				c.Summary["beliefs_same"]++
				continue
			}
			c.Summary["beliefs_changed"]++
		case inA:
			c.Summary["beliefs_only_a"]++
		default:
			c.Summary["beliefs_only_b"]++
		}
		c.Beliefs = append(c.Beliefs, d)
	}
	sort.Slice(c.Beliefs, func(i, j int) bool {
		if math.Abs(c.Beliefs[i].Delta) != math.Abs(c.Beliefs[j].Delta) {
			return math.Abs(c.Beliefs[i].Delta) > math.Abs(c.Beliefs[j].Delta)
		}
		return c.Beliefs[i].Proposition < c.Beliefs[j].Proposition
	})
	c.Personality = minds.Diff(a.Personality, b.Personality)
	c.Config = configDiff(a.Config, b.Config)
	if a.ForkedFromMind != nil && *a.ForkedFromMind == b.ID || b.ForkedFromMind != nil && *b.ForkedFromMind == a.ID {
		c.Notes = append(c.Notes, "one mind is a fork of the other: history before the fork is shared, differences arose afterwards")
	}
	return c, nil
}

func configDiff(a, b minds.CognitiveConfig) []ConfigDiff {
	var ma, mb map[string]any
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	json.Unmarshal(ja, &ma) //nolint:errcheck
	json.Unmarshal(jb, &mb) //nolint:errcheck
	var out []ConfigDiff
	for k, va := range ma {
		vb := mb[k]
		sa, sb := fmt.Sprint(va), fmt.Sprint(vb)
		if sa != sb {
			out = append(out, ConfigDiff{Field: k, A: sa, B: sb})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func round(f float64) float64 { return math.Round(f*1000) / 1000 }

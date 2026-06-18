// Package workspace implements the capacity-limited global workspace and the
// decaying working memory of a mind. Both are plain data with deterministic
// update rules; persistence is a JSON snapshot per mind.
package workspace

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Thought is an item in the workspace.
type Thought struct {
	ID            ids.ID               `json:"id"`
	Process       string               `json:"process"`
	Kind          string               `json:"kind"`
	Subject       string               `json:"subject"`
	Summary       string               `json:"summary"`
	Content       map[string]any       `json:"content,omitempty"`
	Score         float64              `json:"score"`
	Breakdown     attention.Breakdown  `json:"breakdown"`
	Activation    float64              `json:"activation"` // decays each cycle it is not re-admitted
	AdmittedCycle int64                `json:"admitted_cycle"`
	AdmittedAt    time.Time            `json:"admitted_at"`
	EventID       ids.ID               `json:"event_id,omitempty"` // thought_selected event
	Evidence      []events.EvidenceRef `json:"evidence,omitempty"`
	Confidence    float64              `json:"confidence"`
}

// Item is a working-memory entry: something the mind is currently holding.
type Item struct {
	Key         string    `json:"key"`
	Content     string    `json:"content"`
	Kind        string    `json:"kind"` // observation | thought | goal | retrieval | note
	Activation  float64   `json:"activation"`
	AddedCycle  int64     `json:"added_cycle"`
	TouchedAt   time.Time `json:"touched_at"`
	SourceEvent ids.ID    `json:"source_event,omitempty"`
	Ref         string    `json:"ref,omitempty"` // memory/belief/goal id when applicable
}

// State is the persisted snapshot.
type State struct {
	Cycle         int64     `json:"cycle"`
	Mode          string    `json:"mode"`
	LastCycleID   ids.ID    `json:"last_cycle_id,omitempty"`
	LastCycleAt   time.Time `json:"last_cycle_at"`
	LastStimulus  time.Time `json:"last_stimulus_at"`
	Workspace     []Thought `json:"workspace"`
	WorkingMemory []Item    `json:"working_memory"`
}

// Outcome records what happened to one candidate during admission.
type Outcome struct {
	ID        ids.ID  `json:"id"`
	Admitted  bool    `json:"admitted"`
	Rank      int     `json:"rank"`
	Total     float64 `json:"total"`
	Reason    string  `json:"reason"`
	Displaced ids.ID  `json:"displaced,omitempty"`
}

// Decay multiplies activation of every workspace thought and working-memory
// item by factor (0,1), simulating fading when not refreshed.
func (s *State) Decay(factor float64) {
	for i := range s.Workspace {
		s.Workspace[i].Activation = round4(s.Workspace[i].Activation * factor)
	}
	for i := range s.WorkingMemory {
		s.WorkingMemory[i].Activation = round4(s.WorkingMemory[i].Activation * factor)
	}
}

// Admit merges scored candidates into the workspace of the given capacity.
// Existing thoughts compete with their decayed effective score
// (score*activation); a candidate with the same subject as an existing
// thought refreshes it instead of duplicating. Returns the outcome per
// candidate and the ids of thoughts displaced. Deterministic.
func (s *State) Admit(cands []Thought, capacity int, cycle int64, now time.Time) ([]Outcome, []ids.ID) {
	if capacity < 1 {
		capacity = 1
	}
	type entry struct {
		t        Thought
		eff      float64
		isNew    bool
		refresh  bool
		oldIndex int
	}
	entries := make([]entry, 0, len(s.Workspace)+len(cands))
	for i, t := range s.Workspace {
		entries = append(entries, entry{t: t, eff: round4(t.Score * t.Activation), oldIndex: i})
	}
	for _, c := range cands {
		c.Activation = 1
		c.AdmittedCycle, c.AdmittedAt = cycle, now
		refreshed := false
		for i := range entries {
			if !entries[i].isNew && entries[i].t.Subject != "" && entries[i].t.Subject == c.Subject && entries[i].t.Kind == c.Kind {
				// Refresh: keep identity of the workspace slot, take the new score.
				entries[i].t.Score, entries[i].t.Breakdown, entries[i].t.Summary, entries[i].t.Content = c.Score, c.Breakdown, c.Summary, c.Content
				entries[i].t.Activation, entries[i].t.Evidence, entries[i].t.Confidence = 1, c.Evidence, c.Confidence
				entries[i].t.ID, entries[i].t.AdmittedCycle, entries[i].t.AdmittedAt = c.ID, cycle, now
				entries[i].eff, entries[i].refresh = c.Score, true
				refreshed = true
				break
			}
		}
		if !refreshed {
			entries = append(entries, entry{t: c, eff: c.Score, isNew: true})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].eff != entries[j].eff {
			return entries[i].eff > entries[j].eff
		}
		return entries[i].t.ID < entries[j].t.ID
	})
	var kept []Thought
	var displaced []ids.ID
	outcomes := make([]Outcome, 0, len(cands))
	candRank := map[ids.ID]int{}
	rank := 0
	for _, e := range entries {
		if e.isNew || e.refresh {
			rank++
			candRank[e.t.ID] = rank
		}
	}
	cutoff := math.Inf(-1)
	for i, e := range entries {
		if i < capacity {
			kept = append(kept, e.t)
			if i == capacity-1 && len(entries) > capacity {
				cutoff = e.eff
			}
			continue
		}
		if !e.isNew && !e.refresh {
			displaced = append(displaced, e.t.ID)
		}
	}
	for _, c := range cands {
		o := Outcome{ID: c.ID, Total: c.Score, Rank: candRank[c.ID]}
		found := false
		for i, k := range kept {
			if k.ID == c.ID {
				found = true
				o.Admitted = true
				if i < len(kept) {
					o.Reason = "admitted"
				}
				break
			}
		}
		if !found {
			if math.IsInf(cutoff, -1) {
				o.Reason = "rejected: outscored"
			} else {
				o.Reason = "rejected: below cutoff " + trim(cutoff)
			}
		}
		outcomes = append(outcomes, o)
	}
	s.Workspace = kept
	return outcomes, displaced
}

func trim(v float64) string {
	return strings.TrimRight(strings.TrimRight(formatFloat(v), "0"), ".")
}

func formatFloat(v float64) string {
	return strconvFormat(v)
}

// Hold puts or refreshes an item in working memory, evicting the least active
// item when over capacity. Returns the evicted key, if any.
func (s *State) Hold(it Item, capacity int, cycle int64, now time.Time) (evicted string) {
	if capacity < 1 {
		capacity = 1
	}
	it.Activation, it.TouchedAt = 1, now
	if it.AddedCycle == 0 {
		it.AddedCycle = cycle
	}
	for i := range s.WorkingMemory {
		if s.WorkingMemory[i].Key == it.Key {
			it.AddedCycle = s.WorkingMemory[i].AddedCycle
			s.WorkingMemory[i] = it
			return ""
		}
	}
	s.WorkingMemory = append(s.WorkingMemory, it)
	if len(s.WorkingMemory) <= capacity {
		return ""
	}
	sort.SliceStable(s.WorkingMemory, func(i, j int) bool {
		if s.WorkingMemory[i].Activation != s.WorkingMemory[j].Activation {
			return s.WorkingMemory[i].Activation > s.WorkingMemory[j].Activation
		}
		return s.WorkingMemory[i].TouchedAt.After(s.WorkingMemory[j].TouchedAt)
	})
	evicted = s.WorkingMemory[len(s.WorkingMemory)-1].Key
	s.WorkingMemory = s.WorkingMemory[:capacity]
	return evicted
}

// Prune drops thoughts and items whose activation fell below the floor.
func (s *State) Prune(floor float64) (dropped int) {
	var ws []Thought
	for _, t := range s.Workspace {
		if t.Activation >= floor {
			ws = append(ws, t)
		} else {
			dropped++
		}
	}
	s.Workspace = ws
	var wm []Item
	for _, it := range s.WorkingMemory {
		if it.Activation >= floor {
			wm = append(wm, it)
		} else {
			dropped++
		}
	}
	s.WorkingMemory = wm
	return dropped
}

// Top returns the highest-scoring workspace thought, if any.
func (s *State) Top() (Thought, bool) {
	if len(s.Workspace) == 0 {
		return Thought{}, false
	}
	return s.Workspace[0], true
}

// Tokens returns the lower-cased word set of working memory contents, used
// for cheap context-relevance overlap by processes.
func (s *State) Tokens() map[string]struct{} {
	out := map[string]struct{}{}
	for _, it := range s.WorkingMemory {
		for _, w := range Tokenise(it.Content) {
			out[w] = struct{}{}
		}
	}
	for _, t := range s.Workspace {
		for _, w := range Tokenise(t.Summary) {
			out[w] = struct{}{}
		}
	}
	return out
}

// Tokenise splits text into lower-case alphanumeric words of length >= 3.
func Tokenise(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 3 {
			out = append(out, cur.String())
		}
		cur.Reset()
	}
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// Overlap returns |a∩b| / |a| for token sets (0 when a is empty).
func Overlap(a []string, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	seen := map[string]struct{}{}
	for _, w := range a {
		if _, dup := seen[w]; dup {
			continue
		}
		seen[w] = struct{}{}
		if _, ok := b[w]; ok {
			n++
		}
	}
	return round4(float64(n) / float64(len(seen)))
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
)

// Seed is the mind-seed document (schema noema.mind-seed/1): identity,
// personality, configuration and starting knowledge. It is not the full
// export/import archive (phase 15); it is how sample and template minds are
// loaded. Treated as hostile input: strict decoding, size limits, operator
// trust on every item, no secrets, no policies.
type Seed struct {
	Schema      string                 `json:"schema"`
	Slug        string                 `json:"slug"`
	DisplayName string                 `json:"display_name"`
	Notes       string                 `json:"notes"`
	Personality *minds.Personality     `json:"personality"`
	Config      *minds.CognitiveConfig `json:"config"`
	Memories    []struct {
		Kind       concepts.MemoryKind `json:"kind"`
		Epistemic  concepts.Epistemic  `json:"epistemic"`
		Content    string              `json:"content"`
		Confidence float64             `json:"confidence"`
		Importance float64             `json:"importance"`
	} `json:"memories"`
	Beliefs []struct {
		Proposition string             `json:"proposition"`
		Subject     string             `json:"subject"`
		Epistemic   concepts.Epistemic `json:"epistemic"`
		Weight      float64            `json:"weight"`
		Note        string             `json:"note"`
	} `json:"beliefs"`
}

const maxSeedItems = 500

func (s *Server) importRoutes() {
	s.mux.Handle("POST /api/v1/minds/import", require(auth.RoleAdministrator, s.mindImport))
}

// mindImport creates a mind from a seed document.
func (s *Server) mindImport(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	var seed Seed
	if err := dec.Decode(&seed); err != nil {
		fail(w, r, ValidationError("invalid seed: "+err.Error()))
		return
	}
	if seed.Schema != "noema.mind-seed/1" {
		fail(w, r, ValidationError("unsupported schema; expected noema.mind-seed/1"))
		return
	}
	if len(seed.Memories) > maxSeedItems || len(seed.Beliefs) > maxSeedItems {
		fail(w, r, ValidationError("too many seed items"))
		return
	}
	m, err := s.deps.Minds.Create(r.Context(), minds.CreateParams{Slug: seed.Slug, DisplayName: seed.DisplayName, Personality: seed.Personality, Config: seed.Config, Notes: seed.Notes, CreatedBy: actor(r)})
	if err != nil {
		fail(w, r, mindErr(err))
		return
	}
	var memCount, beliefCount int
	var problems []string
	for _, sm := range seed.Memories {
		mem, err := s.deps.Memory.Create(r.Context(), s.deps.DB, memory.New{MindID: m.ID, Kind: sm.Kind, Epistemic: sm.Epistemic, Content: sm.Content, Source: "seed", Trust: events.TrustOperator, Confidence: sm.Confidence, Importance: sm.Importance, Salience: 0.5})
		if err != nil {
			problems = append(problems, "memory: "+err.Error())
			continue
		}
		memCount++
		s.deps.Events.Append(r.Context(), events.New{MindID: m.ID, Type: events.MemoryCreated, Source: "seed", Summary: "seeded " + string(mem.Kind) + " memory: " + trunc(mem.Content, 120), Salience: 0.3, Confidence: mem.Confidence, //nolint:errcheck
			Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(mem.ID)}}, Metadata: map[string]any{"memory_id": mem.ID, "kind": mem.Kind, "actor": actor(r)}})
	}
	for _, sb := range seed.Beliefs {
		weight := sb.Weight
		if weight <= 0 {
			weight = 0.5
		}
		_, err := s.deps.Beliefs.Create(r.Context(), s.deps.DB, s.Emitter(), beliefs.New{MindID: m.ID, Proposition: sb.Proposition, Subject: sb.Subject, Epistemic: sb.Epistemic, Source: "seed",
			Evidence: []beliefs.EvidenceInput{{Direction: "for", Kind: "operator", Source: "seed:" + actor(r), Weight: weight, Credibility: events.TrustOperator.Credibility(), Note: firstNonEmpty(sb.Note, "seeded by "+actor(r))}}})
		if err != nil {
			if errors.Is(err, beliefs.ErrInvalid) || errors.Is(err, beliefs.ErrBlocked) {
				problems = append(problems, "belief: "+err.Error())
				continue
			}
			fail(w, r, err)
			return
		}
		beliefCount++
	}
	s.deps.Brain.Reconcile(r.Context()) //nolint:errcheck
	s.deps.Audit.Log(r.Context(), "mind.import", "mind", m.ID.String(), map[string]any{"slug": m.Slug, "memories": memCount, "beliefs": beliefCount, "problems": problems})
	writeJSON(w, http.StatusCreated, map[string]any{"mind": m, "memories": memCount, "beliefs": beliefCount, "problems": problems})
}

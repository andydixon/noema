// Package concepts defines the epistemic vocabulary Noema refuses to collapse:
// the difference between an observation, a claim, a hypothesis and a belief is
// carried on every memory, belief and evidence link (spec §0.1).
package concepts

// Epistemic classifies how something is known.
type Epistemic string

// Epistemic kinds.
const (
	Observation Epistemic = "observation" // directly perceived through a channel
	Fact        Epistemic = "fact"        // verified against an authoritative source
	Memory      Epistemic = "memory"      // recalled episode; subject to distortion
	Claim       Epistemic = "claim"       // asserted by someone else, unverified
	Hypothesis  Epistemic = "hypothesis"  // proposed explanation under evaluation
	Belief      Epistemic = "belief"      // held with confidence derived from evidence
	Prediction  Epistemic = "prediction"  // expectation about the future
	Assumption  Epistemic = "assumption"  // taken as given without evidence
	Inference   Epistemic = "inference"   // derived by reasoning from other items
	Opinion     Epistemic = "opinion"     // preference or judgement, not truth-apt
)

// AllEpistemic lists kinds in a stable order.
var AllEpistemic = []Epistemic{Observation, Fact, Memory, Claim, Hypothesis, Belief, Prediction, Assumption, Inference, Opinion}

// Valid reports whether e is known.
func (e Epistemic) Valid() bool {
	for _, k := range AllEpistemic {
		if k == e {
			return true
		}
	}
	return false
}

// MemoryKind is the storage system a memory belongs to.
type MemoryKind string

// Memory kinds (spec §8).
const (
	Episodic         MemoryKind = "episodic"
	Semantic         MemoryKind = "semantic"
	Procedural       MemoryKind = "procedural"
	Autobiographical MemoryKind = "autobiographical"
)

// AllMemoryKinds lists kinds in a stable order.
var AllMemoryKinds = []MemoryKind{Episodic, Semantic, Procedural, Autobiographical}

// Valid reports whether k is known.
func (k MemoryKind) Valid() bool {
	for _, x := range AllMemoryKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Relation labels an edge between memories or entities.
type Relation string

// Relations (spec §24).
const (
	RelatedTo      Relation = "related_to"
	CausedBy       Relation = "caused_by"
	Contradicts    Relation = "contradicts"
	Supports       Relation = "supports"
	DependsOn      Relation = "depends_on"
	BelongsTo      Relation = "belongs_to"
	DiscussedWith  Relation = "discussed_with"
	ObservedDuring Relation = "observed_during"
	DerivedFrom    Relation = "derived_from"
	SimilarTo      Relation = "similar_to"
	Follows        Relation = "follows"
)

// AllRelations lists relations in a stable order.
// Association is not causation. CoOccursWith and TemporallyPrecedes record
// what was observed together or in sequence; PossiblyCausedBy is a causal
// hypothesis awaiting evidence; CausedBy is reserved for established causes
// (an explicit operator statement or a confirmed causal prediction);
// AssertedBy links a proposition to whoever stated it.
const (
	CoOccursWith       Relation = "co_occurs_with"
	PossiblyCausedBy   Relation = "possibly_caused_by"
	TemporallyPrecedes Relation = "temporally_precedes"
	AssertedBy         Relation = "asserted_by"
)

var AllRelations = []Relation{RelatedTo, CausedBy, Contradicts, Supports, DependsOn, BelongsTo, DiscussedWith, ObservedDuring, DerivedFrom, SimilarTo, Follows, CoOccursWith, PossiblyCausedBy, TemporallyPrecedes, AssertedBy}

// Causal reports whether a relation asserts an established cause.
func (r Relation) Causal() bool { return r == CausedBy }

// Hypothetical reports whether a relation is a proposal rather than a finding.
func (r Relation) Hypothetical() bool { return r == PossiblyCausedBy }

// Valid reports whether r is known.
func (r Relation) Valid() bool {
	for _, x := range AllRelations {
		if x == r {
			return true
		}
	}
	return false
}

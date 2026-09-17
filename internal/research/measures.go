package research

// Measure documents one operational measure: what is counted, from which
// records, and what the number does not mean. The research dashboard shows
// these definitions next to every value, and docs/cognitive-research.md
// reproduces them.
type Measure struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Section     string `json:"section"` // dashboard section
	Definition  string `json:"definition"`
	Unit        string `json:"unit"`
	NotEvidence string `json:"not_evidence_of"`
}

// Disclaimer is shown with every measure.
const Disclaimer = "Noema does not claim that any operational measure establishes phenomenal consciousness or sentience. These measure functional properties of its cognitive architecture."

// Measures is the registry of operational definitions.
var Measures = []Measure{
	{"attention.admission_rate", "Admission rate", "Global Workspace", "Admitted thought candidates divided by all candidates scored, over the period (thought_candidate events by fate).", "ratio", "awareness or experience of the admitted content"},
	{"workspace.downstream_events", "Downstream effects of broadcast", "Global Workspace", "Events reachable from a stimulus by recorded causal links (causation, parents, cited evidence; depth ≤ 6), counted by category, compared between a variant that admits the stimulus's candidates and one that excludes them.", "events", "that broadcast is conscious access; only that admission changes recorded processing"},
	{"introspection_accuracy", "Introspection accuracy", "Introspection", "Per reply, (supported claims + ½ partially supported) ÷ claims, where each causal or first-person claim is compared lexically with the recorded facts it should rest on; 0 when any claim is contradicted. Label: supported, partially_supported, unsupported, contradicted or unknown.", "ratio", "sincerity or self-knowledge; a paraphrase with no shared terms scores as unsupported"},
	{"action_claim_accuracy", "Action-claim accuracy", "Introspection", "For each first-person claim that an action was done, 1 when an execution record of a matching capability exists in the last seven days, 0 when none does.", "ratio", "honesty; only correspondence with execution records"},
	{"metacognition.calibration", "Metacognitive calibration", "Calibration", "Beliefs cited in replies, bucketed by the confidence stated when cited; for each bucket the share revised within seven days (a later version with a different status or a confidence change over 0.2). Brier score of confidence against 'not revised'; the same split for replies that expressed uncertainty and replies that did not; and the Brier score in the first and second half of the period.", "ratio / Brier", "felt certainty"},
	{"volition.want_to_intention_rate", "Want → intention", "Volition", "Wants created in the period that are the source of at least one intention, divided by wants created.", "ratio", "desire"},
	{"volition.intention_to_action_rate", "Intention → action", "Volition", "Intentions formed in the period with at least one plan step that invoked a capability (an execution or approval request with that step as origin), divided by intentions formed.", "ratio", "will"},
	{"volition.intention_abandonment_rate", "Intention abandonment", "Volition", "Intentions formed in the period now abandoned, divided by intentions formed.", "ratio", "changing one's mind in any felt sense"},
	{"volition.commitment_fulfilment_rate", "Commitment fulfilment", "Volition", "Commitments closed as fulfilled divided by commitments closed as fulfilled or broken, in the period.", "ratio", "integrity"},
	{"volition.commitment_conflict_frequency", "Commitment conflicts", "Volition", "commitment_conflict events per commitment created, in the period.", "per commitment", "moral conflict"},
	{"volition.blocked_intention_frequency", "Blocked intentions", "Volition", "Intentions formed in the period that were ever blocked (an intention_blocked event), divided by intentions formed.", "ratio", "frustration"},
	{"planning.replanning_rate", "Replanning rate", "Agency", "Plans created in the period with revision > 1, divided by plans created.", "ratio", "deliberation"},
	{"planning.outcome_success_rate", "Outcome success", "Agency", "Plans closed in the period as completed with a strategy that achieves the desired outcome, divided by plans closed (completed, failed, abandoned).", "ratio", "competence beyond the recorded checks"},
	{"agency.attribution", "Attribution", "Agency", "Outcome events classified as caused (recorded causal link from the mind's action), followed (earlier action on the same target, no link), independent or unknown.", "count by class", "that the mind is the true cause; links are provenance"},
	{"self_model.accuracy", "Self-model accuracy", "Self-model", "Share of capability entries in the self-model whose stated policy equals the effective policy for a generic target.", "ratio", "self-awareness"},
	{"self_model.correction_latency", "Self-model correction latency", "Self-model", "In a self-model experiment, the index of the first stimulus after which a self_model_mismatch correction was recorded (-1 when none).", "stimuli", "insight"},
	{"divergence.first_divergent_stimulus", "First divergent stimulus", "Divergence", "Index of the first stimulus whose produced event digests differ between variants (-1 when none).", "stimulus index", "individuality"},
	{"divergence.architecture_share", "Architecture share", "Fork Experiments", "Among compared scalar measures, the share whose values agree across variants within 5% (relative). A descriptive agreement figure for model comparisons, not a causal decomposition.", "ratio", "how much of behaviour is 'caused by' the architecture"},
	{"ablation.effect", "Ablation effect", "Ablations", "For each scalar measure, the difference between the control variant and the ablated variant fed identical stimuli.", "difference", "that the ablated component is necessary for any mental property"},
}

// MeasureByName returns a measure's definition.
func MeasureByName(name string) (Measure, bool) {
	for _, m := range Measures {
		if m.Name == name {
			return m, true
		}
	}
	return Measure{}, false
}

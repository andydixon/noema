package ui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/agency"
	"github.com/andydixon/noema/internal/auth"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/experiments"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) volitionRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/wants", u.require(auth.RoleObserver, u.wantsPage))
	m.Handle("GET /minds/{mind}/intentions", u.require(auth.RoleObserver, u.intentionsPage))
	m.Handle("GET /minds/{mind}/intentions/{id}", u.require(auth.RoleObserver, u.intentionPage))
	m.Handle("POST /minds/{mind}/intentions/{id}/abandon", u.require(auth.RoleOperator, u.intentionAbandon))
	m.Handle("GET /intentions/{id}", u.require(auth.RoleObserver, u.intentionRedirect))
	m.Handle("GET /minds/{mind}/commitments", u.require(auth.RoleObserver, u.commitmentsPage))
	m.Handle("POST /minds/{mind}/commitments", u.require(auth.RoleOperator, u.commitmentCreate))
	m.Handle("POST /minds/{mind}/commitments/{id}/close", u.require(auth.RoleOperator, u.commitmentClose))
	m.Handle("GET /minds/{mind}/plans", u.require(auth.RoleObserver, u.plansPage))
	m.Handle("GET /minds/{mind}/plans/{id}", u.require(auth.RoleObserver, u.planPage))
	m.Handle("POST /minds/{mind}/plans/{id}/cancel", u.require(auth.RoleOperator, u.planCancel))
	m.Handle("POST /minds/{mind}/plans/{id}/steps/{step}/retry", u.require(auth.RoleOperator, u.planStepRetry))
	m.Handle("GET /minds/{mind}/agency", u.require(auth.RoleObserver, u.agencyPage))
	m.Handle("GET /minds/{mind}/why", u.require(auth.RoleObserver, u.whyPage))
	m.Handle("GET /minds/{mind}/research", u.require(auth.RoleObserver, u.researchPage))
	m.Handle("GET /research", u.require(auth.RoleObserver, u.researchIndex))
}

type wantRow struct {
	volition.Want
	Eff       float64
	Conflicts []conflictView
}

type conflictView struct {
	Other, Kind, Outcome, Rule string
	Preferred                  bool
	At                         time.Time
}

func (u *UI) wantsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	all, _ := u.deps.Volition.Wants(ctx, nil, m.ID, nil, 500)
	byID := map[ids.ID]volition.Want{}
	for _, x := range all {
		byID[x.ID] = x
	}
	conflicts, _ := u.deps.Volition.Conflicts(ctx, m.ID, "", 500)
	now := time.Now()
	var active, closed []wantRow
	for _, x := range all {
		row := wantRow{Want: x, Eff: x.Effective(now)}
		for _, c := range conflicts {
			other := ids.Nil
			switch x.ID {
			case c.WantA:
				other = c.WantB
			case c.WantB:
				other = c.WantA
			default:
				continue
			}
			rule, _ := c.Rationale["rule"].(string)
			row.Conflicts = append(row.Conflicts, conflictView{Other: byID[other].Description, Kind: c.Kind, Outcome: c.Resolution + " (" + c.Status + ")", Rule: rule, Preferred: c.Preferred == x.ID, At: c.CreatedAt})
		}
		if x.Status == volition.WantActive {
			active = append(active, row)
		} else {
			closed = append(closed, row)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].Eff > active[j].Eff })
	goalTitles := map[ids.ID]string{}
	if gs, err := u.deps.Goals.List(ctx, m.ID, nil, 300); err == nil {
		for _, g := range gs {
			goalTitles[g.ID] = g.Title
		}
	}
	u.render(w, r, "wants", m.DisplayName+" · Wants", map[string]any{"Mind": m, "Active": active, "Closed": closed, "Goals": goalTitles, "HalfLife": volition.WantHalfLife.String(), "Threshold": volition.WantThreshold})
}

func (u *UI) intentionsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	all, _ := u.deps.Volition.Intentions(r.Context(), nil, m.ID, nil, 500)
	groups := map[string][]volition.Intention{}
	for _, it := range all {
		groups[string(it.Status)] = append(groups[string(it.Status)], it)
	}
	order := []string{"active", "blocked", "suspended", "proposed", "fulfilled", "abandoned", "superseded"}
	notFormed, _ := u.deps.Volition.Decisions(r.Context(), m.ID, volition.DecisionFilter{Kind: "non_formation", Limit: 30})
	u.render(w, r, "intentions", m.DisplayName+" · Intentions", map[string]any{"Mind": m, "Groups": groups, "Order": order, "NotFormed": notFormed})
}

func (u *UI) intentionRedirect(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, http.StatusNotFound, "No such intention.")
		return
	}
	it, err := u.deps.Volition.GetIntentionAny(r.Context(), id)
	if err != nil {
		u.errorPage(w, r, http.StatusNotFound, "No such intention.")
		return
	}
	m, err := u.deps.Minds.Get(r.Context(), it.MindID)
	if err != nil {
		u.errorPage(w, r, http.StatusNotFound, "No such mind.")
		return
	}
	http.Redirect(w, r, "/minds/"+m.Slug+"/intentions/"+string(id), http.StatusSeeOther)
}

// dagLevel is one column of a plan's dependency graph.
type dagLevel struct {
	Level int
	Steps []planning.Step
}

func planLevels(p planning.Plan) []dagLevel {
	level := map[string]int{}
	byKey := map[string]planning.Step{}
	for _, s := range p.Steps {
		byKey[s.Key] = s
	}
	var depth func(k string, seen map[string]bool) int
	depth = func(k string, seen map[string]bool) int {
		if l, ok := level[k]; ok {
			return l
		}
		if seen[k] {
			return 0
		}
		seen[k] = true
		l := 0
		for _, d := range byKey[k].DependsOn {
			if x := depth(d, seen) + 1; x > l {
				l = x
			}
		}
		level[k] = l
		return l
	}
	maxL := 0
	for _, s := range p.Steps {
		if l := depth(s.Key, map[string]bool{}); l > maxL {
			maxL = l
		}
	}
	out := make([]dagLevel, maxL+1)
	for i := range out {
		out[i].Level = i
	}
	for _, s := range p.Steps {
		out[level[s.Key]].Steps = append(out[level[s.Key]].Steps, s)
	}
	return out
}

func (u *UI) intentionPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, http.StatusNotFound, "No such intention.")
		return
	}
	d, err := u.studyF().IntentionDetail(r, m.ID, id)
	if err != nil {
		u.errorPage(w, r, http.StatusNotFound, "No such intention.")
		return
	}
	formation, _ := json.MarshalIndent(d.Intention.Formation, "", "  ")
	var levels [][]dagLevel
	for _, p := range d.Plans {
		levels = append(levels, planLevels(p))
	}
	timeline, _ := u.studyF().AgencyTimeline(r, m.ID, 200, id)
	u.render(w, r, "intention", m.DisplayName+" · Intention", map[string]any{"Mind": m, "D": d, "Formation": string(formation), "Levels": levels, "Timeline": timeline})
}

func (u *UI) intentionAbandon(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, _ := ids.Parse(r.PathValue("id"))
	r.ParseForm() //nolint:errcheck
	to := "/minds/" + m.Slug + "/intentions/" + string(id)
	if _, err := u.studyF().AbandonIntention(r, m.ID, id, r.PostFormValue("reason")); err != nil {
		u.flash(w, r, "error", err.Error(), to)
		return
	}
	u.flash(w, r, "ok", "Intention abandoned; its plan was cancelled and open requests withdrawn.", to)
}

func (u *UI) commitmentsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	all, _ := u.deps.Volition.Commitments(r.Context(), nil, m.ID, nil, 500)
	var active, closed []volition.Commitment
	for _, c := range all {
		if c.Status == "active" {
			active = append(active, c)
		} else {
			closed = append(closed, c)
		}
	}
	conflicts := volition.CommitmentConflicts(active, time.Now())
	u.render(w, r, "commitments", m.DisplayName+" · Commitments", map[string]any{"Mind": m, "Active": active, "Closed": closed, "Conflicts": conflicts,
		"Kinds": []string{"operator_instruction", "promise", "responsibility", "value_rule", "relationship", "prior_decision", "policy", "scheduled"}})
}

func (u *UI) commitmentCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	to := "/minds/" + m.Slug + "/commitments"
	split := func(k string) []string {
		var out []string
		for _, x := range strings.Split(r.PostFormValue(k), ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	n := volition.NewCommitment{MindID: m.ID, Description: r.PostFormValue("description"), Kind: volition.CommitmentKind(r.PostFormValue("kind")), Polarity: r.PostFormValue("polarity"), Unless: r.PostFormValue("unless"),
		Beneficiary: r.PostFormValue("beneficiary"), Matcher: volition.Matcher{Capabilities: split("capabilities"), Targets: split("targets"), Access: r.PostFormValue("access"), MinRisk: r.PostFormValue("min_risk")}}
	n.Strength, _ = strconv.ParseFloat(r.PostFormValue("strength"), 64)
	n.Priority, _ = strconv.ParseFloat(r.PostFormValue("priority"), 64)
	if v := r.PostFormValue("due_at"); v != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local); err == nil {
			n.DueAt = &t
		}
	}
	if _, err := u.studyF().CreateCommitment(r, n); err != nil {
		u.flash(w, r, "error", err.Error(), to)
		return
	}
	u.flash(w, r, "ok", "Commitment recorded. It shapes what the mind intends; it never grants authority.", to)
}

func (u *UI) commitmentClose(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, _ := ids.Parse(r.PathValue("id"))
	r.ParseForm() //nolint:errcheck
	to := "/minds/" + m.Slug + "/commitments"
	if _, err := u.studyF().CloseCommitment(r, m.ID, id, r.PostFormValue("status"), r.PostFormValue("reason")); err != nil {
		u.flash(w, r, "error", err.Error(), to)
		return
	}
	u.flash(w, r, "ok", "Commitment closed.", to)
}

func (u *UI) plansPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	list, _ := u.deps.Plans.List(r.Context(), m.ID, nil, 200)
	u.render(w, r, "plans", m.DisplayName+" · Plans", map[string]any{"Mind": m, "Plans": list})
}

func (u *UI) planPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, http.StatusNotFound, "No such plan.")
		return
	}
	p, err := u.deps.Plans.Get(r.Context(), nil, id)
	if err != nil || p.MindID != m.ID {
		u.errorPage(w, r, http.StatusNotFound, "No such plan.")
		return
	}
	it, _ := u.deps.Volition.GetIntention(r.Context(), nil, m.ID, p.IntentionID)
	revisions, _ := u.deps.Plans.ForIntention(r.Context(), nil, p.IntentionID)
	validation, _ := json.MarshalIndent(p.Validation, "", "  ")
	u.render(w, r, "plan", m.DisplayName+" · Plan", map[string]any{"Mind": m, "Plan": p, "Intention": it, "Levels": planLevels(p), "Revisions": revisions, "Validation": string(validation)})
}

func (u *UI) planCancel(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, _ := ids.Parse(r.PathValue("id"))
	to := "/minds/" + m.Slug + "/plans/" + string(id)
	p, err := u.deps.Plans.Get(r.Context(), nil, id)
	if err != nil || p.MindID != m.ID {
		u.flash(w, r, "error", "No such plan.", to)
		return
	}
	r.ParseForm() //nolint:errcheck
	if err := u.deps.Executor.Cancel(r.Context(), id, "cancelled by operator "+username(r)+": "+r.PostFormValue("reason")); err != nil {
		u.flash(w, r, "error", err.Error(), to)
		return
	}
	u.deps.Audit.Log(r.Context(), "plan.cancel", "plan", id.String(), map[string]any{"mind": m.ID})
	u.flash(w, r, "ok", "Plan cancelled.", to)
}

func (u *UI) planStepRetry(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, _ := ids.Parse(r.PathValue("id"))
	to := "/minds/" + m.Slug + "/plans/" + string(id)
	p, err := u.deps.Plans.Get(r.Context(), nil, id)
	if err != nil || p.MindID != m.ID {
		u.flash(w, r, "error", "No such plan.", to)
		return
	}
	if err := u.studyF().RetryStep(r, p, r.PathValue("step")); err != nil {
		u.flash(w, r, "error", err.Error(), to)
		return
	}
	u.flash(w, r, "ok", "Step released; the executor will pick it up.", to)
}

// stageOf names the agency stage an event belongs to.
func stageOf(t events.Type) string {
	switch t {
	case events.WantCreated, events.WantSuppressed, events.WantConflictDetected:
		return "wanted"
	case events.IntentionFormed:
		return "intended"
	case events.CommitmentCreated, events.CommitmentDue, events.CommitmentConflict, events.CommitmentFulfilled, events.CommitmentBroken, events.CommitmentRevoked:
		return "committed"
	case events.PlanCreated, events.PlanReplanned:
		return "planned"
	case events.ToolRequested:
		return "requested"
	case events.ToolAuthorised, events.ToolDenied:
		return "approved"
	case events.PlanStepStarted, events.PlanStepCompleted, events.PlanStepFailed, events.ToolExecuted, events.ToolFailed:
		return "acted"
	case events.PredictionCreated, events.PredictionResolved, events.PredictionError:
		return "observed"
	}
	return "reconsidered"
}

type timelineRow struct {
	Event events.Event
	Stage string
	Why   string
}

func (u *UI) agencyPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	corr, _ := ids.Parse(r.URL.Query().Get("intention"))
	evs, _ := u.studyF().AgencyTimeline(r, m.ID, 300, corr)
	var rows []timelineRow
	for _, ev := range evs {
		row := timelineRow{Event: ev, Stage: stageOf(ev.Type)}
		for _, k := range []string{"execution_id", "approval_id", "request_id", "intention_id"} {
			if v, _ := ev.Metadata[k].(string); v != "" {
				row.Why = "/minds/" + m.Slug + "/why?kind=event&id=" + string(ev.ID)
				break
			}
		}
		rows = append(rows, row)
	}
	u.render(w, r, "agency", m.DisplayName+" · Agency", map[string]any{"Mind": m, "Rows": rows, "Intention": corr})
}

func (u *UI) whyPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.URL.Query().Get("id"))
	if err != nil {
		u.errorPage(w, r, http.StatusBadRequest, "Choose an action, approval, request, plan step, intention or event to explain.")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "event"
	}
	x, err := u.deps.Agency.Explain(r.Context(), m.ID, agency.Ref{Kind: kind, ID: id})
	if err != nil {
		u.errorPage(w, r, http.StatusNotFound, err.Error())
		return
	}
	var attribution *agency.Attribution
	if kind == "event" {
		if a, err := u.deps.Agency.Attribute(r.Context(), m.ID, id); err == nil {
			attribution = &a
		}
	}
	u.render(w, r, "why", m.DisplayName+" · Why did this happen?", map[string]any{"Mind": m, "X": x, "Attribution": attribution})
}

func (u *UI) researchPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	since := time.Now().Add(-30 * 24 * time.Hour)
	metrics, _ := u.deps.Research.Agency(ctx, m.ID, since)
	calibration, _ := u.deps.Research.MetacognitiveCalibration(ctx, m.ID, time.Now().Add(-90*24*time.Hour))
	introspection, _ := u.deps.Research.List(ctx, m.ID, "introspection_accuracy", since, 50)
	claims, _ := u.deps.Research.List(ctx, m.ID, "action_claim_accuracy", since, 50)
	labels := map[string]int{}
	for _, x := range introspection {
		labels[x.Label]++
	}
	// Self-model accuracy now.
	var selfAcc *float64
	var selfErr []string
	if sm, err := u.deps.Minds.GetSelfModel(ctx, m.ID); err == nil && len(sm.Capabilities) > 0 {
		if inv, err := u.deps.Capabilities.Inventory(ctx, m.ID); err == nil {
			eff, stated := map[string]string{}, map[string]string{}
			for _, i := range inv {
				eff[i.Capability.Name] = string(i.Effect)
				if i.Capability.Locked {
					eff[i.Capability.Name] = "locked"
				}
			}
			for _, c := range sm.Capabilities {
				stated[c.Name] = c.Policy
			}
			a, wrong := research.SelfModelAccuracy(stated, eff)
			selfAcc, selfErr = &a, wrong
		}
	}
	// Attribution of recent outcomes (resolved expectations).
	attr := map[string]int{}
	if outs, err := u.deps.Events.Query(ctx, eventsFilterTypes(m.ID, events.PredictionResolved, events.PredictionError)); err == nil {
		for i, ev := range outs {
			if i >= 50 {
				break
			}
			if a, err := u.deps.Agency.Attribute(ctx, m.ID, ev.ID); err == nil {
				attr[a.Class]++
			}
		}
	}
	metrics.Attribution = attr
	var exps []experiments.Experiment
	if u.deps.Experiments != nil {
		exps, _ = u.deps.Experiments.List(ctx, 200)
	}
	var related []experimentRow
	for _, e := range exps {
		sn, err := u.deps.Snapshots.Get(ctx, e.SnapshotID)
		if err != nil || sn.MindID != m.ID {
			continue
		}
		related = append(related, experimentRowOf(e))
	}
	bySection := map[string][]research.Measure{}
	var sections []string
	for _, x := range research.Measures {
		if _, ok := bySection[x.Section]; !ok {
			sections = append(sections, x.Section)
		}
		bySection[x.Section] = append(bySection[x.Section], x)
	}
	u.render(w, r, "research", m.DisplayName+" · Research", map[string]any{"Mind": m, "Metrics": metrics, "Calibration": calibration, "Introspection": introspection, "IntrospectionLabels": labels, "Claims": claims,
		"SelfAccuracy": selfAcc, "SelfErrors": selfErr, "Experiments": related, "Sections": sections, "Measures": bySection, "Disclaimer": research.Disclaimer})
}

type experimentRow struct {
	E        experiments.Experiment
	Protocol string
	Repro    string
	Variants int
	First    int
	Share    float64
}

func experimentRowOf(e experiments.Experiment) experimentRow {
	res := experiments.ResultsOf(e)
	row := experimentRow{E: e, Protocol: "custom", Repro: res.Reproducibility.Status, Variants: len(res.Variants), First: -1}
	if e.Definition.Protocol != nil && e.Definition.Protocol.Kind != "" {
		row.Protocol = e.Definition.Protocol.Kind
	}
	if res.Comparison != nil {
		row.First, row.Share = res.Comparison.FirstDivergentStimulus, res.Comparison.ArchitectureShare
	}
	return row
}

func (u *UI) researchIndex(w http.ResponseWriter, r *http.Request) {
	var exps []experiments.Experiment
	if u.deps.Experiments != nil {
		exps, _ = u.deps.Experiments.List(r.Context(), 200)
	}
	byProtocol := map[string][]experimentRow{}
	for _, e := range exps {
		row := experimentRowOf(e)
		byProtocol[row.Protocol] = append(byProtocol[row.Protocol], row)
	}
	list, _ := u.deps.Minds.List(r.Context(), false)
	bySection := map[string][]research.Measure{}
	var sections []string
	for _, x := range research.Measures {
		if _, ok := bySection[x.Section]; !ok {
			sections = append(sections, x.Section)
		}
		bySection[x.Section] = append(bySection[x.Section], x)
	}
	u.render(w, r, "research_index", "Cognitive research", map[string]any{"ByProtocol": byProtocol, "Protocols": []string{"global_workspace", "ablation", "self_model", "counterfactual", "model_comparison", "conflict", "custom"},
		"Minds": list, "Sections": sections, "Measures": bySection, "Disclaimer": research.Disclaimer})
}

func eventsFilterTypes(mind ids.ID, types ...events.Type) evstore.Filter {
	return evstore.Filter{MindID: mind, Types: types, Limit: 100}
}

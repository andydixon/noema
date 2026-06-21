package ui

import (
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) beliefRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/beliefs", u.require(auth.RoleObserver, u.beliefsPage))
	m.Handle("POST /minds/{mind}/beliefs", u.require(auth.RoleOperator, u.beliefAssert))
	m.Handle("GET /minds/{mind}/beliefs/{id}", u.require(auth.RoleObserver, u.beliefDetail))
	m.Handle("POST /minds/{mind}/beliefs/{id}/evidence", u.require(auth.RoleOperator, u.beliefEvidence))
	m.Handle("POST /minds/{mind}/beliefs/{id}/alternatives", u.require(auth.RoleOperator, u.beliefAlternative))
	m.Handle("POST /minds/{mind}/beliefs/{id}/retract", u.require(auth.RoleOperator, u.beliefRetract))
	m.Handle("POST /minds/{mind}/corrections", u.require(auth.RoleOperator, u.correctionCreate))
}

func (u *UI) beliefsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := api.ParseBeliefFilter(q)
	f.Limit = 200
	list, err := u.deps.Beliefs.List(r.Context(), m.ID, f)
	if err != nil {
		u.errorPage(w, r, 500, "Could not list beliefs.")
		return
	}
	flat := map[string]string{}
	for k := range q {
		flat[k] = q.Get(k)
	}
	stats, _ := u.deps.Beliefs.Stats(r.Context(), m.ID)
	corr, _ := u.deps.Beliefs.Corrections(r.Context(), m.ID, "")
	u.render(w, r, "beliefs", m.DisplayName+" · Beliefs", map[string]any{"Mind": m, "Beliefs": list, "Q": flat, "Stats": stats, "Corrections": corr})
}

func (u *UI) beliefAssert(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	weight, _ := strconv.ParseFloat(r.PostFormValue("weight"), 64)
	res, err := u.deps.Beliefs.Create(r.Context(), u.deps.DB, u.emitF(), beliefs.New{MindID: m.ID, Proposition: r.PostFormValue("proposition"), Subject: r.PostFormValue("subject"),
		Epistemic: concepts.Epistemic(r.PostFormValue("epistemic")), Source: "operator",
		Evidence: []beliefs.EvidenceInput{{Direction: "for", Kind: "operator", Source: "operator:" + username(r), Weight: weight, Credibility: events.TrustOperator.Credibility(), Note: "asserted by operator " + username(r)}}})
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/beliefs")
		return
	}
	u.deps.Audit.Log(r.Context(), "belief.create", "belief", res.Belief.ID.String(), map[string]any{"mind": m.Slug, "proposition": res.Belief.Proposition})
	u.flash(w, r, "ok", "Belief recorded with confidence "+strconv.FormatFloat(res.Belief.Confidence, 'f', 2, 64)+".", "/minds/"+m.Slug+"/beliefs/"+res.Belief.ID.String())
}

func (u *UI) beliefDetail(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such belief.")
		return
	}
	d, err := u.belF(r, m.ID, id)
	if err != nil {
		u.errorPage(w, r, 404, "No such belief.")
		return
	}
	u.render(w, r, "belief_detail", "Belief", map[string]any{"Mind": m, "Detail": d})
}

func (u *UI) beliefEvidence(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such belief.")
		return
	}
	r.ParseForm() //nolint:errcheck
	weight, _ := strconv.ParseFloat(r.PostFormValue("weight"), 64)
	note := r.PostFormValue("note")
	if _, err := u.deps.Beliefs.AddEvidence(r.Context(), u.deps.DB, u.emitF(), m.ID, id, beliefs.EvidenceInput{Direction: r.PostFormValue("direction"), Kind: "operator", Source: "operator:" + username(r), Weight: weight, Credibility: events.TrustOperator.Credibility(), Note: note}, "operator evidence: "+note, ids.Nil, ids.Nil); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/beliefs/"+id.String())
		return
	}
	u.deps.Audit.Log(r.Context(), "belief.evidence", "belief", id.String(), map[string]any{"direction": r.PostFormValue("direction"), "note": note})
	u.flash(w, r, "ok", "Evidence added and confidence re-evaluated.", "/minds/"+m.Slug+"/beliefs/"+id.String())
}

func (u *UI) beliefAlternative(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such belief.")
		return
	}
	if _, err := u.deps.Beliefs.AddAlternative(r.Context(), m.ID, id, r.PostFormValue("proposition"), "operator"); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/beliefs/"+id.String())
		return
	}
	u.flash(w, r, "ok", "Alternative recorded.", "/minds/"+m.Slug+"/beliefs/"+id.String())
}

func (u *UI) beliefRetract(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such belief.")
		return
	}
	reason := r.PostFormValue("reason")
	dni := r.PostFormValue("do_not_infer") == "1"
	if _, err := u.deps.Beliefs.Retract(r.Context(), u.emitF(), m.ID, id, reason, username(r), dni); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/beliefs/"+id.String())
		return
	}
	u.deps.Audit.Log(r.Context(), "belief.retract", "belief", id.String(), map[string]any{"reason": reason, "do_not_infer": dni})
	u.flash(w, r, "ok", "Belief marked wrong. Its history is preserved.", "/minds/"+m.Slug+"/beliefs/"+id.String())
}

func (u *UI) correctionCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	stmt, reason := r.PostFormValue("statement"), r.PostFormValue("reason")
	evs, err := u.deps.Events.Append(r.Context(), events.New{MindID: m.ID, Type: events.OperatorAction, Source: "operator", Summary: "Operator " + username(r) + " forbade inference: " + stmt,
		Salience: 0.6, Confidence: 1, Metadata: map[string]any{"action": "correction.do_not_infer", "actor": username(r)}, Payload: map[string]any{"statement": stmt, "reason": reason}})
	if err != nil {
		u.flash(w, r, "error", "Could not record.", "/minds/"+m.Slug+"/beliefs")
		return
	}
	if _, err := u.deps.Beliefs.AddCorrection(r.Context(), u.deps.DB, m.ID, beliefs.Correction{Kind: "do_not_infer", Statement: stmt, Reason: reason, CreatedBy: username(r), EventID: evs[0].ID}); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/beliefs")
		return
	}
	u.deps.Audit.Log(r.Context(), "correction.create", "mind", m.ID.String(), map[string]any{"kind": "do_not_infer", "statement": stmt})
	u.flash(w, r, "ok", "The mind will not infer that again.", "/minds/"+m.Slug+"/beliefs")
}

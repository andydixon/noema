package ui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) capabilityRoutes(m *http.ServeMux) {
	m.Handle("GET /policies", u.require(auth.RoleObserver, u.policiesPage))
	m.Handle("POST /policies", u.require(auth.RoleAdministrator, u.policyCreate))
	m.Handle("POST /policies/{id}/delete", u.require(auth.RoleAdministrator, u.policyDelete))
	m.Handle("POST /policies/grants/{id}/revoke", u.require(auth.RoleOperator, u.grantRevoke))
	m.Handle("GET /approvals", u.require(auth.RoleObserver, u.approvalsPage))
	m.Handle("POST /approvals/{id}/decide", u.require(auth.RoleOperator, u.approvalDecide))
}

func (u *UI) mindNames(r *http.Request) (map[ids.ID]string, []any) {
	list, _ := u.deps.Minds.List(r.Context(), true)
	names := map[ids.ID]string{}
	var live []any
	for _, m := range list {
		names[m.ID] = m.DisplayName
		if m.Status == "active" {
			live = append(live, m)
		}
	}
	return names, live
}

func (u *UI) policiesPage(w http.ResponseWriter, r *http.Request) {
	names, _ := u.mindNames(r)
	mindsList, _ := u.deps.Minds.List(r.Context(), false)
	caps := u.deps.Capabilities.Registry.List()
	matrix := map[ids.ID]map[string]capabilities.InventoryItem{}
	for _, m := range mindsList {
		inv, _ := u.deps.Capabilities.Inventory(r.Context(), m.ID)
		row := map[string]capabilities.InventoryItem{}
		for _, i := range inv {
			row[i.Capability.Name] = i
		}
		matrix[m.ID] = row
	}
	pols, _ := u.deps.Capabilities.Policies(r.Context(), ids.Nil)
	grants, _ := u.deps.Capabilities.Grants(r.Context(), ids.Nil)
	u.render(w, r, "policies", "Capabilities and policies", map[string]any{"Capabilities": caps, "Minds": mindsList, "MindNames": names, "Matrix": matrix, "Policies": pols, "Grants": grants})
}

func (u *UI) policyCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	p := capabilities.Policy{Capability: r.PostFormValue("capability"), TargetPattern: r.PostFormValue("target_pattern"), Effect: capabilities.Effect(r.PostFormValue("effect")), Reason: r.PostFormValue("reason")}
	if v := r.PostFormValue("mind_id"); v != "" {
		p.MindID, _ = ids.Parse(v)
	}
	if hf, err := strconv.Atoi(r.PostFormValue("hours_from")); err == nil {
		if ht, err := strconv.Atoi(r.PostFormValue("hours_to")); err == nil {
			p.HoursFrom, p.HoursTo = &hf, &ht
		}
	}
	if _, err := u.studyF().CreatePolicy(r, p); err != nil {
		u.flash(w, r, "error", err.Error(), "/policies")
		return
	}
	u.flash(w, r, "ok", "Rule added.", "/policies")
}

func (u *UI) policyDelete(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil || u.deps.Capabilities.DeletePolicy(r.Context(), id) != nil {
		u.flash(w, r, "error", "Rule not found.", "/policies")
		return
	}
	u.deps.Audit.Log(r.Context(), "policy.delete", "policy", id.String(), nil)
	u.flash(w, r, "ok", "Rule deleted.", "/policies")
}

func (u *UI) grantRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil || u.deps.Capabilities.RevokeGrant(r.Context(), id) != nil {
		u.flash(w, r, "error", "Grant not found.", "/policies")
		return
	}
	u.deps.Audit.Log(r.Context(), "grant.revoke", "grant", id.String(), nil)
	u.flash(w, r, "ok", "Session grant revoked.", "/policies")
}

func (u *UI) approvalsPage(w http.ResponseWriter, r *http.Request) {
	names, _ := u.mindNames(r)
	pending, _ := u.deps.Capabilities.Approvals(r.Context(), ids.Nil, "pending", 100)
	all, _ := u.deps.Capabilities.Approvals(r.Context(), ids.Nil, "", 60)
	var decided []capabilities.Approval
	for _, a := range all {
		if a.Status != "pending" {
			decided = append(decided, a)
		}
	}
	execs, _ := u.deps.Capabilities.Executions(r.Context(), ids.Nil, 30)
	u.render(w, r, "approvals", "Approval centre", map[string]any{"Pending": pending, "Decided": decided, "Executions": execs, "MindNames": names})
}

func (u *UI) approvalDecide(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.flash(w, r, "error", "Bad request id.", "/approvals")
		return
	}
	r.ParseForm() //nolint:errcheck
	var params map[string]any
	if raw := strings.TrimSpace(r.PostFormValue("params")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			u.flash(w, r, "error", "Parameters are not a JSON object.", "/approvals")
			return
		}
		// Only treat as a modification when it differs from the original.
		if a, err := u.deps.Capabilities.GetApproval(r.Context(), id); err == nil {
			orig, _ := json.Marshal(a.Params)
			now, _ := json.Marshal(params)
			if string(orig) == string(now) {
				params = nil
			}
		}
	}
	decision := r.PostFormValue("decision")
	if _, err := u.studyF().Decide(r, id, decision, r.PostFormValue("note"), params); err != nil {
		u.flash(w, r, "error", err.Error(), "/approvals")
		return
	}
	u.flash(w, r, "ok", "Decision recorded: "+decision+".", "/approvals")
}

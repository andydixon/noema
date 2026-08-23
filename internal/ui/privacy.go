package ui

import (
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) privacyRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/redactions", u.require(auth.RoleOperator, u.redactionsPage))
	m.Handle("POST /minds/{mind}/redactions", u.require(auth.RoleAdministrator, u.redact))
}

func (u *UI) redactionsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	list, _ := u.deps.Privacy.List(r.Context(), m.ID, 200)
	u.render(w, r, "redactions", m.DisplayName+" · Redactions", map[string]any{"Mind": m, "Redactions": list, "Kind": r.URL.Query().Get("kind"), "ID": r.URL.Query().Get("id")})
}

func (u *UI) redact(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PostFormValue("id"))
	if err != nil {
		u.flash(w, r, "error", "Bad id.", "/minds/"+m.Slug+"/redactions")
		return
	}
	cascade, _ := strconv.ParseBool(r.PostFormValue("cascade"))
	p, c, err := u.studyF().Redact(r, m.ID, api.RedactionInput{Kind: r.PostFormValue("kind"), ID: id, Reason: r.PostFormValue("reason"), Policy: r.PostFormValue("policy"), Cascade: cascade}, username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/redactions")
		return
	}
	u.flash(w, r, "ok", "Redacted "+string(p.Kind)+" "+p.TargetID.String()[len(p.TargetID.String())-8:]+" and "+itoa(len(c))+" dependant row(s). The content is irrecoverable; hash "+p.ContentHash[:23]+"… attests what was removed.", "/minds/"+m.Slug+"/redactions")
}

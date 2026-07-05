package ui

import (
	"encoding/json"
	"net/http"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) conversationRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/conversations", u.require(auth.RoleObserver, u.conversations))
	m.Handle("POST /minds/{mind}/conversations", u.require(auth.RoleOperator, u.conversationCreate))
	m.Handle("GET /minds/{mind}/conversations/{id}", u.require(auth.RoleObserver, u.conversationPage))
	m.Handle("POST /minds/{mind}/conversations/{id}/messages", u.require(auth.RoleOperator, u.conversationPost))
}

var suggestions = []string{"What are you thinking about?", "What do you believe about atlas?", "Why do you believe that?", "What are you uncertain about?", "Which of your beliefs contradict each other?", "What did you learn this week?", "What have you changed your mind about?"}

func (u *UI) conversations(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	list, _ := u.deps.Conversations.List(r.Context(), m.ID, 100)
	u.render(w, r, "conversations", m.DisplayName+" · Conversations", map[string]any{"Mind": m, "Conversations": list})
}

func (u *UI) conversationCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	c, err := u.deps.Conversations.Create(r.Context(), m.ID, r.PostFormValue("title"), "web", r.PostFormValue("person"), r.PostFormValue("project"))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/conversations")
		return
	}
	http.Redirect(w, r, "/minds/"+m.Slug+"/conversations/"+c.ID.String(), http.StatusSeeOther)
}

func (u *UI) conversationPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such conversation.")
		return
	}
	c, err := u.deps.Conversations.Get(r.Context(), m.ID, id)
	if err != nil {
		u.errorPage(w, r, 404, "No such conversation.")
		return
	}
	msgs, _ := u.deps.Conversations.Messages(r.Context(), m.ID, id, 200)
	u.render(w, r, "conversation", c.Title, map[string]any{"Mind": m, "Conversation": c, "Messages": msgs, "Suggestions": suggestions, "LLM": u.deps.LLM.Available(r.Context(), llm.RoleRespond)})
}

func (u *UI) conversationPost(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such conversation.")
		return
	}
	c, err := u.deps.Conversations.Get(r.Context(), m.ID, id)
	if err != nil {
		u.errorPage(w, r, 404, "No such conversation.")
		return
	}
	if _, err := u.postF(r, m.ID, c, r.PostFormValue("text"), username(r)); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/conversations/"+id.String())
		return
	}
	http.Redirect(w, r, "/minds/"+m.Slug+"/conversations/"+id.String(), http.StatusSeeOther) // #nosec G710 -- path built from a validated slug and a parsed id
}

func prettyRaw(raw json.RawMessage) string { return pretty(raw) }

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) conversationRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/conversations", require(auth.RoleObserver, s.conversationsList))
	m.Handle("POST /api/v1/minds/{mind}/conversations", require(auth.RoleOperator, s.conversationsCreate))
	m.Handle("GET /api/v1/minds/{mind}/conversations/{id}", require(auth.RoleObserver, s.conversationGet))
	m.Handle("POST /api/v1/minds/{mind}/conversations/{id}/messages", require(auth.RoleOperator, s.messagePost))
	m.Handle("POST /api/v1/minds/{mind}/ask", require(auth.RoleOperator, s.ask))
}

func convErr(err error) error {
	if errors.Is(err, conversation.ErrInvalid) {
		return ValidationError(err.Error())
	}
	return err
}

func (s *Server) conversationsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Conversations.List(r.Context(), m.ID, 100)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []conversation.Conversation{}
	}
	writeJSON(w, http.StatusOK, page[conversation.Conversation]{Items: list})
}

type conversationIn struct {
	Title   string `json:"title"`
	Channel string `json:"channel"`
	Person  string `json:"person"`
	Project string `json:"project"`
}

func (s *Server) conversationsCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in conversationIn
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, r, err)
			return
		}
	}
	if in.Person == "" {
		in.Person = actor(r)
	}
	c, err := s.deps.Conversations.Create(r.Context(), m.ID, in.Title, firstNonEmpty(in.Channel, "api"), in.Person, in.Project)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// ConversationView is a conversation with its messages.
type ConversationView struct {
	Conversation conversation.Conversation `json:"conversation"`
	Messages     []conversation.Message    `json:"messages"`
}

func (s *Server) conversationGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	c, err := s.deps.Conversations.Get(r.Context(), m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	msgs, _ := s.deps.Conversations.Messages(r.Context(), m.ID, id, 200)
	if msgs == nil {
		msgs = []conversation.Message{}
	}
	writeJSON(w, http.StatusOK, ConversationView{Conversation: c, Messages: msgs})
}

// PostMessage records a user message as an observation event so the cognitive
// cycle perceives it. Shared with the UI. It returns the stored message.
func (s *Server) PostMessage(r *http.Request, mindID ids.ID, conv conversation.Conversation, text, who string) (conversation.Message, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 20000 {
		return conversation.Message{}, ValidationError("message required (max 20000 chars)")
	}
	trust := events.TrustUser
	if p, ok := auth.PrincipalFrom(r.Context()); ok && p.User.Role.AtLeast(auth.RoleAdministrator) {
		trust = events.TrustOperator
	}
	msg, err := s.deps.Conversations.AddMessage(r.Context(), s.deps.DB, conversation.Message{ConversationID: conv.ID, MindID: mindID, Role: "user", Actor: who, Content: text})
	if err != nil {
		return conversation.Message{}, convErr(err)
	}
	summary := text
	if len(summary) > 160 {
		summary = summary[:157] + "..."
	}
	evs, err := s.deps.Events.Append(r.Context(), events.New{MindID: mindID, Type: events.Observation, Source: "conversation", Summary: who + ": " + summary, Salience: 0.8, Confidence: 1,
		Metadata: map[string]any{"conversation_id": string(conv.ID), "message_id": string(msg.ID), "trust": trust, "actor": who, "channel": conv.Channel, "person": conv.Person, "project": conv.Project},
		Payload:  events.ObservationPayload{Text: text, ContentType: "text/plain", Source: "conversation", Trust: trust, Method: "chat", Reference: string(msg.ID), Channel: string(conv.ID), Actor: who}})
	if err != nil {
		return msg, err
	}
	s.deps.Conversations.SetEventID(r.Context(), s.deps.DB, msg.ID, evs[0].ID) //nolint:errcheck
	msg.EventID = evs[0].ID
	return msg, nil
}

// WaitReply polls for the mind's reply to a message for up to d.
func (s *Server) WaitReply(r *http.Request, mindID, msgID ids.ID, d time.Duration) (conversation.Message, bool) {
	deadline := time.Now().Add(d)
	for {
		if reply, err := s.deps.Conversations.Reply(r.Context(), mindID, msgID); err == nil {
			return reply, true
		} else if !errors.Is(err, storage.ErrNotFound) {
			return conversation.Message{}, false
		}
		if time.Now().After(deadline) || r.Context().Err() != nil {
			return conversation.Message{}, false
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.Context().Done():
			return conversation.Message{}, false
		}
	}
}

func (s *Server) messagePost(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	conv, err := s.deps.Conversations.Get(r.Context(), m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in struct {
		Text string `json:"text"`
		Wait int    `json:"wait_seconds"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	msg, err := s.PostMessage(r, m.ID, conv, in.Text, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	out := map[string]any{"message": msg}
	if in.Wait > 0 {
		if in.Wait > 120 {
			in.Wait = 120
		}
		if reply, ok := s.WaitReply(r, m.ID, msg.ID, time.Duration(in.Wait)*time.Second); ok {
			out["reply"] = reply
		} else {
			out["reply"] = nil
			out["note"] = "no reply within the wait window; the mind may be paused, busy or over budget"
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// ask is the one-shot conversational endpoint used by noemactl ask: it posts
// into (or creates) the caller's CLI conversation and waits for the reply.
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Text           string `json:"text"`
		ConversationID ids.ID `json:"conversation_id"`
		Wait           int    `json:"wait_seconds"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	var conv conversation.Conversation
	var err error
	if !in.ConversationID.IsZero() {
		conv, err = s.deps.Conversations.Get(r.Context(), m.ID, in.ConversationID)
	} else {
		list, _ := s.deps.Conversations.List(r.Context(), m.ID, 50)
		for _, c := range list {
			if c.Channel == "cli" && c.Person == actor(r) {
				conv = c
				break
			}
		}
		if conv.ID.IsZero() {
			conv, err = s.deps.Conversations.Create(r.Context(), m.ID, "", "cli", actor(r), "")
		}
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	msg, err := s.PostMessage(r, m.ID, conv, in.Text, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Wait <= 0 {
		in.Wait = 30
	}
	if in.Wait > 120 {
		in.Wait = 120
	}
	out := map[string]any{"conversation_id": conv.ID, "message": msg}
	if reply, ok := s.WaitReply(r, m.ID, msg.ID, time.Duration(in.Wait)*time.Second); ok {
		out["reply"] = reply
	} else {
		out["note"] = "no reply within " + strconv.Itoa(in.Wait) + "s"
	}
	writeJSON(w, http.StatusOK, out)
}

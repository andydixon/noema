package ui

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) memoryRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/memory", u.require(auth.RoleObserver, u.memoryExplorer))
	m.Handle("POST /minds/{mind}/memory", u.require(auth.RoleOperator, u.memoryAdd))
	m.Handle("GET /minds/{mind}/memory/{id}", u.require(auth.RoleObserver, u.memoryDetail))
	m.Handle("POST /minds/{mind}/memory/{id}/retract", u.require(auth.RoleOperator, u.memoryRetract))
	m.Handle("POST /minds/{mind}/memory/{id}/relate", u.require(auth.RoleOperator, u.memoryRelate))
}

func (u *UI) memoryExplorer(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := api.ParseMemoryFilter(q)
	f.Limit = 100
	flat := map[string]string{}
	for k := range q {
		flat[k] = q.Get(k)
	}
	stats, _ := u.deps.Memory.Stats(r.Context(), m.ID)
	data := map[string]any{"Mind": m, "Q": flat, "Stats": stats, "Semantic": u.deps.Memory.SemanticEnabled()}
	if f.Search != "" {
		hits, err := u.deps.Memory.Retrieve(r.Context(), m.ID, memory.Query{Text: f.Search, Kinds: f.Kinds, Limit: 30, Semantic: true, Strict: q.Get("strict") == "1"})
		if err != nil {
			u.errorPage(w, r, 500, "Search failed.")
			return
		}
		data["Hits"] = hits
	} else {
		list, err := u.deps.Memory.List(r.Context(), m.ID, f)
		if err != nil {
			u.errorPage(w, r, 500, "Could not list memories.")
			return
		}
		data["Memories"] = list
		if len(list) == 100 {
			data["NextBefore"] = list[len(list)-1].OccurredAt.Format(time.RFC3339Nano)
		}
		rq := url.Values{}
		for _, k := range []string{"kind", "epistemic", "status", "min_confidence", "min_importance"} {
			if v := q.Get(k); v != "" {
				rq.Set(k, v)
			}
		}
		data["RawQuery"] = rq.Encode()
	}
	u.render(w, r, "memory", m.DisplayName+" · Memory", data)
}

func (u *UI) memoryAdd(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	r.ParseForm() //nolint:errcheck
	conf, _ := strconv.ParseFloat(r.PostFormValue("confidence"), 64)
	imp, _ := strconv.ParseFloat(r.PostFormValue("importance"), 64)
	mem, err := u.deps.Memory.Create(r.Context(), u.deps.DB, memory.New{MindID: m.ID, Kind: concepts.MemoryKind(r.PostFormValue("kind")), Epistemic: concepts.Epistemic(r.PostFormValue("epistemic")),
		Content: r.PostFormValue("content"), Source: "operator", Trust: events.TrustOperator, Confidence: conf, Importance: imp, Salience: 0.5})
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/memory")
		return
	}
	u.deps.Events.Append(r.Context(), events.New{MindID: m.ID, Type: events.MemoryCreated, Source: "operator", Summary: "operator added " + string(mem.Kind) + " memory: " + truncateStr(mem.Content, 120), //nolint:errcheck
		Salience: 0.5, Confidence: mem.Confidence, Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(mem.ID)}},
		Metadata: map[string]any{"memory_id": mem.ID, "kind": mem.Kind, "actor": username(r)}, Payload: map[string]any{"memory_id": mem.ID, "kind": mem.Kind, "epistemic": mem.Epistemic, "content": mem.Content}})
	u.deps.Audit.Log(r.Context(), "memory.create", "memory", mem.ID.String(), map[string]any{"mind": m.Slug, "kind": mem.Kind})
	u.flash(w, r, "ok", "Memory stored.", "/minds/"+m.Slug+"/memory/"+mem.ID.String())
}

func (u *UI) memoryDetail(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such memory.")
		return
	}
	d, err := u.memF(r, m.ID, id)
	if err != nil {
		u.errorPage(w, r, 404, "No such memory.")
		return
	}
	u.render(w, r, "memory_detail", "Memory", map[string]any{"Mind": m, "Detail": d})
}

func (u *UI) memoryRetract(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such memory.")
		return
	}
	reason := r.PostFormValue("reason")
	if err := u.deps.Memory.SetStatus(r.Context(), m.ID, id, memory.StatusRetracted); err != nil {
		u.flash(w, r, "error", "Could not retract.", "/minds/"+m.Slug+"/memory/"+id.String())
		return
	}
	u.deps.Events.OperatorAction(r.Context(), m.ID, username(r), "memory.retract", "Operator "+username(r)+" marked a memory inaccurate: "+reason, map[string]any{"memory_id": id, "reason": reason})
	u.deps.Audit.Log(r.Context(), "memory.retract", "memory", id.String(), map[string]any{"reason": reason})
	if affected, err := u.deps.Beliefs.RetractEvidence(r.Context(), u.deps.DB, m.ID, "memory", string(id)); err == nil {
		for _, bid := range affected {
			if res, err := u.deps.Beliefs.Reevaluate(r.Context(), u.deps.DB, u.emitF(), m.ID, bid, "supporting memory retracted by operator"); err == nil {
				u.deps.Events.Publish(res.Events...)
			}
		}
	}
	u.flash(w, r, "ok", "Memory marked inaccurate. It remains in history.", "/minds/"+m.Slug+"/memory/"+id.String())
}

func (u *UI) memoryRelate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	to, err2 := ids.Parse(r.PostFormValue("to"))
	if err != nil || err2 != nil {
		u.flash(w, r, "error", "Bad memory id.", "/minds/"+m.Slug+"/memory")
		return
	}
	if _, err := u.deps.Memory.Relate(r.Context(), u.deps.DB, m.ID, id, to, concepts.Relation(r.PostFormValue("relation")), 0.8, "operator"); err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/memory/"+id.String())
		return
	}
	u.deps.Audit.Log(r.Context(), "memory.relate", "memory", id.String(), map[string]any{"to": to, "relation": r.PostFormValue("relation")})
	u.flash(w, r, "ok", "Linked.", "/minds/"+m.Slug+"/memory/"+id.String())
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

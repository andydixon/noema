// Package ui serves the server-rendered administration interface. Every page
// is a Go handler plus an html/template; live updates reuse the same templates
// through SSE fragments (ADR-0007).
package ui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/consolidation"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/reflection"
	"github.com/andydixon/noema/internal/version"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/ids"
	"github.com/andydixon/noema/web"
)

// UI holds handlers and the parsed template set.
type UI struct {
	deps      app.Deps
	pages     map[string]*template.Template
	static    fs.FS
	statusF   func(r *http.Request) any                                  // system status provider injected by api
	ctxF      func(r *http.Request, id ids.ID) (api.EventContext, error) // event forensic context, shared with api
	wsF       func(r *http.Request, m minds.Mind) (api.WorkspaceView, error)
	memF      func(r *http.Request, mind, id ids.ID) (api.MemoryDetail, error)
	belF      func(r *http.Request, mind, id ids.ID) (api.BeliefDetail, error)
	emitF     func() beliefs.Emitter
	postF     func(r *http.Request, mind ids.ID, conv conversation.Conversation, text, who string) (conversation.Message, error)
	goalF     func(r *http.Request, n goals.New) (goals.Goal, error)
	goalUpdF  func(r *http.Request, mind, id ids.ID, u goals.Update) (goals.Goal, error)
	predF     func(r *http.Request, n predictions.New) (predictions.Prediction, error)
	resolveF  func(r *http.Request, mind, id ids.ID, outcome predictions.Status, reason string) (predictions.Result, error)
	resetF    func(r *http.Request, mind ids.ID) (motivation.State, error)
	consolF   func(r *http.Request, mind ids.ID) (consolidation.Job, error)
	reflF     func(r *http.Request, mind ids.ID) (reflection.Reflection, error)
	entF      func(r *http.Request, mind, id ids.ID) (api.EntityDetail, error)
	mergeF    func(r *http.Request, mind ids.ID, e graph.Entity, other string) error
	distinctF func(r *http.Request, mind ids.ID, e graph.Entity, other string) error
	policyF   func(r *http.Request, p capabilities.Policy) (capabilities.Policy, error)
	decideF   func(r *http.Request, id ids.ID, decision, note string, params map[string]any) (any, error)
}

// Actions groups the server-side operations the UI delegates to the API layer
// so both surfaces share one implementation and one audit path.
type Actions struct {
	Status      func(r *http.Request) any
	Context     func(r *http.Request, id ids.ID) (api.EventContext, error)
	Workspace   func(r *http.Request, m minds.Mind) (api.WorkspaceView, error)
	Memory      func(r *http.Request, mind, id ids.ID) (api.MemoryDetail, error)
	Belief      func(r *http.Request, mind, id ids.ID) (api.BeliefDetail, error)
	Emitter     func() beliefs.Emitter
	Post        func(r *http.Request, mind ids.ID, conv conversation.Conversation, text, who string) (conversation.Message, error)
	Goal        func(r *http.Request, n goals.New) (goals.Goal, error)
	GoalUpdate  func(r *http.Request, mind, id ids.ID, u goals.Update) (goals.Goal, error)
	Predict     func(r *http.Request, n predictions.New) (predictions.Prediction, error)
	Resolve     func(r *http.Request, mind, id ids.ID, outcome predictions.Status, reason string) (predictions.Result, error)
	ResetState  func(r *http.Request, mind ids.ID) (motivation.State, error)
	Consolidate func(r *http.Request, mind ids.ID) (consolidation.Job, error)
	Reflect     func(r *http.Request, mind ids.ID) (reflection.Reflection, error)
	Entity      func(r *http.Request, mind, id ids.ID) (api.EntityDetail, error)
	Merge       func(r *http.Request, mind ids.ID, e graph.Entity, other string) error
	Distinct    func(r *http.Request, mind ids.ID, e graph.Entity, other string) error
	Policy      func(r *http.Request, p capabilities.Policy) (capabilities.Policy, error)
	Decide      func(r *http.Request, id ids.ID, decision, note string, params map[string]any) (any, error)
}

// View is the data passed to every template.
type View struct {
	Title     string
	Path      string
	Principal *auth.Principal
	CSRF      string
	Flash     *Flash
	Version   string
	Unread    int64 // unread notifications, for the navigation badge
	Data      any
}

// Flash is a one-shot message shown on the next page load.
type Flash struct{ Kind, Message string }

const flashCookie = "noema_flash"

// New parses templates. In development, when ./web exists on disk, templates
// and static files are read from disk so edits show without a rebuild.
func New(deps app.Deps, a Actions) (*UI, error) {
	tmplFS, staticFS := web.Templates(), web.Static()
	if !deps.Config.Production() {
		if st, err := os.Stat("web/templates"); err == nil && st.IsDir() {
			tmplFS, staticFS = os.DirFS("web/templates"), os.DirFS("web/static")
			deps.Log.Info("ui: serving templates and static files from ./web (development)")
		}
	}
	u := &UI{deps: deps, static: staticFS, statusF: a.Status, ctxF: a.Context, wsF: a.Workspace, memF: a.Memory, belF: a.Belief, emitF: a.Emitter, postF: a.Post, goalF: a.Goal, goalUpdF: a.GoalUpdate, predF: a.Predict, resolveF: a.Resolve, resetF: a.ResetState, consolF: a.Consolidate, reflF: a.Reflect, entF: a.Entity, mergeF: a.Merge, distinctF: a.Distinct, policyF: a.Policy, decideF: a.Decide}
	if err := u.parse(tmplFS); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *UI) parse(fsys fs.FS) error {
	funcs := template.FuncMap{
		"hasPrefix": strings.HasPrefix,
		"hasSuffix": strings.HasSuffix,
		"roles":     auth.AllRoles,
		"date":      func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
		"ago":       ago,
		"duration":  func(s int64) string { return (time.Duration(s) * time.Second).Truncate(time.Second).String() },
		"bytes":     humanBytes,
		"pct":       func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"conf": func(v any) string {
			switch x := v.(type) {
			case float64:
				return fmt.Sprintf("%.2f", x)
			case float32:
				return fmt.Sprintf("%.2f", x)
			case *float64:
				if x != nil {
					return fmt.Sprintf("%.2f", *x)
				}
			}
			return "–"
		},
		"inc": func(i int) int { return i + 1 },
		"deref": func(v any) any {
			switch x := v.(type) {
			case *float64:
				if x == nil {
					return 0.0
				}
				return *x
			case *bool:
				if x == nil {
					return false
				}
				return *x
			}
			return v
		},
		"title":          func(s string) string { s = strings.ReplaceAll(s, "_", " "); return strings.ToUpper(s[:1]) + s[1:] },
		"valueKeys":      func() []string { return minds.ValueKeys },
		"features":       func() []string { return minds.AttentionFeatures },
		"memoryKinds":    func() []concepts.MemoryKind { return concepts.AllMemoryKinds },
		"epistemicKinds": func() []concepts.Epistemic { return concepts.AllEpistemic },
		"relations":      func() []concepts.Relation { return concepts.AllRelations },
		"llmRoles":       func() []llm.RoleInfo { return llm.Roles },
		"pretty":         prettyRaw,
		"entityKinds":    func() []graph.Kind { return graph.AllKinds },
		"jsonOf": func(v any) json.RawMessage {
			b, _ := json.Marshal(v)
			return b
		},
		"join": strings.Join,
		"outputs": func(raw json.RawMessage) []consolidation.Output {
			var out []consolidation.Output
			json.Unmarshal(raw, &out) //nolint:errcheck
			return out
		},
		"affectMap": func(a motivation.Affect) map[string]float64 {
			return map[string]float64{"valence": a.Valence, "arousal": a.Arousal, "curiosity": a.Curiosity, "concern": a.Concern, "frustration": a.Frustration, "confidence": a.Confidence, "uncertainty": a.Uncertainty, "engagement": a.Engagement, "task_pressure": a.TaskPressure, "load": a.Load}
		},
		"valencePos": func(v float64) float64 {
			if v < 0 {
				return 0.5 + v/2
			}
			return 0.5
		},
		"valenceWidth": func(v float64) float64 {
			if v < 0 {
				return -v / 2
			}
			return v / 2
		},
		"deref_id": func(id *ids.ID) ids.ID {
			if id == nil {
				return ids.Nil
			}
			return *id
		},
		"deref_int": func(i *int) int {
			if i == nil {
				return 0
			}
			return *i
		},
		"has_id": func(list []ids.ID, id ids.ID) bool {
			for _, x := range list {
				if x == id {
					return true
				}
			}
			return false
		},
		"beliefStatuses": func() []beliefs.Status {
			return []beliefs.Status{beliefs.StatusHypothesis, beliefs.StatusTentative, beliefs.StatusLikely, beliefs.StatusAccepted, beliefs.StatusContested, beliefs.StatusRejected, beliefs.StatusSuperseded}
		},
		"trunc": func(s string, n int) string {
			if len(s) > n {
				return s[:n] + "…"
			}
			return s
		},
		"candSummary": func(s string) string {
			if i := strings.Index(s, "] "); i > 0 && strings.HasPrefix(s, "[") {
				return s[i+2:]
			}
			return s
		},
		"payloadField": func(raw json.RawMessage, field string) string {
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				return ""
			}
			return fmt.Sprint(m[field])
		},
		"breakdownOf": func(raw json.RawMessage) *attention.Breakdown {
			var p struct {
				Breakdown attention.Breakdown `json:"breakdown"`
			}
			if json.Unmarshal(raw, &p) != nil || len(p.Breakdown.Terms) == 0 {
				return nil
			}
			return &p.Breakdown
		},
		"traitKeys": func() []string { return minds.TraitKeys },
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
	}
	base, err := template.New("base").Funcs(funcs).ParseFS(fsys, "layout.html")
	if err != nil {
		return fmt.Errorf("ui: parse layout: %w", err)
	}
	if partials, _ := fs.Glob(fsys, "partials/*.html"); len(partials) > 0 {
		if _, err := base.ParseFS(fsys, partials...); err != nil {
			return fmt.Errorf("ui: parse partials: %w", err)
		}
	}
	pages, err := fs.Glob(fsys, "pages/*.html")
	if err != nil {
		return err
	}
	u.pages = map[string]*template.Template{}
	for _, p := range pages {
		t, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := t.ParseFS(fsys, p); err != nil {
			return fmt.Errorf("ui: parse %s: %w", p, err)
		}
		u.pages[strings.TrimPrefix(strings.TrimSuffix(p, ".html"), "pages/")] = t
	}
	return nil
}

// render executes a page template. Rendering into a buffer first means a
// template error yields a proper 500 instead of a half-written page.
func (u *UI) render(w http.ResponseWriter, r *http.Request, page, title string, data any) {
	t, ok := u.pages[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	v := View{Title: title, Path: r.URL.Path, Version: version.Version, Data: data}
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		v.Principal = &p
		if tok, err := u.deps.Auth.CSRFToken(r.Context(), p.SessionID); err == nil {
			v.CSRF = tok
		}
		if u.deps.Notifications != nil {
			v.Unread, _ = u.deps.Notifications.CountNew(r.Context(), ids.Nil)
		}
	}
	if c, err := r.Cookie(flashCookie); err == nil && c.Value != "" {
		if raw, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			if kind, msg, ok := strings.Cut(string(raw), "|"); ok {
				v.Flash = &Flash{Kind: kind, Message: msg}
			}
		}
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		u.deps.Log.Error("template execution failed", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	buf.WriteTo(w) //nolint:errcheck
}

// flash stores a one-shot message and redirects.
func (u *UI) flash(w http.ResponseWriter, r *http.Request, kind, msg, to string) {
	val := base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + msg))
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: val, Path: "/", MaxAge: 60, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: u.deps.Config.Production()})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (u *UI) errorPage(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.WriteHeader(status)
	u.render(w, r, "error", http.StatusText(status), map[string]any{"Status": status, "Message": msg})
}

// uiAuthFail redirects anonymous users to login and shows 403 to others.
func (u *UI) uiAuthFail(w http.ResponseWriter, r *http.Request, authenticated bool) {
	if authenticated {
		u.errorPage(w, r, http.StatusForbidden, "Your role does not permit this page.")
		return
	}
	next := ""
	if r.Method == http.MethodGet && r.URL.Path != "/" {
		next = "?next=" + url.QueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, "/login"+next, http.StatusSeeOther)
}

func (u *UI) require(min auth.Role, h http.HandlerFunc) http.Handler {
	return auth.RequireRole(min, u.uiAuthFail)(h)
}

// Register mounts all UI routes.
func (u *UI) Register(m *http.ServeMux) {
	m.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(u.static))))
	m.HandleFunc("GET /login", u.loginForm)
	m.HandleFunc("POST /login", u.loginSubmit)
	m.HandleFunc("POST /logout", u.logout)
	m.HandleFunc("GET /setup", u.setupForm)
	m.HandleFunc("POST /setup", u.setupSubmit)
	m.HandleFunc("GET /about", func(w http.ResponseWriter, r *http.Request) { u.render(w, r, "about", "Interpretation boundary", nil) })

	m.Handle("GET /{$}", u.require(auth.RoleObserver, u.dashboard))
	m.Handle("GET /system", u.require(auth.RoleObserver, u.system))
	m.Handle("GET /audit", u.require(auth.RoleOperator, u.audit))
	m.Handle("GET /account", u.require(auth.RoleObserver, u.account))
	m.Handle("POST /account/password", u.require(auth.RoleObserver, u.accountPassword))
	m.Handle("POST /account/sessions/{id}/revoke", u.require(auth.RoleObserver, u.accountSessionRevoke))
	m.Handle("POST /account/tokens", u.require(auth.RoleObserver, u.accountTokenCreate))
	m.Handle("POST /account/tokens/{id}/revoke", u.require(auth.RoleObserver, u.accountTokenRevoke))
	m.Handle("GET /admin/users", u.require(auth.RoleAdministrator, u.users))
	m.Handle("POST /admin/users", u.require(auth.RoleAdministrator, u.usersCreate))
	m.Handle("POST /admin/users/{id}", u.require(auth.RoleAdministrator, u.usersUpdate))

	u.mindRoutes(m)
	u.eventRoutes(m)
	u.memoryRoutes(m)
	u.beliefRoutes(m)
	u.llmRoutes(m)
	u.conversationRoutes(m)
	u.motivationRoutes(m)
	u.cognitionRoutes(m)
	u.graphRoutes(m)
	u.capabilityRoutes(m)

	for _, p := range []struct{ path, title, note string }{
		{"/integrations", "Integrations", "Integrations arrive in phase 13."},
	} {
		m.Handle("GET "+p.path, u.require(auth.RoleObserver, func(w http.ResponseWriter, r *http.Request) { u.render(w, r, "placeholder", p.title, p.note) }))
	}
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		h.ServeHTTP(w, r)
	})
}

func ago(t any) string {
	var tm time.Time
	switch v := t.(type) {
	case time.Time:
		tm = v
	case *time.Time:
		if v == nil {
			return "never"
		}
		tm = *v
	default:
		return ""
	}
	d := time.Since(tm)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

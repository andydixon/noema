package ui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) llmRoutes(m *http.ServeMux) {
	m.Handle("GET /llm", u.require(auth.RoleObserver, u.llmPage))
	m.Handle("POST /llm/providers", u.require(auth.RoleAdministrator, u.providerCreate))
	m.Handle("GET /llm/providers/{id}", u.require(auth.RoleObserver, u.providerPage))
	m.Handle("POST /llm/providers/{id}", u.require(auth.RoleAdministrator, u.providerSave))
	m.Handle("POST /llm/providers/{id}/test", u.require(auth.RoleOperator, u.providerTest))
	m.Handle("POST /llm/providers/{id}/clear-key", u.require(auth.RoleAdministrator, u.providerClearKey))
	m.Handle("POST /llm/providers/{id}/delete", u.require(auth.RoleAdministrator, u.providerDelete))
	m.Handle("POST /llm/models", u.require(auth.RoleAdministrator, u.modelCreate))
	m.Handle("POST /llm/models/{id}/delete", u.require(auth.RoleAdministrator, u.modelDelete))
	m.Handle("POST /llm/roles/{role}", u.require(auth.RoleAdministrator, u.roleSave))
	m.Handle("GET /prompts", u.require(auth.RoleObserver, u.promptsPage))
	m.Handle("POST /prompts", u.require(auth.RoleAdministrator, u.promptCreate))
	m.Handle("GET /prompts/{name}", u.require(auth.RoleObserver, u.promptPage))
	m.Handle("POST /prompts/{name}", u.require(auth.RoleAdministrator, u.promptSave))
	m.Handle("POST /prompts/{name}/test", u.require(auth.RoleOperator, u.promptTest))
	m.Handle("POST /prompts/{name}/clone", u.require(auth.RoleAdministrator, u.promptClone))
	m.Handle("POST /prompts/{name}/versions/{n}/restore", u.require(auth.RoleAdministrator, u.promptRestore))
	m.Handle("GET /prompts/{name}/diff", u.require(auth.RoleObserver, u.promptDiff))
}

type roleRow struct {
	Info       llm.RoleInfo
	Assignment router.RoleAssignment
}

func (u *UI) llmPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provs, _ := u.deps.LLMStore.ListProviders(ctx)
	for i := range provs {
		provs[i].KeyDescribed = secrets.Describe(provs[i].APIKeyRef)
	}
	models, _ := u.deps.LLMStore.ListModels(ctx)
	roles, _ := u.deps.LLMStore.ListRoles(ctx)
	byRole := map[llm.Role]router.RoleAssignment{}
	for _, a := range roles {
		byRole[a.Role] = a
	}
	var rows []roleRow
	for _, info := range llm.Roles {
		rows = append(rows, roleRow{Info: info, Assignment: byRole[info.Role]})
	}
	usage, _ := u.deps.LLMStore.Usage(ctx, time.Now().Add(-24*time.Hour), ids.Nil)
	errs, _ := u.deps.LLMStore.ListCalls(ctx, router.CallFilter{ErrorsOnly: true, Limit: 8})
	used, _ := u.deps.LLMStore.TokensSince(ctx, time.Now().Truncate(24*time.Hour), ids.Nil)
	u.render(w, r, "llm", "Language models", map[string]any{"Providers": provs, "Models": models, "Roles": rows, "Usage": usage, "Errors": errs,
		"GlobalBudget": u.deps.Config.DailyTokenBudget, "UsedToday": used, "MaxConcurrent": u.deps.Config.MaxConcurrentLLM})
}

func (u *UI) keyRef(typed string) (string, error) {
	typed = strings.TrimSpace(typed)
	if typed == "" || secrets.IsReference(typed) {
		return typed, nil
	}
	return u.deps.Secrets.Encrypt(typed)
}

func (u *UI) providerInput(r *http.Request) (router.ProviderInput, error) {
	r.ParseForm() //nolint:errcheck
	ref, err := u.keyRef(r.PostFormValue("api_key"))
	if err != nil {
		return router.ProviderInput{}, err
	}
	timeout, _ := strconv.Atoi(r.PostFormValue("timeout_ms"))
	enabled := r.PostFormValue("enabled") != "" || r.PostFormValue("enabled_default") == "1"
	return router.ProviderInput{Name: r.PostFormValue("name"), Kind: llm.ProviderKind(r.PostFormValue("kind")), Endpoint: r.PostFormValue("endpoint"), APIKeyRef: ref,
		AllowPrivate: r.PostFormValue("allow_private") != "", Timeout: time.Duration(timeout) * time.Millisecond, Enabled: enabled}, nil
}

func (u *UI) providerCreate(w http.ResponseWriter, r *http.Request) {
	in, err := u.providerInput(r)
	if err != nil {
		u.flash(w, r, "error", "Cannot store key: "+err.Error()+". Set NOEMA_MASTER_KEY or use an env: reference.", "/llm")
		return
	}
	in.Enabled = true
	p, err := u.deps.LLMStore.CreateProvider(r.Context(), in)
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/llm")
		return
	}
	u.deps.LLM.Invalidate()
	u.deps.Audit.Log(r.Context(), "llm.provider.create", "llm_provider", p.ID.String(), map[string]any{"name": p.Name, "kind": p.Kind, "endpoint": p.Endpoint})
	u.flash(w, r, "ok", "Provider added. Add a model, then test it.", "/llm")
}

func (u *UI) provider(w http.ResponseWriter, r *http.Request) (router.Provider, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such provider.")
		return router.Provider{}, false
	}
	p, err := u.deps.LLMStore.GetProvider(r.Context(), id)
	if err != nil {
		u.errorPage(w, r, 404, "No such provider.")
		return router.Provider{}, false
	}
	p.KeyDescribed = secrets.Describe(p.APIKeyRef)
	return p, true
}

func (u *UI) providerPage(w http.ResponseWriter, r *http.Request) {
	p, ok := u.provider(w, r)
	if !ok {
		return
	}
	all, _ := u.deps.LLMStore.ListModels(r.Context())
	var mine []router.Model
	for _, m := range all {
		if m.ProviderID == p.ID {
			mine = append(mine, m)
		}
	}
	u.render(w, r, "provider", "Provider "+p.Name, map[string]any{"Provider": p, "Models": mine})
}

func (u *UI) providerSave(w http.ResponseWriter, r *http.Request) {
	p, ok := u.provider(w, r)
	if !ok {
		return
	}
	in, err := u.providerInput(r)
	if err != nil {
		u.flash(w, r, "error", "Cannot store key: "+err.Error(), "/llm/providers/"+p.ID.String())
		return
	}
	if _, err := u.deps.LLMStore.UpdateProvider(r.Context(), p.ID, in); err != nil {
		u.flash(w, r, "error", err.Error(), "/llm/providers/"+p.ID.String())
		return
	}
	u.deps.LLM.Invalidate()
	u.deps.Audit.Log(r.Context(), "llm.provider.update", "llm_provider", p.ID.String(), map[string]any{"name": in.Name, "enabled": in.Enabled, "key_changed": in.APIKeyRef != ""})
	u.flash(w, r, "ok", "Provider saved.", "/llm/providers/"+p.ID.String())
}

func (u *UI) providerTest(w http.ResponseWriter, r *http.Request) {
	p, ok := u.provider(w, r)
	if !ok {
		return
	}
	resp, err := u.deps.LLM.TestProvider(r.Context(), p.ID, r.PostFormValue("model"))
	u.deps.Audit.Log(r.Context(), "llm.provider.test", "llm_provider", p.ID.String(), map[string]any{"ok": err == nil})
	if err != nil {
		u.flash(w, r, "error", "Test failed: "+err.Error(), "/llm")
		return
	}
	u.flash(w, r, "ok", "Provider ok: "+resp.Model+" answered in "+strconv.FormatInt(resp.Latency.Milliseconds(), 10)+" ms.", "/llm")
}

func (u *UI) providerClearKey(w http.ResponseWriter, r *http.Request) {
	p, ok := u.provider(w, r)
	if !ok {
		return
	}
	u.deps.LLMStore.ClearProviderKey(r.Context(), p.ID) //nolint:errcheck
	u.deps.LLM.Invalidate()
	u.deps.Audit.Log(r.Context(), "llm.provider.clear_key", "llm_provider", p.ID.String(), nil)
	u.flash(w, r, "ok", "Key reference removed.", "/llm/providers/"+p.ID.String())
}

func (u *UI) providerDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := u.provider(w, r)
	if !ok {
		return
	}
	if err := u.deps.LLMStore.DeleteProvider(r.Context(), p.ID); err != nil {
		u.flash(w, r, "error", err.Error(), "/llm")
		return
	}
	u.deps.LLM.Invalidate()
	u.deps.Audit.Log(r.Context(), "llm.provider.delete", "llm_provider", p.ID.String(), map[string]any{"name": p.Name})
	u.flash(w, r, "ok", "Provider deleted.", "/llm")
}

func (u *UI) modelCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	pid, err := ids.Parse(r.PostFormValue("provider_id"))
	if err != nil {
		u.flash(w, r, "error", "Choose a provider.", "/llm")
		return
	}
	cw, _ := strconv.Atoi(r.PostFormValue("context_window"))
	in, _ := strconv.ParseFloat(r.PostFormValue("input_cost_per_m"), 64)
	out, _ := strconv.ParseFloat(r.PostFormValue("output_cost_per_m"), 64)
	m, err := u.deps.LLMStore.CreateModel(r.Context(), router.ModelInput{ProviderID: pid, Name: r.PostFormValue("name"), Kind: r.PostFormValue("kind"), ContextWindow: cw, InputCostPerM: in, OutputCostPerM: out, Enabled: true})
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/llm")
		return
	}
	u.deps.LLM.Invalidate()
	u.deps.Audit.Log(r.Context(), "llm.model.create", "llm_model", m.ID.String(), map[string]any{"name": m.Name, "provider": m.Provider})
	u.flash(w, r, "ok", "Model added.", "/llm")
}

func (u *UI) modelDelete(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil || u.deps.LLMStore.DeleteModel(r.Context(), id) != nil {
		u.flash(w, r, "error", "Model not found.", "/llm")
		return
	}
	u.deps.LLM.Invalidate()
	u.deps.Audit.Log(r.Context(), "llm.model.delete", "llm_model", id.String(), nil)
	u.flash(w, r, "ok", "Model removed.", "/llm")
}

func (u *UI) roleSave(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	a := router.RoleAssignment{Role: llm.Role(r.PathValue("role")), UpdatedBy: username(r), FallbackIDs: []ids.ID{}}
	if v := r.PostFormValue("model_id"); v != "" {
		if id, err := ids.Parse(v); err == nil {
			a.ModelID = &id
		}
	}
	for _, v := range r.PostForm["fallback_ids"] {
		if id, err := ids.Parse(v); err == nil && (a.ModelID == nil || id != *a.ModelID) {
			a.FallbackIDs = append(a.FallbackIDs, id)
		}
	}
	if v := r.PostFormValue("temperature"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			a.Temperature = &f
		}
	}
	if v := r.PostFormValue("max_tokens"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			a.MaxTokens = &n
		}
	}
	if err := u.deps.LLMStore.SetRole(r.Context(), a); err != nil {
		u.flash(w, r, "error", err.Error(), "/llm")
		return
	}
	u.deps.LLM.Invalidate()
	u.deps.Memory.SetEmbedder(u.deps.LLM.Embedder(r.Context()))
	u.deps.Audit.Log(r.Context(), "llm.role.set", "llm_role", string(a.Role), map[string]any{"model_id": a.ModelID, "fallbacks": a.FallbackIDs})
	u.flash(w, r, "ok", "Role "+string(a.Role)+" saved.", "/llm")
}

// ---- prompts ----

func (u *UI) promptsPage(w http.ResponseWriter, r *http.Request) {
	list, _ := u.deps.Prompts.List(r.Context())
	u.render(w, r, "prompts", "Prompts", map[string]any{"Prompts": list})
}

func (u *UI) promptCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	p, err := u.deps.Prompts.Save(r.Context(), prompts.Input{Name: r.PostFormValue("name"), Description: r.PostFormValue("description"), Role: llm.Role(r.PostFormValue("role")),
		Template: "{{data \"input\" .Text}}", Note: "created", By: username(r)})
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/prompts")
		return
	}
	u.deps.Audit.Log(r.Context(), "prompt.save", "prompt", p.Name, map[string]any{"version": 1})
	http.Redirect(w, r, "/prompts/"+p.Name, http.StatusSeeOther)
}

var samples = map[string]string{
	"perceive.message":  `{"Text":"Is atlas slow again since the deploy?","Actor":"andy","Trust":"user","Channel":"web"}`,
	"respond":           `{"Brief":"You are Dani, an investigator.","Intent":"Explain that the deployment hypothesis is weak.","Beliefs":[{"Proposition":"the deployment caused the latency","Confidence":0.32,"Status":"hypothesis"}],"Memories":[],"Uncertainty":"Only two co-occurring observations.","Limitations":["I cannot access servers."],"Message":"Was it the deploy?"}`,
	"hypothesise":       `{"Observations":["deployment at 09:10","CPU spike at 09:11","latency alert at 09:12"],"Beliefs":[],"Max":3}`,
	"summarise":         `{"Text":"Long text to summarise…","MaxWords":40}`,
	"judge.equivalence": `{"A":"atlas is slow","B":"atlas is not slow"}`,
	"narrate":           `{"Workspace":["latency alert on atlas"],"Recent":["hypothesis formed"],"Goals":[],"Contradictions":0}`,
}

func (u *UI) promptPage(w http.ResponseWriter, r *http.Request) {
	p, err := u.deps.Prompts.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		u.errorPage(w, r, 404, "No such prompt.")
		return
	}
	vs, _ := u.deps.Prompts.Versions(r.Context(), p.Name)
	sample := samples[p.Name]
	if sample == "" {
		sample = `{"Text":"sample input"}`
	}
	u.render(w, r, "prompt", "Prompt "+p.Name, map[string]any{"Prompt": p, "Versions": vs, "Sample": sample, "RoleAvailable": u.deps.LLM.Available(r.Context(), p.Role)})
}

func (u *UI) promptSave(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	name := r.PathValue("name")
	var schema json.RawMessage
	if s := strings.TrimSpace(r.PostFormValue("schema")); s != "" {
		schema = json.RawMessage(s)
	}
	p, err := u.deps.Prompts.Save(r.Context(), prompts.Input{Name: name, Description: r.PostFormValue("description"), System: r.PostFormValue("system"), Template: r.PostFormValue("template"), Schema: schema, Note: r.PostFormValue("note"), By: username(r)})
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/prompts/"+name)
		return
	}
	u.deps.Audit.Log(r.Context(), "prompt.save", "prompt", p.Name, map[string]any{"version": p.CurrentVersion, "note": r.PostFormValue("note")})
	u.flash(w, r, "ok", "Saved as version "+strconv.Itoa(p.CurrentVersion)+".", "/prompts/"+name)
}

func (u *UI) promptTest(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	p, err := u.deps.Prompts.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		u.errorPage(w, r, 404, "No such prompt.")
		return
	}
	var data map[string]any
	raw := r.PostFormValue("data")
	result := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		result["Error"] = "Sample data is not a JSON object: " + err.Error()
	} else if req, err := p.Render(data); err != nil {
		result["Error"] = err.Error()
	} else {
		result["Rendered"] = req
		if r.PostFormValue("run") == "1" {
			req.Purpose = "prompt test by " + username(r)
			resp, err := u.deps.LLM.Complete(r.Context(), req)
			if err != nil {
				result["Error"] = err.Error()
			} else {
				result["Response"] = resp
			}
			u.deps.Audit.Log(r.Context(), "prompt.test", "prompt", p.Name, map[string]any{"ran": true, "ok": err == nil})
		}
	}
	vs, _ := u.deps.Prompts.Versions(r.Context(), p.Name)
	u.render(w, r, "prompt", "Prompt "+p.Name, map[string]any{"Prompt": p, "Versions": vs, "Sample": raw, "Test": result, "RoleAvailable": u.deps.LLM.Available(r.Context(), p.Role)})
}

func (u *UI) promptClone(w http.ResponseWriter, r *http.Request) {
	p, err := u.deps.Prompts.Clone(r.Context(), r.PathValue("name"), r.PostFormValue("name"), username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/prompts/"+r.PathValue("name"))
		return
	}
	u.deps.Audit.Log(r.Context(), "prompt.clone", "prompt", p.Name, map[string]any{"from": r.PathValue("name")})
	http.Redirect(w, r, "/prompts/"+p.Name, http.StatusSeeOther)
}

func (u *UI) promptRestore(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	p, err := u.deps.Prompts.Restore(r.Context(), r.PathValue("name"), n, username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/prompts/"+r.PathValue("name"))
		return
	}
	u.deps.Audit.Log(r.Context(), "prompt.restore", "prompt", p.Name, map[string]any{"restored": n, "version": p.CurrentVersion})
	u.flash(w, r, "ok", "Restored version "+strconv.Itoa(n)+" as version "+strconv.Itoa(p.CurrentVersion)+".", "/prompts/"+p.Name)
}

type diffLine struct{ Class, Text string }

func (u *UI) promptDiff(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a, _ := strconv.Atoi(r.URL.Query().Get("a"))
	b, _ := strconv.Atoi(r.URL.Query().Get("b"))
	va, errA := u.deps.Prompts.GetVersion(r.Context(), name, a)
	vb, errB := u.deps.Prompts.GetVersion(r.Context(), name, b)
	if errA != nil || errB != nil {
		u.errorPage(w, r, 404, "Version not found.")
		return
	}
	lines := lineDiff(va.System+"\n---\n"+va.Template, vb.System+"\n---\n"+vb.Template)
	u.render(w, r, "prompt_diff", "Prompt diff", map[string]any{"Name": name, "A": va, "B": vb, "Lines": lines})
}

// lineDiff is a small LCS line diff; prompts are short so O(n·m) is fine.
func lineDiff(a, b string) []diffLine {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	n, m := len(al), len(bl)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []diffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case al[i] == bl[j]:
			out = append(out, diffLine{"ctx", "  " + al[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{"del", "- " + al[i]})
			i++
		default:
			out = append(out, diffLine{"add", "+ " + bl[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{"del", "- " + al[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{"add", "+ " + bl[j]})
	}
	return out
}

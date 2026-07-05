package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/experiments"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) studyRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/snapshots", u.require(auth.RoleObserver, u.snapshotsPage))
	m.Handle("POST /minds/{mind}/snapshots", u.require(auth.RoleOperator, u.snapshotCreate))
	m.Handle("POST /snapshots/{id}/fork", u.require(auth.RoleAdministrator, u.snapshotFork))
	m.Handle("POST /snapshots/{id}/delete", u.require(auth.RoleAdministrator, u.snapshotDelete))
	m.Handle("POST /minds/import-archive", u.require(auth.RoleAdministrator, u.importArchive))
	m.Handle("GET /minds/{mind}/compare", u.require(auth.RoleObserver, u.comparePage))
	m.Handle("GET /minds/{mind}/replays", u.require(auth.RoleObserver, u.replaysPage))
	m.Handle("POST /minds/{mind}/replays", u.require(auth.RoleOperator, u.replayStart))
	m.Handle("GET /replays/{id}", u.require(auth.RoleObserver, u.replayPage))
	m.Handle("GET /experiments", u.require(auth.RoleObserver, u.experimentsPage))
	m.Handle("POST /experiments", u.require(auth.RoleOperator, u.experimentCreate))
	m.Handle("GET /experiments/{id}", u.require(auth.RoleObserver, u.experimentPage))
	m.Handle("POST /experiments/{id}/run", u.require(auth.RoleOperator, u.experimentRun))
	m.Handle("POST /experiments/{id}/delete", u.require(auth.RoleAdministrator, u.experimentDelete))
}

func (u *UI) snapshotsPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	list, _ := u.deps.Snapshots.List(r.Context(), m.ID, 100)
	all, _ := u.deps.Minds.List(r.Context(), true)
	var others []minds.Mind
	for _, o := range all {
		if o.ID != m.ID {
			others = append(others, o)
		}
	}
	var parent *minds.Mind
	if m.ForkedFromMind != nil {
		if p, err := u.deps.Minds.Get(r.Context(), *m.ForkedFromMind); err == nil {
			parent = &p
		}
	}
	u.render(w, r, "snapshots", m.DisplayName+" · Snapshots", map[string]any{"Mind": m, "Snapshots": list, "Others": others, "Parent": parent})
}

func (u *UI) snapshotCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	sn, err := u.studyF().CreateSnapshot(r.Context(), m, r.PostFormValue("name"), r.PostFormValue("note"), username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/snapshots")
		return
	}
	u.flash(w, r, "ok", "Snapshot "+sn.Name+" taken at event "+strconv.FormatInt(sn.EventSeq, 10)+".", "/minds/"+m.Slug+"/snapshots")
}

func (u *UI) snapshotByPath(w http.ResponseWriter, r *http.Request) (snapshots.Snapshot, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such snapshot.")
		return snapshots.Snapshot{}, false
	}
	sn, err := u.deps.Snapshots.Get(r.Context(), id)
	if err != nil {
		u.errorPage(w, r, 404, "No such snapshot.")
		return sn, false
	}
	return sn, true
}

func (u *UI) snapshotFork(w http.ResponseWriter, r *http.Request) {
	sn, ok := u.snapshotByPath(w, r)
	if !ok {
		return
	}
	src, _ := u.deps.Minds.Get(r.Context(), sn.MindID)
	m, err := u.studyF().ForkSnapshot(r.Context(), sn, r.PostFormValue("slug"), strings.TrimSpace(r.PostFormValue("display_name")), username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+src.Slug+"/snapshots")
		return
	}
	u.flash(w, r, "ok", "Forked into "+m.DisplayName+". History before event "+strconv.FormatInt(sn.EventSeq, 10)+" is shared with "+src.DisplayName+".", "/minds/"+m.Slug)
}

func (u *UI) snapshotDelete(w http.ResponseWriter, r *http.Request) {
	sn, ok := u.snapshotByPath(w, r)
	if !ok {
		return
	}
	src, _ := u.deps.Minds.Get(r.Context(), sn.MindID)
	if err := u.deps.Snapshots.Delete(r.Context(), sn.ID); err != nil {
		u.flash(w, r, "error", "Snapshot is referenced by a replay or experiment and cannot be deleted.", "/minds/"+src.Slug+"/snapshots")
		return
	}
	u.deps.Audit.Log(r.Context(), "snapshot.delete", "snapshot", sn.ID.String(), map[string]any{"name": sn.Name})
	u.flash(w, r, "ok", "Snapshot deleted.", "/minds/"+src.Slug+"/snapshots")
}

func (u *UI) importArchive(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, snapshots.MaxArchiveBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil { // #nosec G120 -- body bounded by MaxBytesReader on the previous line
		u.flash(w, r, "error", "Upload too large or malformed.", "/minds")
		return
	}
	var raw []byte
	if f, _, err := r.FormFile("archive"); err == nil {
		defer f.Close()
		raw, _ = io.ReadAll(io.LimitReader(f, snapshots.MaxArchiveBytes+1))
	} else {
		raw = []byte(r.PostFormValue("archive_json"))
	}
	m, err := u.studyF().ImportArchive(r.Context(), raw, r.PostFormValue("slug"), strings.TrimSpace(r.PostFormValue("display_name")), username(r))
	if err != nil {
		u.flash(w, r, "error", "Import refused: "+err.Error(), "/minds")
		return
	}
	u.flash(w, r, "ok", "Imported "+m.DisplayName+".", "/minds/"+m.Slug)
}

func (u *UI) comparePage(w http.ResponseWriter, r *http.Request) {
	a, ok := u.mind(w, r)
	if !ok {
		return
	}
	b, err := u.deps.Minds.Resolve(r.Context(), r.URL.Query().Get("with"))
	if err != nil {
		u.flash(w, r, "error", "Choose a mind to compare with.", "/minds/"+a.Slug+"/snapshots")
		return
	}
	c, err := u.deps.Snapshots.Compare(r.Context(), a, b)
	if err != nil {
		u.errorPage(w, r, 500, err.Error())
		return
	}
	u.render(w, r, "compare", a.DisplayName+" vs "+b.DisplayName, map[string]any{"Mind": a, "Other": b, "C": c})
}

func (u *UI) replaysPage(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	list, _ := u.deps.Replay.List(r.Context(), m.ID, 50)
	snaps, _ := u.deps.Snapshots.List(r.Context(), m.ID, 100)
	var lastSeq int64
	if st, err := u.deps.Events.Stats(r.Context(), m.ID, time.Time{}); err == nil {
		lastSeq = st.LastSeq
	}
	u.render(w, r, "replays", m.DisplayName+" · Replay", map[string]any{"Mind": m, "Replays": list, "Snapshots": snaps, "LastSeq": lastSeq})
}

func (u *UI) replayStart(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	snID, err := ids.Parse(r.PostFormValue("snapshot_id"))
	if err != nil {
		u.flash(w, r, "error", "Choose a snapshot.", "/minds/"+m.Slug+"/replays")
		return
	}
	from, _ := strconv.ParseInt(r.PostFormValue("from_seq"), 10, 64)
	to, _ := strconv.ParseInt(r.PostFormValue("to_seq"), 10, 64)
	rp, err := u.studyF().StartReplay(r.Context(), m, snID, from, to, username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/minds/"+m.Slug+"/replays")
		return
	}
	u.flash(w, r, "ok", "Replay started; steps appear as they complete.", "/replays/"+rp.ID.String())
}

func (u *UI) replayPage(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such replay.")
		return
	}
	rp, err := u.deps.Replay.Get(r.Context(), id)
	if err != nil {
		u.errorPage(w, r, 404, "No such replay.")
		return
	}
	m, _ := u.deps.Minds.Get(r.Context(), rp.MindID)
	sn, _ := u.deps.Snapshots.Get(r.Context(), rp.SnapshotID)
	var sandbox *minds.Mind
	if rp.ReplayMindID != nil {
		if sb, err := u.deps.Minds.Get(r.Context(), *rp.ReplayMindID); err == nil {
			sandbox = &sb
		}
	}
	u.render(w, r, "replay", "Replay of "+m.DisplayName, map[string]any{"Mind": m, "Replay": rp, "Steps": replay.StepsOf(rp), "Snapshot": sn, "Sandbox": sandbox})
}

func (u *UI) experimentsPage(w http.ResponseWriter, r *http.Request) {
	list, _ := u.deps.Experiments.List(r.Context(), 100)
	snaps, _ := u.deps.Snapshots.List(r.Context(), ids.Nil, 200)
	names, _ := u.mindNames(r)
	sample, _ := json.MarshalIndent(experiments.Definition{Config: map[string]any{"workspace_capacity": 5}, Personality: map[string]any{"traits": map[string]any{"curiosity": 0.9}},
		Stimuli:  []experiments.Stimulus{{Text: "Deployment of the payments service finished at 09:10"}, {Text: "Latency alert on the payments service at 09:12"}},
		Expected: []experiments.Expectation{{EventType: "hypothesis_created", Contains: "payments"}, {EventType: "tool_executed", Absent: true}}, Seed: "run-1", Duration: 300}, "", "  ")
	u.render(w, r, "experiments", "Experiments", map[string]any{"Experiments": list, "Snapshots": snaps, "MindNames": names, "Sample": string(sample)})
}

func (u *UI) experimentCreate(w http.ResponseWriter, r *http.Request) {
	snID, err := ids.Parse(r.PostFormValue("snapshot_id"))
	if err != nil {
		u.flash(w, r, "error", "Choose a snapshot.", "/experiments")
		return
	}
	var def experiments.Definition
	dec := json.NewDecoder(strings.NewReader(r.PostFormValue("definition")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&def); err != nil {
		u.flash(w, r, "error", "Definition is not valid JSON: "+err.Error(), "/experiments")
		return
	}
	e, err := u.studyF().CreateExperiment(r.Context(), api.ExperimentInput{Name: r.PostFormValue("name"), Hypothesis: r.PostFormValue("hypothesis"), SnapshotID: snID, Definition: def}, username(r))
	if err != nil {
		u.flash(w, r, "error", err.Error(), "/experiments")
		return
	}
	http.Redirect(w, r, "/experiments/"+e.ID.String(), http.StatusSeeOther)
}

func (u *UI) experimentByPath(w http.ResponseWriter, r *http.Request) (experiments.Experiment, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such experiment.")
		return experiments.Experiment{}, false
	}
	e, err := u.deps.Experiments.Get(r.Context(), id)
	if err != nil {
		u.errorPage(w, r, 404, "No such experiment.")
		return e, false
	}
	return e, true
}

func (u *UI) experimentPage(w http.ResponseWriter, r *http.Request) {
	e, ok := u.experimentByPath(w, r)
	if !ok {
		return
	}
	sn, _ := u.deps.Snapshots.Get(r.Context(), e.SnapshotID)
	base, _ := u.deps.Minds.Get(r.Context(), sn.MindID)
	var subject *minds.Mind
	if e.MindID != nil {
		if s, err := u.deps.Minds.Get(r.Context(), *e.MindID); err == nil {
			subject = &s
		}
	}
	def, _ := json.MarshalIndent(e.Definition, "", "  ")
	u.render(w, r, "experiment", "Experiment "+e.Name, map[string]any{"E": e, "Results": experiments.ResultsOf(e), "Snapshot": sn, "Base": base, "Subject": subject, "DefinitionJSON": string(def)})
}

func (u *UI) experimentRun(w http.ResponseWriter, r *http.Request) {
	e, ok := u.experimentByPath(w, r)
	if !ok {
		return
	}
	if err := u.studyF().RunExperiment(r.Context(), e, username(r)); err != nil {
		u.flash(w, r, "error", err.Error(), "/experiments/"+e.ID.String())
		return
	}
	u.flash(w, r, "ok", "Experiment running; results appear when the stimuli finish.", "/experiments/"+e.ID.String())
}

func (u *UI) experimentDelete(w http.ResponseWriter, r *http.Request) {
	e, ok := u.experimentByPath(w, r)
	if !ok {
		return
	}
	if err := u.deps.Experiments.Delete(r.Context(), e.ID); err != nil {
		u.flash(w, r, "error", err.Error(), "/experiments/"+e.ID.String())
		return
	}
	u.deps.Audit.Log(r.Context(), "experiment.delete", "experiment", e.ID.String(), map[string]any{"name": e.Name})
	u.flash(w, r, "ok", "Experiment deleted; its subject mind is archived.", "/experiments")
}

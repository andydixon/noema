package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/experiments"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/pkg/ids"
)

// studyRoutes: snapshots, forks, export/import, comparison, replay, experiments.
func (s *Server) studyRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/snapshots", require(auth.RoleObserver, s.snapshotsList))
	m.Handle("POST /api/v1/minds/{mind}/snapshots", require(auth.RoleOperator, s.snapshotCreate))
	m.Handle("GET /api/v1/snapshots/{id}", require(auth.RoleObserver, s.snapshotGet))
	m.Handle("GET /api/v1/snapshots/{id}/archive", require(auth.RoleOperator, s.snapshotArchive))
	m.Handle("POST /api/v1/snapshots/{id}/fork", require(auth.RoleAdministrator, s.snapshotFork))
	m.Handle("DELETE /api/v1/snapshots/{id}", require(auth.RoleAdministrator, s.snapshotDelete))
	m.Handle("GET /api/v1/minds/{mind}/export", require(auth.RoleOperator, s.mindExport))
	m.Handle("POST /api/v1/minds/import-archive", require(auth.RoleAdministrator, s.mindImportArchive))
	m.Handle("GET /api/v1/minds/{mind}/compare/{other}", require(auth.RoleObserver, s.mindCompare))
	m.Handle("GET /api/v1/minds/{mind}/replays", require(auth.RoleObserver, s.replaysList))
	m.Handle("POST /api/v1/minds/{mind}/replays", require(auth.RoleOperator, s.replayStart))
	m.Handle("GET /api/v1/replays/{id}", require(auth.RoleObserver, s.replayGet))
	m.Handle("GET /api/v1/experiments", require(auth.RoleObserver, s.experimentsList))
	m.Handle("POST /api/v1/experiments", require(auth.RoleOperator, s.experimentCreate))
	m.Handle("GET /api/v1/experiments/{id}", require(auth.RoleObserver, s.experimentGet))
	m.Handle("POST /api/v1/experiments/{id}/run", require(auth.RoleOperator, s.experimentRun))
	m.Handle("GET /api/v1/experiments/{id}/export", require(auth.RoleObserver, s.experimentExport))
	m.Handle("DELETE /api/v1/experiments/{id}", require(auth.RoleAdministrator, s.experimentDelete))
}

func (s *Server) snapshotsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Snapshots.List(r.Context(), m.ID, 100)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []snapshots.Snapshot{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// CreateSnapshot captures a mind; shared with the UI.
func (s *Server) CreateSnapshot(ctx context.Context, m minds.Mind, name, note, by string) (snapshots.Snapshot, error) {
	sn, err := s.deps.Snapshots.Create(ctx, m, name, note, by)
	if err != nil {
		return sn, err
	}
	s.deps.Audit.Log(ctx, "snapshot.create", "snapshot", sn.ID.String(), map[string]any{"mind": m.Slug, "name": sn.Name, "event_seq": sn.EventSeq})
	s.deps.Events.OperatorAction(ctx, m.ID, by, "snapshot.create", "Operator "+by+" took snapshot "+sn.Name, map[string]any{"snapshot_id": sn.ID, "event_seq": sn.EventSeq})
	return sn, nil
}

func (s *Server) snapshotCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct{ Name, Note string }
	decode(r, &in) //nolint:errcheck // both optional
	sn, err := s.CreateSnapshot(r.Context(), m, in.Name, in.Note, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sn)
}

func (s *Server) snapshotByPath(w http.ResponseWriter, r *http.Request) (snapshots.Snapshot, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad snapshot id"))
		return snapshots.Snapshot{}, false
	}
	sn, err := s.deps.Snapshots.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return sn, false
	}
	return sn, true
}

func (s *Server) snapshotGet(w http.ResponseWriter, r *http.Request) {
	if sn, ok := s.snapshotByPath(w, r); ok {
		writeJSON(w, http.StatusOK, sn)
	}
}

func (s *Server) snapshotArchive(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.snapshotByPath(w, r)
	if !ok {
		return
	}
	a, err := s.deps.Snapshots.Archive(r.Context(), sn.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "snapshot.export", "snapshot", sn.ID.String(), nil)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+a.Mind.Slug+"-"+sn.ID.String()[len(sn.ID.String())-8:]+`.noema.json"`)
	json.NewEncoder(w).Encode(a) //nolint:errcheck
}

// ForkSnapshot creates a mind from a snapshot; shared with the UI.
func (s *Server) ForkSnapshot(ctx context.Context, sn snapshots.Snapshot, slug, display, by string) (minds.Mind, error) {
	m, err := s.deps.Snapshots.Fork(ctx, sn.ID, snapshots.ForkOptions{Slug: slug, DisplayName: display, By: by})
	if err != nil {
		return m, mindErr(err)
	}
	s.deps.Audit.Log(ctx, "mind.fork", "mind", m.ID.String(), map[string]any{"snapshot": sn.ID, "source": sn.MindID, "slug": m.Slug})
	s.deps.Events.OperatorAction(ctx, m.ID, by, "mind.fork", "Mind forked from snapshot "+sn.Name+" (history before event "+strconv.FormatInt(sn.EventSeq, 10)+" is shared with the source)", map[string]any{"snapshot_id": sn.ID, "source_mind": sn.MindID, "event_seq": sn.EventSeq})
	s.deps.Events.OperatorAction(ctx, sn.MindID, by, "mind.fork", "Operator "+by+" forked this mind into "+m.Slug+" from snapshot "+sn.Name, map[string]any{"snapshot_id": sn.ID, "fork_mind": m.ID})
	s.deps.Brain.Reconcile(ctx) //nolint:errcheck
	return m, nil
}

func (s *Server) snapshotFork(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.snapshotByPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	m, err := s.ForkSnapshot(r.Context(), sn, in.Slug, in.DisplayName, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) snapshotDelete(w http.ResponseWriter, r *http.Request) {
	sn, ok := s.snapshotByPath(w, r)
	if !ok {
		return
	}
	if err := s.deps.Snapshots.Delete(r.Context(), sn.ID); err != nil {
		fail(w, r, ValidationError("snapshot is referenced by a replay or experiment"))
		return
	}
	s.deps.Audit.Log(r.Context(), "snapshot.delete", "snapshot", sn.ID.String(), map[string]any{"name": sn.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) mindExport(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	a, err := snapshots.Capture(r.Context(), s.deps.DB, s.deps.Events, m)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "mind.export", "mind", m.ID.String(), map[string]any{"event_seq": a.Source.EventSeq})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+m.Slug+`.noema.json"`)
	json.NewEncoder(w).Encode(a) //nolint:errcheck
}

// ImportArchive restores a hostile archive into a new mind; shared with the UI.
func (s *Server) ImportArchive(ctx context.Context, raw []byte, slug, display, by string) (minds.Mind, error) {
	if len(raw) > snapshots.MaxArchiveBytes {
		return minds.Mind{}, ValidationError("archive too large")
	}
	var a snapshots.Archive
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return minds.Mind{}, ValidationError("invalid archive: " + err.Error())
	}
	m, err := s.deps.Snapshots.Import(ctx, a, snapshots.ForkOptions{Slug: slug, DisplayName: display, By: by, Notes: "imported archive of " + a.Source.Slug})
	if err != nil {
		if errors.Is(err, snapshots.ErrInvalid) {
			return m, ValidationError(err.Error())
		}
		return m, mindErr(err)
	}
	s.deps.Audit.Log(ctx, "mind.import", "mind", m.ID.String(), map[string]any{"source": a.Source.Slug, "checksum": a.Checksum, "counts": a.Counts()})
	s.deps.Events.OperatorAction(ctx, m.ID, by, "mind.import", "Mind imported from archive of "+a.Source.Slug+" ("+a.Checksum+")", map[string]any{"counts": a.Counts()})
	s.deps.Brain.Reconcile(ctx) //nolint:errcheck
	return m, nil
}

func (s *Server) mindImportArchive(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug        string          `json:"slug"`
		DisplayName string          `json:"display_name"`
		Archive     json.RawMessage `json:"archive"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, snapshots.MaxArchiveBytes+4096))
	if err := dec.Decode(&in); err != nil {
		fail(w, r, ValidationError("invalid request: "+err.Error()))
		return
	}
	m, err := s.ImportArchive(r.Context(), in.Archive, in.Slug, in.DisplayName, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) mindCompare(w http.ResponseWriter, r *http.Request) {
	a, ok := s.mind(w, r)
	if !ok {
		return
	}
	b, err := s.deps.Minds.Resolve(r.Context(), r.PathValue("other"))
	if err != nil {
		fail(w, r, err)
		return
	}
	c, err := s.deps.Snapshots.Compare(r.Context(), a, b)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"a": a.Slug, "b": b.Slug, "comparison": c})
}

func (s *Server) replaysList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Replay.List(r.Context(), m.ID, 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []replay.Replay{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// StartReplay records a replay and executes it in the background; shared with the UI.
func (s *Server) StartReplay(ctx context.Context, m minds.Mind, snapshotID ids.ID, from, to int64, by string) (replay.Replay, error) {
	rp, err := s.deps.Replay.Begin(ctx, m, snapshotID, from, to, by)
	if err != nil {
		return rp, ValidationError(err.Error())
	}
	s.deps.Audit.Log(ctx, "replay.start", "replay", rp.ID.String(), map[string]any{"mind": m.Slug, "snapshot": snapshotID, "from": rp.FromSeq, "to": rp.ToSeq})
	bg := context.WithoutCancel(ctx)
	go func() {
		if _, err := s.deps.Replay.Execute(bg, rp); err != nil {
			s.deps.Log.Warn("replay failed", "replay", rp.ID, "err", err)
		}
	}()
	return rp, nil
}

func (s *Server) replayStart(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		SnapshotID ids.ID `json:"snapshot_id"`
		FromSeq    int64  `json:"from_seq"`
		ToSeq      int64  `json:"to_seq"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	rp, err := s.StartReplay(r.Context(), m, in.SnapshotID, in.FromSeq, in.ToSeq, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, rp)
}

func (s *Server) replayGet(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad replay id"))
		return
	}
	rp, err := s.deps.Replay.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rp)
}

func (s *Server) experimentsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Experiments.List(r.Context(), 100)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []experiments.Experiment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// ExperimentInput is the create body (API and UI).
type ExperimentInput struct {
	Name       string                 `json:"name"`
	Hypothesis string                 `json:"hypothesis"`
	SnapshotID ids.ID                 `json:"snapshot_id"`
	Definition experiments.Definition `json:"definition"`
}

// CreateExperiment stores a draft; shared with the UI.
func (s *Server) CreateExperiment(ctx context.Context, in ExperimentInput, by string) (experiments.Experiment, error) {
	e, err := s.deps.Experiments.Create(ctx, in.Name, in.Hypothesis, in.SnapshotID, in.Definition, by)
	if err != nil {
		return e, ValidationError(err.Error())
	}
	s.deps.Audit.Log(ctx, "experiment.create", "experiment", e.ID.String(), map[string]any{"name": e.Name, "snapshot": e.SnapshotID})
	return e, nil
}

func (s *Server) experimentCreate(w http.ResponseWriter, r *http.Request) {
	var in ExperimentInput
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	e, err := s.CreateExperiment(r.Context(), in, actor(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) experimentByPath(w http.ResponseWriter, r *http.Request) (experiments.Experiment, bool) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad experiment id"))
		return experiments.Experiment{}, false
	}
	e, err := s.deps.Experiments.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return e, false
	}
	return e, true
}

func (s *Server) experimentGet(w http.ResponseWriter, r *http.Request) {
	if e, ok := s.experimentByPath(w, r); ok {
		writeJSON(w, http.StatusOK, e)
	}
}

// RunExperiment starts a run in the background; shared with the UI.
func (s *Server) RunExperiment(ctx context.Context, e experiments.Experiment, by string) error {
	if e.Status == "running" {
		return ValidationError("experiment is already running")
	}
	s.deps.Audit.Log(ctx, "experiment.run", "experiment", e.ID.String(), map[string]any{"name": e.Name})
	bg := context.WithoutCancel(ctx)
	go func() {
		if _, err := s.deps.Experiments.Run(bg, e.ID, by); err != nil {
			s.deps.Log.Warn("experiment failed", "experiment", e.ID, "err", err)
		}
	}()
	return nil
}

func (s *Server) experimentRun(w http.ResponseWriter, r *http.Request) {
	e, ok := s.experimentByPath(w, r)
	if !ok {
		return
	}
	if err := s.RunExperiment(r.Context(), e, actor(r)); err != nil {
		fail(w, r, err)
		return
	}
	e.Status = "running"
	writeJSON(w, http.StatusAccepted, e)
}

func (s *Server) experimentExport(w http.ResponseWriter, r *http.Request) {
	e, ok := s.experimentByPath(w, r)
	if !ok {
		return
	}
	out, err := s.deps.Experiments.Export(r.Context(), e.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="experiment-`+e.ID.String()[len(e.ID.String())-8:]+`.json"`)
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

func (s *Server) experimentDelete(w http.ResponseWriter, r *http.Request) {
	e, ok := s.experimentByPath(w, r)
	if !ok {
		return
	}
	if err := s.deps.Experiments.Delete(r.Context(), e.ID); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "experiment.delete", "experiment", e.ID.String(), map[string]any{"name": e.Name})
	w.WriteHeader(http.StatusNoContent)
}

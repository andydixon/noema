package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/brain"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
)

func (s *Server) workspaceRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/workspace", require(auth.RoleObserver, s.workspaceGet))
	m.Handle("POST /api/v1/minds/{mind}/cycle", require(auth.RoleOperator, s.workspaceCycle))
	m.Handle("POST /api/v1/minds/{mind}/mode", require(auth.RoleOperator, s.workspaceMode))
	m.Handle("POST /api/v1/minds/{mind}/reset", require(auth.RoleOperator, s.workspaceReset))
}

// WorkspaceView is the payload for the workspace page and API.
type WorkspaceView struct {
	Running    bool            `json:"running"`
	State      workspace.State `json:"state"`
	Candidates []events.Event  `json:"last_candidates"`
}

// Workspace loads the live snapshot when the runtime is running, otherwise the
// persisted one, plus the last cycle's candidate events. Shared with the UI.
func (s *Server) Workspace(ctx context.Context, m minds.Mind) (WorkspaceView, error) {
	var v WorkspaceView
	if rt := s.deps.Brain.Get(m.ID); rt != nil {
		st, err := rt.Snapshot(ctx)
		if err != nil {
			return v, err
		}
		v.Running, v.State = true, st
	} else {
		var raw []byte
		if err := s.deps.DB.QueryRow(ctx, `SELECT state FROM workspace_states WHERE mind_id=$1`, m.ID).Scan(&raw); err == nil {
			json.Unmarshal(raw, &v.State) //nolint:errcheck
		}
	}
	if !v.State.LastCycleID.IsZero() {
		v.Candidates, _ = s.deps.Events.Query(ctx, evstore.Filter{MindID: m.ID, CorrelationID: v.State.LastCycleID, Types: []events.Type{events.ThoughtCandidate}, Ascending: true, Limit: 200})
	}
	if v.Candidates == nil {
		v.Candidates = []events.Event{}
	}
	if v.State.Workspace == nil {
		v.State.Workspace = []workspace.Thought{}
	}
	if v.State.WorkingMemory == nil {
		v.State.WorkingMemory = []workspace.Item{}
	}
	return v, nil
}

func (s *Server) workspaceGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	v, err := s.Workspace(r.Context(), m)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func brainErr(err error) error {
	if errors.Is(err, brain.ErrNotRunning) {
		return ValidationError("mind runtime is not running (is the mind active?)")
	}
	if err != nil && (err.Error() == "mind is paused") {
		return ValidationError(err.Error())
	}
	return err
}

func (s *Server) workspaceCycle(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	if err := s.deps.Brain.Command(r.Context(), m.ID, "cycle", nil); err != nil {
		fail(w, r, brainErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "mind.cycle", "mind", m.ID.String(), nil)
	v, _ := s.Workspace(r.Context(), m)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workspaceMode(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	cmd := map[string]string{"paused": "pause", "awake": "resume"}[in.Mode]
	if cmd == "" {
		fail(w, r, ValidationError("mode must be paused or awake"))
		return
	}
	if err := s.deps.Brain.Command(r.Context(), m.ID, cmd, nil); err != nil {
		fail(w, r, brainErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "mind.mode", "mind", m.ID.String(), map[string]any{"mode": in.Mode})
	v, _ := s.Workspace(r.Context(), m)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workspaceReset(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Workspace     bool `json:"workspace"`
		WorkingMemory bool `json:"working_memory"`
		Loops         bool `json:"loops"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	args := map[string]bool{"workspace": in.Workspace, "working_memory": in.WorkingMemory, "loops": in.Loops}
	if err := s.deps.Brain.Command(r.Context(), m.ID, "reset", args); err != nil {
		fail(w, r, brainErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "mind.reset", "mind", m.ID.String(), args)
	v, _ := s.Workspace(r.Context(), m)
	writeJSON(w, http.StatusOK, v)
}

package api

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/version"
)

// SystemStatus is the payload of GET /api/v1/system/status.
type SystemStatus struct {
	Version       string          `json:"version"`
	UptimeSeconds int64           `json:"uptime_seconds"`
	Database      DatabaseStatus  `json:"database"`
	Migrations    []MigrationInfo `json:"migrations"`
	Goroutines    int             `json:"goroutines"`
	HeapBytes     uint64          `json:"heap_bytes"`
	SysBytes      uint64          `json:"sys_bytes"`
	NumCPU        int             `json:"num_cpu"`
	GoVersion     string          `json:"go_version"`
}

// DatabaseStatus reports connectivity and pool usage.
type DatabaseStatus struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Version   string `json:"version,omitempty"`
	PoolTotal int32  `json:"pool_total"`
	PoolIdle  int32  `json:"pool_idle"`
	PoolMax   int32  `json:"pool_max"`
	PGVector  bool   `json:"pgvector"`
}

// MigrationInfo is one migration's state.
type MigrationInfo struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Applied   bool   `json:"applied"`
	AppliedAt string `json:"applied_at,omitempty"`
	Drift     bool   `json:"drift"`
}

// Status collects the system status; shared with the UI system page.
func (s *Server) Status(ctx context.Context) SystemStatus {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st := SystemStatus{Version: version.Version, UptimeSeconds: int64(time.Since(s.deps.Started).Seconds()),
		Goroutines: runtime.NumGoroutine(), HeapBytes: ms.HeapAlloc, SysBytes: ms.Sys, NumCPU: runtime.NumCPU(), GoVersion: runtime.Version()}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.deps.DB.QueryRow(ctx, `SELECT version()`).Scan(&st.Database.Version); err != nil {
		st.Database.Error = err.Error()
	} else {
		st.Database.OK = true
		s.deps.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&st.Database.PGVector) //nolint:errcheck
	}
	stat := s.deps.DB.Stat()
	st.Database.PoolTotal, st.Database.PoolIdle, st.Database.PoolMax = stat.TotalConns(), stat.IdleConns(), stat.MaxConns()
	if rows, err := storage.Status(ctx, s.deps.DB, s.deps.Migrations); err == nil {
		for _, m := range rows {
			st.Migrations = append(st.Migrations, MigrationInfo{Version: m.Version, Name: m.Name, Applied: m.Applied, AppliedAt: m.AppliedAt, Drift: m.Drift})
		}
	}
	return st
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Status(r.Context()))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte("ok")) //nolint:errcheck
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.deps.DB.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ready")) //nolint:errcheck
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, err := s.deps.Audit.List(r.Context(), before, limit, q.Get("target_kind"), q.Get("target_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

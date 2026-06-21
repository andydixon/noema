// Package app holds the dependency bundle shared by the HTTP API, the web UI
// and the CLI-facing services. It exists so that constructors take one typed
// value instead of a growing parameter list, without resorting to globals.
package app

import (
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/observability"
)

// Deps is everything a handler may need. Fields are added per phase.
type Deps struct {
	Config     config.Config
	Log        *slog.Logger
	DB         *pgxpool.Pool
	Auth       *auth.Service
	Audit      *audit.Logger
	Metrics    *observability.Metrics
	Minds      *minds.Store
	Events     *events.Store
	Brain      *brain.Manager
	Memory     *memory.Store
	Beliefs    *beliefs.Store
	Migrations fs.FS
	Started    time.Time
}

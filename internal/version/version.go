// Package version exposes build information injected at link time.
package version

// Version is set via -ldflags "-X github.com/andydixon/noema/internal/version.Version=...".
var Version = "dev"

// Package web embeds the templates and static assets of the administration
// interface (ADR-0007). Handlers live in internal/ui.
package web

import (
	"embed"
	"io/fs"
)

//go:embed templates static
var files embed.FS

// Templates returns the template tree.
func Templates() fs.FS { f, _ := fs.Sub(files, "templates"); return f }

// Static returns the static asset tree.
func Static() fs.FS { f, _ := fs.Sub(files, "static"); return f }

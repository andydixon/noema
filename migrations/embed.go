// Package migrations embeds the SQL schema migrations applied by
// internal/storage. Files are named NNNN_name.up.sql / NNNN_name.down.sql and
// are never edited once applied anywhere.
package migrations

import "embed"

// FS contains every migration file.
//
//go:embed *.sql
var FS embed.FS

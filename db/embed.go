// Package db embeds the versioned SQL migrations so every binary carries the
// exact schema it was built against.
package db

import "embed"

// Migrations holds db/migrations/*.sql (goose format).
//
//go:embed migrations/*.sql
var Migrations embed.FS

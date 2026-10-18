// Package schema embeds the Archive manager SQLite schema.
package schema

import _ "embed"

//go:embed archive.sql
var archiveSQL string

// AllSQL returns the full Archive manager schema.
func AllSQL() string {
	return archiveSQL
}

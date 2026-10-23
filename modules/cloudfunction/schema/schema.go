// Package schema embeds the FunctionInstance SQLite schema.
package schema

import _ "embed"

// cloudfunctionSQL is the FunctionInstance control-plane schema.
//
//go:embed cloudfunction.sql
var cloudfunctionSQL string

// AllSQL returns the full FunctionInstance schema.
func AllSQL() string {
	return cloudfunctionSQL
}

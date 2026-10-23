// Package schema embeds the CloudNode SQLite schema.
package schema

import _ "embed"

// cloudnodeSQL is the CloudNode control-plane schema.
//
//go:embed cloudnode.sql
var cloudnodeSQL string

// AllSQL returns the full CloudNode schema.
func AllSQL() string {
	return cloudnodeSQL
}

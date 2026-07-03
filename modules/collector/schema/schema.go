// Package schema embeds the Collector SQLite schema.
package schema

import _ "embed"

// collectorSQL is the Collector control-plane schema.
//
//go:embed collector.sql
var collectorSQL string

// AllSQL returns the full Collector schema.
func AllSQL() string {
	return collectorSQL
}

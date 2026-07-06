// Package schema embeds the Factor SQLite schema.
package schema

import _ "embed"

// factorSQL is the Factor control-plane schema.
//
//go:embed factor.sql
var factorSQL string

// AllSQL returns the full Factor schema.
func AllSQL() string {
	return factorSQL
}

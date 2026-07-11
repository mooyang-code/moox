package schema

import _ "embed"

//go:embed strategy.sql
var allSQL string

func AllSQL() string { return allSQL }

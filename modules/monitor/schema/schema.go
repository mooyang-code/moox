package schema

import (
	_ "embed"
)

//go:embed monitor.sql
var monitorSQL string

func SQL() string {
	return monitorSQL
}

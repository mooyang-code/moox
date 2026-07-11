package schema

import (
	"strings"
	"testing"
)

func TestAllSQLContainsCoreTables(t *testing.T) {
	sql := AllSQL()
	for _, name := range []string{"t_strategy_defs", "t_strategy_bindings", "t_strategy_states", "t_strategy_runs", "t_strategy_outbox"} {
		if !strings.Contains(sql, name) {
			t.Fatal(name)
		}
	}
}

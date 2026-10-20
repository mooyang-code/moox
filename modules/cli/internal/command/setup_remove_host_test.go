package command

import (
	"testing"

	"github.com/stretchr/testify/require"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

func TestSetupRemoveHostRefusesControlAndConfiguredHosts(t *testing.T) {
	snapshot := setupSnapshot(t)
	deps := setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }}
	for host, want := range map[string]string{"control": "不能删除控制机", "compute": "还在 moox.toml 里", "": "--host 必填"} {
		cmd := newSetupRemoveHostCommand(deps)
		cmd.SetArgs([]string{"--host", host})
		require.ErrorContains(t, cmd.Execute(), want, host)
	}
}

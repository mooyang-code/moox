package command

import (
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestControlDeployOptionsPreservesObservabilityPolicyExplicitness(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		policy := setupconfig.Observability{DeliverPolicy: "all", DeliverPolicyExplicit: explicit}
		snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{Observability: policy}}
		require.Equal(t, policy, controlDeployOptions(snapshot, t.TempDir()).Observability)
	}
}

package deploy

import (
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestObservabilityCommandEnv(t *testing.T) {
	base := []string{"PATH=/bin", "MOOX_OBSERVABILITY_DELIVER_POLICY=new"}
	env, err := observabilityCommandEnv(base, setupconfig.Observability{DeliverPolicy: "all"})
	require.NoError(t, err)
	require.Equal(t, []string{"PATH=/bin"}, env)
	env, err = observabilityCommandEnv(base, setupconfig.Observability{DeliverPolicy: "all", DeliverPolicyExplicit: true})
	require.NoError(t, err)
	require.Contains(t, env, "MOOX_OBSERVABILITY_DELIVER_POLICY=all")
	_, err = observabilityCommandEnv(base, setupconfig.Observability{DeliverPolicy: "typo", DeliverPolicyExplicit: true})
	require.Error(t, err)
}

package command

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"

	"github.com/stretchr/testify/require"
)

func TestDefaultSetupEnsureSCFPlanIsANoopWhenDisabled(t *testing.T) {
	summary, err := defaultSetupEnsureSCFPlan(context.Background(), &setupSnapshot{Manifest: &topology.Config{}}, "")
	require.NoError(t, err)
	require.Nil(t, summary)
}

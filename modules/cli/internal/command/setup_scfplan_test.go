package command

import (
	"context"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

func TestDefaultSetupEnsureSCFPlanIsANoopWhenDisabled(t *testing.T) {
	summary, err := defaultSetupEnsureSCFPlan(context.Background(), &setupconfig.Snapshot{})
	require.NoError(t, err)
	require.Nil(t, summary)
}

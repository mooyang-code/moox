package main

import (
	"context"
	"testing"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/stretchr/testify/require"
)

func TestStartProductionRuntimeRequiresSpaceID(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "")
	require.ErrorContains(t, startProductionRuntime(context.Background(), runtimeapp.DefaultConfig()), "MOOX_SPACE_ID")
}

func TestStartProductionRuntimeBlocksInCloudFunctionStart(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Chdir(t.TempDir())
	oldRegister := registerCloudFunction
	t.Cleanup(func() { registerCloudFunction = oldRegister })
	called := false
	registerCloudFunction = func() { called = true }
	require.NoError(t, startProductionRuntime(context.Background(), runtimeapp.DefaultConfig()))
	require.True(t, called)
}

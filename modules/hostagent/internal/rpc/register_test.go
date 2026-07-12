package rpc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/hostagent/internal/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
)

func TestRegister_InvalidInputs_ShouldReturnError(t *testing.T) {
	a := &app.Agent{}
	assert.Error(t, Register(nil, a))
	assert.Error(t, Register(nil, nil))
}

func TestRegister_ConfiguredService_ShouldSucceed(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	configDir := filepath.Join(wd, "..", "..", "config")
	require.NoError(t, os.Chdir(configDir))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	require.NoError(t, Register(trpc.NewServer(), &app.Agent{}))
}

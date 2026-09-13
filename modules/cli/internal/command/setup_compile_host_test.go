package command

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunSetupBuildLinuxRejectsUnknownModule(t *testing.T) {
	err := runSetupBuildLinux(t.Context(), nil, "./moox.toml", "trade")
	require.EqualError(t, err, `unsupported linux CGO module "trade"`)
}

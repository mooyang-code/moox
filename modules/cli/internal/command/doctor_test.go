package command

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/config"

	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

func TestDoctorRegistersOnlyV1Modes(t *testing.T) {
	cmd := newDoctorCommand(doctorCommandDeps{})
	names := []string{}
	for _, child := range cmd.Commands() {
		names = append(names, child.Name())
	}
	require.Equal(t, []string{"alerts", "bootstrap", "diagnose"}, names)
	for _, forbidden := range []string{"full", "get", "list", "cancel", "rerun"} {
		found, _, err := cmd.Find([]string{forbidden})
		require.Error(t, err)
		require.NotEqual(t, forbidden, found.Name())
	}
}

func TestValidateDoctorFlagsAndExitCodes(t *testing.T) {
	require.Error(t, validateDoctorFlags("yaml", "", nil))
	require.Error(t, validateDoctorFlags("json", "", []string{"same", "same"}))
	require.Equal(t, 0, doctorExitCode(core.ConclusionHealthy))
	require.Equal(t, 1, doctorExitCode(core.ConclusionDegraded))
	require.Equal(t, 2, doctorExitCode(core.ConclusionUnhealthy))
	require.Equal(t, 3, doctorExitCode(core.ConclusionInconclusive))
}

func TestDoctorRejectsInvalidLocalConfigBeforeOpeningGateway(t *testing.T) {
	failure := errors.New("invalid Doctor configuration")
	cmd := newDoctorModeCommand("diagnose", doctorCommandDeps{loadConfig: func() (*config.Config, error) { return nil, failure }})
	cmd.SetArgs([]string{})
	cmd.SetContext(t.Context())
	require.ErrorIs(t, cmd.Execute(), failure)
}

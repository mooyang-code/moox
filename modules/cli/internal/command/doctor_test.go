package command

import (
	"testing"

	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/stretchr/testify/require"
)

func TestDoctorRegistersOnlyV1Modes(t *testing.T) {
	cmd := newDoctorCommand(doctorCommandDeps{})
	names := []string{}
	for _, child := range cmd.Commands() {
		names = append(names, child.Name())
	}
	require.Equal(t, []string{"bootstrap", "diagnose"}, names)
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

func TestStorageMetadataClientUsesReadOnlySignedIdentity(t *testing.T) {
	client := newSignedStorageMetadataClient("http://127.0.0.1:20200", "storage-secret")
	signed, ok := client.(*signedStorageMetadataClient)
	require.True(t, ok)
	require.Equal(t, "storage-metadata", signed.auth.GetAppId())
	require.Len(t, signed.auth.GetAppKey(), 64)
	require.NotNil(t, signed.proxy)
}

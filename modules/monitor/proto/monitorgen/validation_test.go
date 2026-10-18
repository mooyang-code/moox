package monitorpb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorContextRequestRejectsEmptyAndDuplicateSelections(t *testing.T) {
	require.Error(t, (&GetDoctorContextReq{ComponentIds: []string{""}}).Validate())
	require.Error(t, (&GetDoctorContextReq{ComponentIds: []string{"monitor", "monitor"}}).Validate())
	require.Error(t, (&GetDoctorContextReq{HealthCheckIds: []string{"factor", "factor"}}).Validate())
}

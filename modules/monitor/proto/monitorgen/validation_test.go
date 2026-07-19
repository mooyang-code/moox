package monitorpb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorContextRequestRejectsEmptyAndDuplicateSelections(t *testing.T) {
	require.Error(t, (&GetDoctorContextReq{ComponentIds: []string{""}}).Validate())
	require.Error(t, (&GetDoctorContextReq{ComponentIds: []string{"moox_monitor", "moox_monitor"}}).Validate())
	require.Error(t, (&GetDoctorContextReq{PipelineIds: []string{"factor", "factor"}}).Validate())
}

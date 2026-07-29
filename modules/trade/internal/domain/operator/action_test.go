package operator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperatorActionValidation(t *testing.T) {
	action := Action{
		SpaceID: "space-1", ID: "action-1",
		LogicalAccountID: "logical-1",
		Type:             ActionFlatten, Reason: "risk",
		RequestJSON: `{}`, Status: StatusRunning,
	}
	require.NoError(t, action.Validate())

	action.Reason = ""
	require.ErrorIs(t, action.Validate(), ErrInvalidAction)
	action.Reason = "risk"
	action.Type = "UNKNOWN"
	require.ErrorIs(t, action.Validate(), ErrInvalidAction)
	action.Type = ActionFlatten
	action.Status = "QUEUED"
	require.ErrorIs(t, action.Validate(), ErrInvalidAction)
}

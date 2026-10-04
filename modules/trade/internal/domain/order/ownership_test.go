package order

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrderOwnershipRequiresTrustedShape(t *testing.T) {
	runnerID := "runner-1"
	for _, test := range []struct {
		name    string
		owner   OrderOwner
		wantErr bool
	}{
		{
			name: "target",
			owner: OrderOwner{
				Type: OwnerTarget, OwnerID: "target-1",
				LogicalAccountID: "logical-1", RunnerID: &runnerID,
			},
		},
		{
			name: "operator",
			owner: OrderOwner{
				Type: OwnerOperator, OwnerID: "action-1",
				LogicalAccountID: "logical-1",
			},
		},
		{
			name: "external assigned to logical account",
			owner: OrderOwner{
				Type: OwnerExternal, OwnerID: "exchange-order-1",
				LogicalAccountID: "logical-1",
			},
		},
		{
			name: "external unassigned",
			owner: OrderOwner{
				Type: OwnerExternal, OwnerID: "exchange-order-1",
			},
		},
		{
			name: "unknown type",
			owner: OrderOwner{
				Type: "RPC", OwnerID: "request-1",
				LogicalAccountID: "logical-1",
			},
			wantErr: true,
		},
		{
			name: "target without runner",
			owner: OrderOwner{
				Type: OwnerTarget, OwnerID: "target-1",
				LogicalAccountID: "logical-1",
			},
			wantErr: true,
		},
		{
			name: "operator with runner",
			owner: OrderOwner{
				Type: OwnerOperator, OwnerID: "action-1",
				LogicalAccountID: "logical-1", RunnerID: &runnerID,
			},
			wantErr: true,
		},
		{
			name: "missing owner id",
			owner: OrderOwner{
				Type: OwnerExternal,
			},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.owner.Validate()
			if test.wantErr {
				require.ErrorIs(t, err, ErrInvalidSpec)
				return
			}
			require.NoError(t, err)
		})
	}
}

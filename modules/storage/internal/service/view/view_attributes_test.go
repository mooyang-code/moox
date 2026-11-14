package view

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 维护顺序由 View 的 maintenance_priority 决定：越小越先，没有设置（0）最先，同级按空间与 ID。
func TestMaintenanceOrderFollowsDeclaredPriority(t *testing.T) {
	views := []*pb.View{
		{SpaceId: "s", ViewId: "view_late", MaintenancePriority: 90},
		{SpaceId: "s", ViewId: "view_result", MaintenancePriority: 10},
		{SpaceId: "s", ViewId: "view_first"},
		{SpaceId: "s", ViewId: "view_default"},
	}
	sortViewsForMaintenance(views)
	var order []string
	for _, view := range views {
		order = append(order, view.GetViewId())
	}
	require.Equal(t, []string{"view_default", "view_first", "view_result", "view_late"}, order)
}

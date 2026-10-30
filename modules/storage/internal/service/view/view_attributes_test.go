package view

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 维护顺序由 View 属性 maintenance_priority 声明：整数越小越先，缺失或非法取默认值 50，同级按空间与 ID。
func TestMaintenanceOrderFollowsDeclaredPriority(t *testing.T) {
	views := []*pb.View{
		{SpaceId: "s", ViewId: "view_default"},
		{SpaceId: "s", ViewId: "view_late", Attributes: map[string]string{"maintenance_priority": "90"}},
		{SpaceId: "s", ViewId: "view_result", Attributes: map[string]string{"maintenance_priority": "10"}},
		{SpaceId: "s", ViewId: "view_first", Attributes: map[string]string{"maintenance_priority": "0"}},
		{SpaceId: "s", ViewId: "view_bad", Attributes: map[string]string{"maintenance_priority": "soon"}},
	}
	sortViewsForMaintenance(views)
	var order []string
	for _, view := range views {
		order = append(order, view.GetViewId())
	}
	require.Equal(t, []string{"view_first", "view_result", "view_bad", "view_default", "view_late"}, order)
}

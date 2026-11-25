package view

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 维护顺序由数据集的 maintenance_priority 决定：越小越先，没有设置（0）最先，同级按空间与数据集 ID。
func TestMaintenanceOrderFollowsDeclaredPriority(t *testing.T) {
	specs := []*viewSpec{
		{SpaceID: "s", DatasetID: "dataset_late", MaintenancePriority: 90},
		{SpaceID: "s", DatasetID: "dataset_result", MaintenancePriority: 10},
		{SpaceID: "s", DatasetID: "dataset_first"},
		{SpaceID: "s", DatasetID: "dataset_default"},
	}
	sortSpecsForMaintenance(specs)
	var order []string
	for _, spec := range specs {
		order = append(order, spec.DatasetID)
	}
	require.Equal(t, []string{"dataset_default", "dataset_first", "dataset_result", "dataset_late"}, order)
}

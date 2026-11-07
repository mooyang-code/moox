package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// 已有任务不能在单市场与统一数据集之间切换：编辑时增删标签（现货变现货加永续、统一数据集去掉一个市场）一律由后端拒绝。
func TestUpdateTaskRejectsCrossMarketTagChange(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "single", TaskName: "single", DataType: "kline", Enabled: true, TagIDs: []string{"binance_spot"},
		CollectParams: `{"target_dataset_id":"bars_single","frequency":"1m"}`, ResultDatasetID: "bars_single", ResultViewID: "view_single",
	}))
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "unified", TaskName: "unified", DataType: "kline", Enabled: true, TagIDs: []string{"binance_spot", "binance_swap"},
		CollectParams: `{"target_dataset_id":"bars_unified","frequency":"1m"}`, ResultDatasetID: "bars_unified", ResultViewID: "view_unified",
	}))
	service := &Service{taskRepo: db.Tasks(), datasetSrc: tagValidationSource{validationDatasetSource: validationDatasetSource{
		"bars_single":  storagesource.DatasetInfo{SubjectTags: []string{"binance_spot"}},
		"bars_unified": storagesource.DatasetInfo{SubjectTags: []string{"binance_spot", "binance_swap"}},
	}, tags: map[string]*storagepb.Tag{
		"binance_spot": {TagId: "binance_spot", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}},
		"binance_swap": {TagId: "binance_swap", Attributes: map[string]string{"sync.source": "binance", "market_type": "swap"}},
	}}}

	update := func(taskID string, tags ...any) *pb.UpdateTaskRsp {
		params, err := structpb.NewStruct(map[string]any{"frequency": "1m", "subject_tags": tags})
		require.NoError(t, err)
		rsp, callErr := service.UpdateTask(ctx, &pb.UpdateTaskReq{SpaceId: "crypto", TaskId: taskID, Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskId: taskID, TaskName: taskID, DataType: "kline", CollectParams: params,
		}})
		require.NoError(t, callErr)
		return rsp
	}
	for name, rsp := range map[string]*pb.UpdateTaskRsp{
		"单市场加一个市场":    update("single", "binance_spot", "binance_swap"),
		"单市场换成另一个市场":  update("single", "binance_swap"),
		"统一数据集去掉一个市场": update("unified", "binance_spot"),
	} {
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
		require.NotEmpty(t, rsp.GetRetInfo().GetMsg(), name)
	}
}

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

// 已有任务不能换市场：编辑时增删标签（现货加上永续、现货换成永续）一律由后端拒绝。
func TestUpdateTaskRejectsCrossMarketTagChange(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "single", TaskName: "single", DataType: "kline", Enabled: true, TagIDs: []string{"binance_spot"},
		CollectParams: `{"target_dataset_id":"bars_single","frequency":"1m"}`, ResultDatasetID: "bars_single",
	}))
	service := &Service{taskRepo: db.Tasks(), datasetSrc: tagValidationSource{validationDatasetSource: validationDatasetSource{
		"bars_single": storagesource.DatasetInfo{SubjectTags: []string{"binance_spot"}},
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
		"单市场加一个市场":   update("single", "binance_spot", "binance_swap"),
		"单市场换成另一个市场": update("single", "binance_swap"),
	} {
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
		require.NotEmpty(t, rsp.GetRetInfo().GetMsg(), name)
	}
}

// 修改任务不改采集方式：请求里带的采集方式与现值不同就拒绝并指向切换命令，原样带回现值照常放行。
func TestUpdateTaskRejectsAnIngestChange(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "spot", TaskName: "spot", DataType: "kline", Ingest: domain.IngestStreaming, Enabled: true, TagIDs: []string{"binance_spot"},
		CollectParams: `{"target_dataset_id":"bars_spot","frequency":"1m"}`, ResultDatasetID: "bars_spot",
	}))
	service := &Service{taskRepo: db.Tasks(), datasetSrc: tagValidationSource{validationDatasetSource: validationDatasetSource{
		"bars_spot": storagesource.DatasetInfo{SubjectTags: []string{"binance_spot"}},
	}, tags: map[string]*storagepb.Tag{
		"binance_spot": {TagId: "binance_spot", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}},
	}}}
	update := func(ingest string) *pb.UpdateTaskRsp {
		params, err := structpb.NewStruct(map[string]any{"frequency": "1m", "subject_tags": []any{"binance_spot"}})
		require.NoError(t, err)
		rsp, callErr := service.UpdateTask(ctx, &pb.UpdateTaskReq{SpaceId: "crypto", TaskId: "spot", Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskId: "spot", TaskName: "spot", DataType: "kline", Ingest: ingest, CollectParams: params,
		}})
		require.NoError(t, callErr)
		return rsp
	}

	rsp := update("scf")
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "moox-cli collector ingest-mode set")
	stored, err := db.Tasks().GetByTaskID(ctx, "crypto", "spot")
	require.NoError(t, err)
	require.Equal(t, domain.IngestStreaming, stored.Ingest)

	for _, ingest := range []string{"", " Stream "} {
		rsp = update(ingest)
		// 夹具里没有准备结果数据集，请求会在后面的校验停下；这里只确认它过了采集方式这一关。
		require.NotContains(t, rsp.GetRetInfo().GetMsg(), "ingest-mode", "ingest=%q", ingest)
	}
}

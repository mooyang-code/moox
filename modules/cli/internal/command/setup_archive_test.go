package command

import (
	"context"
	"errors"
	"fmt"
	"testing"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeArchiveSetupGateway struct {
	datasetPages [][]*storagepb.Dataset
	taskPages    [][]*archivepb.ArchiveTask
	created      []string
	conflictKeys map[string]string
	failKeys     map[string]string
	lastSpace    string
}

func (f *fakeArchiveSetupGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	switch service + "/" + method {
	case metadataSvc + "/ListDatasets":
		r := req.(*storagepb.ListDatasetsReq)
		f.lastSpace = r.GetSpaceId()
		out := rsp.(*storagepb.ListDatasetsRsp)
		out.RetInfo = &commonpb.RetInfo{}
		idx := int(r.GetPage().GetPage()) - 1
		if idx >= 0 && idx < len(f.datasetPages) {
			out.Datasets = f.datasetPages[idx]
			out.PageResult = &commonpb.PageResult{Page: r.GetPage().GetPage(), HasMore: idx+1 < len(f.datasetPages)}
		}
		return nil
	case archiveMgrService + "/ListArchiveTasks":
		r := req.(*archivepb.ListArchiveTasksReq)
		out := rsp.(*archivepb.ListArchiveTasksRsp)
		out.RetInfo = &commonpb.RetInfo{}
		idx := int(r.GetPage().GetPage()) - 1
		if idx >= 0 && idx < len(f.taskPages) {
			out.Tasks = f.taskPages[idx]
			out.PageResult = &commonpb.PageResult{Page: r.GetPage().GetPage(), HasMore: idx+1 < len(f.taskPages)}
		}
		return nil
	case archiveMgrService + "/CreateArchiveTask":
		r := req.(*archivepb.CreateArchiveTaskReq)
		key := r.GetSpaceId() + "/" + r.GetDatasetId()
		out := rsp.(*archivepb.CreateArchiveTaskRsp)
		if msg, ok := f.conflictKeys[key]; ok {
			out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: msg}
			return nil
		}
		if msg, ok := f.failKeys[key]; ok {
			out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: msg}
			return nil
		}
		f.created = append(f.created, key)
		out.RetInfo = &commonpb.RetInfo{}
		return nil
	default:
		return fmt.Errorf("unexpected call %s/%s", service, method)
	}
}

func TestEnsureSetupArchiveTasksCreatesMissingAndSkipsNonArchivable(t *testing.T) {
	gw := &fakeArchiveSetupGateway{
		datasetPages: [][]*storagepb.Dataset{
			{
				{SpaceId: "crypto", DatasetId: "ds_spot", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: "840h"},
				{SpaceId: "crypto", DatasetId: "ds_swap", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: "840h"},
				// forever / 非时序 / 停用 / 空保留期均不建任务。
				{SpaceId: "stockcn", DatasetId: "ds_daily", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: "forever"},
				{SpaceId: "stockcn", DatasetId: "ds_fin", DataKind: storagepb.DataKind_DATA_KIND_RECORD, Status: "active", Retention: "840h"},
				{SpaceId: "stockcn", DatasetId: "ds_disabled", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "disabled", Retention: "840h"},
				{SpaceId: "stockcn", DatasetId: "ds_empty", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: ""},
			},
			{
				{SpaceId: "stockcn", DatasetId: "ds_cn_5m", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: "7200h"},
				{SpaceId: "stockhk", DatasetId: "ds_hk_5m", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: "7200h"},
			},
		},
		taskPages: [][]*archivepb.ArchiveTask{
			{{SpaceId: "crypto", DatasetId: "ds_spot"}},
		},
		conflictKeys: map[string]string{
			// 并发创建已存在视为 unchanged，不报错。
			"stockcn/ds_cn_5m": "数据集 ds_cn_5m 已经有归档任务",
		},
	}

	summary, err := ensureSetupArchiveTasks(context.Background(), gw, "")
	require.NoError(t, err)
	require.Equal(t, &setupArchiveTasksSummary{Enabled: true, Planned: 4, Created: 2, Unchanged: 2}, summary)
	assert.Equal(t, []string{"crypto/ds_swap", "stockhk/ds_hk_5m"}, gw.created)
}

func TestEnsureSetupArchiveTasksRejectsRealConflictAndErrors(t *testing.T) {
	dirConflict := &fakeArchiveSetupGateway{
		datasetPages: [][]*storagepb.Dataset{
			{{SpaceId: "crypto", DatasetId: "ds_a", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: "840h"}},
		},
		conflictKeys: map[string]string{
			"crypto/ds_a": "目录名 xxx 已被其他任务占用",
		},
	}
	_, err := ensureSetupArchiveTasks(context.Background(), dirConflict, "crypto")
	require.ErrorContains(t, err, "目录名")

	internalErr := &fakeArchiveSetupGateway{
		datasetPages: [][]*storagepb.Dataset{
			{{SpaceId: "crypto", DatasetId: "ds_a", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Retention: "840h"}},
		},
		failKeys: map[string]string{"crypto/ds_a": "db busy"},
	}
	_, err = ensureSetupArchiveTasks(context.Background(), internalErr, "crypto")
	require.ErrorContains(t, err, "db busy")
}

func TestDefaultSetupEnsureArchiveTasksSkipsWithoutEnabledArchiveMgr(t *testing.T) {
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) {
		return nil, errors.New("should not open gateway when archive-mgr is absent or disabled")
	}
	t.Cleanup(func() { openCommandGateway = previous })

	// 没有部署 archive-mgr。
	snapshot := testSetupSnapshot(t)
	snapshot.Manifest.Deployments["control"] = []string{"admin", "eventbus", "collector", "storage-primary"}
	summary, err := defaultSetupEnsureArchiveTasks(context.Background(), snapshot, "")
	require.NoError(t, err)
	require.Nil(t, summary)

	// 部署了但已停用 archive-mgr。
	disabled := testSetupSnapshot(t)
	disabled.Manifest.DeploymentsDisabled = map[string][]string{"control": {"archive-mgr"}}
	summary, err = defaultSetupEnsureArchiveTasks(context.Background(), disabled, "")
	require.NoError(t, err)
	require.Nil(t, summary)
}

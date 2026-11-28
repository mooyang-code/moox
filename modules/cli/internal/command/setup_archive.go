package command

import (
	"context"
	"fmt"
	"sort"
	"strings"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

const (
	setupArchivePageSize = 200
	setupArchiveMaxPages = 50
)

// setupArchiveTasksSummary 是初始化时自动补齐归档任务的结果。
type setupArchiveTasksSummary struct {
	Enabled   bool `json:"enabled"`
	Planned   int  `json:"planned"`
	Created   int  `json:"created"`
	Unchanged int  `json:"unchanged"`
}

type archivableDatasetRef struct {
	spaceID   string
	datasetID string
}

func (r archivableDatasetRef) key() string {
	return r.spaceID + "/" + r.datasetID
}

// defaultSetupEnsureArchiveTasks 为已激活且保留期有限的时序数据集幂等创建归档任务：
//   - spaceID 为空时覆盖全部业务空间（setup init 在全部组件就绪后调用，覆盖 Collector 种子建好的原始 K 线数据集）；
//   - spaceID 非空时覆盖该空间（setup start 在导入默认因子任务后调用，补齐新建的因子结果数据集）。
// 未部署或已停用 archive-mgr 时跳过并返回 nil。
func defaultSetupEnsureArchiveTasks(ctx context.Context, snapshot *setupSnapshot, spaceID string) (*setupArchiveTasksSummary, error) {
	if snapshot == nil || snapshot.Manifest == nil {
		return nil, fmt.Errorf("archive_tasks: setup configuration is missing")
	}
	host := snapshot.Manifest.ComponentHost("archive-mgr")
	if host == "" || !snapshot.Manifest.UnitEnabled(host, "archive-mgr") {
		return nil, nil
	}
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return nil, fmt.Errorf("archive_tasks: open gateway: %w", err)
	}
	defer gateway.Close()
	return ensureSetupArchiveTasks(ctx, gateway, strings.TrimSpace(spaceID))
}

// ensureSetupArchiveTasks 比对 Storage 里的有限保留期时序数据集与已有归档任务，为缺失的数据集按默认参数建任务。
func ensureSetupArchiveTasks(ctx context.Context, gateway gatewayclient.Invoker, spaceID string) (*setupArchiveTasksSummary, error) {
	datasets, err := listArchivableDatasetsForSetup(ctx, gateway, spaceID)
	if err != nil {
		return nil, err
	}
	existing, err := listExistingArchiveTasksForSetup(ctx, gateway, spaceID)
	if err != nil {
		return nil, err
	}
	summary := &setupArchiveTasksSummary{Enabled: true, Planned: len(datasets)}
	for _, ds := range datasets {
		if existing[ds.key()] {
			summary.Unchanged++
			continue
		}
		rsp := &archivepb.CreateArchiveTaskRsp{}
		if err := gateway.Invoke(ctx, archiveMgrService, "CreateArchiveTask", &archivepb.CreateArchiveTaskReq{
			SpaceId: ds.spaceID, DatasetId: ds.datasetID,
		}, rsp); err != nil {
			return nil, fmt.Errorf("create archive task %s: %w", ds.key(), err)
		}
		info := rsp.GetRetInfo()
		switch {
		case info.GetCode() == commonpb.ErrorCode_SUCCESS:
			summary.Created++
		case info.GetCode() == commonpb.ErrorCode_CONFLICT && strings.Contains(info.GetMsg(), "已经有归档任务"):
			summary.Unchanged++
		default:
			return nil, fmt.Errorf("create archive task %s: %s (code %d)", ds.key(), info.GetMsg(), info.GetCode())
		}
	}
	return summary, nil
}

// listArchivableDatasetsForSetup 列出应当建立归档任务的数据集，筛选口径与 Monitor 的 archive_coverage 检查完全一致：
// 已激活（status == active）、时序类型（DATA_KIND_TIME_SERIES）、有限保留期（retention 非空且不是 forever）。
func listArchivableDatasetsForSetup(ctx context.Context, gateway gatewayclient.Invoker, spaceID string) ([]archivableDatasetRef, error) {
	seen := map[string]struct{}{}
	var out []archivableDatasetRef
	for page := uint32(1); page <= setupArchiveMaxPages; page++ {
		rsp := &storagepb.ListDatasetsRsp{}
		if err := gateway.Invoke(ctx, metadataSvc, "ListDatasets", &storagepb.ListDatasetsReq{
			SpaceId: spaceID,
			Page:    &commonpb.Page{Page: page, Size: setupArchivePageSize},
		}, rsp); err != nil {
			return nil, fmt.Errorf("list datasets: %w", err)
		}
		if info := rsp.GetRetInfo(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list datasets: %s (code %d)", info.GetMsg(), info.GetCode())
		}
		for _, d := range rsp.GetDatasets() {
			if d == nil || d.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES || !strings.EqualFold(d.GetStatus(), "active") {
				continue
			}
			retention := strings.TrimSpace(d.GetRetention())
			if retention == "" || strings.EqualFold(retention, "forever") {
				continue
			}
			ref := archivableDatasetRef{spaceID: strings.TrimSpace(d.GetSpaceId()), datasetID: strings.TrimSpace(d.GetDatasetId())}
			if ref.spaceID == "" || ref.datasetID == "" {
				continue
			}
			if _, ok := seen[ref.key()]; !ok {
				seen[ref.key()] = struct{}{}
				out = append(out, ref)
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
			return out, nil
		}
	}
	return nil, fmt.Errorf("list datasets: exceeds %d pages", setupArchiveMaxPages)
}

func listExistingArchiveTasksForSetup(ctx context.Context, gateway gatewayclient.Invoker, spaceID string) (map[string]bool, error) {
	existing := map[string]bool{}
	for page := uint32(1); page <= setupArchiveMaxPages; page++ {
		rsp := &archivepb.ListArchiveTasksRsp{}
		if err := archiveCall(ctx, gateway, "ListArchiveTasks", &archivepb.ListArchiveTasksReq{
			SpaceId: spaceID,
			Page:    &commonpb.Page{Page: page, Size: setupArchivePageSize},
		}, rsp, rsp.GetRetInfo); err != nil {
			return nil, err
		}
		for _, task := range rsp.GetTasks() {
			if task == nil {
				continue
			}
			space, dataset := strings.TrimSpace(task.GetSpaceId()), strings.TrimSpace(task.GetDatasetId())
			if space != "" && dataset != "" {
				existing[space+"/"+dataset] = true
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			return existing, nil
		}
	}
	return nil, fmt.Errorf("list archive tasks: exceeds %d pages", setupArchiveMaxPages)
}

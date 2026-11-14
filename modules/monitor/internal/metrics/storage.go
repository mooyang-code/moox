package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorname"
	"trpc.group/trpc-go/trpc-go/client"
)

// MetadataClient 是 Monitor 读取 Storage 元数据所需的最小接口。服务指标只保存最新值（SQLite），
// 不再写入 Storage 历史，因此这里没有任何写方法。
type MetadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
	ListDatasets(context.Context, *storagepb.ListDatasetsReq, ...client.Option) (*storagepb.ListDatasetsRsp, error)
	ListTags(context.Context, *storagepb.ListTagsReq, ...client.Option) (*storagepb.ListTagsRsp, error)
}

// StorageAdapter 只读 Storage 元数据：数据集的有效标的、展示名称和标签，带短期缓存。
type StorageAdapter struct {
	metadata         MetadataClient
	auth             *commonpb.AuthInfo
	subjectMu        sync.Mutex
	subjectRefreshMu sync.Mutex
	subjects         map[string]cachedSubjectCatalog
	nameMu           sync.Mutex
	datasetInfos     map[string]cachedDatasetInfo
	goneMu           sync.Mutex
	datasetGone      map[string]cachedDatasetGone
}

type cachedDatasetGone struct {
	loadedAt time.Time
	gone     bool
}

// cachedDatasetInfo 是数据集的展示名称和品种属性（market_type：spot、swap、equity……）。
type cachedDatasetInfo struct {
	loadedAt   time.Time
	name       string
	marketType string
}

const (
	datasetSubjectPageSize = 1000
	maxDatasetSubjects     = 100000
	subjectCatalogTTL      = time.Minute
	datasetInfoTTL         = 10 * time.Minute
	datasetGoneTTL         = time.Minute
)

type cachedSubjectCatalog struct {
	loadedAt time.Time
	ids      map[string]struct{}
}

func NewStorageAdapter(metadata MetadataClient) *StorageAdapter {
	return &StorageAdapter{metadata: metadata, auth: storageauth.Primary("monitor"), subjects: make(map[string]cachedSubjectCatalog), datasetInfos: make(map[string]cachedDatasetInfo), datasetGone: make(map[string]cachedDatasetGone)}
}

func isActive(status string) bool { return strings.EqualFold(strings.TrimSpace(status), "active") }
func storageOK(action string, ret *commonpb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}

// ListActiveDatasetSubjects returns the authoritative active subject set for a
// monitored dataset. Monitor uses this to ignore metric series left behind by
// retired instruments instead of treating their old watermarks as failures.
// Successful catalogs are cached briefly so a 30-second monitor evaluation does
// not turn metadata into another high-frequency dependency.
func (a *StorageAdapter) ListActiveDatasetSubjects(ctx context.Context, spaceID, datasetID string) (map[string]struct{}, error) {
	if a == nil || a.metadata == nil {
		return nil, errors.New("metrics storage metadata client is not initialized")
	}
	key := strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(datasetID)
	now := time.Now().UTC()
	a.subjectRefreshMu.Lock()
	defer a.subjectRefreshMu.Unlock()
	a.subjectMu.Lock()
	if cached, ok := a.subjects[key]; ok && now.Sub(cached.loadedAt) < subjectCatalogTTL {
		ids := cloneSubjectSet(cached.ids)
		a.subjectMu.Unlock()
		return ids, nil
	}
	a.subjectMu.Unlock()

	ids := make(map[string]struct{})
	rowCount := 0
	for page := uint32(1); ; page++ {
		rsp, err := a.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			AuthInfo: a.auth, SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: page, Size: datasetSubjectPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list monitored dataset subjects: %w", err)
		}
		if err := storageOK("list monitored dataset subjects", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		pageSubjects := rsp.GetDatasetSubjects()
		rowCount += len(pageSubjects)
		if rowCount > maxDatasetSubjects*2 {
			return nil, fmt.Errorf("monitored dataset subject catalog exceeds %d rows", maxDatasetSubjects*2)
		}
		for _, subject := range pageSubjects {
			if subject == nil || !isActive(subject.GetStatus()) || strings.TrimSpace(subject.GetSubjectId()) == "" {
				continue
			}
			if len(ids) >= maxDatasetSubjects {
				return nil, fmt.Errorf("monitored dataset subject catalog exceeds %d subjects", maxDatasetSubjects)
			}
			ids[strings.TrimSpace(subject.GetSubjectId())] = struct{}{}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
		if len(pageSubjects) == 0 {
			return nil, errors.New("monitored dataset subject catalog pagination did not advance")
		}
	}
	a.subjectMu.Lock()
	a.subjects[key] = cachedSubjectCatalog{loadedAt: now, ids: cloneSubjectSet(ids)}
	a.subjectMu.Unlock()
	return ids, nil
}

// DatasetDisplayName returns the name people gave a dataset, such as
// "现货小时K线", for alert text. It returns "" when the name is unknown.
func (a *StorageAdapter) DatasetDisplayName(ctx context.Context, spaceID, datasetID string) string {
	return a.datasetInfo(ctx, spaceID, datasetID).name
}

// DatasetMarketType 返回数据集的品种属性 market_type（小写）；没有这个属性或读不到时为空。
func (a *StorageAdapter) DatasetMarketType(ctx context.Context, spaceID, datasetID string) string {
	return a.datasetInfo(ctx, spaceID, datasetID).marketType
}

// datasetInfo 读数据集的名称和品种属性，缓存 datasetInfoTTL；读取失败时返回上一次读到的值（没有则为空）。
func (a *StorageAdapter) datasetInfo(ctx context.Context, spaceID, datasetID string) cachedDatasetInfo {
	if a == nil || a.metadata == nil || strings.TrimSpace(datasetID) == "" {
		return cachedDatasetInfo{}
	}
	key := strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(datasetID)
	now := time.Now()
	a.nameMu.Lock()
	cached, ok := a.datasetInfos[key]
	a.nameMu.Unlock()
	if ok && now.Sub(cached.loadedAt) < datasetInfoTTL {
		return cached
	}
	rsp, err := a.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: a.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil || storageOK("get dataset", rsp.GetRetInfo()) != nil {
		return cached
	}
	info := cachedDatasetInfo{
		loadedAt: now, name: strings.TrimSpace(rsp.GetDataset().GetName()),
		marketType: strings.ToLower(strings.TrimSpace(rsp.GetDataset().GetAttributes()["market_type"])),
	}
	a.nameMu.Lock()
	a.datasetInfos[key] = info
	a.nameMu.Unlock()
	return info
}

// DatasetGone 报告数据集在 Storage 里已经不存在（被删除）。只有 Storage 明确回答“数据集不存在”才为真：
// 读取失败、超时、没有配置元数据时都按“还在”处理，不会因为 Storage 暂时不可达就把数据集从监控里藏起来。结果缓存 datasetGoneTTL。
func (a *StorageAdapter) DatasetGone(ctx context.Context, spaceID, datasetID string) bool {
	if a == nil || a.metadata == nil || strings.TrimSpace(datasetID) == "" {
		return false
	}
	key := strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(datasetID)
	now := time.Now()
	a.goneMu.Lock()
	cached, ok := a.datasetGone[key]
	a.goneMu.Unlock()
	if ok && now.Sub(cached.loadedAt) < datasetGoneTTL {
		return cached.gone
	}
	rsp, err := a.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: a.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil || rsp == nil || rsp.GetRetInfo() == nil {
		return cached.gone && ok
	}
	code := rsp.GetRetInfo().GetCode()
	gone := code == commonpb.ErrorCode_NOT_FOUND || code == commonpb.ErrorCode_DATASET_NOT_FOUND
	if !gone && code != commonpb.ErrorCode_SUCCESS {
		return false // 其他错误：不能确定，按还在处理
	}
	a.goneMu.Lock()
	a.datasetGone[key] = cachedDatasetGone{loadedAt: now, gone: gone}
	a.goneMu.Unlock()
	return gone
}

// ListTags returns the tag definitions and live member counters used by the
// market canary. Tags are deliberately read through Metadata rather than
// inferred from collector configuration so Monitor observes the same state as
// the collector and the management UI.
func (a *StorageAdapter) ListTags(ctx context.Context, spaceID string) ([]*storagepb.Tag, error) {
	if a == nil || a.metadata == nil {
		return nil, errors.New("metrics storage metadata client is not initialized")
	}
	items := make([]*storagepb.Tag, 0)
	for page := uint32(1); ; page++ {
		rsp, err := a.metadata.ListTags(ctx, &storagepb.ListTagsReq{
			AuthInfo: a.auth,
			SpaceId:  strings.TrimSpace(spaceID),
			Page:     &commonpb.Page{Page: page, Size: datasetSubjectPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list monitored tags: %w", err)
		}
		if err := storageOK("list monitored tags", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		items = append(items, rsp.GetTags()...)
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			return items, nil
		}
		if len(rsp.GetTags()) == 0 {
			return nil, errors.New("monitored tag pagination did not advance")
		}
	}
}

func cloneSubjectSet(input map[string]struct{}) map[string]struct{} {
	if input == nil {
		return nil
	}
	output := make(map[string]struct{}, len(input))
	for id := range input {
		output[id] = struct{}{}
	}
	return output
}

// maxArchivableScans 是列出可归档数据集时最多翻的页数。
const maxArchivableScans = 50

// ListArchivableDatasets 列出应当有归档任务的数据集（"空间/数据集 ID"，排好序）：已激活、有限保留期的时序数据集。
// 保留期为 forever 的不会被删，不要求归档。
func (a *StorageAdapter) ListArchivableDatasets(ctx context.Context) ([]string, error) {
	if a == nil || a.metadata == nil {
		return nil, errors.New("metrics storage metadata client is not initialized")
	}
	var out []string
	for page := uint32(1); page <= maxArchivableScans; page++ {
		rsp, err := a.metadata.ListDatasets(ctx, &storagepb.ListDatasetsReq{AuthInfo: a.auth, Page: &commonpb.Page{Page: page, Size: factorResultPageSize}})
		if err != nil {
			return nil, fmt.Errorf("list archivable datasets: %w", err)
		}
		if err := storageOK("list archivable datasets", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, d := range rsp.GetDatasets() {
			if d == nil || d.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES || !strings.EqualFold(d.GetStatus(), "active") ||
				d.GetRetention() == "" || d.GetRetention() == "forever" {
				continue
			}
			out = append(out, d.GetSpaceId()+"/"+d.GetDatasetId())
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			sort.Strings(out)
			return out, nil
		}
	}
	return nil, fmt.Errorf("dataset catalog exceeds %d pages", maxArchivableScans)
}

// FactorResultDataset 是源数据集上一个因子任务的结果数据集：任务 ID 由数据集 ID 反解，名称就是数据集名称（与任务名相同）。
type FactorResultDataset struct {
	TaskID, Name, DatasetID string
}

const (
	factorResultRole     = "factor_result"
	factorResultPageSize = 200
	maxFactorResultScans = 50
)

// ListFactorResultDatasets 列出源数据集 + 周期上的全部因子任务（按结果数据集的 dataset_role=factor_result 与 source_dataset_id
// 属性筛选），按任务 ID 排序。任务 ID 不能从数据集 ID 反解的数据集（不是任务建出来的）忽略。
func (a *StorageAdapter) ListFactorResultDatasets(ctx context.Context, spaceID, sourceDatasetID, freq string) ([]FactorResultDataset, error) {
	if a == nil || a.metadata == nil {
		return nil, errors.New("metrics storage metadata client is not initialized")
	}
	var out []FactorResultDataset
	for page := uint32(1); page <= maxFactorResultScans; page++ {
		rsp, err := a.metadata.ListDatasets(ctx, &storagepb.ListDatasetsReq{
			AuthInfo: a.auth, SpaceId: spaceID, Freq: freq,
			Page: &commonpb.Page{Page: page, Size: factorResultPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list factor result datasets: %w", err)
		}
		if err := storageOK("list factor result datasets", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, dataset := range rsp.GetDatasets() {
			if dataset == nil || dataset.GetAttributes()["dataset_role"] != factorResultRole || dataset.GetAttributes()["source_dataset_id"] != sourceDatasetID {
				continue
			}
			suffix, err := factorname.ParseResultDatasetID(dataset.GetDatasetId())
			if err != nil {
				continue
			}
			taskID, err := factorname.TaskID(suffix)
			if err != nil {
				continue
			}
			out = append(out, FactorResultDataset{TaskID: taskID, Name: dataset.GetName(), DatasetID: dataset.GetDatasetId()})
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
			return out, nil
		}
	}
	return nil, fmt.Errorf("factor result dataset catalog exceeds %d pages", maxFactorResultScans)
}

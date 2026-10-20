package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

// MetadataClient 是 Monitor 读取 Storage 元数据所需的最小接口。服务指标只保存最新值（SQLite），
// 不再写入 Storage 历史，因此这里没有任何写方法。
type MetadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
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
	datasetNames     map[string]cachedDatasetName
}

type cachedDatasetName struct {
	loadedAt time.Time
	name     string
}

const (
	datasetSubjectPageSize = 1000
	maxDatasetSubjects     = 100000
	subjectCatalogTTL      = time.Minute
	datasetNameTTL         = 10 * time.Minute
)

type cachedSubjectCatalog struct {
	loadedAt time.Time
	ids      map[string]struct{}
}

func NewStorageAdapter(metadata MetadataClient) *StorageAdapter {
	return &StorageAdapter{metadata: metadata, auth: storageauth.Primary("monitor"), subjects: make(map[string]cachedSubjectCatalog), datasetNames: make(map[string]cachedDatasetName)}
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
// "现货小时K线", for alert text. It returns "" when the name is unknown, and
// caches names for datasetNameTTL.
func (a *StorageAdapter) DatasetDisplayName(ctx context.Context, spaceID, datasetID string) string {
	if a == nil || a.metadata == nil || strings.TrimSpace(datasetID) == "" {
		return ""
	}
	key := strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(datasetID)
	now := time.Now()
	a.nameMu.Lock()
	cached, ok := a.datasetNames[key]
	a.nameMu.Unlock()
	if ok && now.Sub(cached.loadedAt) < datasetNameTTL {
		return cached.name
	}
	rsp, err := a.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: a.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil || storageOK("get dataset name", rsp.GetRetInfo()) != nil {
		return cached.name
	}
	name := strings.TrimSpace(rsp.GetDataset().GetName())
	a.nameMu.Lock()
	a.datasetNames[key] = cachedDatasetName{loadedAt: now, name: name}
	a.nameMu.Unlock()
	return name
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

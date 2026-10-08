// Package storagesource loads planner inputs from MooX storage metadata.
package storagesource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"trpc.group/trpc-go/trpc-go/client"
)

const storagePageSize = 500

type metadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ResolveSubjects(context.Context, *storagepb.ResolveSubjectsReq, ...client.Option) (*storagepb.ResolveSubjectsRsp, error)
}

type tagMetadataClient interface {
	GetTag(context.Context, *storagepb.GetTagReq, ...client.Option) (*storagepb.GetTagRsp, error)
}

type datasetColumnClient interface {
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
}

// DatasetInfo is the minimal Dataset contract required by Collector rules.
type DatasetInfo struct {
	DataSourceID string
	DataNodeID   string
	DataKind     storagepb.DataKind
	Status       string
	Freq         string
	SubjectTags  []string
	Columns      []string
	ColumnTypes  map[string]storagepb.FieldValueType
	Attributes   map[string]string
	// Retention is the Dataset's effective retention from the Storage policy:
	// "<n>h", "forever", or empty when Storage could not resolve it.
	Retention string
	Revision  uint64
}

// RetentionWindow returns how far back the Dataset keeps rows, and false when
// it keeps every row or its retention is unknown.
func (d DatasetInfo) RetentionWindow() (time.Duration, bool) {
	period, err := storagepolicy.ParsePeriod(d.Retention)
	if err != nil || period.Forever {
		return 0, false
	}
	return period.Duration, true
}

// DatasetSource loads Dataset metadata and resolves the active Subject union
// described by Dataset.SubjectTags. Provider wire symbols are deliberately not
// persisted; callers resolve them from the canonical SubjectID.
type DatasetSource struct {
	metadata metadataClient
}

// GetTag loads the tag definition when the backing metadata client exposes the
// catalog API. It is intentionally an optional capability so older test and
// embedded metadata clients can continue to provide dataset subject expansion.
func (s *DatasetSource) GetTag(ctx context.Context, spaceID, tagID string) (*storagepb.Tag, error) {
	client, ok := s.metadata.(tagMetadataClient)
	if !ok {
		return nil, fmt.Errorf("metadata client does not support get tag")
	}
	rsp, err := client.GetTag(ctx, &storagepb.GetTagReq{SpaceId: strings.TrimSpace(spaceID), TagId: strings.TrimSpace(tagID)})
	if err != nil {
		return nil, fmt.Errorf("get tag: %w", err)
	}
	if err := ensureStorageOK("get tag", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	if rsp.GetTag() == nil {
		return nil, fmt.Errorf("get tag: empty tag")
	}
	return rsp.GetTag(), nil
}

func (s *DatasetSource) GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error) {
	if s == nil || s.metadata == nil {
		return DatasetInfo{}, fmt.Errorf("dataset source is not initialized")
	}
	spaceID, datasetID = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID)
	if datasetID == "" {
		return DatasetInfo{}, fmt.Errorf("dataset_id is required")
	}
	rsp, err := s.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return DatasetInfo{}, fmt.Errorf("get dataset: %w", err)
	}
	if err := ensureStorageOK("get dataset", rsp.GetRetInfo()); err != nil {
		return DatasetInfo{}, err
	}
	dataset := rsp.GetDataset()
	if dataset == nil {
		return DatasetInfo{}, fmt.Errorf("get dataset: empty dataset")
	}
	info := DatasetInfo{
		DataSourceID: strings.TrimSpace(dataset.GetDataSourceId()),
		DataNodeID:   strings.TrimSpace(dataset.GetDataNodeId()),
		DataKind:     dataset.GetDataKind(),
		Status:       strings.ToLower(strings.TrimSpace(dataset.GetStatus())),
		Freq:         dataset.GetFreq(),
		SubjectTags:  append([]string(nil), dataset.GetSubjectTags()...),
		Attributes:   cloneAttributes(dataset.GetAttributes()),
		Retention:    dataset.GetRetention(),
		Revision:     dataset.GetRevision(),
		ColumnTypes:  make(map[string]storagepb.FieldValueType),
	}
	if columns, ok := s.metadata.(datasetColumnClient); ok {
		for page := uint32(1); page <= 100; page++ {
			columnsRsp, callErr := columns.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{SpaceId: spaceID, DatasetId: datasetID, Page: &storagepb.Page{Page: page, Size: storagePageSize}})
			if callErr != nil {
				return DatasetInfo{}, fmt.Errorf("list dataset columns: %w", callErr)
			}
			if err := ensureStorageOK("list dataset columns", columnsRsp.GetRetInfo()); err != nil {
				return DatasetInfo{}, err
			}
			for _, column := range columnsRsp.GetColumns() {
				if column == nil || !strings.EqualFold(strings.TrimSpace(column.GetStatus()), "active") || strings.TrimSpace(column.GetColumnName()) == "" {
					continue
				}
				name := strings.TrimSpace(column.GetColumnName())
				info.Columns = append(info.Columns, name)
				info.ColumnTypes[name] = column.GetValueType()
			}
			if columnsRsp.GetPageResult() == nil || !columnsRsp.GetPageResult().GetHasMore() || len(columnsRsp.GetColumns()) == 0 {
				break
			}
		}
	}
	return info, nil
}

func cloneAttributes(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// ResolveSubjects returns active members of the selected tags. Storage owns
// the union semantics and therefore remains the source of truth for this
// method.
func (s *DatasetSource) ResolveSubjects(ctx context.Context, spaceID string, tagIDs []string) ([]domain.Subject, error) {
	if s == nil || s.metadata == nil {
		return nil, fmt.Errorf("dataset source is not initialized")
	}
	rsp, err := s.metadata.ResolveSubjects(ctx, &storagepb.ResolveSubjectsReq{SpaceId: strings.TrimSpace(spaceID), TagIds: append([]string(nil), tagIDs...)})
	if err != nil {
		return nil, fmt.Errorf("resolve subjects: %w", err)
	}
	if err := ensureStorageOK("resolve subjects", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	items := make([]domain.Subject, 0, len(rsp.GetSubjects()))
	for _, subject := range rsp.GetSubjects() {
		if subject == nil || strings.TrimSpace(subject.GetSubjectId()) == "" {
			continue
		}
		items = append(items, domain.Subject{SubjectID: strings.TrimSpace(subject.GetSubjectId()), Name: strings.TrimSpace(subject.GetName()), Status: strings.TrimSpace(subject.GetStatus())})
	}
	return items, nil
}

// ListSubjects is retained as a compatibility adapter for the resample
// planner. It derives the list from the Dataset's tags and never reads a
// source-side symbol mapping.
func (s *DatasetSource) ListSubjects(ctx context.Context, spaceID, datasetID, _ string) ([]domain.DatasetSubject, error) {
	dataset, err := s.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	items, err := s.ResolveSubjects(ctx, spaceID, dataset.SubjectTags)
	if err != nil {
		return nil, err
	}
	result := make([]domain.DatasetSubject, 0, len(items))
	for _, item := range items {
		result = append(result, domain.DatasetSubject{SubjectID: item.SubjectID, SubjectName: item.Name, Status: item.Status})
	}
	return result, nil
}

func (s *DatasetSource) ListResampleSubjects(ctx context.Context, spaceID, datasetID string) ([]domain.DatasetSubject, error) {
	return s.ListSubjects(ctx, spaceID, datasetID, "")
}

func (s *DatasetSource) ListResampleSubjectsForTask(ctx context.Context, spaceID, datasetID, _, _ string) ([]domain.DatasetSubject, error) {
	return s.ListSubjects(ctx, spaceID, datasetID, "")
}

// NewDatasetSource 创建经给定 tRPC 客户端选项（gatewayclient）访问 Storage Metadata 的数据集来源。
func NewDatasetSource(options []client.Option) *DatasetSource {
	return &DatasetSource{metadata: storagepb.NewMetadataClientProxy(options...)}
}

func ensureStorageOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}

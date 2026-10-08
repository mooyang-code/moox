package taskresult

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

var (
	ErrUnsupportedOutputFields = errors.New("unsupported output fields")
	ErrResultContract          = errors.New("task result ownership or metadata contract violation")
)

// IDs are the stable metadata identities for one task result. Datasets and
// views are task scoped; the view name is derived from taskID, type and
// frequency so it cannot collide when tags overlap.
type IDs struct {
	DatasetID string
	ViewID    string
}

func resultIDs(_ string, taskID string) IDs {
	slug := resultSlug(taskID)
	return IDs{DatasetID: "dataset_" + slug, ViewID: "view_" + slug}
}

// ResultIDsForTask derives both result identities exclusively from the task.
func ResultIDsForTask(spaceID, taskID, taskType, frequency string) IDs {
	ids := resultIDs(spaceID, taskID)
	if strings.TrimSpace(taskType) == "" {
		return ids
	}
	viewSlug := resultSlugForFields(taskID, taskType, frequency)
	ids.ViewID = "view_" + viewSlug
	return ids
}

func resultIDsForConfig(spaceID, taskID, taskType string, cfg Config) IDs {
	if strings.TrimSpace(taskType) == "" {
		return resultIDs(spaceID, taskID)
	}
	return ResultIDsForTask(spaceID, taskID, taskType, strings.TrimSpace(cfg.Frequency))
}

func resultSlug(taskID string) string {
	slug := strings.ToLower(strings.TrimSpace(taskID))
	var builder strings.Builder
	builder.Grow(len(slug))
	previousUnderscore := false
	for _, char := range slug {
		switch {
		case char == '-' || char == '.' || char == ' ':
			char = '_'
		}
		if char == '_' {
			if previousUnderscore || builder.Len() == 0 {
				continue
			}
			builder.WriteByte('_')
			previousUnderscore = true
			continue
		}
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			previousUnderscore = false
		}
	}
	slug = strings.Trim(builder.String(), "_")
	if i := strings.Index(slug, "_symbols_"); i >= 0 {
		slug = slug[:i+len("_symbols")]
	}
	if slug == "" {
		return "collector_result"
	}
	return slug
}

func resultSlugForFields(parts ...string) string {
	values := make([]string, len(parts))
	for i, part := range parts {
		values[i] = strings.ToLower(strings.TrimSpace(part))
	}
	return resultSlug(strings.Join(values, "_"))
}

// ResultIDs returns the stable metadata identities for a task result.
func ResultIDs(spaceID, taskID string) IDs { return resultIDs(spaceID, taskID) }

// PersistedResultIDs prefers identities already committed to a Collector task
// row and falls back to the task-derived identity before the first commit.
func PersistedResultIDs(spaceID, taskID, datasetID, viewID string) IDs {
	if strings.TrimSpace(datasetID) != "" && strings.TrimSpace(viewID) != "" {
		return IDs{DatasetID: strings.TrimSpace(datasetID), ViewID: strings.TrimSpace(viewID)}
	}
	return resultIDs(spaceID, taskID)
}

type metadataAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	GetView(context.Context, *storagepb.GetViewReq) (*storagepb.GetViewRsp, error)
	CreateView(context.Context, *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error)
	UpdateView(context.Context, *storagepb.UpdateViewReq) (*storagepb.UpdateViewRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
	UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

type metadataProxy struct{ client storagepb.MetadataClientProxy }

func (p metadataProxy) GetTag(ctx context.Context, req *storagepb.GetTagReq) (*storagepb.GetTagRsp, error) {
	return p.client.GetTag(ctx, req)
}

func (p metadataProxy) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	return p.client.GetDataset(ctx, req)
}
func (p metadataProxy) CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	return p.client.CreateDataset(ctx, req)
}
func (p metadataProxy) UpdateDataset(ctx context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	return p.client.UpdateDataset(ctx, req)
}
func (p metadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	return p.client.DeleteDataset(ctx, req)
}
func (p metadataProxy) GetView(ctx context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	return p.client.GetView(ctx, req)
}
func (p metadataProxy) CreateView(ctx context.Context, req *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	return p.client.CreateView(ctx, req)
}
func (p metadataProxy) UpdateView(ctx context.Context, req *storagepb.UpdateViewReq) (*storagepb.UpdateViewRsp, error) {
	return p.client.UpdateView(ctx, req)
}
func (p metadataProxy) DeleteView(ctx context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	return p.client.DeleteView(ctx, req)
}
func (p metadataProxy) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return p.client.UpsertDatasetColumn(ctx, req)
}
func (p metadataProxy) UpsertViewColumn(ctx context.Context, req *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	return p.client.UpsertViewColumn(ctx, req)
}
func (p metadataProxy) CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return p.client.CheckDatasetActivation(ctx, req)
}
func (p metadataProxy) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	return p.client.ActivateDataset(ctx, req)
}

type Manager struct {
	metadata metadataAPI
	cleaner  datasetRowsCleaner
	auth     *storagepb.AuthInfo
}

type datasetRowsCleaner interface {
	DeleteDatasetRows(context.Context, *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error)
	RestoreDatasetRows(context.Context, *storagepb.PrimaryRestoreDatasetRowsReq) (*storagepb.PrimaryRestoreDatasetRowsRsp, error)
}

type primaryDatasetRowsCleaner struct {
	client storagepb.PrimaryStoreClientProxy
}

func (c primaryDatasetRowsCleaner) DeleteDatasetRows(ctx context.Context, req *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	return c.client.DeleteDatasetRows(ctx, req)
}

func (c primaryDatasetRowsCleaner) RestoreDatasetRows(ctx context.Context, req *storagepb.PrimaryRestoreDatasetRowsReq) (*storagepb.PrimaryRestoreDatasetRowsRsp, error) {
	return c.client.RestoreDatasetRows(ctx, req)
}

func NewManagerWithCleaner(client storagepb.MetadataClientProxy, cleaner storagepb.PrimaryStoreClientProxy, auth *storagepb.AuthInfo) *Manager {
	if client == nil || cleaner == nil || auth == nil {
		return nil
	}
	return &Manager{metadata: metadataProxy{client: client}, cleaner: primaryDatasetRowsCleaner{client: cleaner}, auth: auth}
}

func NewManagerWithAPI(metadata metadataAPI, auth *storagepb.AuthInfo) *Manager {
	if metadata == nil || auth == nil {
		return nil
	}
	return &Manager{metadata: metadata, auth: auth}
}

type Config struct {
	ViewID       string
	DataNodeID   string
	KeepDuration string
	Name         string
	Description  string
	Frequency    string
	SubjectTags  []string
	OutputFields []string
}

func configuredResultIDs(spaceID, taskID, dataType string, cfg Config) IDs {
	ids := resultIDsForConfig(spaceID, taskID, dataType, cfg)
	if strings.TrimSpace(cfg.ViewID) != "" {
		ids.ViewID = strings.TrimSpace(cfg.ViewID)
	}
	return ids
}

func normalizeSubjectTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}

func sameSubjectTags(left, right []string) bool {
	a, b := normalizeSubjectTags(left), normalizeSubjectTags(right)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const (
	ResultStatusPending = "pending"
	ResultStatusReady   = "ready"
	ResultStatusError   = "error"
	ResultStatusUnknown = "unknown"
)

type tagReader interface {
	GetTag(context.Context, *storagepb.GetTagReq) (*storagepb.GetTagRsp, error)
}

// resultDataSource returns the data source shared by all of a task's tags.
// A result whose tags span several sources stays without one, which Storage
// allows for Collector-owned Datasets.
func (m *Manager) resultDataSource(ctx context.Context, spaceID string, subjectTags []string) (string, error) {
	reader, ok := m.metadata.(tagReader)
	if !ok || len(subjectTags) == 0 {
		return "", nil
	}
	source := ""
	for _, tagID := range subjectTags {
		rsp, err := reader.GetTag(ctx, &storagepb.GetTagReq{AuthInfo: m.auth, SpaceId: spaceID, TagId: tagID})
		if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || rsp.GetTag() == nil {
			return "", metadataError("get result subject tag", err, func() *storagepb.RetInfo {
				if rsp == nil {
					return nil
				}
				return rsp.GetRetInfo()
			}())
		}
		tagSource := strings.TrimSpace(rsp.GetTag().GetSource())
		if source != "" && tagSource != source {
			return "", nil
		}
		source = tagSource
	}
	return source, nil
}

// Inspection is the read-only result metadata snapshot for one task.
// LastDataTime is empty when Storage does not expose an authoritative indexed
// upper bound.
type Inspection struct {
	IDs           IDs
	Status        string
	Dataset       *storagepb.Dataset
	View          *storagepb.View
	LastDataTime  string
	CoverageStart string
	CoverageEnd   string
	Error         string
}

// Inspect returns the task-owned Dataset/View state without changing Storage.
// Missing metadata is a normal pending state; ownership and metadata failures
// are returned as errors so callers cannot mistake another task's result for
// this task's result.
func (m *Manager) Inspect(ctx context.Context, spaceID, taskID string) (Inspection, error) {
	return m.InspectIDs(ctx, spaceID, taskID, resultIDs(spaceID, taskID))
}

// InspectIDs inspects an explicitly persisted result identity while keeping
// its metadata ownership tied to the collector task.
func (m *Manager) InspectIDs(ctx context.Context, spaceID, taskID string, ids IDs) (Inspection, error) {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	inspection := Inspection{IDs: ids, Status: ResultStatusPending}
	if m == nil || m.metadata == nil || m.auth == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: task result metadata manager is not configured", ErrResultContract))
	}
	if spaceID == "" || taskID == "" {
		return inspectionWithError(inspection, fmt.Errorf("%w: space_id and task_id are required", ErrResultContract))
	}

	datasetRsp, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: inspection.IDs.DatasetID})
	if err != nil {
		return inspectionWithError(inspection, fmt.Errorf("inspect result dataset: %w", err))
	}
	if datasetRsp == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: inspect result dataset: empty response", ErrResultContract))
	}
	if datasetRsp.GetRetInfo() == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: %v", ErrResultContract, metadataError("inspect result dataset", nil, nil)))
	}
	datasetExists := false
	switch datasetRsp.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS:
		dataset := datasetRsp.GetDataset()
		if dataset == nil {
			return inspectionWithError(inspection, fmt.Errorf("%w: inspect result dataset: empty dataset", ErrResultContract))
		}
		if dataset.GetSpaceId() != spaceID || dataset.GetDatasetId() != inspection.IDs.DatasetID {
			return inspectionWithError(inspection, fmt.Errorf("%w: result dataset identity does not match requested space/dataset", ErrResultContract))
		}
		if !ownedByTask(dataset.GetAttributes(), taskID) {
			return inspectionWithError(inspection, fmt.Errorf("%w: result dataset %s is owned by another task", ErrResultContract, inspection.IDs.DatasetID))
		}
		inspection.Dataset = dataset
		datasetExists = true
	case storagepb.ErrorCode_DATASET_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
	default:
		return inspectionWithError(inspection, classifyInspectionMetadataError("inspect result dataset", datasetRsp.GetRetInfo()))
	}

	viewRsp, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: inspection.IDs.ViewID})
	if err != nil {
		return inspectionWithError(inspection, fmt.Errorf("inspect result view: %w", err))
	}
	if viewRsp == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: inspect result view: empty response", ErrResultContract))
	}
	if viewRsp.GetRetInfo() == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: %v", ErrResultContract, metadataError("inspect result view", nil, nil)))
	}
	viewExists := false
	switch viewRsp.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS:
		view := viewRsp.GetView()
		if view == nil {
			return inspectionWithError(inspection, fmt.Errorf("%w: inspect result view: empty view", ErrResultContract))
		}
		if view.GetSpaceId() != spaceID || view.GetViewId() != inspection.IDs.ViewID {
			return inspectionWithError(inspection, fmt.Errorf("%w: result view identity does not match requested space/view", ErrResultContract))
		}
		if !ownedByTask(view.GetAttributes(), taskID) {
			return inspectionWithError(inspection, fmt.Errorf("%w: result view %s is owned by another task", ErrResultContract, inspection.IDs.ViewID))
		}
		if view.GetDatasetId() != inspection.IDs.DatasetID {
			return inspectionWithError(inspection, fmt.Errorf("%w: result view %s does not reference task dataset %s", ErrResultContract, inspection.IDs.ViewID, inspection.IDs.DatasetID))
		}
		inspection.View = view
		viewExists = true
	case storagepb.ErrorCode_VIEW_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
	default:
		return inspectionWithError(inspection, classifyInspectionMetadataError("inspect result view", viewRsp.GetRetInfo()))
	}

	if !datasetExists && viewExists {
		return inspectionWithError(inspection, fmt.Errorf("%w: result view %s exists without task dataset %s", ErrResultContract, inspection.IDs.ViewID, inspection.IDs.DatasetID))
	}
	if !datasetExists || !viewExists {
		return inspection, nil
	}
	inspection.CoverageStart, inspection.CoverageEnd = resultCoverage(inspection.Dataset, inspection.View)
	inspection.LastDataTime = resultLastDataTime(inspection.Dataset, inspection.View)
	if strings.EqualFold(strings.TrimSpace(inspection.Dataset.GetStatus()), "error") ||
		strings.EqualFold(strings.TrimSpace(inspection.View.GetStatus()), "error") ||
		strings.EqualFold(strings.TrimSpace(inspection.View.GetStatus()), "failed") {
		inspection.Status = ResultStatusError
		inspection.Error = "task result metadata is in an error state"
		return inspection, nil
	}
	datasetReady := strings.TrimSpace(inspection.Dataset.GetStatus()) == "" || strings.EqualFold(strings.TrimSpace(inspection.Dataset.GetStatus()), "active")
	viewReady := strings.TrimSpace(inspection.View.GetStatus()) == "" || strings.EqualFold(strings.TrimSpace(inspection.View.GetStatus()), "active")
	if datasetReady && viewReady {
		inspection.Status = ResultStatusReady
	}
	return inspection, nil
}

func inspectionWithError(inspection Inspection, err error) (Inspection, error) {
	inspection.Status = ResultStatusError
	if err != nil {
		inspection.Error = err.Error()
	}
	return inspection, err
}

func classifyInspectionMetadataError(action string, ret *storagepb.RetInfo) error {
	err := metadataError(action, nil, ret)
	if ret == nil {
		return fmt.Errorf("%w: %v", ErrResultContract, err)
	}
	switch ret.GetCode() {
	case storagepb.ErrorCode_INVALID_PARAM, storagepb.ErrorCode_NO_AUTH, storagepb.ErrorCode_NO_PERMISSION, storagepb.ErrorCode_CONFLICT:
		return fmt.Errorf("%w: %v", ErrResultContract, err)
	default:
		return err
	}
}

func resultLastDataTime(dataset *storagepb.Dataset, view *storagepb.View) string {
	if dataset != nil && dataset.GetDataKind() == storagepb.DataKind_DATA_KIND_RECORD {
		for _, object := range []map[string]string{dataset.GetAttributes(), func() map[string]string {
			if view == nil {
				return nil
			}
			return view.GetAttributes()
		}()} {
			for _, key := range []string{"last_data_time", "last_data_time_at", "fetched_at"} {
				if value := strings.TrimSpace(object[key]); value != "" {
					return value
				}
			}
		}
		return ""
	}
	if view != nil {
		if value := strings.TrimSpace(view.GetIndexedTo()); value != "" {
			return value
		}
		for _, key := range []string{"last_data_time", "last_data_time_at", "indexed_to"} {
			if value := strings.TrimSpace(view.GetAttributes()[key]); value != "" {
				return value
			}
		}
	}
	if dataset != nil {
		for _, key := range []string{"last_data_time", "last_data_time_at", "indexed_to"} {
			if value := strings.TrimSpace(dataset.GetAttributes()[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func resultCoverage(dataset *storagepb.Dataset, view *storagepb.View) (string, string) {
	start, end := "", ""
	if view != nil {
		start, end = strings.TrimSpace(view.GetIndexedFrom()), strings.TrimSpace(view.GetIndexedTo())
	}
	if dataset != nil {
		attrs := dataset.GetAttributes()
		if start == "" {
			start = firstNonEmpty(attrs["coverage_start"], attrs["indexed_from"])
		}
		if end == "" {
			end = firstNonEmpty(attrs["coverage_end"], attrs["indexed_to"])
		}
	}
	if view != nil {
		attrs := view.GetAttributes()
		if start == "" {
			start = firstNonEmpty(attrs["coverage_start"], attrs["indexed_from"])
		}
		if end == "" {
			end = firstNonEmpty(attrs["coverage_end"], attrs["indexed_to"])
		}
	}
	return start, end
}

func (m *Manager) Ensure(ctx context.Context, spaceID, taskID, dataType, marketType string, cfg Config) (IDs, error) {
	if m == nil || m.metadata == nil || m.auth == nil {
		return IDs{}, fmt.Errorf("task result metadata manager is not configured")
	}
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	if spaceID == "" || taskID == "" || strings.TrimSpace(cfg.DataNodeID) == "" {
		return IDs{}, fmt.Errorf("space_id, task_id and result data_node_id are required")
	}
	ids := configuredResultIDs(spaceID, taskID, dataType, cfg)
	kind := storagepb.DataKind_DATA_KIND_TIME_SERIES
	if strings.EqualFold(strings.TrimSpace(dataType), "instrument") || strings.EqualFold(strings.TrimSpace(dataType), "symbol") {
		kind = storagepb.DataKind_DATA_KIND_RECORD
	}
	keep := strings.TrimSpace(cfg.KeepDuration)
	if keep == "" {
		keep = "0"
	}
	keep, err := normalizeKeepDuration(keep)
	if err != nil {
		return IDs{}, err
	}
	attrs := map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "collector_task_id": taskID}
	if marketType = strings.TrimSpace(marketType); marketType != "" {
		attrs["market_type"] = marketType
	}
	subjectTags := normalizeSubjectTags(cfg.SubjectTags)
	dataSourceID, err := m.resultDataSource(ctx, spaceID, subjectTags)
	if err != nil {
		return IDs{}, err
	}
	get, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return IDs{}, fmt.Errorf("get result dataset: %w", err)
	}
	createdDataset := false
	if get == nil {
		return IDs{}, metadataError("get result dataset", nil, nil)
	}
	if get.GetRetInfo().GetCode() == storagepb.ErrorCode_DATASET_NOT_FOUND || get.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		created, createErr := m.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: m.auth, Dataset: &storagepb.Dataset{
			SpaceId: spaceID, DatasetId: ids.DatasetID, DataSourceId: dataSourceID, DataNodeId: cfg.DataNodeID,
			Name: resultDisplayName(cfg, taskID), Description: cfg.Description, DataKind: kind, Status: "draft", KeepDuration: keep, Freq: datasetFrequency(kind, cfg.Frequency), Attributes: attrs, SubjectTags: subjectTags,
		}})
		if createErr != nil || created == nil || created.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return IDs{}, metadataError("create result dataset", createErr, retInfoDataset(created))
		}
		createdDataset = true
	} else if get.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return IDs{}, metadataError("get result dataset", nil, get.GetRetInfo())
	} else if !ownedByTask(get.GetDataset().GetAttributes(), taskID) {
		return IDs{}, fmt.Errorf("result dataset %s is owned by another task", ids.DatasetID)
	} else {
		// Subject tags are no longer duplicated in collect_params. A bootstrap
		// reconciliation therefore has no requested tags for an already-created
		// result and must preserve the Dataset-owned scope instead of clearing it.
		if len(subjectTags) == 0 {
			subjectTags = normalizeSubjectTags(get.GetDataset().GetSubjectTags())
		}
		assignSource := dataSourceID != "" && get.GetDataset().GetDataSourceId() == ""
		if !sameSubjectTags(get.GetDataset().GetSubjectTags(), subjectTags) || assignSource {
			updated := proto.Clone(get.GetDataset()).(*storagepb.Dataset)
			updated.SubjectTags = subjectTags
			if assignSource {
				updated.DataSourceId = dataSourceID
			}
			response, updateErr := m.metadata.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{AuthInfo: m.auth, Dataset: updated})
			if updateErr != nil || response == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
				return IDs{}, metadataError("update result dataset subject tags or data source", updateErr, func() *storagepb.RetInfo {
					if response == nil {
						return nil
					}
					return response.GetRetInfo()
				}())
			}
			get = &storagepb.GetDatasetRsp{RetInfo: response.GetRetInfo(), Dataset: response.GetDataset()}
		}
	}
	cleanupCreated := func(original error) (IDs, error) {
		if !createdDataset {
			return IDs{}, original
		}
		if cleanupErr := m.cleanupCreatedDataset(ctx, spaceID, ids.DatasetID); cleanupErr != nil {
			return IDs{}, fmt.Errorf("%w; cleanup newly-created result failed: %v", original, cleanupErr)
		}
		return IDs{}, original
	}
	if err := m.ensureColumns(ctx, spaceID, ids.DatasetID, ids.ViewID, kind, cfg.OutputFields); err != nil {
		return cleanupCreated(err)
	}
	check, err := m.metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return cleanupCreated(fmt.Errorf("check result dataset activation: %w", err))
	}
	if check.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || !check.GetReady() {
		return cleanupCreated(metadataError("check result dataset activation", nil, check.GetRetInfo()))
	}
	get, err = m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return cleanupCreated(fmt.Errorf("get result dataset: %w", err))
	}
	if get.GetDataset() == nil {
		return cleanupCreated(metadataError("get result dataset", nil, nil))
	}
	if get.GetDataset().GetStatus() != "active" {
		activated, activateErr := m.metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID, ExpectedRevision: get.GetDataset().GetRevision()})
		if activateErr != nil || activated == nil || activated.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return cleanupCreated(metadataError("activate result dataset", activateErr, retInfoActivate(activated)))
		}
	}
	// CreateDataset intentionally starts disabled. Restore must run only after
	// activation because Primary resolves the DataNode from active metadata.
	// For an existing result this also clears a prior physical-delete tombstone
	// before the next collection batch is allowed to write.
	if m.cleaner != nil {
		restored, restoreErr := m.cleaner.RestoreDatasetRows(ctx, &storagepb.PrimaryRestoreDatasetRowsReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
		if restoreErr != nil || restored == nil || restored.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return cleanupCreated(metadataError("restore result dataset rows", restoreErr, func() *storagepb.RetInfo {
				if restored == nil {
					return nil
				}
				return restored.GetRetInfo()
			}()))
		}
	}
	viewKeep := keep
	if datasetKeep := strings.TrimSpace(get.GetDataset().GetKeepDuration()); datasetKeep != "" && datasetKeep != "0" && (keep == "" || keep == "0") {
		viewKeep = datasetKeep
	}
	if err := m.ensureView(ctx, spaceID, taskID, ids, kind, viewKeep, cfg); err != nil {
		return cleanupCreated(err)
	}
	if err := m.ensureViewColumns(ctx, spaceID, ids, cfg.OutputFields); err != nil {
		return cleanupCreated(err)
	}
	return ids, nil
}

// UpdateSubjectTags updates the scope of an existing task-owned result Dataset
// without reprovisioning its columns, View, or physical rows.
func (m *Manager) UpdateSubjectTags(ctx context.Context, spaceID, datasetID string, tags []string) error {
	if m == nil || m.metadata == nil || m.auth == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	rsp, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return fmt.Errorf("get result dataset for subject tags: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || rsp.GetDataset() == nil {
		return metadataError("get result dataset for subject tags", nil, func() *storagepb.RetInfo {
			if rsp == nil {
				return nil
			}
			return rsp.GetRetInfo()
		}())
	}
	updated := proto.Clone(rsp.GetDataset()).(*storagepb.Dataset)
	updated.SubjectTags = normalizeSubjectTags(tags)
	if sameSubjectTags(rsp.GetDataset().GetSubjectTags(), updated.GetSubjectTags()) {
		return nil
	}
	result, err := m.metadata.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{AuthInfo: m.auth, Dataset: updated})
	if err != nil {
		return fmt.Errorf("update result dataset subject tags: %w", err)
	}
	if result == nil || result.GetRetInfo() == nil || result.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return metadataError("update result dataset subject tags", nil, func() *storagepb.RetInfo {
			if result == nil {
				return nil
			}
			return result.GetRetInfo()
		}())
	}
	return nil
}

// EnsureOutputFields adds the Dataset columns required by a task-specific
// output projection and appends missing columns to an explicitly projected
// result View without replacing its existing projection.
func (m *Manager) EnsureOutputFields(ctx context.Context, spaceID, taskID, datasetID, viewID, dataType string, outputFields []string) error {
	if m == nil || m.metadata == nil || m.auth == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if len(outputFields) == 0 {
		return fmt.Errorf("at least one output field is required")
	}
	ids := IDs{DatasetID: strings.TrimSpace(datasetID), ViewID: strings.TrimSpace(viewID)}
	if ids.DatasetID == "" || ids.ViewID == "" {
		return fmt.Errorf("result dataset_id and view_id are required")
	}
	datasetExists, viewExists, err := m.resultMetadataState(ctx, spaceID, taskID, ids)
	if err != nil {
		return err
	}
	if !datasetExists || !viewExists {
		return fmt.Errorf("task result Dataset/View is unavailable")
	}
	viewResponse, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil || viewResponse == nil || viewResponse.GetRetInfo() == nil || viewResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || viewResponse.GetView() == nil {
		return metadataError("get result view for output fields", err, func() *storagepb.RetInfo {
			if viewResponse == nil {
				return nil
			}
			return viewResponse.GetRetInfo()
		}())
	}
	view := viewResponse.GetView()
	kind := storagepb.DataKind_DATA_KIND_TIME_SERIES
	if strings.EqualFold(strings.TrimSpace(dataType), "instrument") || strings.EqualFold(strings.TrimSpace(dataType), "symbol") {
		kind = storagepb.DataKind_DATA_KIND_RECORD
	}
	if err := m.ensureColumns(ctx, spaceID, ids.DatasetID, ids.ViewID, kind, outputFields); err != nil {
		return err
	}
	// output_fields is the complete user-facing projection contract. Replacing
	// the View columns removes stale fields (for example provider_id) when a
	// task narrows its projection; append-only updates leave metadata pointing
	// at fields that new rows no longer persist and cause "not projected"
	// query failures after the next View rebuild.
	columns, err := resultViewColumns(spaceID, ids, outputFields)
	if err != nil {
		return err
	}
	nextView := proto.Clone(view).(*storagepb.View)
	nextView.Columns = columns
	updated, err := m.metadata.UpdateView(ctx, &storagepb.UpdateViewReq{AuthInfo: m.auth, View: nextView, ReplaceColumns: true})
	if err != nil || updated == nil || updated.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return metadataError("replace result view output fields", err, func() *storagepb.RetInfo {
			if updated == nil {
				return nil
			}
			return updated.GetRetInfo()
		}())
	}
	return nil
}

func (m *Manager) cleanupCreatedDataset(ctx context.Context, spaceID, datasetID string) error {
	// A newly-created Dataset is disabled until the activation step. Remove its
	// metadata first so compensation also works when activation never happened;
	// the collector has not published any rows before Ensure returns.
	dataset, err := m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err == nil && dataset != nil && (dataset.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS || dataset.GetRetInfo().GetCode() == storagepb.ErrorCode_DATASET_NOT_FOUND || dataset.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND) {
		return nil
	}
	if m.cleaner != nil {
		physical, err := m.cleaner.DeleteDatasetRows(ctx, &storagepb.PrimaryDeleteDatasetRowsReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: datasetID})
		if err != nil || physical == nil || !resultDeleteAccepted(physical.GetRetInfo()) {
			return metadataError("delete newly-created result dataset rows", err, func() *storagepb.RetInfo {
				if physical == nil {
					return nil
				}
				return physical.GetRetInfo()
			}())
		}
	}
	dataset, err = m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil || (dataset != nil && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
		return metadataError("delete newly-created result dataset", err, retInfoDatasetDelete(dataset))
	}
	return nil
}

// EnsureWithCleanup provisions a raw result and returns a compensating action
// for callers that have not yet committed the owning task row. Existing
// task-owned resources are never removed by that action.
func (m *Manager) EnsureWithCleanup(ctx context.Context, spaceID, taskID, dataType, marketType string, cfg Config) (IDs, func(context.Context) error, error) {
	ids := configuredResultIDs(spaceID, taskID, dataType, cfg)
	datasetExists, viewExists, err := m.resultMetadataState(ctx, spaceID, taskID, ids)
	if err != nil {
		return IDs{}, nil, err
	}
	cleanup := func(cleanupCtx context.Context) error {
		if viewExists && datasetExists {
			return nil
		}
		if !viewExists {
			current, getErr := m.metadata.GetView(cleanupCtx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
			if getErr != nil {
				return fmt.Errorf("recheck result view ownership before cleanup: %w", getErr)
			}
			if current == nil || current.GetRetInfo() == nil {
				return fmt.Errorf("recheck result view ownership before cleanup: empty response")
			}
			if current.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS {
				if current.GetView() == nil || !ownedByTask(current.GetView().GetAttributes(), taskID) || current.GetView().GetDatasetId() != ids.DatasetID {
					return nil
				}
				if datasetExists {
					return nil
				}
			} else if current.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_FOUND && current.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND {
				return metadataError("recheck result view ownership before cleanup", nil, current.GetRetInfo())
			}
			view, deleteErr := m.metadata.DeleteView(cleanupCtx, &storagepb.DeleteViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
			if deleteErr != nil || !resultDeleteAccepted(retInfoViewDelete(view)) {
				return metadataError("cleanup result view", deleteErr, retInfoViewDelete(view))
			}
		}
		if !datasetExists {
			current, getErr := m.metadata.GetDataset(cleanupCtx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
			if getErr != nil {
				return fmt.Errorf("recheck result dataset ownership before cleanup: %w", getErr)
			}
			if current == nil || current.GetRetInfo() == nil {
				return fmt.Errorf("recheck result dataset ownership before cleanup: empty response")
			}
			if current.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS && (current.GetDataset() == nil || !ownedByTask(current.GetDataset().GetAttributes(), taskID)) {
				return nil
			}
			if current.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && current.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && current.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND {
				return metadataError("recheck result dataset ownership before cleanup", nil, current.GetRetInfo())
			}
			if m.cleaner != nil {
				physical, cleanErr := m.cleaner.DeleteDatasetRows(cleanupCtx, &storagepb.PrimaryDeleteDatasetRowsReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
				if cleanErr != nil || physical == nil || !resultDeleteAccepted(physical.GetRetInfo()) {
					return metadataError("cleanup result dataset rows", cleanErr, func() *storagepb.RetInfo {
						if physical == nil {
							return nil
						}
						return physical.GetRetInfo()
					}())
				}
			}
			dataset, deleteErr := m.metadata.DeleteDataset(cleanupCtx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
			if deleteErr != nil || !resultDeleteAccepted(retInfoDatasetDelete(dataset)) {
				return metadataError("cleanup result dataset", deleteErr, retInfoDatasetDelete(dataset))
			}
		}
		return nil
	}
	ensured, err := m.Ensure(ctx, spaceID, taskID, dataType, marketType, cfg)
	if err != nil {
		return IDs{}, nil, err
	}
	return ensured, cleanup, nil
}

func (m *Manager) resultMetadataState(ctx context.Context, spaceID, taskID string, ids IDs) (bool, bool, error) {
	dataset, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return false, false, err
	}
	if dataset == nil || dataset.GetRetInfo() == nil {
		return false, false, fmt.Errorf("get result dataset: empty response")
	}
	datasetExists := dataset.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS
	if datasetExists && (dataset.GetDataset() == nil || !ownedByTask(dataset.GetDataset().GetAttributes(), taskID)) {
		return false, false, fmt.Errorf("result dataset %s is owned by another task", ids.DatasetID)
	}
	if !datasetExists && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND {
		return false, false, metadataError("get result dataset", nil, dataset.GetRetInfo())
	}
	view, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil {
		return false, false, err
	}
	if view == nil || view.GetRetInfo() == nil {
		return false, false, fmt.Errorf("get result view: empty response")
	}
	viewExists := view.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS
	if viewExists && view.GetView() != nil && !ownedByTask(view.GetView().GetAttributes(), taskID) {
		return false, false, fmt.Errorf("view %q already exists for another collection configuration", ids.ViewID)
	}
	if viewExists && (view.GetView() == nil || view.GetView().GetDatasetId() != ids.DatasetID) {
		return false, false, fmt.Errorf("result view %s does not reference task dataset %s", ids.ViewID, ids.DatasetID)
	}
	if !viewExists && view.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_FOUND && view.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND {
		return false, false, metadataError("get result view", nil, view.GetRetInfo())
	}
	return datasetExists, viewExists, nil
}

func (m *Manager) ensureColumns(ctx context.Context, spaceID, datasetID, viewID string, kind storagepb.DataKind, outputFields []string) error {
	fields := []struct {
		name string
		kind storagepb.FieldValueType
	}{
		{"open", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"high", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"low", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"close", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"quote_volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"trade_num", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT},
	}
	if kind == storagepb.DataKind_DATA_KIND_RECORD {
		fields = []struct {
			name string
			kind storagepb.FieldValueType
		}{
			{"symbol", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING}, {"external_symbol", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			{"base_asset", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING}, {"quote_asset", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			{"status", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING}, {"min_qty", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
			{"max_qty", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"tick_size", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
			{"lot_size", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"security_code", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			{"provider_symbol", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING}, {"exchange", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			{"instrument_name", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING}, {"instrument_status", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			{"snapshot_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING}, {"source_provider", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			{"fetched_at", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
		}
	} else {
		fields = append(fields,
			struct {
				name string
				kind storagepb.FieldValueType
			}{"amount", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"instrument_name", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"provider_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"source_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"provider_symbol", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"volume_unit", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"amount_unit", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"trade_date", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"close_time", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"provider_timestamp", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"fetched_at", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"request_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"route_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"route_rank", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"source_provider", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"quality_status", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"amount_quality", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		)
	}
	if len(outputFields) > 0 {
		selected := make(map[string]struct{}, len(outputFields))
		for _, name := range outputFields {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" {
				selected[name] = struct{}{}
			}
		}
		filtered := fields[:0]
		for _, field := range fields {
			if _, ok := selected[field.name]; ok {
				filtered = append(filtered, field)
				delete(selected, field.name)
			}
		}
		if len(selected) > 0 {
			unknown := make([]string, 0, len(selected))
			for name := range selected {
				unknown = append(unknown, name)
			}
			sort.Strings(unknown)
			return fmt.Errorf("%w: %s", ErrUnsupportedOutputFields, strings.Join(unknown, ", "))
		}
		if len(filtered) == 0 {
			return fmt.Errorf("%w: at least one supported output field is required", ErrUnsupportedOutputFields)
		}
		fields = filtered
	}
	for _, field := range fields {
		resp, err := m.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: m.auth, Column: &storagepb.DatasetColumn{SpaceId: spaceID, DatasetId: datasetID, ColumnName: field.name, OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: field.name, ValueType: field.kind, Required: true, Status: "active", Attributes: map[string]string{"display_name": resultColumnDisplayName(field.name)}}})
		if err != nil || resp == nil || resp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return metadataError("create result dataset column", err, retInfoDatasetColumn(resp))
		}
	}
	return nil
}

func (m *Manager) ensureViewColumns(ctx context.Context, spaceID string, ids IDs, outputFields []string) error {
	columns, err := resultViewColumns(spaceID, ids, outputFields)
	if err != nil {
		return err
	}
	for _, column := range columns {
		response, callErr := m.metadata.UpsertViewColumn(ctx, &storagepb.UpsertViewColumnReq{AuthInfo: m.auth, Column: column})
		if callErr != nil || response == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return metadataError("create result view column", callErr, func() *storagepb.RetInfo {
				if response == nil {
					return nil
				}
				return response.GetRetInfo()
			}())
		}
	}
	return nil
}

func resultViewColumns(spaceID string, ids IDs, outputFields []string) ([]*storagepb.ViewColumn, error) {
	if len(outputFields) == 0 {
		return nil, nil
	}
	ordered := make([]string, 0, len(outputFields))
	seen := make(map[string]struct{}, len(outputFields))
	for _, rawName := range outputFields {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		if _, ok := resultViewColumnType(name); !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedOutputFields, name)
		}
		seen[name] = struct{}{}
		ordered = append(ordered, name)
	}
	if len(ordered) == 0 {
		return nil, fmt.Errorf("%w: at least one supported output field is required", ErrUnsupportedOutputFields)
	}
	sort.Strings(ordered)
	columns := make([]*storagepb.ViewColumn, 0, len(ordered))
	for i, name := range ordered {
		valueType, _ := resultViewColumnType(name)
		columns = append(columns, &storagepb.ViewColumn{
			SpaceId: spaceID, ViewId: ids.ViewID, ColumnName: name,
			OriginType: storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   name, ValueType: valueType, SortOrder: uint32(i + 1),
			Attributes: map[string]string{"display_name": resultColumnDisplayName(name)},
		})
	}
	return columns, nil
}

func resultViewColumnType(name string) (storagepb.FieldValueType, bool) {
	switch name {
	case "open", "high", "low", "close", "volume", "quote_volume", "amount":
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, true
	case "trade_num", "route_rank":
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, true
	case "provider_id", "source_id", "provider_symbol", "instrument_name", "volume_unit", "amount_unit", "request_id", "route_id", "source_provider", "quality_status", "amount_quality":
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, true
	case "trade_date", "close_time", "provider_timestamp", "fetched_at":
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME, true
	default:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, false
	}
}

func resultColumnDisplayName(name string) string {
	names := map[string]string{
		"open": "开盘", "high": "最高", "low": "最低", "close": "收盘", "volume": "成交量", "quote_volume": "成交额", "trade_num": "成交笔数", "amount": "成交额", "instrument_name": "标的名称", "provider_id": "数据提供方", "source_id": "来源", "provider_symbol": "原始标的", "volume_unit": "量单位", "amount_unit": "额单位", "trade_date": "交易日期", "close_time": "收盘时间", "provider_timestamp": "源时间", "fetched_at": "采集时间", "request_id": "请求标识", "route_id": "路由", "route_rank": "路由序号", "source_provider": "源提供方", "quality_status": "质量状态", "amount_quality": "金额质量",
	}
	if displayName := names[name]; displayName != "" {
		return displayName
	}
	return "结果字段"
}

func (m *Manager) ensureView(ctx context.Context, spaceID, taskID string, ids IDs, kind storagepb.DataKind, keep string, cfg Config) error {
	get, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil {
		return fmt.Errorf("get result view: %w", err)
	}
	if get == nil {
		return metadataError("get result view", nil, nil)
	}
	if get.GetRetInfo().GetCode() == storagepb.ErrorCode_VIEW_NOT_FOUND || get.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		engine := "duckdb"
		grain := []string{"subject_id", "data_time", "freq"}
		if kind == storagepb.DataKind_DATA_KIND_RECORD {
			engine, grain = "bleve", []string{"subject_id", "version"}
		}
		created, createErr := m.metadata.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: m.auth, View: &storagepb.View{SpaceId: spaceID, ViewId: ids.ViewID, Name: resultDisplayName(cfg, taskID), Description: "Collector任务结果视图", DatasetId: ids.DatasetID, GrainKeys: grain, Engine: engine, KeepDuration: keep, Status: "active", Attributes: map[string]string{"owner_module": "collector", "view_role": "collection_browse", "collector_task_id": taskID}}, CreateOnly: true})
		if createErr != nil || created == nil || created.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return metadataError("create result view", createErr, retInfoView(created))
		}
		return nil
	}
	if get.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return metadataError("get result view", nil, get.GetRetInfo())
	}
	if !ownedByTask(get.GetView().GetAttributes(), taskID) {
		return fmt.Errorf("result view %s is owned by another task", ids.ViewID)
	}
	return nil
}

func normalizeKeepDuration(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return "0", nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil && len(raw) > 1 {
		unit := raw[len(raw)-1]
		if unit == 'd' || unit == 'D' || unit == 'w' || unit == 'W' {
			count, parseErr := strconv.ParseInt(raw[:len(raw)-1], 10, 64)
			if parseErr == nil && count > 0 {
				multiplier := 24 * time.Hour
				if unit == 'w' || unit == 'W' {
					multiplier *= 7
				}
				duration = time.Duration(count) * multiplier
				err = nil
			}
		}
	}
	if err != nil || duration <= 0 {
		return "", fmt.Errorf("keep_duration must be 0 or a positive duration: %q", raw)
	}
	return duration.String(), nil
}

func resultDisplayName(cfg Config, taskID string) string {
	if name := shortChineseResultName(resultSlug(taskID)); name != "" {
		return name
	}
	if name := shortChineseResultNameForTaskName(cfg.Name); name != "" {
		return name
	}
	for _, candidate := range []string{cfg.Name, cfg.Description} {
		if name := strings.TrimSpace(candidate); isChineseDisplayName(name) {
			return name
		}
	}
	return "采集结果"
}

func shortChineseResultNameForTaskName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case strings.ToLower("Binance 现货 K 线 1m"):
		return "现货分钟K线"
	case strings.ToLower("Binance 合约 K 线 1m"):
		return "合约分钟K线"
	case strings.ToLower("Binance 现货 K 线 1H"):
		return "现货小时K线"
	case strings.ToLower("Binance 合约 K 线 1H"):
		return "合约小时K线"
	case strings.ToLower("A 股 K 线 1m"):
		return "A股分钟K线"
	default:
		return ""
	}
}

func shortChineseResultName(slug string) string {
	slug = strings.TrimPrefix(strings.TrimSpace(slug), "builtin_")
	switch slug {
	case "binance_spot_kline_1m":
		return "现货分钟K线"
	case "binance_swap_kline_1m":
		return "合约分钟K线"
	case "binance_spot_kline_1h":
		return "现货小时K线"
	case "binance_swap_kline_1h":
		return "合约小时K线"
	case "binance_spot_symbols":
		return "现货标的"
	case "binance_swap_symbols":
		return "合约标的"
	case "stockcn_kline_1m":
		return "A股分钟K线"
	case "stockcn_instrument_1d":
		return "A股标的"
	default:
		return ""
	}
}

func isChineseDisplayName(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > 10 {
		return false
	}
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func ownedByTask(attributes map[string]string, taskID string) bool {
	return strings.TrimSpace(attributes["owner_module"]) == "collector" &&
		strings.TrimSpace(attributes["collector_task_id"]) == strings.TrimSpace(taskID)
}

func (m *Manager) Delete(ctx context.Context, spaceID string, ids IDs) error {
	if m == nil || m.metadata == nil || m.auth == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if _, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID}); err != nil {
		return fmt.Errorf("get result dataset: %w", err)
	}
	if m.cleaner != nil {
		physical, err := m.cleaner.DeleteDatasetRows(ctx, &storagepb.PrimaryDeleteDatasetRowsReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
		if err != nil || physical == nil || !resultDeleteAccepted(physical.GetRetInfo()) {
			return metadataError("delete result dataset rows", err, func() *storagepb.RetInfo {
				if physical == nil {
					return nil
				}
				return physical.GetRetInfo()
			}())
		}
	}
	view, err := m.metadata.DeleteView(ctx, &storagepb.DeleteViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil || !resultDeleteAccepted(retInfoViewDelete(view)) {
		return metadataError("delete result view", err, retInfoViewDelete(view))
	}
	removed, err := m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil || !resultDeleteAccepted(retInfoDatasetDelete(removed)) {
		return metadataError("delete result dataset", err, retInfoDatasetDelete(removed))
	}
	return nil
}

func resultDeleteAccepted(info *storagepb.RetInfo) bool {
	if info == nil {
		return false
	}
	// Physical deletion is intentionally idempotent. A retry after the
	// previous request committed may observe an already removed result; that
	// state is equivalent to success for the task deletion workflow.
	switch info.GetCode() {
	case storagepb.ErrorCode_SUCCESS,
		storagepb.ErrorCode_DATASET_NOT_FOUND,
		storagepb.ErrorCode_VIEW_NOT_FOUND,
		storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
}

// DeleteForTask verifies Collector ownership before deleting metadata. The
// deterministic Dataset ID prevents a malformed task row from targeting an
// unrelated result, while the ownership check protects the View side too.
func (m *Manager) DeleteForTask(ctx context.Context, spaceID, taskID string, ids IDs) error {
	if err := m.ValidateOwnedForTask(ctx, spaceID, taskID, ids); err != nil {
		return err
	}
	return m.Delete(ctx, spaceID, ids)
}

// ValidateOwnedForTask performs the destructive-delete ownership checks
// without changing Storage state. Callers can use it before deleting local
// runtime records so a rejected delete remains fully retryable.
func (m *Manager) ValidateOwnedForTask(ctx context.Context, spaceID, taskID string, ids IDs) error {
	if m == nil || m.metadata == nil || m.auth == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	base := resultIDs(spaceID, taskID)
	if ids.DatasetID != base.DatasetID || strings.TrimSpace(ids.ViewID) == "" {
		return fmt.Errorf("result Dataset identity is not the deterministic result of task %s", taskID)
	}
	dataset, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return fmt.Errorf("get result dataset ownership: %w", err)
	}
	if dataset == nil || dataset.GetRetInfo() == nil {
		return fmt.Errorf("get result dataset ownership: empty response")
	}
	switch dataset.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS:
		if dataset.GetDataset() == nil || dataset.GetDataset().GetSpaceId() != strings.TrimSpace(spaceID) || !ownedByTask(dataset.GetDataset().GetAttributes(), taskID) {
			return fmt.Errorf("result dataset %s is not owned by task %s", ids.DatasetID, taskID)
		}
	case storagepb.ErrorCode_DATASET_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
	default:
		return fmt.Errorf("get result dataset ownership: %s", dataset.GetRetInfo().GetMsg())
	}
	view, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil {
		return fmt.Errorf("get result view ownership: %w", err)
	}
	if view == nil || view.GetRetInfo() == nil {
		return fmt.Errorf("get result view ownership: empty response")
	}
	switch view.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS:
		if view.GetView() == nil || view.GetView().GetSpaceId() != strings.TrimSpace(spaceID) {
			return fmt.Errorf("result view %s is not owned by task %s", ids.ViewID, taskID)
		}
		viewAttrs := view.GetView().GetAttributes()
		if !ownedByTask(viewAttrs, taskID) || view.GetView().GetDatasetId() != ids.DatasetID {
			return fmt.Errorf("result view %s is not owned by task %s", ids.ViewID, taskID)
		}
	case storagepb.ErrorCode_VIEW_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
	default:
		return fmt.Errorf("get result view ownership: %s", view.GetRetInfo().GetMsg())
	}
	return nil
}

// datasetFrequency returns the Dataset freq: a time-series result has the
// task's one frequency, a record result has none.
func datasetFrequency(kind storagepb.DataKind, frequency string) string {
	if kind == storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return strings.TrimSpace(frequency)
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func metadataError(action string, callErr error, ret *storagepb.RetInfo) error {
	if callErr != nil {
		return fmt.Errorf("%s: %w", action, callErr)
	}
	if ret == nil {
		return fmt.Errorf("%s: empty response", action)
	}
	return fmt.Errorf("%s: %s", action, ret.GetMsg())
}

func retInfoDataset(resp *storagepb.CreateDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoActivate(resp *storagepb.ActivateDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoDatasetColumn(resp *storagepb.UpsertDatasetColumnRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoView(resp *storagepb.CreateViewRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoViewDelete(resp *storagepb.DeleteViewRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoDatasetDelete(resp *storagepb.DeleteDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

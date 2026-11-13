package taskresult

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"
	"sort"
	"strconv"
	"strings"
	"trpc.group/trpc-go/trpc-go/client"
	"unicode"
	"unicode/utf8"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/commonpb"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/seriesset"
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
	ListViews(context.Context, *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
	// GetViewState 读 storage-view 里 View 的已建状态，覆盖范围只在这里。
	GetViewState(context.Context, *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error)
}

type MetadataClient interface {
	GetTag(context.Context, *storagepb.GetTagReq, ...client.Option) (*storagepb.GetTagRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq, ...client.Option) (*storagepb.CreateDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq, ...client.Option) (*storagepb.UpdateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
	GetView(context.Context, *storagepb.GetViewReq, ...client.Option) (*storagepb.GetViewRsp, error)
	CreateView(context.Context, *storagepb.CreateViewReq, ...client.Option) (*storagepb.CreateViewRsp, error)
	UpdateView(context.Context, *storagepb.UpdateViewReq, ...client.Option) (*storagepb.UpdateViewRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq, ...client.Option) (*storagepb.DeleteViewRsp, error)
	ListViews(context.Context, *storagepb.ListViewsReq, ...client.Option) (*storagepb.ListViewsRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq, ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error)
}

// ViewStateClient 是 DataView 里结果管理器用到的方法。
type ViewStateClient interface {
	GetViewState(context.Context, *storagepb.GetViewStateReq, ...client.Option) (*storagepb.GetViewStateRsp, error)
}

type metadataProxy struct {
	client MetadataClient
	view   ViewStateClient
}

func (p metadataProxy) GetTag(ctx context.Context, req *storagepb.GetTagReq) (*storagepb.GetTagRsp, error) {
	return p.client.GetTag(ctx, req)
}

func (p metadataProxy) ListViews(ctx context.Context, req *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error) {
	return p.client.ListViews(ctx, req)
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
func (p metadataProxy) GetViewState(ctx context.Context, req *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	return p.view.GetViewState(ctx, req)
}

type Manager struct {
	metadata metadataAPI
	auth     *storagepb.AuthInfo
	// fields 是行情源目录里可用的附加字段：结果数据集里它们的列（类型、显示名、fill 属性）按字段定义生成。
	fields marketdata.FieldOffers
}

// SetFieldOffers 设置可用的附加字段（进程启动时由行情源目录给出）。
func (m *Manager) SetFieldOffers(offers marketdata.FieldOffers) {
	if m != nil {
		m.fields = offers
	}
}

// extraFieldType 把附加字段的值类型换成 Storage 的列类型。
func extraFieldType(spec marketdata.FieldSpec) storagepb.FieldValueType {
	if spec.Type == marketdata.FieldTypeInt {
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
	}
	return storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
}

// extraColumnAttributes 是附加字段列的属性：显示名取字段定义；fill 告诉因子引擎缺行时怎样补（没有这个属性就沿用上一根）。
func extraColumnAttributes(spec marketdata.FieldSpec) map[string]string {
	attributes := map[string]string{"display_name": spec.Name}
	if spec.Name == "" {
		attributes["display_name"] = resultColumnDisplayName(spec.ID)
	}
	if spec.Fill != "" {
		attributes["fill"] = spec.Fill
	}
	return attributes
}

// NewManager 用 Storage 元数据客户端与 DataView 客户端构造结果管理器；删除数据集时 Storage 服务端会一并删掉行。
// DataView 客户端只用来读 View 的覆盖范围。
func NewManager(client MetadataClient, view ViewStateClient, auth *storagepb.AuthInfo) *Manager {
	if client == nil || view == nil || auth == nil {
		return nil
	}
	return &Manager{metadata: metadataProxy{client: client, view: view}, auth: auth}
}

func NewManagerWithAPI(metadata metadataAPI, auth *storagepb.AuthInfo) *Manager {
	if metadata == nil || auth == nil {
		return nil
	}
	return &Manager{metadata: metadata, auth: auth}
}

type Config struct {
	ViewID       string
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

// resultDataSource 返回任务全部标签共享的数据源与市场类型：
//   - 数据源与市场都相同：返回它们；
//   - 数据源相同、市场是现货与永续两个：统一数据集，市场类型为 multi，数据源保留；
//   - 其余（数据源不同，或市场组合不是现货加永续）：返回空值。建任务时已按同样的规则拒绝，这里只是兜底。
func (m *Manager) resultDataSource(ctx context.Context, spaceID string, subjectTags []string) (source, marketType string, err error) {
	reader, ok := m.metadata.(tagReader)
	if !ok || len(subjectTags) == 0 {
		return "", "", nil
	}
	markets := map[string]struct{}{}
	for i, tagID := range subjectTags {
		rsp, getErr := reader.GetTag(ctx, &storagepb.GetTagReq{AuthInfo: m.auth, SpaceId: spaceID, TagId: tagID})
		if getErr != nil || rsp == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || rsp.GetTag() == nil {
			return "", "", metadataError("get result subject tag", getErr, func() *storagepb.RetInfo {
				if rsp == nil {
					return nil
				}
				return rsp.GetRetInfo()
			}())
		}
		tagSource := tagattr.Source(rsp.GetTag())
		if i > 0 && tagSource != source {
			return "", "", nil
		}
		source = tagSource
		markets[tagattr.MarketType(rsp.GetTag())] = struct{}{}
	}
	if len(markets) == 1 {
		for market := range markets {
			return source, market, nil
		}
	}
	if tagattr.IsSpotSwap(markets) {
		return source, seriesset.MarketMulti, nil
	}
	return "", "", nil
}

// Inspection 是一个任务结果元数据的只读快照。
// LastDataTime、CoverageStart、CoverageEnd 优先取 storage-view 里已建索引的覆盖范围；
// 索引还没建好或 storage-view 还没登记这个 View 时视为覆盖范围未知，退回数据集、View 属性，都没有就为空。
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
	indexedFrom, indexedTo, err := m.viewIndexedRange(ctx, spaceID, inspection.IDs.ViewID)
	if err != nil {
		return inspectionWithError(inspection, err)
	}
	inspection.CoverageStart, inspection.CoverageEnd = resultCoverage(inspection.Dataset, inspection.View, indexedFrom, indexedTo)
	inspection.LastDataTime = resultLastDataTime(inspection.Dataset, inspection.View, indexedTo)
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

// viewIndexedRange 从 storage-view 读结果 View 已建索引的覆盖范围（两端都含）。
// storage-view 还没登记这个 View，或索引还没建好（首次建索引、断档重建中）时返回空，表示覆盖范围未知；其他失败照常报错。
func (m *Manager) viewIndexedRange(ctx context.Context, spaceID, viewID string) (string, string, error) {
	state, err := m.metadata.GetViewState(ctx, &storagepb.GetViewStateReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return "", "", fmt.Errorf("inspect result view state: %w", err)
	}
	if state == nil || state.GetRetInfo() == nil {
		return "", "", fmt.Errorf("%w: %v", ErrResultContract, metadataError("inspect result view state", nil, nil))
	}
	switch state.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS:
	case storagepb.ErrorCode_VIEW_NOT_FOUND:
		return "", "", nil
	default:
		return "", "", classifyInspectionMetadataError("inspect result view state", state.GetRetInfo())
	}
	if strings.TrimSpace(state.GetIndexId()) == "" {
		return "", "", nil
	}
	return strings.TrimSpace(state.GetIndexedFrom()), strings.TrimSpace(state.GetIndexedTo()), nil
}

// resultLastDataTime 返回结果最近一条数据的业务时间：记录型取属性；时序型优先取已建索引的上界 indexedTo，再退回属性。
func resultLastDataTime(dataset *storagepb.Dataset, view *storagepb.View, indexedTo string) string {
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
	if indexedTo != "" {
		return indexedTo
	}
	if view != nil {
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

// resultCoverage 返回结果的覆盖范围：优先取已建索引的范围，缺的一端退回数据集、View 属性。
func resultCoverage(dataset *storagepb.Dataset, view *storagepb.View, indexedFrom, indexedTo string) (string, string) {
	start, end := indexedFrom, indexedTo
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
	if spaceID == "" || taskID == "" {
		return IDs{}, fmt.Errorf("space_id and task_id are required")
	}
	ids := configuredResultIDs(spaceID, taskID, dataType, cfg)
	kind := storagepb.DataKind_DATA_KIND_TIME_SERIES
	if strings.EqualFold(strings.TrimSpace(dataType), "instrument") || strings.EqualFold(strings.TrimSpace(dataType), "symbol") {
		kind = storagepb.DataKind_DATA_KIND_RECORD
	}
	subjectTags := normalizeSubjectTags(cfg.SubjectTags)
	dataSourceID, tagMarketType, err := m.resultDataSource(ctx, spaceID, subjectTags)
	if err != nil {
		return IDs{}, err
	}
	// 未显式指定市场类型时，取标签共享的市场类型；重采样等下游据此校验来源数据集。
	attrs := map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "collector_task_id": taskID}
	if marketType = strings.TrimSpace(marketType); marketType == "" {
		marketType = tagMarketType
	}
	// derived 是由市场决定、已有结果数据集缺了要补上的属性：market_type，以及交易时段市场时序结果的 calendar、alignment
	// （策略按 calendar 选交易日历，缺省按 crypto_24x7；按 alignment 校验行键）。
	derived := map[string]string{}
	if kind == storagepb.DataKind_DATA_KIND_TIME_SERIES {
		if derived, err = sessionAttributes(spaceID); err != nil {
			return IDs{}, err
		}
	}
	if marketType != "" {
		derived["market_type"] = marketType
	}
	for key, value := range derived {
		attrs[key] = value
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
		columns, columnsErr := m.resultDatasetColumns(spaceID, kind, cfg.OutputFields)
		if columnsErr != nil {
			return IDs{}, columnsErr
		}
		// 数据集连同列在一次调用里建好，建好即可写入。
		created, createErr := m.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: m.auth, Dataset: &storagepb.Dataset{
			SpaceId: spaceID, DatasetId: ids.DatasetID, DataSourceId: dataSourceID,
			Name: resultDisplayName(cfg, taskID), Description: cfg.Description, DataKind: kind, Freq: datasetFrequency(kind, cfg.Frequency), Attributes: attrs, SubjectTags: subjectTags,
		}, Columns: columns})
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
		missing := map[string]string{}
		for key, value := range derived {
			if get.GetDataset().GetAttributes()[key] == "" {
				missing[key] = value
			}
		}
		if !sameSubjectTags(get.GetDataset().GetSubjectTags(), subjectTags) || assignSource || len(missing) > 0 {
			updated := proto.Clone(get.GetDataset()).(*storagepb.Dataset)
			updated.SubjectTags = subjectTags
			if assignSource {
				updated.DataSourceId = dataSourceID
			}
			if len(missing) > 0 && updated.Attributes == nil {
				updated.Attributes = map[string]string{}
			}
			for key, value := range missing {
				updated.Attributes[key] = value
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
	if !createdDataset {
		// 已有的结果数据集按同一份列定义补齐：上一轮建数据集时缓存没发布成功，这里也会让 Storage 重新发布。
		if err := m.ensureColumns(ctx, spaceID, ids.DatasetID, kind, cfg.OutputFields); err != nil {
			return IDs{}, err
		}
	}
	// View 的列由 Storage 按数据集启用的列推出，这里只建 View 本身。
	if err := m.ensureView(ctx, spaceID, taskID, ids, kind, cfg); err != nil {
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

// AllowedLatenessAttribute 是数据集属性名：周期的允许迟到时间（秒），由 Storage 在建立周期时读取。
const AllowedLatenessAttribute = "allowed_lateness_seconds"

// EnsureAllowedLateness 保证结果数据集的允许迟到时间属性与任务设置一致；没有变化时不更新。
func (m *Manager) EnsureAllowedLateness(ctx context.Context, spaceID, datasetID string, seconds int) error {
	if m == nil || m.metadata == nil || m.auth == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if seconds <= 0 {
		return fmt.Errorf("allowed lateness must be positive")
	}
	rsp, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return fmt.Errorf("get result dataset for allowed lateness: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || rsp.GetDataset() == nil {
		var ret *storagepb.RetInfo
		if rsp != nil {
			ret = rsp.GetRetInfo()
		}
		return metadataError("get result dataset for allowed lateness", nil, ret)
	}
	value := strconv.Itoa(seconds)
	if rsp.GetDataset().GetAttributes()[AllowedLatenessAttribute] == value {
		return nil
	}
	updated := proto.Clone(rsp.GetDataset()).(*storagepb.Dataset)
	if updated.Attributes == nil {
		updated.Attributes = map[string]string{}
	}
	updated.Attributes[AllowedLatenessAttribute] = value
	result, err := m.metadata.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{AuthInfo: m.auth, Dataset: updated})
	if err != nil {
		return fmt.Errorf("update result dataset allowed lateness: %w", err)
	}
	if result == nil || result.GetRetInfo() == nil || result.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		var ret *storagepb.RetInfo
		if result != nil {
			ret = result.GetRetInfo()
		}
		return metadataError("update result dataset allowed lateness", nil, ret)
	}
	return nil
}

// EnsureOutputFields 按任务的输出字段补齐结果数据集的列；View 的列跟着数据集走，由 storage-view 原地加进索引，不需要改 View。
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
	kind := storagepb.DataKind_DATA_KIND_TIME_SERIES
	if strings.EqualFold(strings.TrimSpace(dataType), "instrument") || strings.EqualFold(strings.TrimSpace(dataType), "symbol") {
		kind = storagepb.DataKind_DATA_KIND_RECORD
	}
	return m.ensureColumns(ctx, spaceID, ids.DatasetID, kind, outputFields)
}

// cleanupCreatedDataset 删掉本轮刚建的结果数据集；Storage 服务端一并删掉它的行。
func (m *Manager) cleanupCreatedDataset(ctx context.Context, spaceID, datasetID string) error {
	dataset, err := m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil || !resultDeleteAccepted(retInfoDatasetDelete(dataset)) {
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

// ensureColumns 把结果数据集的列逐个补齐（已有的列按同一定义覆盖）。
func (m *Manager) ensureColumns(ctx context.Context, spaceID, datasetID string, kind storagepb.DataKind, outputFields []string) error {
	columns, err := m.resultDatasetColumns(spaceID, kind, outputFields)
	if err != nil {
		return err
	}
	for _, column := range columns {
		column.SpaceId, column.DatasetId = spaceID, datasetID
		resp, err := m.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: m.auth, Column: column})
		if err != nil || resp == nil || resp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return metadataError("create result dataset column", err, retInfoDatasetColumn(resp))
		}
	}
	return nil
}

// resultDatasetColumns 返回结果数据集的列定义：按数据类型取默认字段；指定了输出字段时只取选中的那些，并校验字段都受支持。
func (m *Manager) resultDatasetColumns(spaceID string, kind storagepb.DataKind, outputFields []string) ([]*storagepb.DatasetColumn, error) {
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
	extra := map[string]marketdata.FieldSpec{}
	if len(outputFields) > 0 && kind != storagepb.DataKind_DATA_KIND_RECORD {
		// 附加字段只在显式选中时建列；输出字段为空（默认字段）时不建。
		for _, spec := range m.fields.Specs(spaceID) {
			extra[spec.ID] = spec
			fields = append(fields, struct {
				name string
				kind storagepb.FieldValueType
			}{spec.ID, extraFieldType(spec)})
		}
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
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedOutputFields, strings.Join(unknown, ", "))
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("%w: at least one supported output field is required", ErrUnsupportedOutputFields)
		}
		fields = filtered
	}
	columns := make([]*storagepb.DatasetColumn, 0, len(fields))
	for _, field := range fields {
		attributes := map[string]string{"display_name": resultColumnDisplayName(field.name)}
		if spec, ok := extra[field.name]; ok {
			attributes = extraColumnAttributes(spec)
		}
		columns = append(columns, &storagepb.DatasetColumn{ColumnName: field.name, OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: field.name, ValueType: field.kind, Required: true, Status: "active", Attributes: attributes})
	}
	return columns, nil
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

func (m *Manager) ensureView(ctx context.Context, spaceID, taskID string, ids IDs, kind storagepb.DataKind, cfg Config) error {
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
		created, createErr := m.metadata.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: m.auth, View: &storagepb.View{SpaceId: spaceID, ViewId: ids.ViewID, Name: resultDisplayName(cfg, taskID), Description: "Collector任务结果视图", DatasetId: ids.DatasetID, GrainKeys: grain, Engine: engine, Status: "active", Attributes: map[string]string{"owner_module": "collector", "view_role": "collection_browse", "maintenance_priority": "0", "collector_task_id": taskID}}, CreateOnly: true})
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
	case strings.ToLower("A 股 K 线 5m"):
		return "A股5分钟K线"
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
	// 先确认没有结果自身 View 之外的引用：否则删掉自身 View 之后，删数据集会被外键拒绝，留下没有 View 的结果。
	if err := m.ensureNoExtraViews(ctx, spaceID, ids); err != nil {
		return err
	}
	// 删数据集时 Storage 服务端先删元数据再删行，这里不用单独删行。
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

// ensureNoExtraViews 拒绝删除仍被其他 View 引用的结果 Dataset，此时数据必须完整保留。
func (m *Manager) ensureNoExtraViews(ctx context.Context, spaceID string, ids IDs) error {
	rsp, err := m.metadata.ListViews(ctx, &storagepb.ListViewsReq{
		AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID,
		Page: &commonpb.Page{Page: 1, Size: 200},
	})
	if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return metadataError("list result dataset views", err, rsp.GetRetInfo())
	}
	for _, view := range rsp.GetViews() {
		if view.GetViewId() != ids.ViewID {
			return fmt.Errorf("%w: 结果 Dataset %s 仍被 View %s 引用，请先删除该 View 后重试", ErrResultContract, ids.DatasetID, view.GetViewId())
		}
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
// task's one frequency in canonical form ("1H" becomes "1h"), a record result
// has none. An unknown frequency is passed through for Storage to reject.
func datasetFrequency(kind storagepb.DataKind, frequency string) string {
	if kind != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return ""
	}
	if canonical, err := frequencypkg.Normalize(frequency); err == nil {
		return canonical
	}
	return strings.TrimSpace(frequency)
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

// sessionAttributes 返回交易时段市场时序结果数据集的 calendar 与 alignment（stockcn 为 cn_stock、session），取自 barclock 的市场登记。
// 连续交易的市场（crypto）不写：策略缺省按 crypto_24x7。市场未登记时报错，不当成连续交易。
func sessionAttributes(spaceID string) (map[string]string, error) {
	market, err := barclock.MarketFor(spaceID)
	if err != nil {
		return nil, err
	}
	if market.Alignment != barclock.AlignmentSession {
		return map[string]string{}, nil
	}
	return map[string]string{"calendar": market.CalendarID, "alignment": market.Alignment}, nil
}

package taskresult

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"google.golang.org/protobuf/proto"
)

var (
	ErrUnsupportedOutputFields = errors.New("unsupported output fields")
	ErrResultContract          = errors.New("task result ownership or metadata contract violation")
)

// IDs 是一个任务结果在 Storage 的稳定身份：结果数据集 ID 只由任务 ID 推出。
// 每个数据集恰好有一个查询索引（View），由 Storage 随数据集建删，这里不再单独登记。
type IDs struct {
	DatasetID string
}

func resultIDs(taskID string) IDs {
	return IDs{DatasetID: "dataset_" + resultSlug(taskID)}
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

// ResultIDs 返回任务结果在 Storage 的稳定身份，只由任务 ID 推出。
func ResultIDs(taskID string) IDs { return resultIDs(taskID) }

// PersistedResultIDs 优先取任务行里已提交的结果数据集 ID，首次提交前退回由任务推出的 ID。
func PersistedResultIDs(taskID, datasetID string) IDs {
	if strings.TrimSpace(datasetID) != "" {
		return IDs{DatasetID: strings.TrimSpace(datasetID)}
	}
	return resultIDs(taskID)
}

type metadataAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	UpsertDataset(context.Context, *storagepb.UpsertDatasetReq) (*storagepb.UpsertDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
	// GetViewState 读 storage-view 里结果数据集查询索引的已建状态，覆盖范围只在这里。
	GetViewState(context.Context, *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error)
}

type MetadataClient interface {
	GetTag(context.Context, *storagepb.GetTagReq, ...client.Option) (*storagepb.GetTagRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	UpsertDataset(context.Context, *storagepb.UpsertDatasetReq, ...client.Option) (*storagepb.UpsertDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
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

func (p metadataProxy) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	return p.client.GetDataset(ctx, req)
}
func (p metadataProxy) UpsertDataset(ctx context.Context, req *storagepb.UpsertDatasetReq) (*storagepb.UpsertDatasetRsp, error) {
	return p.client.UpsertDataset(ctx, req)
}
func (p metadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	return p.client.DeleteDataset(ctx, req)
}
func (p metadataProxy) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return p.client.UpsertDatasetColumn(ctx, req)
}
func (p metadataProxy) GetViewState(ctx context.Context, req *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	return p.view.GetViewState(ctx, req)
}

type Manager struct {
	metadata metadataAPI
	// fields 是行情源目录里可用的附加字段：结果数据集里它们的列（类型、显示名、分组、fill 属性）按字段定义生成。
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

// extraColumnAttributes 是附加字段列的属性：fill 告诉因子引擎缺行时怎样补（没有这个属性就沿用上一根）。
func extraColumnAttributes(spec marketdata.FieldSpec) map[string]string {
	if spec.Fill == "" {
		return nil
	}
	return map[string]string{"fill": spec.Fill}
}

// NewManager 用 Storage 元数据客户端与 DataView 客户端构造结果管理器；删除数据集时 Storage 服务端会一并删掉行与查询索引。
// DataView 客户端只用来读查询索引的覆盖范围。
func NewManager(client MetadataClient, view ViewStateClient) *Manager {
	if client == nil || view == nil {
		return nil
	}
	return &Manager{metadata: metadataProxy{client: client, view: view}}
}

func NewManagerWithAPI(metadata metadataAPI) *Manager {
	if metadata == nil {
		return nil
	}
	return &Manager{metadata: metadata}
}

type Config struct {
	Name         string
	Description  string
	Frequency    string
	SubjectTags  []string
	OutputFields []string
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

// resultDataSource 返回任务全部标签共享的数据源与市场类型；数据源或市场不全相同时返回空值
// （建任务时已按同样的规则拒绝，这里只是兜底）。
func (m *Manager) resultDataSource(ctx context.Context, spaceID string, subjectTags []string) (source, marketType string, err error) {
	reader, ok := m.metadata.(tagReader)
	if !ok || len(subjectTags) == 0 {
		return "", "", nil
	}
	markets := map[string]struct{}{}
	for i, tagID := range subjectTags {
		rsp, getErr := reader.GetTag(ctx, &storagepb.GetTagReq{SpaceId: spaceID, TagId: tagID})
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
	return "", "", nil
}

// Inspection 是一个任务结果元数据的只读快照。
// LastDataTime、CoverageStart、CoverageEnd 优先取 storage-view 里已建索引的覆盖范围；
// 索引还没建好或 storage-view 还没发现这个数据集时视为覆盖范围未知，退回数据集属性，没有就为空。
type Inspection struct {
	IDs           IDs
	Status        string
	Dataset       *storagepb.Dataset
	LastDataTime  string
	CoverageStart string
	CoverageEnd   string
	Error         string
}

// Inspect returns the task-owned Dataset state without changing Storage.
// Missing metadata is a normal pending state; ownership and metadata failures
// are returned as errors so callers cannot mistake another task's result for
// this task's result.
func (m *Manager) Inspect(ctx context.Context, spaceID, taskID string) (Inspection, error) {
	return m.InspectIDs(ctx, spaceID, taskID, resultIDs(taskID))
}

// InspectIDs inspects an explicitly persisted result identity while keeping
// its metadata ownership tied to the collector task.
func (m *Manager) InspectIDs(ctx context.Context, spaceID, taskID string, ids IDs) (Inspection, error) {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	inspection := Inspection{IDs: ids, Status: ResultStatusPending}
	if m == nil || m.metadata == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: task result metadata manager is not configured", ErrResultContract))
	}
	if spaceID == "" || taskID == "" {
		return inspectionWithError(inspection, fmt.Errorf("%w: space_id and task_id are required", ErrResultContract))
	}

	datasetRsp, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: inspection.IDs.DatasetID})
	if err != nil {
		return inspectionWithError(inspection, fmt.Errorf("inspect result dataset: %w", err))
	}
	if datasetRsp == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: inspect result dataset: empty response", ErrResultContract))
	}
	if datasetRsp.GetRetInfo() == nil {
		return inspectionWithError(inspection, fmt.Errorf("%w: %v", ErrResultContract, metadataError("inspect result dataset", nil, nil)))
	}
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
	case storagepb.ErrorCode_DATASET_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
		return inspection, nil
	default:
		return inspectionWithError(inspection, classifyInspectionMetadataError("inspect result dataset", datasetRsp.GetRetInfo()))
	}

	indexedFrom, indexedTo, err := m.viewIndexedRange(ctx, spaceID, inspection.IDs.DatasetID)
	if err != nil {
		return inspectionWithError(inspection, err)
	}
	inspection.CoverageStart, inspection.CoverageEnd = resultCoverage(inspection.Dataset, indexedFrom, indexedTo)
	inspection.LastDataTime = resultLastDataTime(inspection.Dataset, indexedTo)
	status := strings.TrimSpace(inspection.Dataset.GetStatus())
	if strings.EqualFold(status, "error") {
		inspection.Status = ResultStatusError
		inspection.Error = "task result metadata is in an error state"
		return inspection, nil
	}
	if status == "" || strings.EqualFold(status, "active") {
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

// viewIndexedRange 从 storage-view 读结果数据集查询索引的覆盖范围（两端都含）。
// storage-view 还没发现这个数据集、索引还没建好（首次建索引、断档重建中），或 storage-view 暂时不可用（传输失败、内部错误）时
// 记日志后返回空，表示覆盖范围未知：storage-view 暂停或重启不能让任务结果清单整份失败。只有调用契约错误照常上报。
func (m *Manager) viewIndexedRange(ctx context.Context, spaceID, datasetID string) (string, string, error) {
	state, err := m.metadata.GetViewState(ctx, &storagepb.GetViewStateReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		log.WarnContextf(ctx, "读取结果数据集的索引状态失败，覆盖范围按未知处理 space=%s dataset=%s：%v", spaceID, datasetID, err)
		return "", "", nil
	}
	if state == nil || state.GetRetInfo() == nil {
		return "", "", fmt.Errorf("%w: %v", ErrResultContract, metadataError("读取结果数据集的索引状态", nil, nil))
	}
	switch state.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS:
	case storagepb.ErrorCode_VIEW_NOT_FOUND, storagepb.ErrorCode_DATASET_NOT_FOUND:
		return "", "", nil
	default:
		classified := classifyInspectionMetadataError("读取结果数据集的索引状态", state.GetRetInfo())
		if errors.Is(classified, ErrResultContract) {
			return "", "", classified
		}
		log.WarnContextf(ctx, "读取结果数据集的索引状态失败，覆盖范围按未知处理 space=%s dataset=%s：%v", spaceID, datasetID, classified)
		return "", "", nil
	}
	if strings.TrimSpace(state.GetIndexId()) == "" {
		return "", "", nil
	}
	return strings.TrimSpace(state.GetIndexedFrom()), strings.TrimSpace(state.GetIndexedTo()), nil
}

// resultLastDataTime 返回结果最近一条数据的业务时间：优先取已建索引的上界 indexedTo，再退回属性。
func resultLastDataTime(dataset *storagepb.Dataset, indexedTo string) string {
	if dataset == nil || indexedTo != "" {
		return indexedTo
	}
	for _, key := range []string{"last_data_time", "last_data_time_at", "indexed_to"} {
		if value := strings.TrimSpace(dataset.GetAttributes()[key]); value != "" {
			return value
		}
	}
	return ""
}

// resultCoverage 返回结果的覆盖范围：优先取已建索引的范围，缺的一端退回数据集属性。
func resultCoverage(dataset *storagepb.Dataset, indexedFrom, indexedTo string) (string, string) {
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
	return start, end
}

func (m *Manager) Ensure(ctx context.Context, spaceID, taskID, dataType, marketType string, cfg Config) (IDs, error) {
	if m == nil || m.metadata == nil {
		return IDs{}, fmt.Errorf("task result metadata manager is not configured")
	}
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	if spaceID == "" || taskID == "" {
		return IDs{}, fmt.Errorf("space_id and task_id are required")
	}
	ids := resultIDs(taskID)
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
	// derived 是由市场决定、已有结果数据集缺了要补上的属性：market_type，以及交易时段市场时序结果的 calendar
	// （策略按 calendar 选交易日历，缺省按 crypto_24x7）；alignment 是数据集的正式字段（策略按它校验行键）。
	derived, alignment, err := SessionAttributes(spaceID)
	if err != nil {
		return IDs{}, err
	}
	if marketType != "" {
		derived["market_type"] = marketType
	}
	for key, value := range derived {
		attrs[key] = value
	}
	get, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return IDs{}, fmt.Errorf("get result dataset: %w", err)
	}
	if get == nil {
		return IDs{}, metadataError("get result dataset", nil, nil)
	}
	if get.GetRetInfo().GetCode() == storagepb.ErrorCode_DATASET_NOT_FOUND || get.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		columns, columnsErr := m.resultDatasetColumns(spaceID, cfg.OutputFields)
		if columnsErr != nil {
			return IDs{}, columnsErr
		}
		// 数据集连同列在一次调用里建好，建好即可写入。
		created, createErr := m.metadata.UpsertDataset(ctx, &storagepb.UpsertDatasetReq{Dataset: &storagepb.Dataset{
			SpaceId: spaceID, DatasetId: ids.DatasetID, DataSourceId: dataSourceID,
			Name: resultDisplayName(cfg, taskID), Description: cfg.Description, DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freq: datasetFrequency(cfg.Frequency), Alignment: alignment, Attributes: attrs, SubjectTags: subjectTags,
		}, Columns: columns})
		if createErr != nil || created == nil || created.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return IDs{}, metadataError("create result dataset", createErr, retInfoDataset(created))
		}
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
		assignAlignment := alignment != "" && get.GetDataset().GetAlignment() == ""
		if !sameSubjectTags(get.GetDataset().GetSubjectTags(), subjectTags) || assignSource || assignAlignment || len(missing) > 0 {
			updated := proto.Clone(get.GetDataset()).(*storagepb.Dataset)
			updated.SubjectTags = subjectTags
			if assignAlignment {
				updated.Alignment = alignment
			}
			if assignSource {
				updated.DataSourceId = dataSourceID
			}
			if len(missing) > 0 && updated.Attributes == nil {
				updated.Attributes = map[string]string{}
			}
			for key, value := range missing {
				updated.Attributes[key] = value
			}
			response, updateErr := m.metadata.UpsertDataset(ctx, &storagepb.UpsertDatasetReq{Dataset: updated})
			if updateErr != nil || response == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
				return IDs{}, metadataError("update result dataset subject tags or data source", updateErr, func() *storagepb.RetInfo {
					if response == nil {
						return nil
					}
					return response.GetRetInfo()
				}())
			}
		}
		// 已有的结果数据集按同一份列定义补齐：上一轮建数据集时缓存没发布成功，这里也会让 Storage 重新发布。
		if err := m.ensureColumns(ctx, spaceID, ids.DatasetID, cfg.OutputFields); err != nil {
			return IDs{}, err
		}
	}
	return ids, nil
}

// UpdateSubjectTags updates the scope of an existing task-owned result Dataset
// without reprovisioning its columns or physical rows.
func (m *Manager) UpdateSubjectTags(ctx context.Context, spaceID, datasetID string, tags []string) error {
	if m == nil || m.metadata == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	rsp, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
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
	result, err := m.metadata.UpsertDataset(ctx, &storagepb.UpsertDatasetReq{Dataset: updated})
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

// EnsureAllowedLateness 保证结果数据集的允许迟到时间（Storage 建立周期时读取）与任务设置一致；没有变化时不更新。
func (m *Manager) EnsureAllowedLateness(ctx context.Context, spaceID, datasetID string, seconds int) error {
	if m == nil || m.metadata == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if seconds <= 0 {
		return fmt.Errorf("allowed lateness must be positive")
	}
	rsp, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
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
	if rsp.GetDataset().GetAllowedLatenessSeconds() == uint32(seconds) {
		return nil
	}
	updated := proto.Clone(rsp.GetDataset()).(*storagepb.Dataset)
	updated.AllowedLatenessSeconds = uint32(seconds)
	result, err := m.metadata.UpsertDataset(ctx, &storagepb.UpsertDatasetReq{Dataset: updated})
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

// EnsureOutputFields 按任务的输出字段补齐结果数据集的列；查询索引跟着数据集的列走，由 storage-view 原地加列。
func (m *Manager) EnsureOutputFields(ctx context.Context, spaceID, taskID, datasetID, dataType string, outputFields []string) error {
	if m == nil || m.metadata == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if len(outputFields) == 0 {
		return fmt.Errorf("at least one output field is required")
	}
	ids := IDs{DatasetID: strings.TrimSpace(datasetID)}
	if ids.DatasetID == "" {
		return fmt.Errorf("result dataset_id is required")
	}
	datasetExists, err := m.resultDatasetExists(ctx, spaceID, taskID, ids)
	if err != nil {
		return err
	}
	if !datasetExists {
		return fmt.Errorf("task result Dataset is unavailable")
	}
	return m.ensureColumns(ctx, spaceID, ids.DatasetID, outputFields)
}

// EnsureWithCleanup provisions a raw result and returns a compensating action
// for callers that have not yet committed the owning task row. Existing
// task-owned resources are never removed by that action.
func (m *Manager) EnsureWithCleanup(ctx context.Context, spaceID, taskID, dataType, marketType string, cfg Config) (IDs, func(context.Context) error, error) {
	ids := resultIDs(taskID)
	datasetExists, err := m.resultDatasetExists(ctx, spaceID, taskID, ids)
	if err != nil {
		return IDs{}, nil, err
	}
	cleanup := func(cleanupCtx context.Context) error {
		if datasetExists {
			return nil
		}
		current, getErr := m.metadata.GetDataset(cleanupCtx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: ids.DatasetID})
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
		dataset, deleteErr := m.metadata.DeleteDataset(cleanupCtx, &storagepb.DeleteDatasetReq{SpaceId: spaceID, DatasetId: ids.DatasetID})
		if deleteErr != nil || !resultDeleteAccepted(retInfoDatasetDelete(dataset)) {
			return metadataError("cleanup result dataset", deleteErr, retInfoDatasetDelete(dataset))
		}
		return nil
	}
	ensured, err := m.Ensure(ctx, spaceID, taskID, dataType, marketType, cfg)
	if err != nil {
		return IDs{}, nil, err
	}
	return ensured, cleanup, nil
}

// resultDatasetExists 返回结果数据集是否已存在；存在但属于别的任务时报错。
func (m *Manager) resultDatasetExists(ctx context.Context, spaceID, taskID string, ids IDs) (bool, error) {
	dataset, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return false, err
	}
	if dataset == nil || dataset.GetRetInfo() == nil {
		return false, fmt.Errorf("get result dataset: empty response")
	}
	exists := dataset.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS
	if exists && (dataset.GetDataset() == nil || !ownedByTask(dataset.GetDataset().GetAttributes(), taskID)) {
		return false, fmt.Errorf("result dataset %s is owned by another task", ids.DatasetID)
	}
	if !exists && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND {
		return false, metadataError("get result dataset", nil, dataset.GetRetInfo())
	}
	return exists, nil
}

// ensureColumns 把结果数据集的列逐个补齐（已有的列按同一定义覆盖）。
func (m *Manager) ensureColumns(ctx context.Context, spaceID, datasetID string, outputFields []string) error {
	columns, err := m.resultDatasetColumns(spaceID, outputFields)
	if err != nil {
		return err
	}
	for _, column := range columns {
		column.SpaceId, column.DatasetId = spaceID, datasetID
		resp, err := m.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{Column: column})
		if err != nil || resp == nil || resp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return metadataError("create result dataset column", err, retInfoDatasetColumn(resp))
		}
	}
	return nil
}

// OutputField 是结果数据集能建的一列：字段名、值类型、中文显示名与分组。
// 中文名与分组只在这里（基础字段）与 marketdata.FieldSpec（附加字段）登记，建列时原样写到数据集列上。
type OutputField struct {
	Name        string
	Kind        storagepb.FieldValueType
	DisplayName string
	Group       string
}

// 结果列的分组：一级中文名，所有空间通用。
const (
	GroupIdentity = "标识信息"
	GroupQuote    = "行情价格"
	GroupTrading  = "成交估值"
)

const (
	typeDouble = storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	typeInt    = storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
	typeString = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	typeTime   = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
)

// KlineOutputFields 是 K 线（时序）结果数据集的基础输出字段，按建列顺序；任务的输出字段只能从这里与附加字段里选。
// 管理台的任务表单经 GetDataTypeConfigWithFields 读它，合成任务按字段名取显示名与分组，都不另抄一份。
var KlineOutputFields = []OutputField{
	{"open", typeDouble, "开盘价", GroupQuote}, {"high", typeDouble, "最高价", GroupQuote},
	{"low", typeDouble, "最低价", GroupQuote}, {"close", typeDouble, "收盘价", GroupQuote},
	{"volume", typeDouble, "成交量", GroupTrading}, {"quote_volume", typeDouble, "计价币成交额", GroupTrading},
	{"trade_num", typeInt, "成交笔数", GroupTrading}, {"amount", typeDouble, "成交额", GroupTrading},
	{"instrument_name", typeString, "证券名称", GroupIdentity}, {"provider_id", typeString, "数据提供方", GroupIdentity},
	{"source_id", typeString, "数据通道", GroupIdentity}, {"provider_symbol", typeString, "来源代码", GroupIdentity},
	{"volume_unit", typeString, "成交量单位", GroupTrading}, {"amount_unit", typeString, "成交额单位", GroupTrading},
	{"trade_date", typeTime, "交易日", GroupIdentity}, {"close_time", typeTime, "收盘时间", GroupIdentity},
	{"provider_timestamp", typeTime, "源时间戳", GroupIdentity}, {"fetched_at", typeTime, "抓取时间", GroupIdentity},
	{"request_id", typeString, "请求ID", GroupIdentity}, {"route_id", typeString, "路由ID", GroupIdentity},
	{"route_rank", typeInt, "路由优先级", GroupIdentity}, {"source_provider", typeString, "源提供方", GroupIdentity},
	{"quality_status", typeString, "质量状态", GroupTrading}, {"amount_quality", typeString, "成交额质量", GroupTrading},
}

// KlineOutputField 按字段名取基础 K 线字段；不是基础字段时返回 false。
func KlineOutputField(name string) (OutputField, bool) {
	for _, field := range KlineOutputFields {
		if field.Name == name {
			return field, true
		}
	}
	return OutputField{}, false
}

// resultDatasetColumns 返回结果数据集的列定义：默认取全部基础 K 线字段；指定了输出字段时只取选中的那些，并校验字段都受支持。
func (m *Manager) resultDatasetColumns(spaceID string, outputFields []string) ([]*storagepb.DatasetColumn, error) {
	fields := append([]OutputField(nil), KlineOutputFields...)
	extra := map[string]marketdata.FieldSpec{}
	if len(outputFields) > 0 {
		// 附加字段只在显式选中时建列；输出字段为空（默认字段）时不建。
		for _, spec := range m.fields.Specs(spaceID) {
			extra[spec.ID] = spec
			fields = append(fields, OutputField{Name: spec.ID, Kind: extraFieldType(spec), DisplayName: spec.Name, Group: spec.Group})
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
			if _, ok := selected[field.Name]; ok {
				filtered = append(filtered, field)
				delete(selected, field.Name)
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
		column := &storagepb.DatasetColumn{ColumnName: field.Name, ValueType: field.Kind, DisplayName: field.DisplayName, Group: field.Group, Required: true, Status: "active"}
		if spec, ok := extra[field.Name]; ok {
			column.Attributes = extraColumnAttributes(spec)
		}
		columns = append(columns, column)
	}
	return columns, nil
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
	case "stockcn_kline_1m":
		return "A股分钟K线"
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

// Delete 删掉结果数据集：Storage 服务端一并删掉它的行，查询索引由 storage-view 下一轮维护时摘掉。
func (m *Manager) Delete(ctx context.Context, spaceID string, ids IDs) error {
	if m == nil || m.metadata == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	removed, err := m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{SpaceId: spaceID, DatasetId: ids.DatasetID})
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
		storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
}

// DeleteForTask verifies Collector ownership before deleting metadata. The
// deterministic Dataset ID prevents a malformed task row from targeting an
// unrelated result.
func (m *Manager) DeleteForTask(ctx context.Context, spaceID, taskID string, ids IDs) error {
	if err := m.ValidateOwnedForTask(ctx, spaceID, taskID, ids); err != nil {
		return err
	}
	return m.Delete(ctx, spaceID, ids)
}

// ValidateOwnedForTask 按数据集属性 owner_module、collector_task_id 校验归属，不改 Storage。
// 调用方可以先校验再删本地记录，被拒绝的删除仍可完整重试。
func (m *Manager) ValidateOwnedForTask(ctx context.Context, spaceID, taskID string, ids IDs) error {
	if m == nil || m.metadata == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	if ids.DatasetID != resultIDs(taskID).DatasetID {
		return fmt.Errorf("result Dataset identity is not the deterministic result of task %s", taskID)
	}
	dataset, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: ids.DatasetID})
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
	return nil
}

// datasetFrequency returns the Dataset freq in canonical form ("1H" becomes
// "1h"). An unknown frequency is passed through for Storage to reject.
func datasetFrequency(frequency string) string {
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

func retInfoDataset(resp *storagepb.UpsertDatasetRsp) *storagepb.RetInfo {
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

func retInfoDatasetDelete(resp *storagepb.DeleteDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

// IsDatasetNotFound 报告 Storage 元数据错误码是否表示数据集不存在。
func IsDatasetNotFound(code storagepb.ErrorCode) bool {
	return code == storagepb.ErrorCode_DATASET_NOT_FOUND || code == storagepb.ErrorCode_NOT_FOUND
}

// SessionAttributes 返回交易时段市场时序结果数据集的 calendar 属性与 alignment（stockcn 为 cn_stock、session），取自 barclock 的市场登记。
// 连续交易的市场（crypto）都不写：策略缺省按 crypto_24x7。市场未登记时报错，不当成连续交易。
func SessionAttributes(spaceID string) (map[string]string, string, error) {
	market, err := barclock.MarketFor(spaceID)
	if err != nil {
		return nil, "", err
	}
	if market.Alignment != barclock.AlignmentSession {
		return map[string]string{}, "", nil
	}
	return map[string]string{"calendar": market.CalendarID}, market.Alignment, nil
}

// ValueTypeName 把列类型换成接口里的类型名：double、int、string、bool 或 time。
func ValueTypeName(kind storagepb.FieldValueType) string {
	switch kind {
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "double"
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "int"
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "bool"
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return "time"
	default:
		return "string"
	}
}

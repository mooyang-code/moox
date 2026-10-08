package input

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// RPCClient 通过节点网关调用 Storage 的 Metadata / DataView 与 Factor 的 FactorMgr。
type RPCClient struct {
	Metadata storagepb.MetadataClientProxy
	DataView storagepb.DataViewClientProxy
	Factor   factorpb.FactorMgrClientProxy
	// Auth 是 Metadata 的调用身份；ViewAuth 是 DataView 的调用身份（Storage 用不同的密钥校验）。
	Auth     *commonpb.AuthInfo
	ViewAuth *commonpb.AuthInfo
	PageSize uint32
}

func (c *RPCClient) pageSize() uint32 {
	if c != nil && c.PageSize > 0 {
		return c.PageSize
	}
	return 500
}

// GetView 读取 View 及其全部列。
func (c *RPCClient) GetView(ctx context.Context, spaceID, viewID string) (ViewInfo, error) {
	if c == nil || c.Metadata == nil {
		return ViewInfo{}, errors.New("Storage Metadata 客户端未配置")
	}
	rsp, err := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.Auth, SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return ViewInfo{}, fmt.Errorf("读取 View %s：%w", viewID, err)
	}
	if err := retError("GetView", rsp.GetRetInfo()); err != nil {
		return ViewInfo{}, err
	}
	view := rsp.GetView()
	if view == nil {
		return ViewInfo{}, fmt.Errorf("View %s 不存在", viewID)
	}
	info := ViewInfo{ViewID: view.GetViewId(), DatasetID: view.GetDatasetId(), Frequency: view.GetFreq(), Status: view.GetStatus(), ActiveIndexID: strings.TrimSpace(view.GetActiveIndexId())}
	if from := strings.TrimSpace(view.GetIndexedFrom()); from != "" {
		if at, err := time.Parse(time.RFC3339Nano, from); err == nil {
			info.IndexedFrom = at.UTC()
		}
	}
	if to := strings.TrimSpace(view.GetIndexedTo()); to != "" {
		if at, err := time.Parse(time.RFC3339Nano, to); err == nil {
			info.IndexedTo = at.UTC()
		}
	}
	for page := uint32(1); ; page++ {
		columns, err := c.Metadata.ListViewColumns(ctx, &storagepb.ListViewColumnsReq{AuthInfo: c.Auth, SpaceId: spaceID, ViewId: viewID, Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
		if err != nil {
			return ViewInfo{}, fmt.Errorf("读取 View %s 的列：%w", viewID, err)
		}
		if err := retError("ListViewColumns", columns.GetRetInfo()); err != nil {
			return ViewInfo{}, err
		}
		for _, column := range columns.GetColumns() {
			if column == nil || column.GetColumnName() == "" {
				continue
			}
			attributes := make(map[string]string, len(column.GetAttributes()))
			for key, value := range column.GetAttributes() {
				attributes[key] = value
			}
			info.Columns = append(info.Columns, ViewColumn{Name: column.GetColumnName(), Attributes: attributes})
		}
		if !columns.GetPageResult().GetHasMore() {
			break
		}
	}
	return info, nil
}

// GetDataset 读取数据集元数据。
func (c *RPCClient) GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error) {
	if c == nil || c.Metadata == nil {
		return DatasetInfo{}, errors.New("Storage Metadata 客户端未配置")
	}
	rsp, err := c.Metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: c.Auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return DatasetInfo{}, fmt.Errorf("读取数据集 %s：%w", datasetID, err)
	}
	if err := retError("GetDataset", rsp.GetRetInfo()); err != nil {
		return DatasetInfo{}, err
	}
	dataset := rsp.GetDataset()
	if dataset == nil {
		return DatasetInfo{}, fmt.Errorf("数据集 %s 不存在", datasetID)
	}
	attributes := make(map[string]string, len(dataset.GetAttributes()))
	for key, value := range dataset.GetAttributes() {
		attributes[key] = value
	}
	return DatasetInfo{DatasetID: dataset.GetDatasetId(), Status: dataset.GetStatus(), Frequency: dataset.GetFreq(), Retention: dataset.GetRetention(), Attributes: attributes, SubjectTags: append([]string(nil), dataset.GetSubjectTags()...)}, nil
}

// GetTag 读取标签的市场类型。
func (c *RPCClient) GetTag(ctx context.Context, spaceID, tagID string) (TagInfo, error) {
	if c == nil || c.Metadata == nil {
		return TagInfo{}, errors.New("Storage Metadata 客户端未配置")
	}
	rsp, err := c.Metadata.GetTag(ctx, &storagepb.GetTagReq{AuthInfo: c.Auth, SpaceId: spaceID, TagId: tagID})
	if err != nil {
		return TagInfo{}, fmt.Errorf("读取标签 %s：%w", tagID, err)
	}
	if err := retError("GetTag", rsp.GetRetInfo()); err != nil {
		return TagInfo{}, err
	}
	tag := rsp.GetTag()
	if tag == nil {
		return TagInfo{}, fmt.Errorf("标签 %s 不存在", tagID)
	}
	return TagInfo{TagID: tag.GetTagId(), MarketType: strings.ToLower(strings.TrimSpace(tag.GetMarketType()))}, nil
}

// ListDatasetSubjects 读取数据集的全部标的绑定（含停用的）及标的详情。
func (c *RPCClient) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]Subject, error) {
	if c == nil || c.Metadata == nil {
		return nil, errors.New("Storage Metadata 客户端未配置")
	}
	bindings := make(map[string]bool)
	order := make([]string, 0)
	for page := uint32(1); ; page++ {
		rsp, err := c.Metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{AuthInfo: c.Auth, SpaceId: spaceID, DatasetId: datasetID, Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
		if err != nil {
			return nil, fmt.Errorf("读取数据集 %s 的标的绑定：%w", datasetID, err)
		}
		if err := retError("ListDatasetSubjects", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, binding := range rsp.GetDatasetSubjects() {
			id := strings.TrimSpace(binding.GetSubjectId())
			if binding == nil || id == "" {
				continue
			}
			if _, seen := bindings[id]; !seen {
				order = append(order, id)
			}
			bindings[id] = binding.GetStatus() == "active"
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	details := make(map[string]*storagepb.Subject, len(order))
	const batch = 200
	for start := 0; start < len(order); start += batch {
		end := start + batch
		if end > len(order) {
			end = len(order)
		}
		for page := uint32(1); ; page++ {
			rsp, err := c.Metadata.ListSubjects(ctx, &storagepb.ListSubjectsReq{AuthInfo: c.Auth, SpaceId: spaceID, SubjectIds: order[start:end], Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
			if err != nil {
				return nil, fmt.Errorf("读取标的详情：%w", err)
			}
			if err := retError("ListSubjects", rsp.GetRetInfo()); err != nil {
				return nil, err
			}
			for _, subject := range rsp.GetSubjects() {
				if subject != nil && subject.GetSubjectId() != "" {
					details[subject.GetSubjectId()] = subject
				}
			}
			if !rsp.GetPageResult().GetHasMore() {
				break
			}
		}
	}
	subjects := make([]Subject, 0, len(order))
	for _, id := range order {
		subject := Subject{SubjectID: id, InstrumentID: id, Active: bindings[id]}
		if detail, ok := details[id]; ok {
			attributes := detail.GetAttributes()
			subject.Attributes = make(map[string]string, len(attributes))
			for key, value := range attributes {
				subject.Attributes[key] = value
			}
			if instrument := strings.TrimSpace(attributes["instrument_id"]); instrument != "" {
				subject.InstrumentID = instrument
			}
			subject.SeriesTag = seriesTag(attributes)
			subject.Active = subject.Active && detail.GetStatus() == "active"
		} else {
			subject.Active = false
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}

// seriesTag 取标的的序列标签；旧的采集标的没有显式 series_tag 时按交易所派生。
func seriesTag(attributes map[string]string) string {
	if tag := strings.TrimSpace(attributes["series_tag"]); tag != "" {
		return tag
	}
	if exchange := strings.TrimSpace(attributes["exchange"]); exchange != "" {
		return "venue:" + strings.ToLower(exchange)
	}
	return ""
}

// ListTagMembers 返回标签的活跃成员标的 ID。
func (c *RPCClient) ListTagMembers(ctx context.Context, spaceID, tagID string) ([]string, error) {
	if c == nil || c.Metadata == nil {
		return nil, errors.New("Storage Metadata 客户端未配置")
	}
	members := make([]string, 0)
	for page := uint32(1); ; page++ {
		rsp, err := c.Metadata.ListTagMembers(ctx, &storagepb.ListTagMembersReq{AuthInfo: c.Auth, SpaceId: spaceID, TagId: tagID, Status: "active", Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
		if err != nil {
			return nil, fmt.Errorf("读取标签 %s 的成员：%w", tagID, err)
		}
		if err := retError("ListTagMembers", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, member := range rsp.GetMembers() {
			if member == nil || member.GetSubject() == nil {
				continue
			}
			if id := strings.TrimSpace(member.GetSubject().GetSubjectId()); id != "" {
				members = append(members, id)
			}
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return uniqueSorted(members), nil
}

// QueryRows 读取 [Start, End) 内的全部页面，固定活动索引与修订号，变化即返回 ErrStale。
func (c *RPCClient) QueryRows(ctx context.Context, spaceID string, query Query) ([]Row, uint64, error) {
	if c == nil || c.DataView == nil {
		return nil, 0, errors.New("Storage DataView 客户端未配置")
	}
	if strings.TrimSpace(query.ExpectedIndexID) == "" {
		return nil, 0, fmt.Errorf("View %s 没有活动索引", query.ViewID)
	}
	if len(query.Subjects) == 0 {
		return []Row{}, query.ExpectedRevision, nil
	}
	selectors := make([]*storagepb.TimeSeriesSelector, 0, len(query.Subjects))
	for _, subject := range query.Subjects {
		selector := &storagepb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: query.DatasetID, SubjectId: subject.SubjectID, Freq: query.Frequency}
		if subject.SeriesTag != "" {
			tag := subject.SeriesTag
			selector.SeriesTag = &tag
		}
		selectors = append(selectors, selector)
	}
	auth := c.ViewAuth
	if auth == nil {
		auth = c.Auth
	}
	rows := make([]Row, 0, len(selectors))
	revision := query.ExpectedRevision
	for page := uint32(1); ; page++ {
		rsp, err := c.DataView.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: spaceID, ViewId: query.ViewID, Selectors: selectors,
			TimeRange:   &storagepb.TimeRange{StartTime: query.Start.UTC().Format(time.RFC3339Nano), EndTime: query.End.UTC().Format(time.RFC3339Nano)},
			ColumnNames: query.Columns, Page: &commonpb.Page{Page: page, Size: c.pageSize()}, TotalMode: commonpb.TotalMode_NONE,
			ExpectedActiveIndexId: query.ExpectedIndexID, ExpectedActiveIndexRevision: revision,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("读取 View %s 的行：%w", query.ViewID, err)
		}
		if err := viewRetError(rsp.GetRetInfo()); err != nil {
			return nil, 0, err
		}
		if served := rsp.GetServedActiveIndexRevision(); served != 0 {
			if revision != 0 && revision != served {
				return nil, 0, fmt.Errorf("%w：View %s 的索引修订号从 %d 变为 %d", ErrStale, query.ViewID, revision, served)
			}
			revision = served
		}
		if servedIndex := strings.TrimSpace(rsp.GetServedActiveIndexId()); servedIndex != "" && servedIndex != query.ExpectedIndexID {
			return nil, 0, fmt.Errorf("%w：View %s 的活动索引从 %s 变为 %s", ErrStale, query.ViewID, query.ExpectedIndexID, servedIndex)
		}
		for _, row := range rsp.GetRows() {
			converted, err := convertRow(row)
			if err != nil {
				return nil, 0, err
			}
			rows = append(rows, converted)
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return rows, revision, nil
}

func convertRow(row *storagepb.TimeSeriesRow) (Row, error) {
	if row == nil || row.GetKey() == nil {
		return Row{}, errors.New("Storage 返回了无效的时序行")
	}
	at, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
	if err != nil {
		return Row{}, fmt.Errorf("解析 data_time：%w", err)
	}
	values := make(map[string]float64, len(row.GetFields()))
	for _, field := range row.GetFields() {
		if field == nil || field.GetValue() == nil {
			continue
		}
		if number, ok := numericValue(field.GetValue()); ok {
			values[field.GetFieldId()] = number
		}
	}
	return Row{SubjectID: row.GetKey().GetSubjectId(), SeriesTag: row.GetKey().GetSeriesTag(), DataTime: at.UTC(), Values: values}, nil
}

// numericValue 把 Storage 的类型值转为 float64；非数值或非有限值视为缺失。
func numericValue(value *storagepb.TypedValue) (float64, bool) {
	switch typed := value.GetValue().(type) {
	case *storagepb.TypedValue_DoubleValue:
		if math.IsNaN(typed.DoubleValue) || math.IsInf(typed.DoubleValue, 0) {
			return 0, false
		}
		return typed.DoubleValue, true
	case *storagepb.TypedValue_IntValue:
		return float64(typed.IntValue), true
	case *storagepb.TypedValue_StringValue:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed.StringValue), 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}

// GetFactor 读取因子定义的指纹与输出。
func (c *RPCClient) GetFactor(ctx context.Context, factorID string) (FactorInfo, error) {
	if c == nil || c.Factor == nil {
		return FactorInfo{}, errors.New("Factor 客户端未配置")
	}
	rsp, err := c.Factor.GetFactor(ctx, &factorpb.GetFactorReq{FactorId: factorID})
	if err != nil {
		return FactorInfo{}, fmt.Errorf("读取因子 %s：%w", factorID, err)
	}
	if err := retError("GetFactor", rsp.GetRetInfo()); err != nil {
		return FactorInfo{}, err
	}
	factor := rsp.GetFactor()
	if factor == nil || factor.GetFactorId() == "" {
		return FactorInfo{}, fmt.Errorf("因子 %s 不存在", factorID)
	}
	return FactorInfo{FactorID: factor.GetFactorId(), DefinitionHash: strings.TrimSpace(factor.GetDefinitionHash()), Outputs: append([]string(nil), factor.GetOutputs()...)}, nil
}

func retError(method string, info *commonpb.RetInfo) error {
	if info == nil || info.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	return fmt.Errorf("%s 返回 %s：%s", method, info.GetCode().String(), info.GetMsg())
}

// viewRetError 把 Storage 的"索引已变化"错误映射为 ErrStale，其余原样返回。
func viewRetError(info *commonpb.RetInfo) error {
	if info == nil || info.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	message := strings.ToLower(info.GetMsg())
	if info.GetCode() == commonpb.ErrorCode_VIEW_NOT_READY && (strings.Contains(message, "revision changed") || strings.Contains(message, "index changed") || strings.Contains(message, "updated during query")) {
		return fmt.Errorf("%w：%s", ErrStale, info.GetMsg())
	}
	if info.GetCode() == commonpb.ErrorCode_VIEW_COLUMN_NOT_FOUND || info.GetCode() == commonpb.ErrorCode_FIELD_NOT_FOUND {
		// 列不存在是确定性的配置问题，重试不会恢复。
		return &SkipError{Reason: SkipConfigError, Detail: "View 的列已不存在：" + info.GetMsg()}
	}
	return fmt.Errorf("QueryTimeSeriesRows 返回 %s：%s", info.GetCode().String(), info.GetMsg())
}

// Package source reads dataset columns and time-range rows from Storage through the Access gateway.
package source

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"google.golang.org/protobuf/proto"
)

// AppID 是 worker 在 Storage 请求体角色认证里的 AppId，只用于识别调用方。
const AppID = "archive"

// Range 是一次区间读取的请求。
type Range struct {
	SpaceID   string
	DatasetID string
	Columns   []staging.Column
	Start     time.Time
	End       time.Time
	Cursor    string
	PageRows  int
}

// Page 是一页结果。
type Page struct {
	Rows       []staging.Row
	NextCursor string
	// Bytes 是这一页响应的字节数（用于带宽限制）。
	Bytes int64
}

// Client 经网关读取 Storage。
type Client struct {
	gateway gatewayclient.Invoker
	secret  string

	mu    sync.Mutex
	infos map[string][]ColumnInfo
}

// ColumnInfo 是一个值列的元数据（来自 Storage 的数据集列定义），写进 <space>/<purpose>/<dir_name>/dataset.json。
type ColumnInfo struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	DisplayName string            `json:"display_name,omitempty"`
	OriginType  string            `json:"origin_type,omitempty"`
	OriginID    string            `json:"origin_id,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// ColumnInfos 返回最近一次 Columns 调用得到的列元数据（同一数据集）；没有调用过返回 nil。
func (c *Client) ColumnInfos(spaceID, datasetID string) []ColumnInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.infos[spaceID+"/"+datasetID]
}

// New 创建客户端；secret 是 Storage 的 Primary 签名密钥（可空，仅用于没有开启认证的环境）。
func New(gateway gatewayclient.Invoker, secret string) *Client {
	return &Client{gateway: gateway, secret: strings.TrimSpace(secret)}
}

func (c *Client) auth() *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{AppId: AppID, Operator: "archive-worker", RequestId: fmt.Sprintf("archive-worker-%d", time.Now().UnixNano())}
	if c.secret != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(c.secret, []byte(auth.AppId))
	}
	return auth
}

const columnPageSize = 200

// Columns 返回数据集的值列：来源为字段或因子、状态为启用的列，按列名排序；系统列不导出。
func (c *Client) Columns(ctx context.Context, spaceID, datasetID string) ([]staging.Column, error) {
	var out []staging.Column
	var infos []ColumnInfo
	for page := uint32(1); ; page++ {
		rsp := &storagepb.ListDatasetColumnsRsp{}
		req := &storagepb.ListDatasetColumnsReq{AuthInfo: c.auth(), SpaceId: spaceID, DatasetId: datasetID, Page: &commonpb.Page{Page: page, Size: columnPageSize}}
		if err := c.gateway.Invoke(ctx, "trpc.moox.storage.Metadata", "ListDatasetColumns", req, rsp); err != nil {
			return nil, fmt.Errorf("list dataset columns %s/%s: %w", spaceID, datasetID, err)
		}
		if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list dataset columns %s/%s: %s", spaceID, datasetID, rsp.GetRetInfo().GetMsg())
		}
		for _, column := range rsp.GetColumns() {
			if status := column.GetStatus(); status != "" && status != "active" {
				continue
			}
			switch column.GetOriginType() {
			case storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_CUSTOM:
			default:
				continue
			}
			typ, ok := columnType(column.GetValueType())
			if !ok {
				return nil, fmt.Errorf("dataset %s/%s column %q has unsupported value type %s", spaceID, datasetID, column.GetColumnName(), column.GetValueType())
			}
			out = append(out, staging.Column{Name: column.GetColumnName(), Type: typ})
			attributes := column.GetAttributes()
			infos = append(infos, ColumnInfo{
				Name: column.GetColumnName(), Type: typ, DisplayName: attributes["display_name"], OriginType: originName(column.GetOriginType()),
				OriginID: column.GetOriginId(), Required: column.GetRequired(), Aliases: column.GetAliases(), Attributes: attributes,
			})
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dataset %s/%s has no value columns to archive", spaceID, datasetID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	c.mu.Lock()
	if c.infos == nil {
		c.infos = map[string][]ColumnInfo{}
	}
	c.infos[spaceID+"/"+datasetID] = infos
	c.mu.Unlock()
	return out, nil
}

func columnType(t storagepb.FieldValueType) (string, bool) {
	switch t {
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING:
		return staging.TypeString, true
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return staging.TypeInt, true
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return staging.TypeDouble, true
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return staging.TypeBool, true
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return staging.TypeTime, true
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return staging.TypeJSON, true
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return staging.TypeBytes, true
	default:
		return "", false
	}
}

// ReadRange 读取时间区间 [Start, End) 的一页：不指定标的，按游标翻页。
func (c *Client) ReadRange(ctx context.Context, req Range) (Page, error) {
	names := make([]string, len(req.Columns))
	for i, col := range req.Columns {
		names[i] = col.Name
	}
	rsp := &storagepb.ReadTimeSeriesRowsRsp{}
	call := &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: c.auth(), SpaceId: req.SpaceID, DatasetId: req.DatasetID, ColumnNames: names,
		TimeRange: &storagepb.TimeRange{StartTime: req.Start.UTC().Format(time.RFC3339Nano), EndTime: req.End.UTC().Format(time.RFC3339Nano)},
		Page:      &commonpb.Page{Size: uint32(req.PageRows), Cursor: req.Cursor},
	}
	if err := c.gateway.Invoke(gatewayclient.WithoutRetry(ctx), "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", call, rsp); err != nil {
		return Page{}, fmt.Errorf("read rows %s/%s: %w", req.SpaceID, req.DatasetID, err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return Page{}, fmt.Errorf("read rows %s/%s: %s", req.SpaceID, req.DatasetID, rsp.GetRetInfo().GetMsg())
	}
	index := make(map[string]int, len(req.Columns))
	for i, col := range req.Columns {
		index[col.Name] = i
	}
	page := Page{Bytes: int64(proto.Size(rsp))}
	for _, row := range rsp.GetRows() {
		at, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil {
			return Page{}, fmt.Errorf("row %s has invalid data_time %q: %w", row.GetKey().GetSubjectId(), row.GetKey().GetDataTime(), err)
		}
		values := make([]any, len(req.Columns))
		for _, field := range row.GetFields() {
			i, ok := index[field.GetFieldId()]
			if !ok {
				continue
			}
			value, err := convert(req.Columns[i].Type, field.GetValue())
			if err != nil {
				return Page{}, fmt.Errorf("row %s@%s column %s: %w", row.GetKey().GetSubjectId(), row.GetKey().GetDataTime(), field.GetFieldId(), err)
			}
			values[i] = value
		}
		page.Rows = append(page.Rows, staging.Row{SubjectID: row.GetKey().GetSubjectId(), DataTime: at.UTC(), SeriesTag: row.GetKey().GetSeriesTag(), Values: values})
	}
	if pr := rsp.GetPageResult(); pr.GetHasMore() || pr.GetNextCursor() != "" {
		page.NextCursor = pr.GetNextCursor()
		if pr.GetHasMore() && page.NextCursor == "" {
			return Page{}, errors.New("storage reported more rows but returned no cursor")
		}
	}
	return page, nil
}

// ChangeCursor 是 Storage 变更日志的读取位点；序号只在同一个 StoreID 内可比较。
type ChangeCursor struct {
	StoreID  string
	Sequence uint64
}

// Changes 是变更日志的一页。
type Changes struct {
	// Spans 是这一页里时间序列行写入涉及的时间范围（含两端，UTC），不分标的。
	Spans [][2]time.Time
	// Next 是已扫描到的位点，原样用作下一次读取的游标。
	Next    ChangeCursor
	HasMore bool
	// Gap 为真表示游标早于日志保留起点或存储已重建，游标之后的变更读不全。
	Gap bool
	// Bytes 是这一页响应的字节数（用于带宽限制）。
	Bytes int64
}

// changePageLimit 是一页最多扫描的日志条数（Storage 的上限）。
const changePageLimit = 2000

// ReadChanges 从游标之后读一页数据集的变更日志；cursor 为 nil 时只取日志头部的位点。
func (c *Client) ReadChanges(ctx context.Context, spaceID, datasetID string, cursor *ChangeCursor) (Changes, error) {
	req := &storagepb.ReadDatasetChangesReq{AuthInfo: c.auth(), SpaceId: spaceID, DatasetId: datasetID, FromHead: cursor == nil, Limit: changePageLimit}
	if cursor != nil {
		req.Cursor = &storagepb.DatasetChangeCursor{StoreId: cursor.StoreID, Sequence: cursor.Sequence}
	}
	rsp := &storagepb.ReadDatasetChangesRsp{}
	if err := c.gateway.Invoke(ctx, "trpc.moox.storage.PrimaryStore", "ReadDatasetChanges", req, rsp); err != nil {
		return Changes{}, fmt.Errorf("read dataset changes %s/%s: %w", spaceID, datasetID, err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return Changes{}, fmt.Errorf("read dataset changes %s/%s: %s", spaceID, datasetID, rsp.GetRetInfo().GetMsg())
	}
	out := Changes{
		Next:    ChangeCursor{StoreID: rsp.GetNextCursor().GetStoreId(), Sequence: rsp.GetNextCursor().GetSequence()},
		HasMore: rsp.GetHasMore(), Gap: rsp.GetGap(), Bytes: int64(proto.Size(rsp)),
	}
	// 只看时间序列行条目的范围；完成标记与记录型行条目没有范围。
	for _, change := range rsp.GetChanges() {
		for _, r := range change.GetRanges() {
			start, startErr := time.Parse(time.RFC3339Nano, r.GetStartTime())
			end, endErr := time.Parse(time.RFC3339Nano, r.GetEndTime())
			if startErr != nil || endErr != nil {
				return Changes{}, fmt.Errorf("read dataset changes %s/%s: 无效的时间范围 %q-%q", spaceID, datasetID, r.GetStartTime(), r.GetEndTime())
			}
			out.Spans = append(out.Spans, [2]time.Time{start.UTC(), end.UTC()})
		}
	}
	return out, nil
}

// convert 把 Storage 的值转换成暂存列类型对应的 Go 值；缺失或 null 返回 nil。
func convert(columnType string, value *storagepb.TypedValue) (any, error) {
	if value == nil || value.GetValue() == nil {
		return nil, nil
	}
	if _, isNull := value.GetValue().(*storagepb.TypedValue_NullValue); isNull {
		return nil, nil
	}
	switch columnType {
	case staging.TypeDouble:
		switch v := value.GetValue().(type) {
		case *storagepb.TypedValue_DoubleValue:
			return v.DoubleValue, nil
		case *storagepb.TypedValue_IntValue:
			return float64(v.IntValue), nil
		}
	case staging.TypeInt:
		if v, ok := value.GetValue().(*storagepb.TypedValue_IntValue); ok {
			return v.IntValue, nil
		}
	case staging.TypeString:
		if v, ok := value.GetValue().(*storagepb.TypedValue_StringValue); ok {
			return v.StringValue, nil
		}
	case staging.TypeBool:
		if v, ok := value.GetValue().(*storagepb.TypedValue_BoolValue); ok {
			return v.BoolValue, nil
		}
	case staging.TypeTime:
		if v, ok := value.GetValue().(*storagepb.TypedValue_TimeValue); ok {
			at, err := time.Parse(time.RFC3339Nano, v.TimeValue)
			if err != nil {
				return nil, fmt.Errorf("invalid time value %q: %w", v.TimeValue, err)
			}
			return at.UTC(), nil
		}
	case staging.TypeJSON:
		if v, ok := value.GetValue().(*storagepb.TypedValue_JsonValue); ok {
			return v.JsonValue, nil
		}
	case staging.TypeBytes:
		if v, ok := value.GetValue().(*storagepb.TypedValue_BytesValue); ok {
			return v.BytesValue, nil
		}
	}
	return nil, fmt.Errorf("value %T does not match column type %s", value.GetValue(), columnType)
}

func originName(t storagepb.DatasetColumnOriginType) string {
	switch t {
	case storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD:
		return "field"
	case storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_CUSTOM:
		return "custom"
	default:
		return ""
	}
}

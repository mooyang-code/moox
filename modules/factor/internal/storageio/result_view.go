package storageio

import (
	"context"
	"errors"
	"fmt"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
)

// 默认 View 由 factor-mgr 自己建、改、删：Storage 不再替因子结果数据集托管 View。
// 这些属性告诉 Storage 怎么维护它，Storage 不认识“因子”：
//   - follow_dataset_columns：数据集加列后 View 跟着加列；
//   - maintenance_priority：维护顺序，数字越小越先，因子结果排在采集结果之后。
const (
	ViewAttrFollowColumns       = "follow_dataset_columns"
	ViewAttrMaintenancePriority = "maintenance_priority"
	defaultResultViewPriority   = "10"
)

// DefaultResultViewID 是结果数据集的默认 View ID：view_<数据集 ID 去掉 dataset_ 前缀>。
func DefaultResultViewID(datasetID string) string {
	return "view_" + strings.TrimPrefix(strings.TrimSpace(datasetID), "dataset_")
}

func isViewNotFound(code commonpb.ErrorCode) bool {
	return code == commonpb.ErrorCode_NOT_FOUND || code == commonpb.ErrorCode_VIEW_NOT_FOUND
}

// EnsureResultView 保证结果数据集有默认 View：没有就按数据集当前的有效列建一个，已有就确认它属于这个数据集。
// 之后数据集再加列，Storage 按 follow_dataset_columns 自动扩展它。
func (c *Client) EnsureResultView(ctx context.Context, spaceID, datasetID string) error {
	if err := c.metadataReady("ensure result view"); err != nil {
		return err
	}
	viewID := DefaultResultViewID(datasetID)
	existing, err := c.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.auth, SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return rpcError("get result view", err)
	}
	if existing == nil || existing.GetRetInfo() == nil {
		return fmt.Errorf("%w: get result view returned an empty response", ErrInfra)
	}
	if existing.GetRetInfo().GetCode() == commonpb.ErrorCode_SUCCESS {
		if got := existing.GetView().GetDatasetId(); got != datasetID {
			return fmt.Errorf("默认 View %s 属于另一个数据集 %q", viewID, got)
		}
		return nil
	}
	if !isViewNotFound(existing.GetRetInfo().GetCode()) {
		return responseError("get result view", existing.GetRetInfo())
	}
	dataset, err := c.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	if dataset.DataKind != DataKindTimeSeries || dataset.Freq == "" {
		return errors.New("结果数据集必须是带频率的时序数据集才能建默认 View")
	}
	columns, err := c.ListColumns(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	viewColumns := make([]*storagepb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		if column.Status != "" && column.Status != ColumnStatusActive {
			continue
		}
		viewColumns = append(viewColumns, &storagepb.ViewColumn{
			SpaceId: spaceID, ViewId: viewID, ColumnName: column.ColumnName,
			OriginType: storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: column.ColumnName,
			ValueType: columnTypeToProto(column.ValueType), SortOrder: uint32(len(viewColumns)),
			Attributes: cloneStringMap(column.Attributes),
		})
	}
	created, err := c.metadata.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: c.auth, CreateOnly: true, View: &storagepb.View{
		SpaceId: spaceID, ViewId: viewID, Name: dataset.Name, DatasetId: datasetID,
		GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Freq: dataset.Freq,
		Engine: "duckdb", Status: "active", Columns: viewColumns,
		Attributes: map[string]string{
			"owner_module":              "factor",
			"view_role":                 "factor_result", // 页面按它找到结果 View；Storage 不读
			ViewAttrFollowColumns:       "true",
			ViewAttrMaintenancePriority: defaultResultViewPriority,
		},
	}})
	if err != nil {
		return rpcError("create result view", err)
	}
	if created == nil || created.GetRetInfo() == nil {
		return fmt.Errorf("%w: create result view returned an empty response", ErrInfra)
	}
	if created.GetRetInfo().GetCode() == commonpb.ErrorCode_CONFLICT {
		// 并发创建：以已存在的为准，确认它属于这个数据集。
		return c.EnsureResultView(ctx, spaceID, datasetID)
	}
	return responseError("create result view", created.GetRetInfo())
}

// DeleteResultView 删除结果数据集的默认 View；本来就不存在不算失败。
func (c *Client) DeleteResultView(ctx context.Context, spaceID, datasetID string) error {
	if err := c.metadataReady("delete result view"); err != nil {
		return err
	}
	rsp, err := c.metadata.DeleteView(ctx, &storagepb.DeleteViewReq{AuthInfo: c.auth, SpaceId: spaceID, ViewId: DefaultResultViewID(datasetID)})
	if err != nil {
		return rpcError("delete result view", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return fmt.Errorf("%w: delete result view returned an empty response", ErrInfra)
	}
	if isViewNotFound(rsp.GetRetInfo().GetCode()) {
		return nil
	}
	return responseError("delete result view", rsp.GetRetInfo())
}

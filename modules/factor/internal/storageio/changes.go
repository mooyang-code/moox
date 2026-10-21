package storageio

import (
	"context"
	"fmt"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// ChangeCursor 是 Storage 行变更日志的读取位点；序号只在 (NodeID, StoreID) 内可比较。
type ChangeCursor struct {
	NodeID   string
	StoreID  string
	Sequence uint64
}

// ChangeRange 是一个标的在一次写入里受影响的时间点范围（含两端，UTC）。
type ChangeRange struct {
	Subject string
	Freq    string
	Start   time.Time
	End     time.Time
}

// Change 是一次写入对一个数据集的变更。
type Change struct {
	Sequence  uint64
	DatasetID string
	Ranges    []ChangeRange
}

// Changes 是一次读取变更日志的结果。
type Changes struct {
	Changes []Change
	Next    ChangeCursor
	HasMore bool
	// Gap 为真表示游标早于日志保留起点，或游标身份与当前存储对不上：本地副本要走对账。
	Gap bool
}

// ReadDatasetChanges 从游标之后读取数据集所在 DataNode 的行变更日志；fromHead 为真时只返回头部位点（取基线）。
func (c *Client) ReadDatasetChanges(ctx context.Context, spaceID, datasetID string, cursor *ChangeCursor, fromHead bool, limit int) (Changes, error) {
	if err := c.primaryReady("read dataset changes"); err != nil {
		return Changes{}, err
	}
	spaceID, datasetID = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID)
	if spaceID == "" || datasetID == "" {
		return Changes{}, fmt.Errorf("space_id and dataset_id are required")
	}
	req := &storagepb.ReadDatasetChangesReq{AuthInfo: c.auth, SpaceId: spaceID, DatasetId: datasetID, FromHead: fromHead, Limit: uint32(max(limit, 0))}
	if cursor != nil {
		req.Cursor = &storagepb.DatasetChangeCursor{SourceNodeId: cursor.NodeID, SourceStoreId: cursor.StoreID, Sequence: cursor.Sequence}
	}
	rsp, err := c.primary.ReadDatasetChanges(ctx, req)
	if err != nil {
		return Changes{}, rpcError("read dataset changes", err)
	}
	if rsp == nil {
		return Changes{}, fmt.Errorf("%w: read dataset changes returned an empty response", ErrInfra)
	}
	if err := readResponseError("read dataset changes", rsp.GetRetInfo()); err != nil {
		return Changes{}, err
	}
	out := Changes{HasMore: rsp.GetHasMore(), Gap: rsp.GetGap()}
	if next := rsp.GetNextCursor(); next != nil {
		out.Next = ChangeCursor{NodeID: next.GetSourceNodeId(), StoreID: next.GetSourceStoreId(), Sequence: next.GetSequence()}
	}
	for _, change := range rsp.GetChanges() {
		item := Change{Sequence: change.GetSequence(), DatasetID: change.GetDatasetId()}
		for _, r := range change.GetRanges() {
			start, startErr := time.Parse(time.RFC3339Nano, r.GetStartTime())
			end, endErr := time.Parse(time.RFC3339Nano, r.GetEndTime())
			if startErr != nil || endErr != nil {
				return Changes{}, fmt.Errorf("read dataset changes: invalid range %q-%q", r.GetStartTime(), r.GetEndTime())
			}
			item.Ranges = append(item.Ranges, ChangeRange{Subject: r.GetSubjectId(), Freq: r.GetFreq(), Start: start.UTC(), End: end.UTC()})
		}
		out.Changes = append(out.Changes, item)
	}
	return out, nil
}

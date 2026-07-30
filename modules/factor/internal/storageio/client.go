package storageio

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
)

// AccessClient is the Storage Access RPC subset used by factor.
type AccessClient interface {
	ReadTimeSeriesRows(ctx context.Context, req *storagepb.ReadTimeSeriesRowsReq, opts ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
	UpsertFields(ctx context.Context, req *storagepb.PrimaryUpsertFieldsReq, opts ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error)
}

// WindowKey identifies one source time-series scope.
type WindowKey struct {
	SpaceID       string
	SourceDataset string
	SubjectID     string
	Freq          string
}

// Client wraps Storage Access RPCs.
type Client struct {
	access AccessClient
	auth   *commonpb.AuthInfo
}

func NewClientWithCredentials(accessTarget, targetNode string, credentials gatewayauth.Credentials, auth *commonpb.AuthInfo) *Client {
	target := gatewayauth.ServiceGatewayTarget(NormalizeStorageTarget(accessTarget, "11003"))
	return &Client{
		access: storagepb.NewPrimaryStoreClientProxy(gatewayauth.NewTRPCClientOptions(target, targetNode, credentials)...),
		auth:   auth,
	}
}

const rangeReadPageSize = 2000

// RangeChunk contains history plus target rows and the distinct target periods.
type RangeChunk struct {
	Frame         *engine.DataFrame
	TargetPeriods []time.Time
	Complete      bool
	IndexedTo     time.Time
}

// EndExpansion is an event range expanded over dependent source periods.
type EndExpansion struct {
	EndTime   time.Time
	Complete  bool
	IndexedTo time.Time
}

// ReadRangeChunk reads a bounded target range and prepends the required history.
func (c *Client) ReadRangeChunk(
	ctx context.Context,
	key WindowKey,
	startTime, endTime time.Time,
	lookbackPeriods, targetLimit int,
	columns []string,
) (*RangeChunk, error) {
	if targetLimit <= 0 {
		targetLimit = 2000
	}
	targetRows, targetPeriods, complete, indexedTo, err := c.readPeriods(ctx, key, &storagepb.TimeRange{
		StartTime: startTime.UTC().Format(time.RFC3339Nano),
		EndTime:   endTime.UTC().Format(time.RFC3339Nano),
	}, storagepb.SortOrder_SORT_ORDER_ASC, targetLimit, columns)
	if err != nil {
		return nil, err
	}
	targetFrame, err := RowsToDataFrame(targetRows, columns)
	if err != nil {
		return nil, err
	}
	if len(targetFrame.DataTimes) == 0 {
		return &RangeChunk{Frame: targetFrame, Complete: complete, IndexedTo: indexedTo}, nil
	}
	historyLimit := lookbackPeriods - 1
	var historyFrame *engine.DataFrame
	if historyLimit > 0 {
		historyRows, _, _, _, readErr := c.readPeriods(ctx, key, &storagepb.TimeRange{
			EndTime: targetFrame.DataTimes[0].UTC().Format(time.RFC3339Nano),
		}, storagepb.SortOrder_SORT_ORDER_DESC, historyLimit, columns)
		if readErr != nil {
			return nil, readErr
		}
		historyFrame, err = RowsToDataFrame(historyRows, columns)
		if err != nil {
			return nil, err
		}
	} else {
		historyFrame = &engine.DataFrame{Columns: append([]string(nil), columns...)}
	}
	frame := &engine.DataFrame{
		Columns:    append([]string(nil), columns...),
		Rows:       append(append([][]any(nil), historyFrame.Rows...), targetFrame.Rows...),
		DataTimes:  append(append([]time.Time(nil), historyFrame.DataTimes...), targetFrame.DataTimes...),
		SeriesTags: append(append([]string(nil), historyFrame.SeriesTags...), targetFrame.SeriesTags...),
	}
	return &RangeChunk{
		Frame: frame, TargetPeriods: append([]time.Time(nil), targetPeriods...),
		Complete: complete, IndexedTo: indexedTo,
	}, nil
}

// ExpandEndByPeriods advances an event range over the next actual data periods.
func (c *Client) ExpandEndByPeriods(
	ctx context.Context,
	key WindowKey,
	endTime time.Time,
	periods int,
) (*EndExpansion, error) {
	if periods <= 0 {
		return &EndExpansion{EndTime: endTime, Complete: true}, nil
	}
	_, nextPeriods, complete, indexedTo, err := c.readPeriods(ctx, key, &storagepb.TimeRange{
		StartTime: endTime.UTC().Format(time.RFC3339Nano),
	}, storagepb.SortOrder_SORT_ORDER_ASC, periods, nil)
	if err != nil {
		return nil, err
	}
	// Open-ended View reads cannot report coverage complete. Receiving every
	// requested successor period is nevertheless sufficient for expansion.
	expanded := &EndExpansion{
		EndTime: endTime, Complete: complete || len(nextPeriods) >= periods, IndexedTo: indexedTo,
	}
	if len(nextPeriods) == 0 {
		return expanded, nil
	}
	expanded.EndTime = nextPeriods[len(nextPeriods)-1].Add(time.Nanosecond)
	return expanded, nil
}

func (c *Client) readPeriods(
	ctx context.Context,
	key WindowKey,
	timeRange *storagepb.TimeRange,
	order storagepb.SortOrder,
	periodLimit int,
	columns []string,
) ([]*storagepb.TimeSeriesRow, []time.Time, bool, time.Time, error) {
	if periodLimit <= 0 {
		return nil, nil, true, time.Time{}, nil
	}
	var rows []*storagepb.TimeSeriesRow
	periods := make([]time.Time, 0, periodLimit)
	seen := make(map[int64]struct{}, periodLimit+1)
	complete := true
	var indexedTo time.Time
	for page := uint32(1); ; page++ {
		rsp, err := c.readRowsPage(ctx, key, timeRange, order, page, rangeReadPageSize, columns)
		if err != nil {
			return nil, nil, false, time.Time{}, err
		}
		complete = complete && rsp.GetComplete()
		if raw := rsp.GetServedIndexedTo(); raw != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, raw)
			if parseErr != nil {
				return nil, nil, false, time.Time{}, fmt.Errorf("parse served_indexed_to %q: %w", raw, parseErr)
			}
			if parsed.After(indexedTo) {
				indexedTo = parsed.UTC()
			}
		}
		pageRows := rsp.GetRows()
		reachedNextPeriod := false
		for _, row := range pageRows {
			dataTime, parseErr := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
			if parseErr != nil {
				return nil, nil, false, time.Time{}, fmt.Errorf("parse data_time %q: %w", row.GetKey().GetDataTime(), parseErr)
			}
			nanos := dataTime.UTC().UnixNano()
			if _, exists := seen[nanos]; !exists {
				if len(periods) == periodLimit {
					reachedNextPeriod = true
					break
				}
				seen[nanos] = struct{}{}
				periods = append(periods, dataTime.UTC())
			}
			rows = append(rows, row)
		}
		if reachedNextPeriod || len(pageRows) == 0 || !responseHasMore(rsp, len(pageRows)) {
			break
		}
	}
	return rows, periods, complete, indexedTo, nil
}

func responseHasMore(rsp *storagepb.ReadTimeSeriesRowsRsp, rowCount int) bool {
	if rsp.GetPageResult() != nil {
		return rsp.GetPageResult().GetHasMore()
	}
	return rowCount == rangeReadPageSize
}

func (c *Client) readRowsPage(
	ctx context.Context,
	key WindowKey,
	timeRange *storagepb.TimeRange,
	order storagepb.SortOrder,
	page uint32,
	size uint32,
	columns []string,
) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	req := &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo:  c.auth,
		SpaceId:   key.SpaceID,
		DatasetId: key.SourceDataset,
		Selectors: []*storagepb.TimeSeriesSelector{
			{
				SpaceId:   key.SpaceID,
				DatasetId: key.SourceDataset,
				SubjectId: key.SubjectID,
				Freq:      key.Freq,
			},
		},
		TimeRange:   timeRange,
		Order:       order,
		ColumnNames: qualifyDatasetColumns(key.SourceDataset, columns),
		Page:        &commonpb.Page{Page: page, Size: size},
	}
	// Retry is deliberately attached to this idempotent read call only. The
	// shared proxy also performs writes, which must never inherit this policy.
	rsp, err := c.access.ReadTimeSeriesRows(ctx, req, client.WithFilter(trpcretry.ReadOnly()))
	if err != nil {
		return nil, fmt.Errorf("read time-series rows: %w", err)
	}
	if err := ensureStorageOK("read time-series rows", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp, nil
}

func qualifyDatasetColumns(datasetID string, columns []string) []string {
	if columns == nil {
		return nil
	}
	qualified := make([]string, len(columns))
	for index, column := range columns {
		qualified[index] = datasetID + "." + column
	}
	return qualified
}

// NormalizeStorageTarget normalizes bare host:port targets to tRPC ip:// targets.
func NormalizeStorageTarget(raw string, defaultPort string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:" + defaultPort
	}
	if strings.HasPrefix(raw, "ip://") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return raw
	}
	if err == nil && parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return raw
	}
	if err == nil && parsed.Host != "" {
		return "ip://" + parsed.Host
	}
	if strings.Contains(raw, "://") || !strings.Contains(raw, ":") {
		return raw
	}
	return "ip://" + raw
}

func ensureStorageOK(action string, ret *commonpb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}

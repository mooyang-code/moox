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

// RangeChunk contains history plus target rows and the exact target timestamps.
type RangeChunk struct {
	Frame       *engine.DataFrame
	TargetTimes []time.Time
}

// ReadRangeChunk reads a bounded target range and prepends the required history.
func (c *Client) ReadRangeChunk(
	ctx context.Context,
	key WindowKey,
	startTime, endTime time.Time,
	lookbackBars, targetLimit int,
	columns []string,
) (*RangeChunk, error) {
	if targetLimit <= 0 {
		targetLimit = 2000
	}
	targetRows, err := c.readRows(ctx, key, &storagepb.TimeRange{
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
		return &RangeChunk{Frame: targetFrame}, nil
	}
	historyLimit := lookbackBars - 1
	var historyFrame *engine.DataFrame
	if historyLimit > 0 {
		historyRows, readErr := c.readRows(ctx, key, &storagepb.TimeRange{
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
		Columns:   append([]string(nil), columns...),
		Rows:      append(append([][]any(nil), historyFrame.Rows...), targetFrame.Rows...),
		DataTimes: append(append([]time.Time(nil), historyFrame.DataTimes...), targetFrame.DataTimes...),
	}
	return &RangeChunk{Frame: frame, TargetTimes: append([]time.Time(nil), targetFrame.DataTimes...)}, nil
}

func (c *Client) readRows(
	ctx context.Context,
	key WindowKey,
	timeRange *storagepb.TimeRange,
	order storagepb.SortOrder,
	limit int,
	columns []string,
) ([]*storagepb.TimeSeriesRow, error) {
	req := &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: c.auth,
		Keys: []*storagepb.TimeSeriesKey{
			{
				SpaceId:   key.SpaceID,
				DatasetId: key.SourceDataset,
				SubjectId: key.SubjectID,
				Freq:      key.Freq,
			},
		},
		TimeRange:   timeRange,
		Order:       order,
		ColumnNames: columns,
		Page:        &commonpb.Page{Page: 1, Size: uint32(limit)},
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
	return rsp.GetRows(), nil
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

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
	"trpc.group/trpc-go/trpc-go/client"
)

// AccessClient is the Storage Access RPC subset used by factor.
type AccessClient interface {
	ReadTimeSeriesRows(ctx context.Context, req *storagepb.ReadTimeSeriesRowsReq, opts ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
	WriteTimeSeriesRows(ctx context.Context, req *storagepb.WriteTimeSeriesRowsReq, opts ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error)
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

// NewClient creates a StorageIO client from a target string.
func NewClient(accessTarget string, auth *commonpb.AuthInfo) *Client {
	return &Client{
		access: storagepb.NewAccessClientProxy(client.WithTarget(NormalizeStorageTarget(accessTarget, "20102"))),
		auth:   auth,
	}
}

// NewClientWithAccess creates a StorageIO client around a supplied Access client.
func NewClientWithAccess(access AccessClient, auth *commonpb.AuthInfo) *Client {
	return &Client{access: access, auth: auth}
}

// ReadWindow reads up to lookbackBars rows ending at endTime.
func (c *Client) ReadWindow(ctx context.Context, key WindowKey, lookbackBars int, endTime time.Time, columns []string) (*engine.DataFrame, error) {
	if lookbackBars <= 0 {
		lookbackBars = 1
	}
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
		TimeRange:   &storagepb.TimeRange{EndTime: endTime.UTC().Format(time.RFC3339)},
		Order:       storagepb.SortOrder_SORT_ORDER_DESC,
		ColumnNames: columns,
		Page:        &commonpb.Page{Page: 1, Size: uint32(lookbackBars)},
	}
	rsp, err := c.access.ReadTimeSeriesRows(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("read time-series rows: %w", err)
	}
	if err := ensureStorageOK("read time-series rows", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return RowsToDataFrame(rsp.GetRows(), columns)
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

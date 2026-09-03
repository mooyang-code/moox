package storageio

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
)

// AccessClient is the Storage Access RPC subset used by factor.
type AccessClient interface {
	UpsertFields(ctx context.Context, req *storagepb.PrimaryUpsertFieldsReq, opts ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error)
}

type ViewClient interface {
	QueryTimeSeriesRows(ctx context.Context, req *storagepb.QueryTimeSeriesRowsReq, opts ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error)
}

// ViewRevisionReader exposes the persisted write revision used to fence a
// manual/recalc source snapshot before factor tasks are started.
type ViewRevisionReader interface {
	ActiveViewRevision(context.Context, string, string, string, string) (uint64, error)
}

// ViewRevisionAtReader is the fenced form used by recalc. The expected index
// ID is checked by the same DataView request that returns the revision, so an
// A/B cutover cannot be silently split across two probes.
type ViewRevisionAtReader interface {
	ActiveViewRevisionAt(context.Context, string, string, string, string, string) (uint64, error)
}

// WindowKey identifies one source time-series scope.
type WindowKey struct {
	SpaceID                     string
	SourceViewID                string
	SourceDataset               string
	SubjectID                   string
	Freq                        string
	ExpectedActiveIndexID       string
	ExpectedActiveIndexRevision uint64
}

// ActiveViewRevision probes DataView once and returns the revision token of
// the currently active physical index. It is intentionally a read-only
// capability used by recalc, whose synthetic source-ready event has no
// upstream revision field to carry.
func (c *Client) ActiveViewRevision(ctx context.Context, spaceID, viewID, subjectID, frequency string) (uint64, error) {
	return c.activeViewRevision(ctx, spaceID, viewID, subjectID, frequency, "")
}

func (c *Client) ActiveViewRevisionAt(ctx context.Context, spaceID, viewID, subjectID, frequency, expectedIndexID string) (uint64, error) {
	return c.activeViewRevision(ctx, spaceID, viewID, subjectID, frequency, expectedIndexID)
}

func (c *Client) activeViewRevision(ctx context.Context, spaceID, viewID, subjectID, frequency, expectedIndexID string) (uint64, error) {
	if c == nil || c.view == nil {
		return 0, fmt.Errorf("storage View client is unavailable")
	}
	filter := &storagepb.FilterSpec{Groups: []*storagepb.FilterGroup{{Conds: []*storagepb.FilterCond{
		{Column: "subject_id", Op: storagepb.FilterOp_FILTER_OP_EQ, Values: []*storagepb.TypedValue{stringValue(subjectID)}},
		{Column: "freq", Op: storagepb.FilterOp_FILTER_OP_EQ, Values: []*storagepb.TypedValue{stringValue(frequency)}},
	}}}}
	rsp, err := c.view.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{
		AuthInfo: c.viewRequestAuth(), SpaceId: spaceID, ViewId: viewID, Filter: filter, Limit: 1, ExpectedActiveIndexId: expectedIndexID,
		TotalMode: storagepb.TotalMode_NONE,
	})
	if err != nil {
		return 0, fmt.Errorf("read active View revision: %w", err)
	}
	if err := classifyViewReadRet("read active View revision", rsp.GetRetInfo()); err != nil {
		return 0, err
	}
	if rsp.GetServedActiveIndexRevision() == 0 {
		return 0, fmt.Errorf("active View %s has no revision", viewID)
	}
	return rsp.GetServedActiveIndexRevision(), nil
}

// Client wraps Storage Access RPCs.
type Client struct {
	access    AccessClient
	view      ViewClient
	manifests OutputManifestStore
	auth      *commonpb.AuthInfo
	viewAuth  *commonpb.AuthInfo
}

func NewClientWithCredentials(accessTarget, targetNode string, credentials gatewayauth.Credentials, auth *commonpb.AuthInfo) *Client {
	// Storage is an explicit dependency. Do not replace its target with the
	// process-local service gateway: a control-plane Factor may use a remote
	// Storage node and the target-node signature must match that endpoint.
	target := NormalizeStorageTarget(accessTarget, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, targetNode, credentials)
	return &Client{
		access:   storagepb.NewPrimaryStoreClientProxy(options...),
		view:     storagepb.NewDataViewClientProxy(options...),
		auth:     auth,
		viewAuth: auth,
	}
}

// WithViewAuth sets the credentials used for DataView reads. PrimaryStore
// writes and legacy reads continue to use the primary credentials in auth.
// Storage exposes these services with separate secrets in a deployed profile.
func (c *Client) WithViewAuth(auth *commonpb.AuthInfo) *Client {
	if c == nil {
		return c
	}
	c.viewAuth = auth
	return c
}

const rangeReadPageSize = 2000

// RangeChunk contains history plus target rows and the distinct target periods.
type RangeChunk struct {
	Frame         *engine.DataFrame
	TargetPeriods []time.Time
	Complete      bool
	IndexedTo     time.Time
}

type EndExpansion struct {
	EndTime   time.Time
	Complete  bool
	IndexedTo time.Time
}

// ReadPeriodChunk reads one acknowledged target period together with its
// lookback in a single descending View query. View-ready execution calls this
// path once per subject; generic multi-period/manual ranges keep using
// ReadRangeChunk below.
func (c *Client) ReadPeriodChunk(
	ctx context.Context,
	key WindowKey,
	startTime, endTime time.Time,
	lookbackPeriods int,
	columns []string,
) (*RangeChunk, error) {
	if key.SourceViewID == "" {
		key.SourceViewID = key.SourceDataset
	}
	if lookbackPeriods < 1 {
		lookbackPeriods = 1
	}
	rows, periods, complete, indexedTo, err := c.readPeriods(ctx, key, &storagepb.TimeRange{
		EndTime: endTime.UTC().Format(time.RFC3339Nano),
	}, storagepb.SortOrder_SORT_ORDER_DESC, lookbackPeriods, columns)
	if err != nil {
		return nil, err
	}
	frame, err := RowsToDataFrame(rows, columns)
	if err != nil {
		return nil, nonRetryableRead(err)
	}
	targetPeriods := make([]time.Time, 0, 1)
	for _, period := range periods {
		if !period.Before(startTime) && period.Before(endTime) {
			targetPeriods = append(targetPeriods, period.UTC())
		}
	}
	sort.Slice(targetPeriods, func(i, j int) bool { return targetPeriods[i].Before(targetPeriods[j]) })
	return &RangeChunk{
		Frame: frame, TargetPeriods: targetPeriods, Complete: complete, IndexedTo: indexedTo,
	}, nil
}

// ExpandEndByPeriods is retained for manual callers; View-ready execution no longer polls it.
func (c *Client) ExpandEndByPeriods(ctx context.Context, key WindowKey, endTime time.Time, periods int) (*EndExpansion, error) {
	if key.SourceViewID == "" {
		key.SourceViewID = key.SourceDataset
	}
	if periods <= 0 {
		return &EndExpansion{EndTime: endTime, Complete: true}, nil
	}
	_, next, complete, indexedTo, err := c.readPeriods(ctx, key, &storagepb.TimeRange{StartTime: endTime.UTC().Format(time.RFC3339Nano)}, storagepb.SortOrder_SORT_ORDER_ASC, periods, nil)
	if err != nil {
		return nil, err
	}
	result := &EndExpansion{EndTime: endTime, Complete: complete || len(next) >= periods, IndexedTo: indexedTo}
	if len(next) > 0 {
		result.EndTime = next[len(next)-1].Add(time.Nanosecond)
	}
	return result, nil
}

// ReadRangeChunk reads a bounded target range and prepends the required history.
func (c *Client) ReadRangeChunk(
	ctx context.Context,
	key WindowKey,
	startTime, endTime time.Time,
	lookbackPeriods, targetLimit int,
	columns []string,
) (*RangeChunk, error) {
	if key.SourceViewID == "" {
		key.SourceViewID = key.SourceDataset
	}
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
		return nil, nonRetryableRead(err)
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
			return nil, nonRetryableRead(err)
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
	pageKey := key
	for page := uint32(1); ; page++ {
		rsp, err := c.readRowsPage(ctx, pageKey, timeRange, order, page, rangeReadPageSize, columns)
		if err != nil {
			return nil, nil, false, time.Time{}, err
		}
		complete = complete && rsp.GetComplete()
		if servedRevision := rsp.GetServedActiveIndexRevision(); servedRevision != 0 {
			if pageKey.ExpectedActiveIndexRevision != 0 && pageKey.ExpectedActiveIndexRevision != servedRevision {
				return nil, nil, false, time.Time{}, fmt.Errorf("active View revision changed: expected=%d actual=%d", pageKey.ExpectedActiveIndexRevision, servedRevision)
			}
			pageKey.ExpectedActiveIndexRevision = servedRevision
		}
		if raw := rsp.GetServedIndexedTo(); raw != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, raw)
			if parseErr != nil {
				return nil, nil, false, time.Time{}, nonRetryableRead(fmt.Errorf("parse served_indexed_to %q: %w", raw, parseErr))
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
				return nil, nil, false, time.Time{}, nonRetryableRead(fmt.Errorf("parse data_time %q: %w", row.GetKey().GetDataTime(), parseErr))
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

func responseHasMore(rsp *storagepb.QueryTimeSeriesRowsRsp, rowCount int) bool {
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
) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	req := &storagepb.QueryTimeSeriesRowsReq{
		AuthInfo:  c.viewRequestAuth(),
		SpaceId:   key.SpaceID,
		ViewId:    key.SourceViewID,
		TimeRange: timeRange,
		Filter: &storagepb.FilterSpec{Groups: []*storagepb.FilterGroup{{Conds: []*storagepb.FilterCond{
			{Column: "subject_id", Op: storagepb.FilterOp_FILTER_OP_EQ, Values: []*storagepb.TypedValue{stringValue(key.SubjectID)}},
			{Column: "freq", Op: storagepb.FilterOp_FILTER_OP_EQ, Values: []*storagepb.TypedValue{stringValue(key.Freq)}},
		}}}},
		Sorts: []*storagepb.SortSpec{{FieldName: "data_time", Desc: order == storagepb.SortOrder_SORT_ORDER_DESC}},
		// DataView resolves an unqualified logical input to its unique projected
		// column suffix and rejects ambiguity. This pushes the group's column
		// union into DuckDB instead of fetching every View field.
		ColumnNames:                 append([]string(nil), columns...),
		Page:                        &commonpb.Page{Page: page, Size: size},
		TotalMode:                   storagepb.TotalMode_NONE,
		ExpectedActiveIndexId:       key.ExpectedActiveIndexID,
		ExpectedActiveIndexRevision: key.ExpectedActiveIndexRevision,
	}
	// TaskRunner owns the two-attempt tail retry policy. Do not add an RPC-level
	// retry here or one slow subject will retain its read-worker slot.
	if c.view == nil {
		legacy, ok := c.access.(interface {
			ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
		})
		if !ok {
			return nil, nonRetryableRead(fmt.Errorf("storage View client is unavailable"))
		}
		legacyRsp, legacyErr := legacy.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{AuthInfo: c.auth, SpaceId: key.SpaceID, DatasetId: key.SourceViewID, Selectors: []*storagepb.TimeSeriesSelector{{SpaceId: key.SpaceID, DatasetId: key.SourceViewID, SubjectId: key.SubjectID, Freq: key.Freq}}, TimeRange: timeRange, Order: order, ColumnNames: qualifyDatasetColumns(key.SourceViewID, columns), Page: req.Page})
		if legacyErr != nil {
			return nil, fmt.Errorf("read time-series rows: %w", legacyErr)
		}
		if retErr := classifyViewReadRet("read time-series rows", legacyRsp.GetRetInfo()); retErr != nil {
			return nil, retErr
		}
		return &storagepb.QueryTimeSeriesRowsRsp{RetInfo: legacyRsp.GetRetInfo(), Rows: legacyRsp.GetRows(), PageResult: legacyRsp.GetPageResult(), ServedIndexedFrom: legacyRsp.GetServedIndexedFrom(), ServedIndexedTo: legacyRsp.GetServedIndexedTo(), Complete: legacyRsp.GetComplete()}, nil
	}
	rsp, err := c.view.QueryTimeSeriesRows(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("read time-series rows: %w", err)
	}
	if err := classifyViewReadRet("read time-series rows", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp, nil
}

func nonRetryableRead(err error) error {
	if err == nil {
		return nil
	}
	return engine.NonRetryableError{Err: err}
}

func classifyViewReadRet(action string, ret *commonpb.RetInfo) error {
	err := ensureStorageOK(action, ret)
	if err == nil {
		return nil
	}
	if ret != nil {
		switch ret.GetCode() {
		case commonpb.ErrorCode_INNER_ERR, commonpb.ErrorCode_VIEW_NOT_READY, commonpb.ErrorCode_CONFLICT:
			return err
		}
	}
	return nonRetryableRead(err)
}

func (c *Client) viewRequestAuth() *commonpb.AuthInfo {
	if c != nil && c.viewAuth != nil {
		return c.viewAuth
	}
	if c == nil {
		return nil
	}
	return c.auth
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

package binance

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
)

const datasetSubjectPageSize = 1000

type storageWriter struct {
	access      storagepb.PrimaryStoreClientProxy
	metadata    storagepb.MetadataClientProxy
	authInfo    *storagepb.AuthInfo
	writeSource string
}

// BatchStorage is the small Storage surface used by short-lived market fetches.
type BatchStorage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	LatestTimeSeriesTime(context.Context, *storagepb.TimeSeriesSelector) (time.Time, bool, error)
	RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
	BindDatasetSubject(context.Context, *storagepb.DatasetSubject) error
}

// ResampleStorage is the PrimaryStore subset used by Collector-local K-line
// resampling. It remains separate from BatchStorage so existing market-fetch
// fakes do not need to implement exact reads.
type ResampleStorage interface {
	ReadFields(context.Context, []*storagepb.RowKey, []string, []string) ([]*storagepb.RowFieldValues, error)
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
}

type ResampleViewSyncWaiter interface {
	WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error)
}

// ResampleMetadataClient exposes the Metadata proxy and service identity used
// by the Collector-local catalog preparer.
type ResampleMetadataClient struct {
	Client storagepb.MetadataClientProxy
	Auth   *storagepb.AuthInfo
}

// NewResampleMetadataClient creates an authenticated Metadata proxy for local
// target Dataset/View preparation.
func NewResampleMetadataClient(accessTarget, instType string) (*ResampleMetadataClient, error) {
	if normalized, normalizeErr := InstTypeForMarket(instType); normalizeErr == nil {
		instType = normalized
	}
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	target := storageGatewayTarget(accessTarget, accessTarget)
	serviceOptions := gatewayauth.NewTRPCClientOptions(normalizeStorageTarget(target, "11003"), strings.TrimSpace(os.Getenv("MOOX_GATEWAY_TARGET_NODE")), gatewayauth.CredentialsFromEnv())
	return &ResampleMetadataClient{Client: storagepb.NewMetadataClientProxy(serviceOptions...), Auth: storageAuthInfo(binding)}, nil
}

// NewResampleStorage creates a Storage adapter for local Collector workers.
func NewResampleStorage(accessTarget, instType, writeSource string) (ResampleStorage, error) {
	if normalized, normalizeErr := InstTypeForMarket(instType); normalizeErr == nil {
		instType = normalized
	}
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	return newStorageWriter(accessTarget, accessTarget, storageAuthInfo(binding), writeSource), nil
}

// ReconcileSymbolSnapshot disables memberships that disappeared from the
// latest exchange snapshot.
func (w *storageWriter) ReconcileSymbolSnapshot(ctx context.Context, spaceID, datasetID string, active []*exchange.SymbolInfo) error {
	memberships, err := w.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	return reconcileInactiveSymbolMemberships(ctx, w, spaceID, datasetID, active, memberships)
}

// NewBatchStorage creates the shared Storage Primary/Metadata adapter used by
// a short-lived SCF invocation.
func NewBatchStorage(accessTarget, instType string) (BatchStorage, error) {
	return NewBatchStorageWithWriteSource(accessTarget, instType, "")
}

// NewBatchStorageWithWriteSource creates a Storage adapter carrying the
// canonical write source into the Storage outbox event.
func NewBatchStorageWithWriteSource(accessTarget, instType, writeSource string) (BatchStorage, error) {
	if normalized, normalizeErr := InstTypeForMarket(instType); normalizeErr == nil {
		instType = normalized
	}
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	return newStorageWriter(accessTarget, accessTarget, storageAuthInfo(binding), writeSource), nil
}

func newStorageWriter(accessTarget string, metadataTarget string, authInfo *storagepb.AuthInfo, writeSource string) *storageWriter {
	target := storageGatewayTarget(accessTarget, metadataTarget)
	serviceOptions := gatewayauth.NewTRPCClientOptions(normalizeStorageTarget(target, "11003"), strings.TrimSpace(os.Getenv("MOOX_GATEWAY_TARGET_NODE")), gatewayauth.CredentialsFromEnv())
	return &storageWriter{
		access:      storagepb.NewPrimaryStoreClientProxy(serviceOptions...),
		metadata:    storagepb.NewMetadataClientProxy(serviceOptions...),
		authInfo:    authInfo,
		writeSource: strings.TrimSpace(writeSource),
	}
}

func storageGatewayTarget(accessTarget, metadataTarget string) string {
	if target := strings.TrimSpace(accessTarget); target != "" {
		return target
	}
	if target := strings.TrimSpace(metadataTarget); target != "" {
		return target
	}
	return "ip://127.0.0.1:11003"
}

func (w *storageWriter) UpsertFields(ctx context.Context, rows []*storagepb.RowFieldUpsert) error {
	return w.upsertFields(ctx, rows, "")
}

func (w *storageWriter) UpsertFieldsWithSource(ctx context.Context, rows []*storagepb.RowFieldUpsert, sourceEventID string) error {
	return w.upsertFields(ctx, rows, sourceEventID)
}

func (w *storageWriter) ReadFields(ctx context.Context, keys []*storagepb.RowKey, fieldIDs, attributeKeys []string) ([]*storagepb.RowFieldValues, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("read fields: keys are required")
	}
	var rsp *storagepb.PrimaryReadFieldsRsp
	err := retryStorageWithAttemptTimeout(ctx, func(attemptCtx context.Context) error {
		var callErr error
		rsp, callErr = w.access.ReadFields(attemptCtx, &storagepb.PrimaryReadFieldsReq{
			AuthInfo: w.authInfo, Keys: keys, FieldIds: fieldIDs, AttributeKeys: attributeKeys,
		})
		if callErr != nil {
			return fmt.Errorf("read fields: %w", callErr)
		}
		return ensureStorageOK("read fields", rsp.GetRetInfo())
	})
	if err != nil {
		return nil, err
	}
	return rsp.GetRows(), nil
}

func (w *storageWriter) WaitViewSyncPoint(ctx context.Context, req *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error) {
	if req == nil {
		return nil, fmt.Errorf("wait View sync point: request is required")
	}
	// Callers may not carry the Primary auth object (for example the local
	// resample backfill fence). Inject the writer's authenticated snapshot while
	// preserving an explicit auth value used by metadata preparation tests.
	copyReq := *req
	if copyReq.AuthInfo == nil {
		copyReq.AuthInfo = w.authInfo
	}
	return w.access.WaitViewSyncPoint(ctx, &copyReq)
}

// ReportDatasetPeriodCollected appends the completion marker after the
// executor's row write. It is intentionally exposed as a narrow optional
// interface so existing BatchStorage fakes do not need the control-plane RPC.
func (w *storageWriter) ReportDatasetPeriodCollected(ctx context.Context, spaceID string, payload *storageeventpb.DatasetPeriodCollected) error {
	if payload == nil {
		return fmt.Errorf("dataset period payload is nil")
	}
	if strings.TrimSpace(spaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	return retryStorageWithAttemptTimeout(ctx, func(attemptCtx context.Context) error {
		rsp, err := w.access.ReportDatasetPeriodCollected(attemptCtx, &storagepb.ReportDatasetPeriodCollectedReq{
			AuthInfo: w.authInfo,
			SpaceId:  spaceID,
			Marker: &storagepb.DatasetPeriodCollectedMarker{
				DatasetId: payload.GetDatasetId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
				Status: payload.GetStatus(), SubjectIds: payload.GetSubjectIds(), FailedSubjects: payload.GetFailedSubjects(), CollectedAt: payload.GetCollectedAt(),
			},
		})
		if err != nil {
			return fmt.Errorf("report dataset period collected: %w", err)
		}
		return ensureDatasetPeriodMarkerAccepted(rsp.GetRetInfo())
	})
}

func ensureDatasetPeriodMarkerAccepted(ret *storagepb.RetInfo) error {
	// A dataset/frequency/period marker is immutable and uses a stable event ID.
	// CONFLICT therefore means Storage already finalized that logical period.
	// Treat it as accepted so a restarted Collector does not retry one old row
	// forever while newer periods continue advancing.
	if ret != nil && ret.GetCode() == storagepb.ErrorCode_CONFLICT {
		return nil
	}
	return ensureStorageOK("report dataset period collected", ret)
}

func (w *storageWriter) AppendDatasetSyncPoint(ctx context.Context, spaceID, datasetID, requestID, source string) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(requestID) == "" {
		return fmt.Errorf("space_id, dataset_id and request_id are required")
	}
	if strings.TrimSpace(source) == "" {
		source = "catchup"
	}
	return retryStorageWithAttemptTimeout(ctx, func(attemptCtx context.Context) error {
		rsp, err := w.access.AppendDatasetSyncPoint(attemptCtx, &storagepb.AppendDatasetSyncPointReq{
			AuthInfo: w.authInfo,
			SpaceId:  spaceID,
			SyncPoint: &storagepb.DatasetSyncPointMarker{
				RequestId: requestID, DatasetId: datasetID, Source: source,
			},
		})
		if err != nil {
			return fmt.Errorf("append dataset sync point: %w", err)
		}
		return ensureStorageOK("append dataset sync point", rsp.GetRetInfo())
	})
}

func (w *storageWriter) upsertFields(ctx context.Context, rows []*storagepb.RowFieldUpsert, sourceEventID string) error {
	return retryStorageWithAttemptTimeout(ctx, func(attemptCtx context.Context) error {
		rsp, err := w.access.UpsertFields(attemptCtx, &storagepb.PrimaryUpsertFieldsReq{
			AuthInfo:      w.authInfo,
			Rows:          rows,
			SourceEventId: sourceEventID,
			WriteSource:   w.writeSource,
		})
		if err != nil {
			return fmt.Errorf("write time-series rows: %w", err)
		}
		return ensureStorageOK("write time-series rows", rsp.GetRetInfo())
	})
}

func (w *storageWriter) LatestTimeSeriesTime(ctx context.Context, selector *storagepb.TimeSeriesSelector) (time.Time, bool, error) {
	var rsp *storagepb.ReadTimeSeriesRowsRsp
	err := retryStorage(ctx, func() error {
		var callErr error
		rsp, callErr = w.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
			AuthInfo:  w.authInfo,
			SpaceId:   selector.GetSpaceId(),
			DatasetId: selector.GetDatasetId(),
			Selectors: []*storagepb.TimeSeriesSelector{selector},
			Order:     storagepb.SortOrder_SORT_ORDER_DESC,
			Page:      &storagepb.Page{Page: 1, Size: 1},
		})
		if callErr != nil {
			return fmt.Errorf("read latest time-series row: %w", callErr)
		}
		return ensureStorageOK("read latest time-series row", rsp.GetRetInfo())
	})
	if err != nil {
		return time.Time{}, false, err
	}
	if len(rsp.GetRows()) == 0 || rsp.GetRows()[0].GetKey() == nil {
		return time.Time{}, false, nil
	}
	dataTime := strings.TrimSpace(rsp.GetRows()[0].GetKey().GetDataTime())
	if dataTime == "" {
		return time.Time{}, false, fmt.Errorf("read latest time-series row: empty data_time")
	}
	parsed, err := time.Parse(time.RFC3339Nano, dataTime)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read latest time-series row: parse data_time %q: %w", dataTime, err)
	}
	return parsed.UTC(), true, nil
}

func (w *storageWriter) RegisterDataSubject(ctx context.Context, req *storagepb.RegisterDataSubjectReq) error {
	req.AuthInfo = w.authInfo
	return retryMetadataStorage(ctx, func() error {
		rsp, err := w.metadata.RegisterDataSubject(ctx, req)
		if err != nil {
			return fmt.Errorf("register data subject: %w", err)
		}
		return ensureStorageOK("register data subject", rsp.GetRetInfo())
	})
}

func (w *storageWriter) ListDatasetSubjects(
	ctx context.Context,
	spaceID string,
	datasetID string,
) ([]*storagepb.DatasetSubject, error) {
	var all []*storagepb.DatasetSubject
	for page := uint32(1); ; page++ {
		var rsp *storagepb.ListDatasetSubjectsRsp
		err := retryStorage(ctx, func() error {
			var callErr error
			rsp, callErr = w.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
				AuthInfo:  w.authInfo,
				SpaceId:   spaceID,
				DatasetId: datasetID,
				Page:      &storagepb.Page{Page: page, Size: datasetSubjectPageSize},
			})
			if callErr != nil {
				return fmt.Errorf("list dataset subjects: %w", callErr)
			}
			return ensureStorageOK("list dataset subjects", rsp.GetRetInfo())
		})
		if err != nil {
			return nil, err
		}
		all = append(all, rsp.GetDatasetSubjects()...)
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			return all, nil
		}
	}
}

func (w *storageWriter) BindDatasetSubject(ctx context.Context, item *storagepb.DatasetSubject) error {
	return retryStorage(ctx, func() error {
		rsp, err := w.metadata.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{
			AuthInfo:       w.authInfo,
			DatasetSubject: item,
		})
		if err != nil {
			return fmt.Errorf("bind dataset subject: %w", err)
		}
		return ensureStorageOK("bind dataset subject", rsp.GetRetInfo())
	})
}

type storageResponseError struct {
	code storagepb.ErrorCode
	msg  string
}

func (e *storageResponseError) Error() string {
	return e.msg
}

func ensureStorageOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return &storageResponseError{msg: fmt.Sprintf("%s: empty ret_info", action)}
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return &storageResponseError{
			code: ret.GetCode(),
			msg:  fmt.Sprintf("%s: %s", action, ret.GetMsg()),
		}
	}
	return nil
}

func stringField(name, value string) *storagepb.FieldValue {
	return &storagepb.FieldValue{
		FieldId: name,
		Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}},
	}
}

func intField(name string, value int64) *storagepb.FieldValue {
	return &storagepb.FieldValue{
		FieldId: name,
		Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}},
	}
}

func doubleField(name string, value float64) *storagepb.FieldValue {
	return &storagepb.FieldValue{
		FieldId: name,
		Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func storageAuthInfo(binding StorageBinding) *storagepb.AuthInfo {
	appKey := binding.AuthInfo.AppKey
	if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
		appKey = mooxsecurity.HMACSHA256Hex(secret, []byte(binding.AuthInfo.AppID))
	}
	return &storagepb.AuthInfo{
		AppId:     binding.AuthInfo.AppID,
		AppKey:    appKey,
		Operator:  binding.AuthInfo.Operator,
		RequestId: binding.AuthInfo.RequestID,
	}
}

func normalizeStorageTarget(raw string, defaultPort string) string {
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

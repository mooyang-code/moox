package binance

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/transport"
)

type storageWriter struct {
	access   storagepb.PrimaryStoreClientProxy
	metadata storagepb.MetadataClientProxy
	authInfo *storagepb.AuthInfo
}

func newStorageWriter(accessTarget string, metadataTarget string, authInfo *storagepb.AuthInfo) *storageWriter {
	return &storageWriter{
		access: storagepb.NewPrimaryStoreClientProxy(client.WithTarget(normalizeStorageTarget(accessTarget, "20102"))),
		metadata: storagepb.NewMetadataClientProxy(
			client.WithTarget(normalizeStorageTarget(metadataTarget, "20100")),
			client.WithTransport(transport.DefaultClientTransport),
		),
		authInfo: authInfo,
	}
}

func (w *storageWriter) MergeTimeSeriesRows(ctx context.Context, rows []*storagepb.TimeSeriesRow) error {
	rsp, err := w.access.MergeTimeSeriesRows(ctx, &storagepb.MergeTimeSeriesRowsReq{
		AuthInfo: w.authInfo,
		Rows:     rows,
	})
	if err != nil {
		return fmt.Errorf("write time-series rows: %w", err)
	}
	return ensureStorageOK("write time-series rows", rsp.GetRetInfo())
}

func (w *storageWriter) LatestTimeSeriesTime(ctx context.Context, key *storagepb.TimeSeriesKey) (time.Time, bool, error) {
	rsp, err := w.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: w.authInfo,
		Keys:     []*storagepb.TimeSeriesKey{key},
		Order:    storagepb.SortOrder_SORT_ORDER_DESC,
		Page:     &storagepb.Page{Page: 1, Size: 1},
	})
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read latest time-series row: %w", err)
	}
	if err := ensureStorageOK("read latest time-series row", rsp.GetRetInfo()); err != nil {
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

func (w *storageWriter) MergeRecordRows(ctx context.Context, rows []*storagepb.RecordRow) error {
	rsp, err := w.access.MergeRecordRows(ctx, &storagepb.MergeRecordRowsReq{
		AuthInfo: w.authInfo,
		Rows:     rows,
	})
	if err != nil {
		return fmt.Errorf("write record rows: %w", err)
	}
	return ensureStorageOK("write record rows", rsp.GetRetInfo())
}

func (w *storageWriter) RegisterDataSubject(ctx context.Context, req *storagepb.RegisterDataSubjectReq) error {
	req.AuthInfo = w.authInfo
	rsp, err := w.metadata.RegisterDataSubject(ctx, req)
	if err != nil {
		return fmt.Errorf("register data subject: %w", err)
	}
	return ensureStorageOK("register data subject", rsp.GetRetInfo())
}

func ensureStorageOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}

func stringField(name, value string) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{
		ColumnName: name,
		ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}},
	}
}

func intField(name string, value int64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{
		ColumnName: name,
		ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value:      &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}},
	}
}

func doubleField(name string, value float64) *storagepb.ColumnValue {
	return &storagepb.ColumnValue{
		ColumnName: name,
		ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func storageAuthInfo(binding StorageBinding) *storagepb.AuthInfo {
	return &storagepb.AuthInfo{
		AppId:     binding.AuthInfo.AppID,
		AppKey:    binding.AuthInfo.AppKey,
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

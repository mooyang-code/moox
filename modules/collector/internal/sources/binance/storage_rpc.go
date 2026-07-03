package binance

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

type storageWriter struct {
	access   storagepb.AccessClientProxy
	metadata storagepb.MetadataClientProxy
	authInfo *storagepb.AuthInfo
}

func newStorageWriter(accessTarget string, metadataTarget string, authInfo *storagepb.AuthInfo) *storageWriter {
	return &storageWriter{
		access:   storagepb.NewAccessClientProxy(client.WithTarget(normalizeStorageTarget(accessTarget, "20102"))),
		metadata: storagepb.NewMetadataClientProxy(client.WithTarget(normalizeStorageTarget(metadataTarget, "20100"))),
		authInfo: authInfo,
	}
}

func (w *storageWriter) WriteTimeSeriesRows(ctx context.Context, rows []*storagepb.TimeSeriesRow) error {
	rsp, err := w.access.WriteTimeSeriesRows(ctx, &storagepb.WriteTimeSeriesRowsReq{
		AuthInfo: w.authInfo,
		Rows:     rows,
	})
	if err != nil {
		return fmt.Errorf("write time-series rows: %w", err)
	}
	return ensureStorageOK("write time-series rows", rsp.GetRetInfo())
}

func (w *storageWriter) WriteRecordRows(ctx context.Context, rows []*storagepb.RecordRow) error {
	rsp, err := w.access.WriteRecordRows(ctx, &storagepb.WriteRecordRowsReq{
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

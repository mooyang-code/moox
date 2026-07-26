package binance

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

const datasetSubjectPageSize = 200

type storageWriter struct {
	access   storagepb.PrimaryStoreClientProxy
	metadata storagepb.MetadataClientProxy
	authInfo *storagepb.AuthInfo
}

func newStorageWriter(accessTarget string, metadataTarget string, authInfo *storagepb.AuthInfo) *storageWriter {
	target := storageGatewayTarget(accessTarget, metadataTarget)
	serviceOptions := gatewayauth.NewTRPCClientOptions(normalizeStorageTarget(target, "11003"), strings.TrimSpace(os.Getenv("MOOX_GATEWAY_TARGET_NODE")), gatewayauth.CredentialsFromEnv())
	return &storageWriter{
		access:   storagepb.NewPrimaryStoreClientProxy(serviceOptions...),
		metadata: storagepb.NewMetadataClientProxy(serviceOptions...),
		authInfo: authInfo,
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
	rsp, err := w.access.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{
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

func (w *storageWriter) RegisterDataSubject(ctx context.Context, req *storagepb.RegisterDataSubjectReq) error {
	req.AuthInfo = w.authInfo
	rsp, err := w.metadata.RegisterDataSubject(ctx, req)
	if err != nil {
		return fmt.Errorf("register data subject: %w", err)
	}
	return ensureStorageOK("register data subject", rsp.GetRetInfo())
}

func (w *storageWriter) ListDatasetSubjects(
	ctx context.Context,
	spaceID string,
	datasetID string,
) ([]*storagepb.DatasetSubject, error) {
	var all []*storagepb.DatasetSubject
	for page := uint32(1); ; page++ {
		rsp, err := w.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			AuthInfo:  w.authInfo,
			SpaceId:   spaceID,
			DatasetId: datasetID,
			Page:      &storagepb.Page{Page: page, Size: datasetSubjectPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list dataset subjects: %w", err)
		}
		if err := ensureStorageOK("list dataset subjects", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		all = append(all, rsp.GetDatasetSubjects()...)
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			return all, nil
		}
	}
}

func (w *storageWriter) BindDatasetSubject(ctx context.Context, item *storagepb.DatasetSubject) error {
	rsp, err := w.metadata.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{
		AuthInfo:       w.authInfo,
		DatasetSubject: item,
	})
	if err != nil {
		return fmt.Errorf("bind dataset subject: %w", err)
	}
	return ensureStorageOK("bind dataset subject", rsp.GetRetInfo())
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

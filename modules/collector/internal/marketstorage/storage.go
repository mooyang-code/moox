package marketstorage

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
	"google.golang.org/protobuf/proto"
)

const datasetSubjectPageSize = 1000

type BatchStorage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
	LatestTimeSeriesTime(context.Context, *storagepb.TimeSeriesSelector) (time.Time, bool, error)
	RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
	ListSubjectSymbols(context.Context, string, string) ([]*storagepb.SubjectSymbol, error)
	StageDatasetSubjectSet(context.Context, string, string, []*storagepb.DatasetSubject) error
	ActivateDatasetSubjectSet(context.Context, string, string) error
}

// ResampleStorage is the narrow exact-read/write surface used by the local
// derived-kline worker. It lives beside the common market writer so resampling
// does not need to import a particular exchange package.
type ResampleStorage interface {
	ReadFields(context.Context, []*storagepb.RowKey, []string, []string) ([]*storagepb.RowFieldValues, error)
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
}

type ResampleViewSyncWaiter interface {
	WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error)
}

type ResampleMetadataClient struct {
	Client storagepb.MetadataClientProxy
	Auth   *storagepb.AuthInfo
}

type storageWriter struct {
	access      storagepb.PrimaryStoreClientProxy
	metadata    storagepb.MetadataClientProxy
	authInfo    *storagepb.AuthInfo
	writeSource string
}

func NewBatchStorageWithWriteSource(accessTarget, instType, writeSource string) (BatchStorage, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	target := normalizeStorageTarget(accessTarget, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, collectorStorageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	return &storageWriter{
		access: storagepb.NewPrimaryStoreClientProxy(options...), metadata: storagepb.NewMetadataClientProxy(options...),
		authInfo: storageAuthInfo(binding), writeSource: strings.TrimSpace(writeSource),
	}, nil
}

func NewResampleMetadataClient(accessTarget, instType string) (*ResampleMetadataClient, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	target := normalizeStorageTarget(accessTarget, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, collectorStorageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	return &ResampleMetadataClient{Client: storagepb.NewMetadataClientProxy(options...), Auth: storageAuthInfo(binding)}, nil
}

func NewResampleStorage(accessTarget, instType, writeSource string) (ResampleStorage, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	target := normalizeStorageTarget(accessTarget, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, collectorStorageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	return &storageWriter{
		access: storagepb.NewPrimaryStoreClientProxy(options...), metadata: storagepb.NewMetadataClientProxy(options...),
		authInfo: storageAuthInfo(binding), writeSource: strings.TrimSpace(writeSource),
	}, nil
}

func collectorStorageGatewayNodeID() string {
	if nodeID := strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID")); nodeID != "" {
		return nodeID
	}
	return strings.TrimSpace(os.Getenv("MOOX_GATEWAY_TARGET_NODE"))
}

func (w *storageWriter) UpsertFields(ctx context.Context, rows []*storagepb.RowFieldUpsert) error {
	return w.UpsertFieldsWithSource(ctx, rows, "")
}

func (w *storageWriter) UpsertFieldsWithSource(ctx context.Context, rows []*storagepb.RowFieldUpsert, sourceEventID string) error {
	return retryStorage(ctx, func() error {
		response, err := w.access.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{AuthInfo: w.authInfo, Rows: rows, SourceEventId: sourceEventID, WriteSource: w.writeSource})
		if err != nil {
			return fmt.Errorf("write time-series rows: %w", err)
		}
		return ensureStorageOK("write time-series rows", response.GetRetInfo())
	})
}

func (w *storageWriter) LatestTimeSeriesTime(ctx context.Context, selector *storagepb.TimeSeriesSelector) (time.Time, bool, error) {
	var response *storagepb.ReadTimeSeriesRowsRsp
	err := retryStorage(ctx, func() error {
		var err error
		response, err = w.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{AuthInfo: w.authInfo, SpaceId: selector.GetSpaceId(), DatasetId: selector.GetDatasetId(), Selectors: []*storagepb.TimeSeriesSelector{selector}, Order: storagepb.SortOrder_SORT_ORDER_DESC, Page: &storagepb.Page{Page: 1, Size: 1}})
		if err != nil {
			return fmt.Errorf("read latest time-series row: %w", err)
		}
		return ensureStorageOK("read latest time-series row", response.GetRetInfo())
	})
	if err != nil {
		return time.Time{}, false, err
	}
	if len(response.GetRows()) == 0 || response.GetRows()[0].GetKey() == nil {
		return time.Time{}, false, nil
	}
	value := strings.TrimSpace(response.GetRows()[0].GetKey().GetDataTime())
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read latest time-series row: parse data_time %q: %w", value, err)
	}
	return parsed.UTC(), true, nil
}

// ReadTimeSeriesRows is the bounded range-read surface used by the gap audit.
// Keeping the generated response here preserves page metadata so callers can
// inspect an internal hole without introducing a market-specific Storage type.
func (w *storageWriter) ReadTimeSeriesRows(ctx context.Context, req *storagepb.ReadTimeSeriesRowsReq) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	if req == nil {
		return nil, fmt.Errorf("read time-series rows: request is required")
	}
	copyReq := proto.Clone(req).(*storagepb.ReadTimeSeriesRowsReq)
	copyReq.AuthInfo = w.authInfo
	var response *storagepb.ReadTimeSeriesRowsRsp
	err := retryStorage(ctx, func() error {
		var err error
		response, err = w.access.ReadTimeSeriesRows(ctx, copyReq)
		if err != nil {
			return fmt.Errorf("read time-series rows: %w", err)
		}
		return ensureStorageOK("read time-series rows", response.GetRetInfo())
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (w *storageWriter) ReadFields(ctx context.Context, keys []*storagepb.RowKey, fieldIDs, attributeKeys []string) ([]*storagepb.RowFieldValues, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("read fields: keys are required")
	}
	fieldIDs = expandResampleFieldIDs(keys, fieldIDs)
	var response *storagepb.PrimaryReadFieldsRsp
	err := retryStorage(ctx, func() error {
		var err error
		response, err = w.access.ReadFields(ctx, &storagepb.PrimaryReadFieldsReq{AuthInfo: w.authInfo, Keys: keys, FieldIds: fieldIDs, AttributeKeys: attributeKeys})
		if err != nil {
			return fmt.Errorf("read fields: %w", err)
		}
		return ensureStorageOK("read fields", response.GetRetInfo())
	})
	if err != nil {
		return nil, err
	}
	return response.GetRows(), nil
}

// ListInstrumentNames resolves the canonical Subject names needed when a
// market-data row is projected into a user-facing K-line View. It uses the
// exact subject-id filter so a Timer invocation does not scan the catalogue.
func (w *storageWriter) ListInstrumentNames(ctx context.Context, spaceID string, subjectIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(subjectIDs))
	unique := make([]string, 0, len(subjectIDs))
	seen := make(map[string]struct{}, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		subjectID = strings.TrimSpace(subjectID)
		if subjectID == "" {
			continue
		}
		if _, exists := seen[subjectID]; exists {
			continue
		}
		seen[subjectID] = struct{}{}
		unique = append(unique, subjectID)
	}
	for start := 0; start < len(unique); start += datasetSubjectPageSize {
		end := start + datasetSubjectPageSize
		if end > len(unique) {
			end = len(unique)
		}
		var response *storagepb.ListSubjectsRsp
		err := retryStorage(ctx, func() error {
			var err error
			response, err = w.metadata.ListSubjects(ctx, &storagepb.ListSubjectsReq{AuthInfo: w.authInfo, SpaceId: strings.TrimSpace(spaceID), SubjectIds: unique[start:end], Page: &storagepb.Page{Page: 1, Size: uint32(end - start)}})
			if err != nil {
				return fmt.Errorf("list instrument names: %w", err)
			}
			return ensureStorageOK("list instrument names", response.GetRetInfo())
		})
		if err != nil {
			return nil, err
		}
		for _, subject := range response.GetSubjects() {
			if subject == nil || strings.TrimSpace(subject.GetSubjectId()) == "" {
				continue
			}
			result[subject.GetSubjectId()] = strings.TrimSpace(subject.GetName())
		}
	}
	return result, nil
}

func expandResampleFieldIDs(keys []*storagepb.RowKey, fieldIDs []string) []string {
	if len(fieldIDs) == 0 || len(keys) == 0 || keys[0] == nil {
		return fieldIDs
	}
	datasetID := strings.TrimSpace(keys[0].GetDatasetId())
	if datasetID == "" {
		return fieldIDs
	}
	qualified := make([]string, 0, len(fieldIDs)*2)
	seen := make(map[string]struct{}, len(fieldIDs)*2)
	for _, fieldID := range fieldIDs {
		fieldID = strings.TrimSpace(fieldID)
		candidates := []string{fieldID}
		if fieldID != "" && !strings.Contains(fieldID, ".") {
			candidates = append(candidates, datasetID+"."+fieldID)
		}
		for _, candidate := range candidates {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			qualified = append(qualified, candidate)
		}
	}
	return qualified
}

func (w *storageWriter) WaitViewSyncPoint(ctx context.Context, req *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error) {
	if req == nil {
		return nil, fmt.Errorf("wait View sync point: request is required")
	}
	copyReq := proto.Clone(req).(*storagepb.WaitViewSyncPointReq)
	if copyReq.AuthInfo == nil {
		copyReq.AuthInfo = w.authInfo
	}
	return w.access.WaitViewSyncPoint(ctx, copyReq)
}

func (w *storageWriter) RegisterDataSubject(ctx context.Context, req *storagepb.RegisterDataSubjectReq) error {
	if req == nil {
		return fmt.Errorf("register data subject: request is required")
	}
	copyReq := proto.Clone(req).(*storagepb.RegisterDataSubjectReq)
	copyReq.AuthInfo = w.authInfo
	return retryStorage(ctx, func() error {
		response, err := w.metadata.RegisterDataSubject(ctx, copyReq)
		if err != nil {
			return fmt.Errorf("register data subject: %w", err)
		}
		return ensureStorageOK("register data subject", response.GetRetInfo())
	})
}

func (w *storageWriter) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]*storagepb.DatasetSubject, error) {
	var all []*storagepb.DatasetSubject
	for page := uint32(1); ; page++ {
		var response *storagepb.ListDatasetSubjectsRsp
		err := retryStorage(ctx, func() error {
			var err error
			response, err = w.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{AuthInfo: w.authInfo, SpaceId: spaceID, DatasetId: datasetID, Page: &storagepb.Page{Page: page, Size: datasetSubjectPageSize}})
			if err != nil {
				return fmt.Errorf("list dataset subjects: %w", err)
			}
			return ensureStorageOK("list dataset subjects", response.GetRetInfo())
		})
		if err != nil {
			return nil, err
		}
		all = append(all, response.GetDatasetSubjects()...)
		if response.GetPageResult() == nil || !response.GetPageResult().GetHasMore() {
			return all, nil
		}
	}
}

func (w *storageWriter) ListSubjectSymbols(ctx context.Context, spaceID, dataSourceID string) ([]*storagepb.SubjectSymbol, error) {
	var all []*storagepb.SubjectSymbol
	for page := uint32(1); ; page++ {
		var response *storagepb.ListSubjectSymbolsRsp
		err := retryStorage(ctx, func() error {
			var err error
			response, err = w.metadata.ListSubjectSymbols(ctx, &storagepb.ListSubjectSymbolsReq{
				AuthInfo: w.authInfo, SpaceId: spaceID, DataSourceId: dataSourceID,
				Page: &storagepb.Page{Page: page, Size: datasetSubjectPageSize},
			})
			if err != nil {
				return fmt.Errorf("list subject symbols: %w", err)
			}
			return ensureStorageOK("list subject symbols", response.GetRetInfo())
		})
		if err != nil {
			return nil, err
		}
		all = append(all, response.GetSubjectSymbols()...)
		if response.GetPageResult() == nil || !response.GetPageResult().GetHasMore() {
			return all, nil
		}
	}
}

func (w *storageWriter) StageDatasetSubjectSet(ctx context.Context, spaceID, setID string, bindings []*storagepb.DatasetSubject) error {
	return retryStorage(ctx, func() error {
		response, err := w.metadata.StageDatasetSubjectSet(ctx, &storagepb.StageDatasetSubjectSetReq{AuthInfo: w.authInfo, SpaceId: spaceID, SetId: setID, DatasetSubjects: bindings})
		if err != nil {
			return fmt.Errorf("stage dataset subject set: %w", err)
		}
		return ensureStorageOK("stage dataset subject set", response.GetRetInfo())
	})
}

func (w *storageWriter) ActivateDatasetSubjectSet(ctx context.Context, spaceID, setID string) error {
	_, err := w.ActivateDatasetSubjectSetWithCount(ctx, spaceID, setID)
	return err
}

// ActivateDatasetSubjectSetWithCount exposes the Storage response count to the
// common InstrumentPipeline. A zero count means a sharded snapshot is still
// waiting for another shard; it is not an activation failure.
func (w *storageWriter) ActivateDatasetSubjectSetWithCount(ctx context.Context, spaceID, setID string) (int, error) {
	var count uint32
	err := retryStorage(ctx, func() error {
		response, err := w.metadata.ActivateDatasetSubjectSet(ctx, &storagepb.ActivateDatasetSubjectSetReq{AuthInfo: w.authInfo, SpaceId: spaceID, SetId: setID})
		if err != nil {
			return fmt.Errorf("activate dataset subject set: %w", err)
		}
		if err := ensureStorageOK("activate dataset subject set", response.GetRetInfo()); err != nil {
			return err
		}
		count = response.GetCount()
		return nil
	})
	return int(count), err
}

type storageResponseError struct{ message string }

func (e *storageResponseError) Error() string { return e.message }

func ensureStorageOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return &storageResponseError{message: fmt.Sprintf("%s: empty ret_info", action)}
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return &storageResponseError{message: fmt.Sprintf("%s: %s", action, ret.GetMsg())}
	}
	return nil
}

func storageAuthInfo(binding StorageBinding) *storagepb.AuthInfo {
	appKey := binding.AuthInfo.AppKey
	if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
		appKey = mooxsecurity.HMACSHA256Hex(secret, []byte(binding.AuthInfo.AppID))
	}
	return &storagepb.AuthInfo{AppId: binding.AuthInfo.AppID, AppKey: appKey, Operator: binding.AuthInfo.Operator, RequestId: binding.AuthInfo.RequestID}
}

func normalizeStorageTarget(raw, defaultPort string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:" + defaultPort
	}
	if strings.HasPrefix(raw, "ip://") || strings.Contains(raw, "://") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Host != "" {
		return "ip://" + parsed.Host
	}
	if strings.Contains(raw, ":") {
		return "ip://" + raw
	}
	return raw
}

package marketstorage

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

const datasetSubjectPageSize = 1000

type BatchStorage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
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
	Client  storagepb.MetadataClientProxy
	Primary storagepb.PrimaryStoreClientProxy
	Auth    *storagepb.AuthInfo
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
	return &ResampleMetadataClient{Client: storagepb.NewMetadataClientProxy(options...), Primary: storagepb.NewPrimaryStoreClientProxy(options...), Auth: storageAuthInfo(binding)}, nil
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

// StorageGatewayNodeID returns the gateway node used by collector control-plane
// calls. Subject synchronization is a separate binary, but it must use the
// same routing identity as the regular collector.
func StorageGatewayNodeID() string { return collectorStorageGatewayNodeID() }

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

// ReportCollectorPeriodCompleted appends the terminal collector-period marker
// that Storage View uses to publish ViewDataReady.
func (w *storageWriter) ReportCollectorPeriodCompleted(ctx context.Context, spaceID string, payload *storageeventpb.CollectorPeriodCompleted) error {
	if w == nil || w.access == nil {
		return fmt.Errorf("report collector period completed: storage client is required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" || payload == nil || strings.TrimSpace(payload.GetDatasetId()) == "" || strings.TrimSpace(payload.GetFrequency()) == "" || payload.GetPeriodTime() <= 0 {
		return fmt.Errorf("report collector period completed: space_id, dataset, frequency, period_time and payload are required")
	}
	positions := make([]*storagepb.CommittedPosition, 0, len(payload.GetCommittedPositions()))
	for _, position := range payload.GetCommittedPositions() {
		if position == nil {
			continue
		}
		positions = append(positions, &storagepb.CommittedPosition{NodeId: position.GetNodeId(), StoreId: position.GetStoreId(), Sequence: position.GetSequence()})
	}
	marker := &storagepb.CollectorPeriodCompletedMarker{
		DatasetId: payload.GetDatasetId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
		Status: payload.GetStatus(), BatchId: payload.GetBatchId(), ConfigSnapshotId: payload.GetConfigSnapshotId(),
		ExpectedScopeRef: payload.GetExpectedScopeRef(), UniverseSubjectIds: append([]string(nil), payload.GetUniverseSubjectIds()...),
		FailedSubjects: append([]string(nil), payload.GetFailedSubjects()...), CommittedPositions: positions,
		CollectedAt: payload.GetCollectedAt(),
	}
	return retryStorage(ctx, func() error {
		response, err := w.access.ReportCollectorPeriodCompleted(ctx, &storagepb.ReportCollectorPeriodCompletedReq{AuthInfo: w.authInfo, SpaceId: spaceID, Marker: marker})
		if err != nil {
			return fmt.Errorf("report collector period completed: %w", err)
		}
		return ensureStorageOK("report collector period completed", response.GetRetInfo())
	})
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

// StorageAuthInfo returns the metadata caller identity for a configured market
// binding. The subject synchronizer uses the spot binding as its control-plane
// identity for both crypto and stock metadata calls.
func ResolveStorageAuthInfo(instType string) (*storagepb.AuthInfo, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	return storageAuthInfo(binding), nil
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

// NormalizeStorageTarget normalizes a gateway target for standalone collector
// binaries that share the collector's Storage client configuration.
func NormalizeStorageTarget(raw, defaultPort string) string {
	return normalizeStorageTarget(raw, defaultPort)
}

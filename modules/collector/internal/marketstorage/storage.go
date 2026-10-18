package marketstorage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

const datasetSubjectPageSize = 1000

var ErrDatasetPeriodWriteUnconfirmed = errors.New("dataset period write was not confirmed")

type BatchStorage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
	CommitTimeSeriesBatch(context.Context, *storagepb.DatasetPeriodExpectation, []*storagepb.TimeSeriesBatchRow, string) error
}

type periodStorageAccess interface {
	CommitTimeSeriesBatch(context.Context, *storagepb.PrimaryCommitTimeSeriesBatchReq, ...client.Option) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error)
}

// ResampleStorage is the narrow exact-read/write surface used by the local
// derived-kline worker. It lives beside the common market writer so resampling
// does not need to import a particular exchange package.
type ResampleStorage interface {
	ReadRows(context.Context, []*storagepb.RowKey, []string, []string) ([]*storagepb.RowFieldValues, error)
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
}

type ResampleViewSyncWaiter interface {
	WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error)
}

type ResampleMetadataClient struct {
	Client  storageio.Metadata
	Primary storageio.Primary
	Auth    *storagepb.AuthInfo
}

type storageWriter struct {
	access      storageio.Primary
	period      periodStorageAccess
	metadata    storageio.Metadata
	authInfo    *storagepb.AuthInfo
	writeSource string
}

func newGatewayWriter(gateway gatewayclient.Invoker, instType, writeSource string) (*storageWriter, error) {
	if gateway == nil {
		return nil, fmt.Errorf("collector gateway client is required")
	}
	auth, err := ResolveStorageAuthInfo(instType)
	if err != nil {
		return nil, err
	}
	adapter := storageio.NewGatewayClient(gateway)
	return &storageWriter{access: adapter, period: adapter, metadata: adapter, authInfo: auth, writeSource: strings.TrimSpace(writeSource)}, nil
}

func NewGatewayBatchStorage(gateway gatewayclient.Invoker, instType, writeSource string) (BatchStorage, error) {
	return newGatewayWriter(gateway, instType, writeSource)
}

func NewGatewayResampleStorage(gateway gatewayclient.Invoker, instType, writeSource string) (ResampleStorage, error) {
	return newGatewayWriter(gateway, instType, writeSource)
}

func NewGatewayResampleMetadataClient(gateway gatewayclient.Invoker, instType string) (*ResampleMetadataClient, error) {
	writer, err := newGatewayWriter(gateway, instType, "")
	if err != nil {
		return nil, err
	}
	return &ResampleMetadataClient{Client: writer.metadata, Primary: writer.access, Auth: writer.authInfo}, nil
}

func (w *storageWriter) runRPC(ctx context.Context, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

func (w *storageWriter) UpsertFields(ctx context.Context, rows []*storagepb.RowFieldUpsert) error {
	return w.UpsertFieldsWithSource(ctx, rows, "")
}

func (w *storageWriter) CommitTimeSeriesBatch(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation, items []*storagepb.TimeSeriesBatchRow, sourceEventID string) error {
	if expectation == nil || len(items) == 0 {
		return fmt.Errorf("commit time-series batch: expectation and items are required")
	}
	return w.runRPC(ctx, func() error {
		response, err := w.period.CommitTimeSeriesBatch(ctx, &storagepb.PrimaryCommitTimeSeriesBatchReq{AuthInfo: w.authInfo, Expectation: expectation, Items: items, SourceEventId: sourceEventID, WriteSource: w.writeSource})
		if err != nil {
			return fmt.Errorf("commit time-series batch: %w", err)
		}
		if response == nil {
			return fmt.Errorf("commit time-series batch: empty response")
		}
		if err := ensureStorageOK("commit time-series batch", response.GetRetInfo()); err != nil {
			return err
		}
		if err := validateAcceptedSeriesIndexes(items, response.GetAcceptedSeriesIndexes()); err != nil {
			return fmt.Errorf("%w: %v", ErrDatasetPeriodWriteUnconfirmed, err)
		}
		return nil
	})
}

func (w *storageWriter) UpsertFieldsWithSource(ctx context.Context, rows []*storagepb.RowFieldUpsert, sourceEventID string) error {
	return w.runRPC(ctx, func() error {
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
	return w.runRPC(ctx, func() error {
		response, err := w.access.ReportCollectorPeriodCompleted(ctx, &storagepb.ReportCollectorPeriodCompletedReq{AuthInfo: w.authInfo, SpaceId: spaceID, Marker: marker})
		if err != nil {
			return fmt.Errorf("report collector period completed: %w", err)
		}
		return ensureStorageOK("report collector period completed", response.GetRetInfo())
	})
}

func (w *storageWriter) ReadRows(ctx context.Context, keys []*storagepb.RowKey, fieldIDs, attributeKeys []string) ([]*storagepb.RowFieldValues, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("read fields: keys are required")
	}
	fieldIDs = expandResampleFieldIDs(keys, fieldIDs)
	var response *storagepb.PrimaryReadRowsRsp
	err := w.runRPC(ctx, func() error {
		var err error
		response, err = w.access.ReadRows(ctx, &storagepb.PrimaryReadRowsReq{AuthInfo: w.authInfo, Keys: keys, ColumnNames: fieldIDs, AttributeKeys: attributeKeys})
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
		err := w.runRPC(ctx, func() error {
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

func validateAcceptedSeriesIndexes(items []*storagepb.TimeSeriesBatchRow, accepted []uint32) error {
	expected := make(map[uint32]struct{}, len(items))
	for _, item := range items {
		if item == nil {
			return fmt.Errorf("request contains a nil row")
		}
		expected[item.GetSeriesIndex()] = struct{}{}
	}
	seen := make(map[uint32]struct{}, len(accepted))
	for _, index := range accepted {
		if _, duplicate := seen[index]; duplicate {
			return fmt.Errorf("Storage returned duplicate accepted series index %d", index)
		}
		seen[index] = struct{}{}
		if _, exists := expected[index]; !exists {
			return fmt.Errorf("Storage accepted unexpected series index %d", index)
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("Storage accepted %d unique series indexes, want %d", len(seen), len(expected))
	}
	return nil
}

// StorageAuthInfo returns the metadata caller identity for a configured market
// binding. The subject synchronizer uses the spot binding as its control-plane
// identity for both crypto and stock metadata calls.
func ResolveStorageAuthInfo(instType string) (*storagepb.AuthInfo, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	return storageAuthInfo(binding)
}

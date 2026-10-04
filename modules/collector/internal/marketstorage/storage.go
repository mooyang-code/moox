package marketstorage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

const datasetSubjectPageSize = 1000

var ErrDatasetPeriodConflict = errors.New("dataset period expectation conflict")
var ErrDatasetPeriodNotFound = errors.New("dataset period not found")
var ErrDatasetPeriodWriteUnconfirmed = errors.New("dataset period write was not confirmed")

type BatchStorage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
	EnsureDatasetPeriod(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
	GetDatasetPeriodStatus(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
	CommitTimeSeriesBatch(context.Context, *storagepb.DatasetPeriodExpectation, []*storagepb.TimeSeriesBatchRow, string) error
	RecordDatasetPeriodFailures(context.Context, *storagepb.DatasetPeriodExpectation, []uint32) ([]*storagepb.DatasetPeriodFailureResult, error)
}

type periodStorageAccess interface {
	EnsureDatasetPeriod(context.Context, *storagepb.PrimaryEnsureDatasetPeriodReq, ...client.Option) (*storagepb.PrimaryEnsureDatasetPeriodRsp, error)
	GetDatasetPeriodStatus(context.Context, *storagepb.PrimaryGetDatasetPeriodStatusReq, ...client.Option) (*storagepb.PrimaryGetDatasetPeriodStatusRsp, error)
	CommitTimeSeriesBatch(context.Context, *storagepb.PrimaryCommitTimeSeriesBatchReq, ...client.Option) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error)
	RecordDatasetPeriodFailures(context.Context, *storagepb.PrimaryRecordDatasetPeriodFailuresReq, ...client.Option) (*storagepb.PrimaryRecordDatasetPeriodFailuresRsp, error)
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
	period      periodStorageAccess
	metadata    storagepb.MetadataClientProxy
	authInfo    *storagepb.AuthInfo
	writeSource string
}

func NewBatchStorageWithWriteSource(accessTarget, instType, writeSource string) (BatchStorage, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	auth, err := storageAuthInfo(binding)
	if err != nil {
		return nil, err
	}
	target := normalizeStorageTarget(accessTarget, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, collectorStorageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	primary := storagepb.NewPrimaryStoreClientProxy(options...)
	return &storageWriter{
		access: primary, period: primary, metadata: storagepb.NewMetadataClientProxy(options...),
		authInfo: auth, writeSource: strings.TrimSpace(writeSource),
	}, nil
}

func NewResampleMetadataClient(accessTarget, instType string) (*ResampleMetadataClient, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	auth, err := storageAuthInfo(binding)
	if err != nil {
		return nil, err
	}
	target := normalizeStorageTarget(accessTarget, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, collectorStorageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	return &ResampleMetadataClient{Client: storagepb.NewMetadataClientProxy(options...), Primary: storagepb.NewPrimaryStoreClientProxy(options...), Auth: auth}, nil
}

func NewResampleStorage(accessTarget, instType, writeSource string) (ResampleStorage, error) {
	binding, err := ResolveStorageBinding(instType)
	if err != nil {
		return nil, err
	}
	auth, err := storageAuthInfo(binding)
	if err != nil {
		return nil, err
	}
	target := normalizeStorageTarget(accessTarget, "11003")
	options := gatewayauth.NewTRPCClientOptions(target, collectorStorageGatewayNodeID(), gatewayauth.CredentialsFromEnv())
	primary := storagepb.NewPrimaryStoreClientProxy(options...)
	return &storageWriter{
		access: primary, period: primary, metadata: storagepb.NewMetadataClientProxy(options...),
		authInfo: auth, writeSource: strings.TrimSpace(writeSource),
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

func (w *storageWriter) EnsureDatasetPeriod(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	if w == nil || w.period == nil {
		return domain.PeriodStorageState{}, fmt.Errorf("ensure dataset period: Storage client is required")
	}
	if err := validateDatasetPeriodExpectation(expectation, true); err != nil {
		return domain.PeriodStorageState{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return domain.PeriodStorageState{}, err
		}
		response, err := w.period.EnsureDatasetPeriod(ctx, &storagepb.PrimaryEnsureDatasetPeriodReq{AuthInfo: w.authInfo, Expectation: expectation})
		var unknownOutcomeErr error
		if err != nil {
			unknownOutcomeErr = fmt.Errorf("ensure dataset period: %w", err)
		} else {
			if response == nil {
				return domain.PeriodStorageState{}, fmt.Errorf("ensure dataset period: empty response")
			}
			ret := response.GetRetInfo()
			if ret != nil && ret.GetCode() == storagepb.ErrorCode_CONFLICT {
				return domain.PeriodStorageState{}, fmt.Errorf("%w: %s", ErrDatasetPeriodConflict, ret.GetMsg())
			}
			if ret != nil && ret.GetCode() == storagepb.ErrorCode_INNER_ERR {
				unknownOutcomeErr = ensureStorageOK("ensure dataset period", ret)
			} else {
				if err := ensureStorageOK("ensure dataset period", ret); err != nil {
					return domain.PeriodStorageState{}, err
				}
				state, err := periodStorageStateFromEnsure(expectation, response)
				if err != nil {
					return domain.PeriodStorageState{}, err
				}
				return state, nil
			}
		}
		state, statusErr := w.GetDatasetPeriodStatus(ctx, expectation)
		if statusErr == nil {
			return state, nil
		}
		lastErr = errors.Join(unknownOutcomeErr, fmt.Errorf("read status after Ensure unknown outcome: %w", statusErr))
		if err := ctx.Err(); err != nil {
			return domain.PeriodStorageState{}, err
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return domain.PeriodStorageState{}, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			}
		}
	}
	return domain.PeriodStorageState{}, lastErr
}

func (w *storageWriter) GetDatasetPeriodStatus(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	if w == nil || w.period == nil {
		return domain.PeriodStorageState{}, fmt.Errorf("get dataset period status: Storage client is required")
	}
	if err := validateDatasetPeriodExpectation(expectation, false); err != nil {
		return domain.PeriodStorageState{}, err
	}
	query := &storagepb.DatasetPeriodExpectation{
		SpaceId: expectation.GetSpaceId(), DatasetId: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(),
		PeriodTime: expectation.GetPeriodTime(), SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(),
	}
	response, err := w.period.GetDatasetPeriodStatus(ctx, &storagepb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: w.authInfo, Expectation: query})
	if err != nil {
		return domain.PeriodStorageState{}, fmt.Errorf("get dataset period status: %w", err)
	}
	if response == nil {
		return domain.PeriodStorageState{}, fmt.Errorf("get dataset period status: empty response")
	}
	if ret := response.GetRetInfo(); ret != nil && ret.GetCode() == storagepb.ErrorCode_NOT_FOUND {
		return domain.PeriodStorageState{}, fmt.Errorf("%w: %s", ErrDatasetPeriodNotFound, ret.GetMsg())
	}
	if err := ensureStorageOK("get dataset period status", response.GetRetInfo()); err != nil {
		return domain.PeriodStorageState{}, err
	}
	if response.GetSeriesHash() != strings.ToLower(strings.TrimSpace(expectation.GetSeriesHash())) || response.GetExpectedCount() != expectation.GetExpectedCount() {
		return domain.PeriodStorageState{}, fmt.Errorf("get dataset period status: Storage identity does not match request")
	}
	return periodStorageStateFromValues(expectation, response.GetStatus(), response.GetSeriesHash(), response.GetExpectedCount(), response.GetDeadlineAt())
}

func (w *storageWriter) CommitTimeSeriesBatch(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation, items []*storagepb.TimeSeriesBatchRow, sourceEventID string) error {
	if expectation == nil || len(items) == 0 {
		return fmt.Errorf("commit time-series batch: expectation and items are required")
	}
	return retryStorage(ctx, func() error {
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

func (w *storageWriter) RecordDatasetPeriodFailures(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation, seriesIndexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
	if expectation == nil || len(seriesIndexes) == 0 {
		return nil, fmt.Errorf("record dataset period failures: expectation and series_indexes are required")
	}
	dedupedIndexes := make([]uint32, 0, len(seriesIndexes))
	seenIndexes := make(map[uint32]struct{}, len(seriesIndexes))
	for _, index := range seriesIndexes {
		if _, exists := seenIndexes[index]; exists {
			continue
		}
		seenIndexes[index] = struct{}{}
		dedupedIndexes = append(dedupedIndexes, index)
	}
	sort.Slice(dedupedIndexes, func(i, j int) bool { return dedupedIndexes[i] < dedupedIndexes[j] })
	var confirmed []*storagepb.DatasetPeriodFailureResult
	err := retryStorage(ctx, func() error {
		response, err := w.period.RecordDatasetPeriodFailures(ctx, &storagepb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: w.authInfo, Expectation: expectation, SeriesIndexes: dedupedIndexes})
		if err != nil {
			return fmt.Errorf("record dataset period failures: %w", err)
		}
		if response == nil {
			return fmt.Errorf("record dataset period failures: empty response")
		}
		if ret := response.GetRetInfo(); ret != nil && ret.GetCode() == storagepb.ErrorCode_CONFLICT {
			return fmt.Errorf("%w: %s", ErrDatasetPeriodConflict, ret.GetMsg())
		}
		if err := ensureStorageOK("record dataset period failures", response.GetRetInfo()); err != nil {
			return err
		}
		status := strings.ToLower(strings.TrimSpace(response.GetPeriodStatus()))
		if status != domain.PeriodStatusWaiting && status != domain.PeriodStatusComplete && status != domain.PeriodStatusDegraded {
			return fmt.Errorf("record dataset period failures: Storage returned invalid period status %q", status)
		}
		if err := validatePeriodFailureResults(dedupedIndexes, response.GetResults()); err != nil {
			return fmt.Errorf("record dataset period failures: %w", err)
		}
		confirmed = append([]*storagepb.DatasetPeriodFailureResult(nil), response.GetResults()...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return confirmed, nil
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

func validateDatasetPeriodExpectation(expectation *storagepb.DatasetPeriodExpectation, requireDeadline bool) error {
	if expectation == nil || strings.TrimSpace(expectation.GetSpaceId()) == "" || strings.TrimSpace(expectation.GetDatasetId()) == "" || strings.TrimSpace(expectation.GetFrequency()) == "" || expectation.GetPeriodTime() <= 0 || strings.TrimSpace(expectation.GetSeriesHash()) == "" || expectation.GetExpectedCount() == 0 {
		return fmt.Errorf("dataset period space, dataset, frequency, period, hash and positive expected_count are required")
	}
	if requireDeadline && expectation.GetDeadlineAt() <= 0 {
		return fmt.Errorf("dataset period deadline_at is required")
	}
	return nil
}

func periodStorageStateFromEnsure(expectation *storagepb.DatasetPeriodExpectation, response *storagepb.PrimaryEnsureDatasetPeriodRsp) (domain.PeriodStorageState, error) {
	return periodStorageStateFromValues(expectation, response.GetStatus(), expectation.GetSeriesHash(), expectation.GetExpectedCount(), response.GetDeadlineAt())
}

func periodStorageStateFromValues(expectation *storagepb.DatasetPeriodExpectation, status, seriesHash string, expectedCount uint32, deadlineAt int64) (domain.PeriodStorageState, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	seriesHash = strings.ToLower(strings.TrimSpace(seriesHash))
	if status != domain.PeriodStatusWaiting && status != domain.PeriodStatusComplete && status != domain.PeriodStatusDegraded {
		return domain.PeriodStorageState{}, fmt.Errorf("Storage returned invalid dataset period status %q", status)
	}
	if seriesHash == "" || seriesHash != strings.ToLower(strings.TrimSpace(expectation.GetSeriesHash())) || expectedCount == 0 || expectedCount != expectation.GetExpectedCount() {
		return domain.PeriodStorageState{}, fmt.Errorf("Storage returned mismatched dataset period hash/count")
	}
	if deadlineAt <= 0 {
		return domain.PeriodStorageState{}, fmt.Errorf("Storage returned invalid dataset period deadline")
	}
	state := domain.PeriodStorageState{
		Key: domain.PeriodKey{
			SpaceID: strings.TrimSpace(expectation.GetSpaceId()), DatasetID: strings.TrimSpace(expectation.GetDatasetId()),
			Frequency: strings.TrimSpace(expectation.GetFrequency()), PeriodTime: time.Unix(expectation.GetPeriodTime(), 0).UTC(),
		},
		SeriesHash: seriesHash, ExpectedCount: expectedCount, DeadlineAt: time.Unix(deadlineAt, 0).UTC(),
		Status: status, ConfirmedAt: time.Now().UTC(),
	}
	state.Normalize()
	return state, nil
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

func validatePeriodFailureResults(requested []uint32, results []*storagepb.DatasetPeriodFailureResult) error {
	expected := make(map[uint32]struct{}, len(requested))
	for _, index := range requested {
		expected[index] = struct{}{}
	}
	seen := make(map[uint32]struct{}, len(results))
	for _, result := range results {
		if result == nil {
			return fmt.Errorf("Storage returned a nil failure result")
		}
		index := result.GetSeriesIndex()
		if _, duplicate := seen[index]; duplicate {
			return fmt.Errorf("Storage returned duplicate failure series index %d", index)
		}
		seen[index] = struct{}{}
		if _, exists := expected[index]; !exists {
			return fmt.Errorf("Storage returned unexpected failure series index %d", index)
		}
		switch result.GetDisposition() {
		case storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED,
			storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED,
			storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE:
		default:
			return fmt.Errorf("Storage returned unknown failure disposition %d for series index %d", result.GetDisposition(), index)
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("Storage returned %d unique failure results, want %d", len(seen), len(expected))
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

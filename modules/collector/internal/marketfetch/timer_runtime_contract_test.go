package marketfetch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTimerClaimRequired(t *testing.T) {
	setTimerClaimEnvironment(t)
	var storageCalls, executeCalls int
	handler := &Handler{
		TimerRuntimeClient: timerRuntimeClientFunc(func(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
			return nil, context.DeadlineExceeded
		}),
		NewStorage: func(string, string) (Storage, error) {
			storageCalls++
			return timerHandlerStorage{}, nil
		},
		Execute: func(context.Context, Request, Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
			executeCalls++
			return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
		},
	}

	response, err := handler.HandleTimerAt(context.Background(), "request-1", "function-1", time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NotNil(t, response)
	require.False(t, response.Success, "Timer must fail closed when the durable Collector claim is unavailable")
	require.Zero(t, storageCalls, "Storage writer must not be created before a valid Claim")
	require.Zero(t, executeCalls, "providers must not be called before a valid Claim")
}

func TestTimerNoWorkAndAuthFailureDoNotCreateStorage(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *collectorpb.ClaimTimerBatchRsp
		wantOK   bool
	}{
		{name: "no work", response: &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}}, wantOK: true},
		{name: "authentication failure", response: &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_NO_AUTH, Msg: "denied"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			setTimerClaimEnvironment(t)
			var storageCalls, executeCalls int
			handler := &Handler{
				TimerRuntimeClient: timerRuntimeClientFunc(func(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
					return test.response, nil
				}),
				NewStorage: func(string, string) (Storage, error) { storageCalls++; return timerHandlerStorage{}, nil },
				Execute: func(context.Context, Request, Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
					executeCalls++
					return nil, nil
				},
			}
			response, err := handler.HandleTimerAt(context.Background(), "request-1", "function-1", time.Now())
			require.NoError(t, err)
			require.Equal(t, test.wantOK, response.Success)
			require.Zero(t, storageCalls)
			require.Zero(t, executeCalls)
		})
	}
}

func TestTimerIncompleteClaimIdentityDoesNotCreateStorage(t *testing.T) {
	setTimerClaimEnvironment(t)
	raw, err := json.Marshal(Request{BatchID: "batch", SpaceID: "stockcn", Provider: "sina", MarketType: "equity", DatasetID: StockCNDatasetID,
		Frequency: "1m", FunctionName: "function-1", GroupID: 3, GroupCount: 200, BindingHash: "binding-hash",
		Items: []domain.CollectionItem{{SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID}}})
	require.NoError(t, err)
	var storageCalls int
	handler := &Handler{
		TimerRuntimeClient: timerRuntimeClientFunc(func(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
			return &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}, Claimed: true, RequestJson: raw}, nil
		}),
		NewStorage: func(string, string) (Storage, error) { storageCalls++; return timerHandlerStorage{}, nil },
	}
	response, err := handler.HandleTimerAt(context.Background(), "request-1", "function-1", time.Now())
	require.NoError(t, err)
	require.False(t, response.Success)
	require.Zero(t, storageCalls)
}

func TestTimerPublishesDurableCompletion(t *testing.T) {
	setTimerClaimEnvironment(t)
	reqJSON, err := json.Marshal(validClaimedTimerRequest())
	require.NoError(t, err)
	var published int
	handler := &Handler{
		TimerRuntimeClient: timerRuntimeClientFunc(func(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
			return &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}, Claimed: true, RequestJson: reqJSON}, nil
		}),
		NewStorage: func(string, string) (Storage, error) { return timerHandlerStorage{}, nil },
		Execute: func(_ context.Context, req Request, _ Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
			return &marketfetchpb.MarketFetchBatchCompleted{BatchId: req.BatchID, Status: "succeeded"}, nil
		},
		Publish: func(_ context.Context, req Request, payload proto.Message) error {
			published++
			return nil
		},
	}

	response, err := handler.HandleTimerAt(context.Background(), "request-1", "function-1", time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NotNil(t, response)
	require.True(t, response.Success)
	require.Equal(t, 1, published, "a successful durable Timer batch must publish one Completion")
}

func TestReservedDeadlineStoragePeriodContract(t *testing.T) {
	underlying := &reservedDeadlinePeriodStorageStub{}
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wrapped := &reservedDeadlineStorage{Storage: underlying, parent: parent, timeout: 100 * time.Millisecond}
	period, ok := any(wrapped).(periodStorage)
	require.True(t, ok, "the deadline wrapper must expose the complete period storage contract")

	expectation := &storagepb.DatasetPeriodExpectation{SpaceId: "stockcn", DatasetId: StockCNDatasetID, Frequency: "1m", PeriodTime: 1, SeriesHash: "hash", ExpectedCount: 1}
	_, err := period.EnsureDatasetPeriod(context.Background(), expectation)
	require.NoError(t, err)
	err = period.CommitTimeSeriesBatch(context.Background(), expectation, []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 0}}, "event")
	require.NoError(t, err)
	_, err = period.RecordDatasetPeriodFailures(context.Background(), expectation, []uint32{0})
	require.NoError(t, err)
	_, err = period.GetDatasetPeriodStatus(context.Background(), expectation)
	require.NoError(t, err)
	require.Equal(t, []string{"ensure", "commit", "record", "status"}, underlying.calls)
	for _, remaining := range underlying.deadlineRemaining {
		require.Positive(t, remaining)
		require.LessOrEqual(t, remaining, 110*time.Millisecond)
	}
}

func TestRequiredPeriodCommitRejectsMissingTargetIdentity(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"batch_id": "period-batch", "space_id": "stockcn", "dataset_id": StockCNDatasetID,
		"frequency": "1m", "provider": "sina", "market_type": "equity", "require_period_commit": true,
		"items":   []any{map[string]any{"instance_id": "instance-1", "subject_id": "600000.XSHG", "symbol": "sh600000", "dataset_id": StockCNDatasetID, "series_hash": "series-hash", "expected_count": 1, "require_period_commit": true}},
		"targets": []any{map[string]any{"write_target_id": "target-1", "space_id": "stockcn", "instance_id": "instance-1", "task_id": "task-1", "dataset_id": StockCNDatasetID, "series_hash": "series-hash", "expected_count": 1, "frequency": "1m"}},
	})
	require.NoError(t, err)
	var request Request
	var eventData map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &eventData))
	err = decodeRequest(eventData, &request)
	require.ErrorContains(t, err, "target_data_time is required for period commit")
}

func validClaimedTimerRequest() Request {
	period := time.Date(2026, 9, 30, 2, 59, 0, 0, time.UTC)
	return Request{
		BatchID: "batch-1", BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		Frequency: "1m", Provider: "sina", MarketType: "equity", FunctionName: "function-1", GroupID: 3, GroupCount: 200,
		BindingHash: "binding-hash", RequirePeriodCommit: true,
		Items:   []domain.CollectionItem{{InstanceID: "instance-1", SubjectID: "600000.XSHG", Symbol: "sh600000", Provider: "sina", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", TargetDataTime: period.Format(time.RFC3339Nano), SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1, BarLimit: 1, RequirePeriodCommit: true}},
		Targets: []domain.WriteTarget{{ID: "target-1", SpaceID: StockCNSpaceID, InstanceID: "instance-1", TaskID: "task-1", DatasetID: StockCNDatasetID, SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1, Frequency: "1m", TargetDataTime: period.Format(time.RFC3339Nano)}},
	}
}

type timerRuntimeClientFunc func(context.Context, *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error)

func (f timerRuntimeClientFunc) ClaimTimerBatch(ctx context.Context, req *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
	return f(ctx, req)
}

type reservedDeadlinePeriodStorageStub struct {
	calls             []string
	deadlineRemaining []time.Duration
}

func (*reservedDeadlinePeriodStorageStub) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}

func (s *reservedDeadlinePeriodStorageStub) record(ctx context.Context, call string) {
	s.calls = append(s.calls, call)
	if deadline, ok := ctx.Deadline(); ok {
		s.deadlineRemaining = append(s.deadlineRemaining, time.Until(deadline))
	}
}

func (s *reservedDeadlinePeriodStorageStub) EnsureDatasetPeriod(ctx context.Context, _ *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	s.record(ctx, "ensure")
	return domain.PeriodStorageState{}, nil
}

func (s *reservedDeadlinePeriodStorageStub) CommitTimeSeriesBatch(ctx context.Context, _ *storagepb.DatasetPeriodExpectation, _ []*storagepb.TimeSeriesBatchRow, _ string) error {
	s.record(ctx, "commit")
	return nil
}

func (s *reservedDeadlinePeriodStorageStub) RecordDatasetPeriodFailures(ctx context.Context, _ *storagepb.DatasetPeriodExpectation, _ []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
	s.record(ctx, "record")
	return nil, nil
}

func (s *reservedDeadlinePeriodStorageStub) GetDatasetPeriodStatus(ctx context.Context, _ *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	s.record(ctx, "status")
	return domain.PeriodStorageState{}, nil
}

package pebble

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPeriodFailureDeadlineFence(t *testing.T) {
	deadline := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			var first *eventpb.EventMessage
			for _, finalizeFirst := range []bool{false, true} {
				clock := newPeriodReceiptClock(deadline.Add(offset))
				store := newPeriodReceiptStore(t, clock)
				exp := periodExpectationForTest(deadline.Add(-time.Minute), 2)
				exp.DeadlineAt = deadline.Unix()
				_, err := store.EnsureDatasetPeriod(context.Background(), exp)
				require.NoError(t, err)
				if finalizeFirst {
					_, err = store.FinalizeDatasetPeriod(context.Background(), exp, time.Time{})
					require.NoError(t, err)
				}
				retry := exp
				retry.DeadlineAt += 3600
				result, err := store.RecordDatasetPeriodFailures(context.Background(), retry, []uint32{1, 0, 1})
				require.NoError(t, err)
				require.Equal(t, exp.DeadlineAt, result.DeadlineAt)
				require.Len(t, result.FailureResults, 2)
				progress, err := store.GetDatasetPeriodProgress(context.Background(), exp)
				require.NoError(t, err)
				if offset < 0 {
					require.Equal(t, "waiting", result.Status)
					require.Equal(t, []uint32{0, 1}, progress.FailedSeriesIndexes)
					assertPeriodFailureReceipt(t, result, []uint32{0, 1}, recordedFailure, recordedFailure)
					require.Equal(t, 0, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
					continue
				}
				require.Equal(t, "degraded", result.Status)
				require.Empty(t, progress.FailedSeriesIndexes, "post-deadline new failures must not leak into the marker")
				assertPeriodFailureReceipt(t, result, []uint32{0, 1}, missedFailure, missedFailure)
				_, err = store.FinalizeDatasetPeriod(context.Background(), exp, time.Time{})
				require.NoError(t, err)
				require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
				message := periodReceiptMarker(t, store)
				record, err := store.readPeriodValue(datasetMarkerRecordKey(message.GetEventId()))
				require.NoError(t, err)
				persisted := &eventpb.EventMessage{}
				require.NoError(t, proto.Unmarshal(record, persisted))
				require.True(t, proto.Equal(message, persisted))
				iter, err := store.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(markerRecordPrefix), UpperBound: nextPrefix([]byte(markerRecordPrefix))})
				require.NoError(t, err)
				markers := 0
				for valid := iter.First(); valid; valid = iter.Next() {
					markers++
				}
				require.NoError(t, iter.Error())
				require.NoError(t, iter.Close())
				require.Equal(t, 1, markers)
				if first == nil {
					first = message
				} else {
					require.Equal(t, first.GetEventId(), message.GetEventId())
					require.Equal(t, first.GetPayload(), message.GetPayload(), "lock order must not change marker payload")
				}
			}
		})
	}
}

func TestPeriodFailureReplayAfterFinalization(t *testing.T) {
	field := (&pb.RecordDatasetPeriodFailuresRsp{}).ProtoReflect().Descriptor().Fields().ByNumber(3)
	require.NotNil(t, field, "failure RPC must expose per-index dispositions for an ACK-loss replay")
	deadline := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	clock := newPeriodReceiptClock(deadline.Add(-time.Second))
	store := newPeriodReceiptStore(t, clock)
	exp := periodExpectationForTest(deadline.Add(-time.Minute), 3, "BTC-USDT", "ETH-USDT", "SOL-USDT")
	exp.DeadlineAt = deadline.Unix()
	ctx := context.Background()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	_, err = store.RecordDatasetPeriodFailures(ctx, exp, []uint32{0, 1})
	require.NoError(t, err)
	committed, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(time.Unix(exp.PeriodTime, 0), "BTC-USDT")}}, "", "collector")
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, committed.AcceptedSeriesIndexes)
	clock.set(deadline)
	result, err := store.RecordDatasetPeriodFailures(ctx, exp, []uint32{2, 1, 0, 1})
	require.NoError(t, err)
	require.Equal(t, "degraded", result.Status)
	assertPeriodFailureReceipt(t, result, []uint32{0, 1, 2}, succeededFailure, recordedFailure, missedFailure)
	clock.set(deadline.Add(time.Hour))
	replay, err := store.RecordDatasetPeriodFailures(ctx, exp, []uint32{2, 1, 0, 1})
	require.NoError(t, err)
	require.Equal(t, result, replay, "persisted failure ACK must survive crossing the cutoff")
	require.Equal(t, []string{"ETH-USDT"}, collectorPeriodMarkerFromOutbox(t, store).GetFailedSubjects())
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestPeriodFailureTerminalValidatesIndexes(t *testing.T) {
	store := newPeriodTestStore(t)
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Minute), 1, "BTC-USDT")
	_, err := store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.FinalizeDatasetPeriod(context.Background(), exp, time.Unix(exp.DeadlineAt, 0))
	require.NoError(t, err)
	_, err = store.RecordDatasetPeriodFailures(context.Background(), exp, []uint32{0, 1})
	require.Error(t, err, "terminal replay must not bypass complete index validation")
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
	progress, err := store.GetDatasetPeriodProgress(context.Background(), exp)
	require.NoError(t, err)
	require.Empty(t, progress.FailedSeriesIndexes)

	store = newPeriodTestStore(t)
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.RecordDatasetPeriodFailures(context.Background(), exp, []uint32{0, 1})
	require.Error(t, err)
	progress, err = store.GetDatasetPeriodProgress(context.Background(), exp)
	require.NoError(t, err)
	require.Empty(t, progress.FailedSeriesIndexes, "an illegal index must reject all valid indexes in the same request")
	require.Equal(t, 0, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
	exp.DeadlineAt = exp.PeriodTime
	clock := newPeriodReceiptClock(time.Unix(exp.DeadlineAt, 0))
	store = newPeriodReceiptStore(t, clock)
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.RecordDatasetPeriodFailures(context.Background(), exp, []uint32{0, 1})
	require.Error(t, err)
	progress, err = store.GetDatasetPeriodProgress(context.Background(), exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", progress.Status, "invalid requests must not finalize even an expired period")
	require.Empty(t, progress.FailedSeriesIndexes)
	require.Equal(t, 0, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestPeriodCommitAcceptedIndexes(t *testing.T) {
	field := (&pb.CommitTimeSeriesBatchRsp{}).ProtoReflect().Descriptor().Fields().ByNumber(4)
	require.NotNil(t, field, "commit RPC must expose persisted target-period success indexes, not infer acceptance from request keys")
	deadline := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	period := deadline.Add(-time.Minute)
	clock := newPeriodReceiptClock(deadline.Add(-time.Nanosecond))
	store := newPeriodReceiptStore(t, clock)
	exp := periodExpectationForTest(period, 3, "BTC-USDT", "ETH-USDT", "SOL-USDT")
	exp.DeadlineAt = deadline.Unix()
	ctx := context.Background()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	items := []TimeSeriesBatchItem{{SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}, {SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}, {SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}, {SeriesIndex: 2, Row: periodRowForTest(period.Add(-time.Minute), "SOL-USDT")}}
	result, err := store.CommitTimeSeriesBatch(ctx, exp, items, "source", "collector")
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1}, result.AcceptedSeriesIndexes, "historical physical rows do not acknowledge target-period progress")
	clock.set(deadline)
	result, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 2, Row: periodRowForTest(period, "SOL-USDT")}}, "late", "collector")
	require.NoError(t, err)
	require.Equal(t, "degraded", result.Status)
	require.Empty(t, result.AcceptedSeriesIndexes)
	result, err = store.CommitTimeSeriesBatch(ctx, exp, items, "source", "collector")
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1}, result.AcceptedSeriesIndexes)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))

	for _, offset := range []time.Duration{0, time.Nanosecond} {
		clock := newPeriodReceiptClock(deadline.Add(offset))
		store := newPeriodReceiptStore(t, clock)
		_, err := store.EnsureDatasetPeriod(ctx, exp)
		require.NoError(t, err)
		result, err := store.CommitTimeSeriesBatch(ctx, exp, items, "late", "collector")
		require.NoError(t, err)
		require.Empty(t, result.AcceptedSeriesIndexes)
		progress, err := store.GetDatasetPeriodProgress(ctx, exp)
		require.NoError(t, err)
		require.Zero(t, bitmapCount(progress.Bitmap))
	}

	clock.set(deadline.Add(-time.Second))
	store = newPeriodReceiptStore(t, clock)
	_, err = store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	row := periodRowForTest(period, "BTC-USDT")
	_, err = store.UpsertFieldsEventWithSource(ctx, []*pb.RowFieldUpsert{row}, "generic-source", func(space, dataset string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return BuildDatasetRowsUpsertedMessageForSourceWithWriteSource("node-a", "generic-source", "collector", space, dataset, rows)
	})
	require.NoError(t, err)
	result, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: row}}, "generic-source", "collector")
	require.NoError(t, err)
	require.Empty(t, result.AcceptedSeriesIndexes, "source-event dedupe and physical row existence cannot fabricate a period bit")
}

func TestPeriodCommitRejectsWrongSeriesBinding(t *testing.T) {
	store := newPeriodTestStore(t)
	period := time.Now().UTC().Truncate(time.Minute)
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	_, err := store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	row := periodRowForTest(period, "BTC-USDT")
	row.Key.GetTimeSeries().SeriesTag = "binance"
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: row}}, "", "collector")
	require.Error(t, err, "empty expected tag is a real default series, not a wildcard")
	progress, err := store.GetDatasetPeriodProgress(context.Background(), exp)
	require.NoError(t, err)
	require.Zero(t, bitmapCount(progress.Bitmap))
	require.Equal(t, 0, countOutboxEvent(t, store, events.DatasetRowsUpserted.Name()))

	exp = periodExpectationForTest(period, 2, "BTC-USDT", "BTC-USDT")
	exp.SeriesSnapshot[0].SeriesTag = "okx"
	exp.SeriesSnapshot[1].SeriesTag = "binance"
	store = newPeriodTestStore(t)
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	okx := periodRowForTest(period, "BTC-USDT")
	okx.Key.GetTimeSeries().SeriesTag = "okx"
	binance := periodRowForTest(period, "BTC-USDT")
	binance.Key.GetTimeSeries().SeriesTag = "binance"
	for _, items := range [][]TimeSeriesBatchItem{
		{{SeriesIndex: 0, Row: binance}},
		{{SeriesIndex: 1, Row: binance}, {SeriesIndex: 0, Row: binance}},
		{{SeriesIndex: 0, Row: okx}, {SeriesIndex: 2, Row: binance}},
	} {
		_, err = store.CommitTimeSeriesBatch(context.Background(), exp, items, "", "collector")
		require.Error(t, err)
		progress, err = store.GetDatasetPeriodProgress(context.Background(), exp)
		require.NoError(t, err)
		require.Zero(t, bitmapCount(progress.Bitmap))
		require.Equal(t, 0, countOutboxEvent(t, store, events.DatasetRowsUpserted.Name()))
		require.Equal(t, 0, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
	}
	result, err := store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: binance}, {SeriesIndex: 0, Row: okx}}, "", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", result.Status)
	require.Equal(t, []uint32{0, 1}, result.AcceptedSeriesIndexes)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: binance}}, "", "collector")
	require.Error(t, err, "terminal replay still validates the persisted subject/tag binding")
	result, err = store.RecordDatasetPeriodFailures(context.Background(), exp, []uint32{1, 0, 1})
	require.NoError(t, err)
	assertPeriodFailureReceipt(t, result, []uint32{0, 1}, succeededFailure, succeededFailure)
	require.Equal(t, 1, countOutboxEvent(t, store, events.DatasetRowsUpserted.Name()))
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
	exp.DeadlineAt = exp.PeriodTime
	clock := newPeriodReceiptClock(time.Unix(exp.DeadlineAt, 0))
	store = newPeriodReceiptStore(t, clock)
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(context.Background(), exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: binance}, {SeriesIndex: 0, Row: binance}}, "", "collector")
	require.Error(t, err)
	progress, err = store.GetDatasetPeriodProgress(context.Background(), exp)
	require.NoError(t, err)
	require.Equal(t, "waiting", progress.Status)
	require.Zero(t, bitmapCount(progress.Bitmap))
	require.Equal(t, 0, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestPeriodCommitRejectsPhysicalIdentityAliases(t *testing.T) {
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		frequency string
		mutate    func(*pb.RowKey)
	}{
		{name: "space_whitespace", mutate: func(k *pb.RowKey) { k.SpaceId = " crypto " }},
		{name: "dataset_whitespace", mutate: func(k *pb.RowKey) { k.DatasetId = " bars " }},
		{name: "subject_whitespace", mutate: func(k *pb.RowKey) { k.GetTimeSeries().SubjectId = " ETH-USDT " }},
		{name: "tag_whitespace", mutate: func(k *pb.RowKey) { k.GetTimeSeries().SeriesTag = " binance " }},
		{name: "month_is_not_minute", mutate: func(k *pb.RowKey) { k.GetTimeSeries().Freq = "1M" }},
		{name: "frequency_whitespace", mutate: func(k *pb.RowKey) { k.GetTimeSeries().Freq = " 1m " }},
		{name: "hour_case_alias", frequency: "1h", mutate: func(k *pb.RowKey) { k.GetTimeSeries().Freq = "1H" }},
		{name: "fractional_target_second", mutate: func(k *pb.RowKey) {
			k.GetTimeSeries().DataTime = period.Add(500 * time.Millisecond).Format(time.RFC3339Nano)
		}},
	} {
		for _, terminal := range []bool{false, true} {
			state := "waiting"
			if terminal {
				state = "complete"
			}
			t.Run(test.name+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				clock := newPeriodReceiptClock(period.Add(time.Minute))
				store := newPeriodReceiptStore(t, clock)
				exp := periodExpectationForTest(period, 2)
				exp.DeadlineAt = period.Add(time.Hour).Unix()
				if test.frequency != "" {
					exp.Frequency = test.frequency
				}
				exp.SeriesSnapshot[0].SeriesTag = "okx"
				exp.SeriesSnapshot[1].SeriesTag = "binance"
				_, err := store.EnsureDatasetPeriod(ctx, exp)
				require.NoError(t, err)
				rows := []*pb.RowFieldUpsert{periodRowForTest(period, "BTC-USDT"), periodRowForTest(period, "ETH-USDT")}
				for index, row := range rows {
					row.Key.GetTimeSeries().Freq = exp.Frequency
					row.Key.GetTimeSeries().SeriesTag = exp.SeriesSnapshot[index].SeriesTag
				}
				if terminal {
					_, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: rows[0]}, {SeriesIndex: 1, Row: rows[1]}}, "initial", "collector")
					require.NoError(t, err)
				}
				bad := proto.Clone(rows[1]).(*pb.RowFieldUpsert)
				test.mutate(bad.Key)
				result, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: rows[0]}, {SeriesIndex: 1, Row: bad}}, "alias", "collector")
				require.Error(t, err, "period acceptance must match the physical RowKey, not a trim/lower/Unix alias")
				require.Empty(t, result.AcceptedSeriesIndexes)
				progress, err := store.GetDatasetPeriodProgress(ctx, exp)
				require.NoError(t, err)
				require.Equal(t, state, progress.Status)
				if terminal {
					require.Equal(t, 2, bitmapCount(progress.Bitmap))
					require.Equal(t, 1, countOutboxEvent(t, store, events.DatasetRowsUpserted.Name()))
					require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
				} else {
					require.Zero(t, bitmapCount(progress.Bitmap))
					require.Equal(t, 0, countOutboxEvent(t, store, events.DatasetRowsUpserted.Name()))
					require.Equal(t, 0, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
					if _, err := NormalizeRowKey(bad.Key); err == nil {
						_, existing, err := store.ReadFieldsWithPresence(ctx, []*pb.RowKey{rows[0].Key, bad.Key}, []string{"close"}, nil)
						require.NoError(t, err)
						require.Empty(t, existing, "the valid portion of an invalid request must not be persisted")
					}
				}
			})
		}
	}
}

func TestPeriodCommitAcceptedIndexesPreservesHistoricalBars(t *testing.T) {
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	store := newPeriodReceiptStore(t, newPeriodReceiptClock(period.Add(time.Minute)))
	exp := periodExpectationForTest(period, 1, "BTC-USDT")
	exp.DeadlineAt = period.Add(time.Hour).Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	historical := periodRowForTest(period.Add(-time.Minute), "BTC-USDT")
	result, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: historical}}, "history", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", result.Status)
	require.Empty(t, result.AcceptedSeriesIndexes)
	_, existing, err := store.ReadFieldsWithPresence(ctx, []*pb.RowKey{historical.Key}, []string{"close"}, nil)
	require.NoError(t, err)
	require.Len(t, existing, 1, "legal historical bars remain physically writable")
	progress, err := store.GetDatasetPeriodProgress(ctx, exp)
	require.NoError(t, err)
	require.Zero(t, bitmapCount(progress.Bitmap))
	target := periodRowForTest(period, "BTC-USDT")
	target.Key.GetTimeSeries().DataTime = period.In(time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339Nano)
	result, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: target}}, "target", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", result.Status)
	require.Equal(t, []uint32{0}, result.AcceptedSeriesIndexes, "equivalent timestamp notation is canonicalized before exact target comparison")
}

func TestPeriodMonthFrequencyDoesNotAliasMinute(t *testing.T) {
	ctx := context.Background()
	period := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store := newPeriodReceiptStore(t, newPeriodReceiptClock(period.Add(time.Minute)))
	month := periodExpectationForTest(period, 1, "BTC-USDT")
	month.Frequency = "1M"
	month.DeadlineAt = period.Add(time.Hour).Unix()
	minute := month
	minute.Frequency = "1m"

	_, err := store.EnsureDatasetPeriod(ctx, month)
	require.NoError(t, err)
	_, err = store.EnsureDatasetPeriod(ctx, minute)
	require.NoError(t, err)

	monthRow := periodRowForTest(period, "BTC-USDT")
	monthRow.Key.GetTimeSeries().Freq = "1M"
	monthResult, err := store.CommitTimeSeriesBatch(ctx, month, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: monthRow}}, "month-row", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", monthResult.Status)
	require.Equal(t, []uint32{0}, monthResult.AcceptedSeriesIndexes)

	minuteRow := periodRowForTest(period, "BTC-USDT")
	minuteResult, err := store.CommitTimeSeriesBatch(ctx, minute, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: minuteRow}}, "minute-row", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", minuteResult.Status)
	require.Equal(t, []uint32{0}, minuteResult.AcceptedSeriesIndexes)
	require.Equal(t, 2, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()), "minute and month are distinct period identities at the same timestamp")
}

func TestPeriodHistoricalHourFrequencyDoesNotAliasLowercaseHour(t *testing.T) {
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	store := newPeriodReceiptStore(t, newPeriodReceiptClock(period.Add(time.Minute)))
	historical := periodExpectationForTest(period, 1, "BTC-USDT")
	historical.Frequency = "1H"
	historical.DeadlineAt = period.Add(time.Hour).Unix()
	canonical := historical
	canonical.Frequency = "1h"

	_, err := store.EnsureDatasetPeriod(ctx, historical)
	require.NoError(t, err)
	_, err = store.EnsureDatasetPeriod(ctx, canonical)
	require.NoError(t, err)

	historicalRow := periodRowForTest(period, "BTC-USDT")
	historicalRow.Key.GetTimeSeries().Freq = "1H"
	historicalResult, err := store.CommitTimeSeriesBatch(ctx, historical, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: historicalRow}}, "historical-hour-row", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", historicalResult.Status)
	require.Equal(t, []uint32{0}, historicalResult.AcceptedSeriesIndexes)

	canonicalRow := periodRowForTest(period, "BTC-USDT")
	canonicalRow.Key.GetTimeSeries().Freq = "1h"
	canonicalResult, err := store.CommitTimeSeriesBatch(ctx, canonical, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: canonicalRow}}, "canonical-hour-row", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", canonicalResult.Status)
	require.Equal(t, []uint32{0}, canonicalResult.AcceptedSeriesIndexes)
	require.Equal(t, 2, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()), "the catalog's 1H spelling and 1h are distinct Storage period keys")
}

func TestPeriodEnsureRejectsInvalidSeriesTagsWithoutSideEffects(t *testing.T) {
	for _, test := range []struct {
		name string
		tag  string
	}{
		{name: "leading_whitespace", tag: " binance"},
		{name: "control_character", tag: "binance\x1f"},
		{name: "over_limit", tag: strings.Repeat("x", 129)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			store := newPeriodReceiptStore(t, newPeriodReceiptClock(period))
			exp := periodExpectationForTest(period, 1, "BTC-USDT")
			exp.SeriesSnapshot[0].SeriesTag = test.tag
			_, err := store.EnsureDatasetPeriod(ctx, exp)
			require.Error(t, err, "snapshot tags must be valid physical Storage row identities")

			query := exp
			query.SeriesSnapshot = nil
			_, err = store.GetDatasetPeriodStatus(ctx, query)
			require.ErrorIs(t, err, cpebble.ErrNotFound, "rejected snapshots must not initialize a period")
			entries, err := store.ListOutbox(ctx, 0, 100)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestPeriodSeriesSnapshotStorageIdentity(t *testing.T) {
	store := newPeriodTestStore(t)
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Minute), 2, "BTC-USDT", "BTC-USDT")
	_, err := store.EnsureDatasetPeriod(context.Background(), exp)
	require.Error(t, err, "one physical subject/tag series cannot occupy two expected indexes")
	exp.SeriesSnapshot[0].SeriesTag = "okx"
	exp.SeriesSnapshot[1].SeriesTag = "binance"
	result, err := store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	require.Equal(t, exp.DeadlineAt, result.DeadlineAt)
	loaded, err := store.GetDatasetPeriodStatus(context.Background(), exp)
	require.NoError(t, err)
	require.Equal(t, exp.SeriesSnapshot, loaded.SeriesSnapshot)
	changed := exp
	changed.SeriesSnapshot = append([]DatasetPeriodSeries(nil), exp.SeriesSnapshot...)
	changed.SeriesSnapshot[1].SeriesTag = "kraken"
	_, err = store.EnsureDatasetPeriod(context.Background(), changed)
	var conflict PeriodConflictError
	require.ErrorAs(t, err, &conflict)
	retry := exp
	retry.DeadlineAt += 3600
	result, err = store.EnsureDatasetPeriod(context.Background(), retry)
	require.NoError(t, err)
	require.Equal(t, exp.DeadlineAt, result.DeadlineAt, "Ensure response must return the canonical first deadline")
}

func TestPeriodStatusQueryDoesNotCreate(t *testing.T) {
	store := newPeriodTestStore(t)
	exp := periodExpectationForTest(time.Now().UTC().Truncate(time.Minute), 1, "BTC-USDT")
	lightweight := exp
	lightweight.SeriesSnapshot = nil
	_, err := store.GetDatasetPeriodStatus(context.Background(), lightweight)
	require.ErrorIs(t, err, cpebble.ErrNotFound)
	_, found, err := store.readPeriodStatus(periodBase(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime))
	require.NoError(t, err)
	require.False(t, found, "status query must not create any period status")
	_, err = store.EnsureDatasetPeriod(context.Background(), exp)
	require.NoError(t, err)
	conflict := exp
	conflict.SeriesHash = "wrong-hash"
	_, err = store.GetDatasetPeriodStatus(context.Background(), conflict)
	var identityConflict PeriodConflictError
	require.ErrorAs(t, err, &identityConflict, "read-only status must validate lightweight hash/count identity")
	conflict = lightweight
	conflict.ExpectedCount++
	_, err = store.GetDatasetPeriodStatus(context.Background(), conflict)
	require.ErrorAs(t, err, &identityConflict)
	_, err = store.FinalizeDatasetPeriod(context.Background(), conflict, time.Unix(exp.DeadlineAt, 0))
	require.ErrorAs(t, err, &identityConflict, "an explicit finalizer cannot bypass the period identity")
	progress, err := store.GetDatasetPeriodStatus(context.Background(), lightweight)
	require.NoError(t, err)
	require.Equal(t, "waiting", progress.Status)
	require.Equal(t, exp.SeriesHash, progress.SeriesHash)
	require.Equal(t, exp.ExpectedCount, progress.ExpectedCount)
	require.Equal(t, exp.DeadlineAt, progress.DeadlineAt)
	_, err = store.FinalizeDatasetPeriod(context.Background(), exp, time.Unix(exp.DeadlineAt, 0))
	require.NoError(t, err)
	progress, err = store.GetDatasetPeriodStatus(context.Background(), lightweight)
	require.NoError(t, err)
	require.Equal(t, "degraded", progress.Status)
}

const (
	recordedFailure  = pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED
	succeededFailure = pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED
	missedFailure    = pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE
)

type periodReceiptClock struct{ nanos atomic.Int64 }

func newPeriodReceiptClock(now time.Time) *periodReceiptClock {
	clock := &periodReceiptClock{}
	clock.set(now)
	return clock
}

func (c *periodReceiptClock) set(now time.Time) { c.nanos.Store(now.UnixNano()) }
func (c *periodReceiptClock) now() time.Time    { return time.Unix(0, c.nanos.Load()).UTC() }

func newPeriodReceiptStore(t *testing.T, clock *periodReceiptClock) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "node"), NodeID: "node-a", PeriodNow: clock.now})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func assertPeriodFailureReceipt(t *testing.T, result DatasetPeriodResult, indexes []uint32, dispositions ...pb.PeriodFailureDisposition) {
	t.Helper()
	require.Len(t, result.FailureResults, len(indexes))
	require.Len(t, dispositions, len(indexes))
	for i, index := range indexes {
		require.Equal(t, index, result.FailureResults[i].GetSeriesIndex())
		require.Equal(t, dispositions[i], result.FailureResults[i].GetDisposition())
		require.NotEqual(t, pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_UNSPECIFIED, result.FailureResults[i].GetDisposition())
	}
}

func periodReceiptMarker(t *testing.T, store *Store) *eventpb.EventMessage {
	t.Helper()
	entries, err := store.ListOutbox(context.Background(), 0, 1000)
	require.NoError(t, err)
	for _, entry := range entries {
		message := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(entry.Data, message))
		if message.GetEventName() == events.CollectorPeriodCompleted.Name() {
			return message
		}
	}
	t.Fatal("completion marker not found")
	return nil
}

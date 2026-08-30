package storageio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type loaderReader struct {
	subjects []input.Subject
	rows     map[string][]ViewRow
	history  map[string]int
}

type pinnedLoaderReader struct {
	loaderReader
	started  bool
	expected map[string]string
}

func (r *pinnedLoaderReader) BeginViewSnapshot(context.Context, string, []string) (ViewReader, error) {
	r.started = true
	return r.loaderReader, nil
}

func (r *pinnedLoaderReader) BeginViewSnapshotAt(_ context.Context, _ string, _ []string, expected map[string]string) (ViewReader, error) {
	r.started = true
	r.expected = expected
	return r.loaderReader, nil
}

func (r loaderReader) ReadPeriod(_ context.Context, _, viewID string, _ time.Time) ([]ViewRow, error) {
	return r.rows[viewID], nil
}

func (r loaderReader) ListSubjects(context.Context, string, string) ([]input.Subject, error) {
	return r.subjects, nil
}

func (r loaderReader) HistoryPeriods(context.Context, string, string, string, time.Time, time.Time) (map[string]int, error) {
	return r.history, nil
}

func TestLoaderAcceptsCompleteNonEmptyPool(t *testing.T) {
	reader := loaderReader{
		subjects: []input.Subject{{SubjectID: "btc-binance", InstrumentID: "BTC-USDT-SPOT", Exchange: "binance", Active: true}},
		history:  map[string]int{"btc-binance": 3},
		rows: map[string][]ViewRow{
			"source": {{InstrumentID: "BTC-USDT-SPOT", DataTime: time.Unix(60, 0), Values: map[string]string{"close": "100"}}},
			"factor": {{InstrumentID: "BTC-USDT-SPOT", DataTime: time.Unix(60, 0), Values: map[string]string{"bias": "0.25"}}},
		},
	}
	compiled := compiler.CompiledStrategy{
		SpaceID: "space", SourceView: compiler.CompiledView{ID: "source", Frequency: "1m"},
		InstrumentPool: config.InstrumentPoolRule{Exchanges: []string{"binance"}, MinHistoryPeriods: 2},
		Factors:        []compiler.CompiledFactor{{FactorID: "bias", ResultViewID: "factor", ColumnName: "bias"}},
		Dependencies:   compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor"}},
	}
	got, err := (Loader{Reader: reader}).Load(context.Background(), domain.StrategyRunner{SpaceID: "space", StrategyID: "strategy"}, compiled, time.Unix(60, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Values["bias"].String() != "0.25" {
		t.Fatalf("loaded input = %+v", got)
	}
}

func TestLoaderUsesPinnedViewSnapshotWhenReaderSupportsIt(t *testing.T) {
	reader := &pinnedLoaderReader{loaderReader: loaderReader{
		subjects: []input.Subject{{SubjectID: "btc-binance", InstrumentID: "BTC-USDT-SPOT", Exchange: "binance", Active: true}},
		history:  map[string]int{"btc-binance": 2},
		rows: map[string][]ViewRow{
			"source": {{InstrumentID: "BTC-USDT-SPOT", DataTime: time.Unix(60, 0), Values: map[string]string{"close": "100"}}},
			"factor": {{InstrumentID: "BTC-USDT-SPOT", DataTime: time.Unix(60, 0), Values: map[string]string{"bias": "0.25"}}},
		},
	}}
	compiled := compiler.CompiledStrategy{
		SpaceID: "space", SourceView: compiler.CompiledView{ID: "source", Frequency: "1m"},
		InstrumentPool: config.InstrumentPoolRule{Exchanges: []string{"binance"}, MinHistoryPeriods: 2},
		Factors:        []compiler.CompiledFactor{{FactorID: "bias", ResultViewID: "factor", ColumnName: "bias"}},
		Dependencies:   compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor"}},
	}
	if _, err := (Loader{Reader: reader}).Load(context.Background(), domain.StrategyRunner{SpaceID: "space", StrategyID: "strategy"}, compiled, time.Unix(60, 0)); err != nil {
		t.Fatal(err)
	}
	if !reader.started {
		t.Fatal("loader did not establish a pinned View snapshot")
	}
}

func TestLoaderPassesReadyIndexProvenanceToSnapshotReader(t *testing.T) {
	reader := &pinnedLoaderReader{loaderReader: loaderReader{
		subjects: []input.Subject{{SubjectID: "btc-binance", InstrumentID: "BTC-USDT-SPOT", Exchange: "binance", Active: true}},
		history:  map[string]int{"btc-binance": 2},
		rows: map[string][]ViewRow{
			"source": {{InstrumentID: "BTC-USDT-SPOT", DataTime: time.Unix(60, 0), Values: map[string]string{"close": "100"}}},
			"factor": {{InstrumentID: "BTC-USDT-SPOT", DataTime: time.Unix(60, 0), Values: map[string]string{"bias": "0.25"}}},
		},
	}}
	compiled := compiler.CompiledStrategy{
		SpaceID: "space", SourceView: compiler.CompiledView{ID: "source", Frequency: "1m"},
		InstrumentPool: config.InstrumentPoolRule{Exchanges: []string{"binance"}, MinHistoryPeriods: 2},
		Factors:        []compiler.CompiledFactor{{FactorID: "bias", ResultViewID: "factor", ColumnName: "bias"}},
		Dependencies:   compiler.DependenciesSnapshot{FactorResultViewIDs: []string{"factor"}},
	}
	if _, err := (Loader{Reader: reader}).LoadAt(context.Background(), domain.StrategyRunner{SpaceID: "space", StrategyID: "strategy"}, compiled, time.Unix(60, 0), map[string]string{"source": "source-a", "factor": "factor-a"}); err != nil {
		t.Fatal(err)
	}
	if reader.expected["source"] != "source-a" || reader.expected["factor"] != "factor-a" {
		t.Fatalf("expected index provenance=%v", reader.expected)
	}
}

func TestRetErrorClassifiesViewGenerationMismatchAsStale(t *testing.T) {
	err := retError(&commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "active View index revision changed: expected=1 actual=2"})
	if !errors.Is(err, input.ErrStaleViewSnapshot) {
		t.Fatalf("error=%v, want stale View snapshot", err)
	}
}

func TestRetErrorKeepsUnpreparedViewRetryable(t *testing.T) {
	err := retError(&commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "view has no active index"})
	if errors.Is(err, input.ErrStaleViewSnapshot) {
		t.Fatalf("error=%v, no-active-index must remain retryable", err)
	}
	if errors.Is(err, compiler.ErrDependencyMismatch) {
		t.Fatalf("error=%v, no-active-index must not be classified as permanent", err)
	}
}

func TestRetErrorClassifiesMissingViewAsPermanentDependency(t *testing.T) {
	err := retError(&commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_FOUND, Msg: "view missing"})
	if !errors.Is(err, compiler.ErrDependencyMismatch) {
		t.Fatalf("error=%v, want permanent dependency mismatch", err)
	}
}

func TestRetErrorForViewKeepsStorageStartupMismatchRetryable(t *testing.T) {
	err := retErrorForView(&commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "active View index changed: expected=prices-a actual=prices"}, "prices")
	if errors.Is(err, input.ErrStaleViewSnapshot) {
		t.Fatalf("error=%v, startup runtime mismatch must remain retryable", err)
	}
	err = retErrorForView(&commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "active View index changed: expected=prices-a actual="}, "prices")
	if errors.Is(err, input.ErrStaleViewSnapshot) {
		t.Fatalf("error=%v, empty runtime index during restore must remain retryable", err)
	}
}

func TestRetErrorForViewClassifiesPhysicalCutoverAsStale(t *testing.T) {
	err := retErrorForView(&commonpb.RetInfo{Code: commonpb.ErrorCode_VIEW_NOT_READY, Msg: "active View index changed: expected=prices-a actual=prices-b"}, "prices")
	if !errors.Is(err, input.ErrStaleViewSnapshot) {
		t.Fatalf("error=%v, physical cutover must be stale", err)
	}
}

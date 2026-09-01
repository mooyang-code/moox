package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// PipelineRequest contains the canonical identity used by one K-line write.
// Provider and source are selected by the manifest/router, never by a caller
// that can make an arbitrary source masquerade as another one.
type PipelineRequest struct {
	SpaceID       string
	DatasetID     string
	SeriesTag     string
	SourceEventID string
	SourceKey     marketdata.SourceKey
	Request       marketdata.KlineRequest
}

type PipelineResult struct {
	RowsWritten int
	LastBar     time.Time
	SourceKey   marketdata.SourceKey
}

type KlineRowWriter interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
}

type sourcedKlineRowWriter interface {
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
}

type KlinePipeline struct {
	Fetcher     marketdata.KlineFetcher
	Writer      KlineRowWriter
	Now         func() time.Time
	SettleDelay time.Duration
}

func (p *KlinePipeline) FetchAndWrite(ctx context.Context, request PipelineRequest) (PipelineResult, error) {
	if p == nil || p.Fetcher == nil || p.Writer == nil {
		return PipelineResult{}, fmt.Errorf("kline pipeline is not initialized")
	}
	if strings.TrimSpace(request.SpaceID) == "" || strings.TrimSpace(request.DatasetID) == "" {
		return PipelineResult{}, fmt.Errorf("space_id and dataset_id are required")
	}
	if strings.TrimSpace(request.SourceEventID) == "" {
		return PipelineResult{}, fmt.Errorf("source_event_id is required")
	}
	if err := request.Request.Validate(); err != nil {
		return PipelineResult{}, err
	}
	spec := p.Fetcher.KlineSpec()
	descriptor := p.Fetcher.Descriptor()
	if !descriptor.Status.IsEnabled() {
		return PipelineResult{}, fmt.Errorf("%w: source %s has status %q", marketdata.ErrNotSupported, descriptor.Key(), descriptor.Status)
	}
	if request.SourceKey.ProviderID != "" || request.SourceKey.SourceID != "" {
		if request.SourceKey != descriptor.Key() {
			return PipelineResult{}, fmt.Errorf("source_key %s does not match fetcher %s", request.SourceKey, descriptor.Key())
		}
	}
	if err := spec.Validate(); err != nil {
		return PipelineResult{}, fmt.Errorf("fetcher spec: %w", err)
	}
	if !spec.SupportsMarketInstrument(request.Request.MarketID, request.Request.InstrumentType) {
		return PipelineResult{}, fmt.Errorf("market/instrument %s/%s is not supported by %s", request.Request.MarketID, request.Request.InstrumentType, descriptor.Key())
	}
	if !spec.SupportsFrequency(request.Request.Frequency) {
		return PipelineResult{}, fmt.Errorf("frequency %q is not supported by %s", request.Request.Frequency, descriptor.Key())
	}
	if (!request.Request.StartTime.IsZero() || !request.Request.EndTime.IsZero()) && !spec.SupportsRange {
		return PipelineResult{}, fmt.Errorf("%w: source %s does not support range requests", marketdata.ErrNotSupported, descriptor.Key())
	}
	bars, err := p.Fetcher.FetchKlines(ctx, request.Request)
	if err != nil {
		return PipelineResult{}, err
	}
	if len(bars) == 0 {
		return PipelineResult{}, fmt.Errorf("%w: source %s returned no bars", marketdata.ErrUnavailable, descriptor.Key())
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	if p.SettleDelay < 0 {
		return PipelineResult{}, fmt.Errorf("settle delay cannot be negative")
	}
	closedBefore := now.Add(-p.SettleDelay)
	closedBars := make([]marketdata.NormalizedKline, 0, len(bars))
	for index, bar := range bars {
		if bar.ProviderID != descriptor.ProviderID || bar.SourceID != descriptor.SourceID {
			return PipelineResult{}, fmt.Errorf("bar %d source %s/%s does not match fetcher %s", index, bar.ProviderID, bar.SourceID, descriptor.Key())
		}
		if bar.SubjectID != request.Request.SubjectID || bar.ProviderSymbol != request.Request.ProviderSymbol || bar.Frequency != request.Request.Frequency {
			return PipelineResult{}, fmt.Errorf("bar %d identity subject_id=%q provider_symbol=%q frequency=%q does not match request", index, bar.SubjectID, bar.ProviderSymbol, bar.Frequency)
		}
		if err := bar.ValidateOHLCV(); err != nil {
			return PipelineResult{}, fmt.Errorf("bar %d from %s: %w", index, descriptor.Key(), err)
		}
		// Filter the still-forming bar before validating optional fields. Some
		// providers omit amount or return provisional values for the current bar;
		// that must not discard older, otherwise valid bars in the same response.
		if bar.BarEnd.After(closedBefore) {
			continue
		}
		if spec.HasAmount && (!bar.Amount.Valid || bar.Amount.Null) {
			return PipelineResult{}, fmt.Errorf("bar %d from %s is missing required amount", index, descriptor.Key())
		}
		if err := bar.Validate(); err != nil {
			return PipelineResult{}, fmt.Errorf("bar %d from %s: %w", index, descriptor.Key(), err)
		}
		closedBars = append(closedBars, bar)
	}
	if len(closedBars) == 0 {
		return PipelineResult{}, fmt.Errorf("%w: source %s returned no bars settled by %s", marketdata.ErrNoClosedBar, descriptor.Key(), closedBefore.Format(time.RFC3339Nano))
	}
	rows, result, err := NormalizeKlineRows(request.SpaceID, request.DatasetID, request.SeriesTag, request.SourceEventID, closedBars)
	if err != nil {
		return PipelineResult{}, err
	}
	result.SourceKey = descriptor.Key()
	if len(rows) == 0 {
		return result, nil
	}
	if writer, ok := p.Writer.(sourcedKlineRowWriter); ok {
		if err := writer.UpsertFieldsWithSource(ctx, rows, request.SourceEventID); err != nil {
			return PipelineResult{}, fmt.Errorf("write kline rows: %w", err)
		}
	} else if err := p.Writer.UpsertFields(ctx, rows); err != nil {
		return PipelineResult{}, fmt.Errorf("write kline rows: %w", err)
	}
	return result, nil
}

func NormalizeKlineRows(spaceID, datasetID, seriesTag, sourceEventID string, bars []marketdata.NormalizedKline) ([]*storagepb.RowFieldUpsert, PipelineResult, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(sourceEventID) == "" {
		return nil, PipelineResult{}, fmt.Errorf("space_id, dataset_id and source_event_id are required")
	}
	ordered := append([]marketdata.NormalizedKline(nil), bars...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].BarStart.Before(ordered[j].BarStart) })
	rows := make([]*storagepb.RowFieldUpsert, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	result := PipelineResult{}
	for index, bar := range ordered {
		if err := bar.Validate(); err != nil {
			return nil, PipelineResult{}, fmt.Errorf("bar %d: %w", index, err)
		}
		key := bar.SubjectID + "\x00" + bar.Frequency + "\x00" + bar.BarStart.UTC().Format(time.RFC3339Nano)
		if _, exists := seen[key]; exists {
			return nil, PipelineResult{}, fmt.Errorf("duplicate kline %s", key)
		}
		seen[key] = struct{}{}
		fields := []*storagepb.FieldValue{
			doubleField("open", decimalFloat(bar.Open)),
			doubleField("high", decimalFloat(bar.High)),
			doubleField("low", decimalFloat(bar.Low)),
			doubleField("close", decimalFloat(bar.Close)),
			doubleField("volume", decimalFloat(bar.Volume)),
		}
		if bar.Amount.Valid && !bar.Amount.Null {
			fields = append(fields, doubleField("amount", decimalFloat(bar.Amount.Value)))
		} else {
			// Amount is part of the canonical row contract. An unavailable
			// source value must clear an old value rather than leave a stale
			// field behind after Storage's field-level merge.
			fields = append(fields, nullField("amount"))
		}
		// Source identity is part of the canonical dataset schema. Keep the
		// attributes as well so operational queries can inspect it without a
		// schema-specific projection.
		fields = append(fields,
			stringField("provider_id", bar.ProviderID),
			stringField("source_id", bar.SourceID),
			stringField("provider_symbol", bar.ProviderSymbol),
		)
		attributes := map[string]*storagepb.TypedValue{
			"provider_id":     stringValue(bar.ProviderID),
			"source_id":       stringValue(bar.SourceID),
			"provider_symbol": stringValue(bar.ProviderSymbol),
			"volume_unit":     stringValue(bar.VolumeUnit),
		}
		if bar.AmountUnit != "" {
			attributes["amount_unit"] = stringValue(bar.AmountUnit)
		}
		if bar.ProviderTime.IsZero() == false {
			attributes["provider_time"] = stringValue(bar.ProviderTime.UTC().Format(time.RFC3339Nano))
		}
		if !bar.FetchedAt.IsZero() {
			attributes["fetched_at"] = stringValue(bar.FetchedAt.UTC().Format(time.RFC3339Nano))
		}
		rows = append(rows, &storagepb.RowFieldUpsert{
			Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: bar.SubjectID, Freq: bar.Frequency, DataTime: bar.BarStart.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag,
			}}},
			Fields:     fields,
			Attributes: attributes,
		})
		result.RowsWritten++
		if result.LastBar.IsZero() || bar.BarStart.After(result.LastBar) {
			result.LastBar = bar.BarStart
		}
	}
	return rows, result, nil
}

func decimalFloat(value common.Decimal) float64 {
	result, _ := value.Float64()
	return result
}

func stringValue(value string) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}
}

func doubleField(name string, value float64) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func stringField(name, value string) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: stringValue(value)}
}

func nullField(name string) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}}
}

package marketfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// K 线批次的来源，只用于写入来源与指标。
const (
	KlineOriginStreaming = "stream"
	KlineOriginPull      = "pull"
	KlineOriginBackfill  = "backfill"
)

// KlineBatch 是与采集方式无关的 K 线批次：推送（周期闸门）、SCF 拉取、本地拉取、补采都只产出它，经 KlineCommitter 写 Storage。
// 周期提交时 Dataset 只带该周期生效的那一份（期望标的、序号、hash）。
type KlineBatch struct {
	SpaceID string
	// BatchID 是写入来源 ID 的前缀，内容摘要追加在后面（同一内容的重试复用同一 ID）。
	BatchID string
	Market  PlanMarket
	Dataset PlanDataset
	Period  time.Time
	// Symbols 把标的映射到交易所 symbol（provider_symbol 字段），与计划一致。
	Symbols func(subjectID string) string
	Bars    []marketdata.NormalizedKline
	Origin  string
}

// KlineCommitter 是 K 线写 Storage 的唯一边界：行构造、series_tag、source_id、写入 ID 都只在这里，
// 保证 SCF、stream、补采写出的行逐字节一致。
type KlineCommitter struct {
	Storage        Storage
	SpaceID        string
	MarketID       string
	InstrumentType marketdata.InstrumentType
	SeriesTag      string
	DatasetID      string
}

// NewKlineCommitter 按计划的市场构造提交器，行身份与 SCF 的 crypto 流水线相同。
func NewKlineCommitter(storage Storage, market PlanMarket) *KlineCommitter {
	instrument := marketdata.InstrumentType(strings.ToLower(strings.TrimSpace(market.InstrumentType)))
	return &KlineCommitter{Storage: storage, SpaceID: market.MarketID, MarketID: market.MarketID, InstrumentType: instrument}
}

// KlineCommitResult 是一次周期提交的结果。
type KlineCommitResult struct {
	Rows      int
	Missing   []string // 期望标的里没有 K 线的
	Unplanned int      // 不在该份期望标的里而被丢弃的 K 线
}

// CommitPeriod 用周期提交写一个数据集一个周期：请求按计划构造（与 Plan.BuildRequests 同一套目标、序号、hash、总数）。
func (c *KlineCommitter) CommitPeriod(ctx context.Context, batch KlineBatch) (KlineCommitResult, error) {
	req, rows, result, err := c.periodRows(batch)
	if err != nil {
		return result, err
	}
	if len(rows) == 0 {
		return result, nil
	}
	_, _, writeErr := c.writeDatasets(ctx, req, map[string][]*storagepb.RowFieldUpsert{batch.Dataset.DatasetID: rows})
	return result, writeErr
}

// periodRows 构造周期提交的请求与行；导出给测试比较 SCF 与 stream 写出的行。
func (c *KlineCommitter) periodRows(batch KlineBatch) (Request, []*storagepb.RowFieldUpsert, KlineCommitResult, error) {
	var result KlineCommitResult
	selected, ok := batch.Dataset.SelectCopy(batch.Period)
	if !ok {
		return Request{}, nil, result, fmt.Errorf("dataset %s has no copy effective at %s", batch.Dataset.DatasetID, batch.Period.UTC().Format(time.RFC3339))
	}
	dataset := batch.Dataset
	dataset.Copies = []PlanCopy{selected}
	plan := &Plan{V: 1, Market: batch.Market, Datasets: []PlanDataset{dataset}}
	frequency, err := marketdata.ParseFrequency(dataset.Frequency)
	if err != nil {
		return Request{}, nil, result, err
	}
	planned, ok, err := plan.buildFrequencyRequest(batch.SpaceID, batch.BatchID, batch.BatchID, dataset.Frequency, frequency, batch.Period.UTC())
	if err != nil || !ok {
		return Request{}, nil, result, errors.Join(err, errors.New("period request has no targets"))
	}
	req := planned.Request
	req.BatchID = batch.BatchID
	itemsBySubject := make(map[string]domain.CollectionItem, len(req.Items))
	for _, item := range req.Items {
		itemsBySubject[item.SubjectID] = item
	}
	seen := make(map[string]struct{}, len(batch.Bars))
	var rows []*storagepb.RowFieldUpsert
	for _, bar := range batch.Bars {
		subject := strings.ToUpper(strings.TrimSpace(bar.SubjectID))
		item, planned := itemsBySubject[subject]
		if !planned || !bar.BarStart.UTC().Equal(batch.Period.UTC()) {
			result.Unplanned++
			continue
		}
		if _, dup := seen[subject]; dup {
			continue
		}
		seen[subject] = struct{}{}
		if batch.Symbols != nil {
			bar.ProviderSymbol = batch.Symbols(subject)
		}
		if strings.TrimSpace(bar.RequestID) == "" {
			bar.RequestID = batch.BatchID
		}
		row, rowErr := c.rowFor(bar, req, requestKlineRouteID(&KlinePipeline{MarketID: c.MarketID, SpaceID: c.SpaceID, InstrumentType: c.InstrumentType}, req.Frequency), 1)
		if rowErr != nil {
			return req, nil, result, fmt.Errorf("subject %s: %w", subject, rowErr)
		}
		expanded, missing := expandTargetRows(req, item, []*storagepb.RowFieldUpsert{row})
		if len(missing) > 0 {
			return req, nil, result, fmt.Errorf("subject %s has no supported output fields", subject)
		}
		rows = append(rows, expanded...)
	}
	for subject := range itemsBySubject {
		if _, ok := seen[subject]; !ok {
			result.Missing = append(result.Missing, subject)
		}
	}
	sort.Strings(result.Missing)
	sortRows(rows)
	result.Rows = len(rows)
	return req, rows, result, nil
}

// Upsert 用普通写入写一批 K 线（补采）：不涉及周期状态。
func (c *KlineCommitter) Upsert(ctx context.Context, batch KlineBatch) error {
	req := Request{SpaceID: batch.SpaceID, DatasetID: batch.Dataset.DatasetID, Frequency: batch.Dataset.Frequency, BatchID: batch.BatchID, WriteIDByContent: true,
		MarketID: batch.Market.MarketID, InstrumentType: batch.Market.InstrumentType, MarketType: batch.Market.MarketType, Provider: batch.Market.Provider, SourceID: batch.Market.SourceID}
	var rows []*storagepb.RowFieldUpsert
	for _, bar := range batch.Bars {
		if batch.Symbols != nil {
			bar.ProviderSymbol = batch.Symbols(strings.ToUpper(strings.TrimSpace(bar.SubjectID)))
		}
		if strings.TrimSpace(bar.RequestID) == "" {
			bar.RequestID = batch.BatchID
		}
		row, err := c.rowFor(bar, req, requestKlineRouteID(&KlinePipeline{MarketID: c.MarketID, SpaceID: c.SpaceID, InstrumentType: c.InstrumentType}, req.Frequency), 1)
		if err != nil {
			return err
		}
		row.Fields = selectOutputFields(row.Fields, batch.Dataset.OutputFields)
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	sortRows(rows)
	_, _, err := c.writeDatasets(ctx, req, map[string][]*storagepb.RowFieldUpsert{batch.Dataset.DatasetID: rows})
	return err
}

func sortRows(rows []*storagepb.RowFieldUpsert) {
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i].GetKey().GetTimeSeries(), rows[j].GetKey().GetTimeSeries()
		if left.GetSubjectId() == right.GetSubjectId() {
			return left.GetDataTime() < right.GetDataTime()
		}
		return left.GetSubjectId() < right.GetSubjectId()
	})
}

// expandTargetRows 把一个取数项的行复制到它的各个写入目标（数据集），并按目标选择输出字段。
// 周期提交请求里某个目标一行都没有时，返回该目标的失败结果。
func expandTargetRows(req Request, item domain.CollectionItem, itemRows []*storagepb.RowFieldUpsert) ([]*storagepb.RowFieldUpsert, []domain.TargetResult) {
	if len(req.Targets) == 0 {
		return itemRows, nil
	}
	targets := req.Targets
	if item.InstanceID != "" {
		targets = make([]domain.WriteTarget, 0, len(req.Targets))
		for _, candidate := range req.Targets {
			if candidate.InstanceID == "" || candidate.InstanceID == item.InstanceID {
				targets = append(targets, candidate)
			}
		}
	}
	var rows []*storagepb.RowFieldUpsert
	var missingTargets []domain.TargetResult
	for _, target := range targets {
		targetRowCount := 0
		for _, sourceRow := range itemRows {
			row, ok := proto.Clone(sourceRow).(*storagepb.RowFieldUpsert)
			if !ok || row.GetKey() == nil {
				continue
			}
			row.GetKey().DatasetId = target.DatasetID
			fields := item.OutputFields
			if target.OutputFields != "" {
				var selected []string
				if json.Unmarshal([]byte(target.OutputFields), &selected) == nil {
					fields = selected
				}
			}
			row.Fields = selectOutputFields(row.Fields, fields)
			if len(row.Fields) > 0 {
				rows = append(rows, row)
				targetRowCount++
			}
		}
		if req.RequirePeriodCommit && targetRowCount == 0 {
			missingTargets = append(missingTargets, domain.TargetResult{WriteTargetID: target.ID, DatasetID: target.DatasetID, Status: "failed", ErrorSummary: "required period target has no supported output rows"})
		}
	}
	return rows, missingTargets
}

// writeDatasets 逐个数据集写入：周期提交请求走 CommitTimeSeriesBatch，其余走普通写入。一个数据集失败不影响其他数据集。
func (c *KlineCommitter) writeDatasets(ctx context.Context, req Request, targetRows map[string][]*storagepb.RowFieldUpsert) (map[string]domain.TargetResult, int, error) {
	var writeErr error
	writeSuccesses := 0
	targetStatus := make(map[string]domain.TargetResult)
	datasets := make([]string, 0, len(targetRows))
	for datasetID := range targetRows {
		datasets = append(datasets, datasetID)
	}
	sort.Strings(datasets)
	for _, datasetID := range datasets {
		datasetRows := targetRows[datasetID]
		var err error
		expectation, periodItems, periodEnabled, periodErr := periodCommitForDataset(req, datasetID, datasetRows)
		if periodErr != nil {
			err = periodErr
		} else if periodEnabled {
			storage, ok := c.Storage.(periodStorage)
			if !ok {
				err = fmt.Errorf("dataset %s requires CommitTimeSeriesBatch but storage client does not support period commits", datasetID)
			} else {
				err = storage.CommitTimeSeriesBatch(ctx, expectation, periodItems, writeEventID(req, datasetRows))
			}
		} else if storage, ok := c.Storage.(sourceStorage); ok {
			err = storage.UpsertFieldsWithSource(ctx, datasetRows, writeEventID(req, datasetRows))
		} else {
			err = c.Storage.UpsertFields(ctx, datasetRows)
		}
		if err != nil {
			if writeErr == nil {
				writeErr = fmt.Errorf("dataset %s: %w", datasetID, err)
			}
			targetStatus[datasetID] = domain.TargetResult{DatasetID: datasetID, Status: "failed", ErrorSummary: err.Error()}
		} else {
			writeSuccesses++
			targetStatus[datasetID] = domain.TargetResult{DatasetID: datasetID, Status: "succeeded"}
		}
	}
	return targetStatus, writeSuccesses, writeErr
}

func (p *KlineCommitter) rowFor(bar marketdata.NormalizedKline, req Request, routeID string, routeRank int, instrumentName ...string) (*storagepb.RowFieldUpsert, error) {
	if strings.TrimSpace(req.Frequency) != "" {
		requestedFrequency, err := marketdata.ParseFrequency(req.Frequency)
		if err != nil {
			return nil, fmt.Errorf("request frequency is invalid: %w", err)
		}
		barFrequency, err := marketdata.ParseFrequency(bar.Frequency)
		if err != nil || requestedFrequency != barFrequency {
			return nil, fmt.Errorf("provider frequency %q does not match request frequency %q", bar.Frequency, req.Frequency)
		}
		bar.Frequency = strings.TrimSpace(req.Frequency)
	}
	if (p.MarketID == StockCNSpaceID || req.SpaceID == StockCNSpaceID) &&
		(p.InstrumentType == "" || p.InstrumentType == marketdata.InstrumentEquity) {
		name := ""
		if len(instrumentName) > 0 {
			name = instrumentName[0]
		}
		return stockKlineRow(bar, firstNonEmptyString(p.DatasetID, req.DatasetID), routeID, routeRank, name)
	}
	if err := marketdata.ValidateNormalizedKline(bar); err != nil {
		return nil, err
	}
	if strings.EqualFold(firstNonEmptyString(p.MarketID, req.SpaceID), "crypto") {
		bar.SubjectID = strings.ToUpper(strings.TrimSpace(bar.SubjectID))
	}
	seriesTag := strings.TrimSpace(p.SeriesTag)
	if p.MarketID == StockCNSpaceID || req.SpaceID == StockCNSpaceID {
		seriesTag = "default"
	} else if seriesTag == "" {
		seriesTag = defaultMarketSeriesTag(bar.ProviderID, firstNonEmptyString(bar.SourceID, req.SourceID), req.MarketType)
	}
	fields := []*storagepb.FieldValue{
		doubleValue("open", bar.Open), doubleValue("high", bar.High), doubleValue("low", bar.Low), doubleValue("close", bar.Close),
		doubleValue("volume", bar.VolumeShares),
	}
	if strings.EqualFold(firstNonEmptyString(p.MarketID, req.SpaceID), "crypto") {
		fields = append(fields, doubleValue("quote_volume", bar.AmountCNY), intValue("trade_num", bar.TradeCount))
	} else {
		// 估算成交额（close × volume）不是真实成交额：显式写 NULL，也覆盖该行之前写入过的估算值。
		fields = append(fields, amountField(bar))
		if p.InstrumentType == marketdata.InstrumentIndex {
			fields = append(fields, stringValue("index_code", bar.ProviderSymbol))
		}
	}
	fields = append(fields,
		stringValue("provider_id", bar.ProviderID), stringValue("source_id", bar.SourceID),
		stringValue("provider_symbol", bar.ProviderSymbol),
	)
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{SpaceId: req.SpaceID, DatasetId: req.DatasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: bar.SubjectID, Freq: bar.Frequency, DataTime: bar.BarStart.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag,
		}}},
		Fields: fields,
	}, nil
}

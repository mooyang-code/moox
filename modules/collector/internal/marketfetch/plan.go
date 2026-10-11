package marketfetch

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
)

// PlanEnvName 是承载采集计划的 SCF 环境变量：gzip 后再 base64 的 JSON。
const PlanEnvName = "MOOX_MARKET_FETCH_PLAN"

const (
	maxPlanEncodedBytes = 64 << 10
	maxPlanJSONBytes    = 1 << 20
)

// Plan 是 Collector 下发给一个函数的采集计划。函数只认“市场 + 标的 + 频率”，
// 以及结果写到哪些数据集；不知道数据集的规则。
type Plan struct {
	V                int               `json:"v"`
	Market           PlanMarket        `json:"market"`
	Fetch            PlanFetch         `json:"fetch"`
	SymbolExceptions map[string]string `json:"symbol_exceptions,omitempty"`
	Datasets         []PlanDataset     `json:"datasets"`
}

// PlanMarket 描述这个函数服务的市场（现货与合约是不同市场）。
type PlanMarket struct {
	MarketID       string `json:"market_id"`
	InstrumentType string `json:"instrument_type"`
	Provider       string `json:"provider"`
	SourceID       string `json:"source_id"`
	MarketType     string `json:"market_type"`
}

// PlanFetch 是拉取参数，按数据源下发。
type PlanFetch struct {
	Concurrency      int `json:"concurrency"`
	RPS              int `json:"rps"`
	Burst            int `json:"burst"`
	FeedConcurrency  int `json:"feed_concurrency"`
	RequestTimeoutMS int `json:"request_timeout_ms"`
	StorageTimeoutMS int `json:"storage_timeout_ms,omitempty"`
	// StaggerMS 是函数内按函数名哈希的确定性错峰上限（毫秒）。
	StaggerMS int `json:"stagger_ms,omitempty"`
}

// PlanDataset 是一个结果数据集：每个数据集 1～2 份（生效起点、hash、总数、本函数标的与序号）。
type PlanDataset struct {
	DatasetID    string     `json:"dataset_id"`
	Frequency    string     `json:"frequency"`
	LatenessS    int        `json:"lateness_s"`
	OutputFields []string   `json:"output_fields,omitempty"`
	Copies       []PlanCopy `json:"copies"`
}

// PlanCopy 是一份完整的数据集快照视图。序号是该标的在整个数据集快照中的稠密序号。
type PlanCopy struct {
	From     int64         `json:"from"`
	Hash     string        `json:"hash"`
	Count    uint32        `json:"count"`
	Subjects []PlanSubject `json:"subjects"`
}

// PlanSubject 是函数负责的一个标的及其在数据集快照中的序号。
type PlanSubject struct {
	ID    string `json:"id"`
	Index uint32 `json:"i"`
}

// EncodePlan 把计划编码为环境变量值。
func EncodePlan(plan *Plan) (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := writer.Write(raw); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(buffer.Bytes())
	if len(encoded) > maxPlanEncodedBytes {
		return "", fmt.Errorf("encoded plan is %d bytes, limit %d", len(encoded), maxPlanEncodedBytes)
	}
	return encoded, nil
}

// DecodePlan 解码并校验环境变量值。
func DecodePlan(encoded string) (*Plan, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("market fetch plan is empty")
	}
	if len(encoded) > maxPlanEncodedBytes {
		return nil, errors.New("market fetch plan exceeds the encoded size limit")
	}
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("market fetch plan is not valid base64: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("market fetch plan is not valid gzip: %w", err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, maxPlanJSONBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read market fetch plan: %w", err)
	}
	if len(raw) > maxPlanJSONBytes {
		return nil, errors.New("market fetch plan exceeds the decoded size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return nil, fmt.Errorf("decode market fetch plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

// Validate 检查计划的结构与取值范围。
func (p *Plan) Validate() error {
	if p == nil {
		return errors.New("market fetch plan is required")
	}
	if p.V != 1 {
		return fmt.Errorf("unsupported market fetch plan version %d", p.V)
	}
	m := p.Market
	if strings.TrimSpace(m.Provider) == "" || strings.TrimSpace(m.SourceID) == "" || strings.TrimSpace(m.MarketType) == "" || strings.TrimSpace(m.MarketID) == "" {
		return errors.New("plan market requires market_id, provider, source_id and market_type")
	}
	f := p.Fetch
	if f.Concurrency < 1 || f.Concurrency > MaxConcurrency {
		return fmt.Errorf("plan fetch.concurrency must be between 1 and %d", MaxConcurrency)
	}
	if f.RPS < 0 || f.Burst < 0 || f.FeedConcurrency < 0 || f.RequestTimeoutMS < 0 || f.StorageTimeoutMS < 0 || f.StaggerMS < 0 || f.StaggerMS > 5000 {
		return errors.New("plan fetch limits are out of range")
	}
	if len(p.Datasets) == 0 {
		return errors.New("plan requires at least one dataset")
	}
	seen := make(map[string]struct{}, len(p.Datasets))
	for index, dataset := range p.Datasets {
		if strings.TrimSpace(dataset.DatasetID) == "" {
			return fmt.Errorf("plan datasets[%d].dataset_id is required", index)
		}
		frequency, err := marketdata.ParseFrequency(dataset.Frequency)
		if err != nil || string(frequency) != dataset.Frequency {
			return fmt.Errorf("plan datasets[%d].frequency must be canonical", index)
		}
		identity := dataset.DatasetID + "\x00" + dataset.Frequency
		if _, dup := seen[identity]; dup {
			return fmt.Errorf("plan datasets[%d] is duplicated", index)
		}
		seen[identity] = struct{}{}
		// 下限给提交与日志预留时间：拉取只能用到“截止 − 提交预留”之前。
		if dataset.LatenessS < 5 || dataset.LatenessS > 24*3600 {
			return fmt.Errorf("plan datasets[%d].lateness_s must be between 5 and 86400", index)
		}
		if len(dataset.Copies) == 0 || len(dataset.Copies) > 2 {
			return fmt.Errorf("plan datasets[%d] requires one or two copies", index)
		}
		var previous int64
		for copyIndex, copy := range dataset.Copies {
			if copy.From <= 0 || (copyIndex > 0 && copy.From <= previous) {
				return fmt.Errorf("plan datasets[%d].copies must have strictly increasing positive from", index)
			}
			previous = copy.From
			// 后一份允许没有标的：标的移到别的函数、或数据集不再采集时，从该份起本函数不再抓取。
			if len(copy.Subjects) > MaxPlanItems {
				return fmt.Errorf("plan datasets[%d].copies[%d] has more than %d subjects", index, copyIndex, MaxPlanItems)
			}
			if strings.TrimSpace(copy.Hash) == "" || copy.Count == 0 || (copyIndex == 0 && len(copy.Subjects) == 0) {
				return fmt.Errorf("plan datasets[%d].copies[%d] requires hash, count and subjects", index, copyIndex)
			}
			subjects := make(map[string]struct{}, len(copy.Subjects))
			for _, subject := range copy.Subjects {
				if strings.TrimSpace(subject.ID) == "" || subject.Index >= copy.Count {
					return fmt.Errorf("plan datasets[%d].copies[%d] has an invalid subject", index, copyIndex)
				}
				if _, dup := subjects[subject.ID]; dup {
					return fmt.Errorf("plan datasets[%d].copies[%d] has a duplicated subject", index, copyIndex)
				}
				subjects[subject.ID] = struct{}{}
			}
		}
	}
	return nil
}

// SelectCopy 返回目标周期应使用的那份：生效起点 ≤ 周期且起点最晚；没有则返回 false。
func (d PlanDataset) SelectCopy(period time.Time) (PlanCopy, bool) {
	var selected PlanCopy
	found := false
	for _, candidate := range d.Copies {
		if candidate.From <= period.Unix() {
			selected, found = candidate, true
		}
	}
	return selected, found
}

// PlannedRequest 是由计划与触发时间构造的一次执行。
type PlannedRequest struct {
	Request   Request
	Frequency string
	Period    time.Time
	// StorageDeadline 是 Storage 为该周期设定的截止时间（收盘 + 最小允许迟到时间）。
	StorageDeadline time.Time
}

// frequencyDue 判断触发时刻是否是该频率的 K 线边界，并返回刚收盘那根 K 线的开始时间。
func frequencyDue(frequency marketdata.Frequency, scheduled time.Time) (time.Time, bool) {
	if frequency.Duration() == 0 {
		// 月线：每月 1 日 00:00 UTC。
		if scheduled.Day() != 1 || scheduled.Hour() != 0 || scheduled.Minute() != 0 {
			return time.Time{}, false
		}
		return scheduled.AddDate(0, -1, 0), true
	}
	step := int64(frequency.Duration() / time.Second)
	if step <= 0 || scheduled.Unix()%step != 0 {
		return time.Time{}, false
	}
	return scheduled.Add(-frequency.Duration()), true
}

// ExchangeSymbol 返回标的的交易所 symbol：例外表优先，否则 crypto 去掉连字符。
func (p *Plan) ExchangeSymbol(subjectID string) string {
	if symbol, ok := p.SymbolExceptions[subjectID]; ok {
		return symbol
	}
	return strings.ReplaceAll(strings.TrimSpace(subjectID), "-", "")
}

// PlanSeriesTag 返回写入行使用的 series_tag；Collector 登记快照时必须使用同一个函数。
func PlanSeriesTag(spaceID string, market PlanMarket) string {
	return collectionItemSeriesTag(spaceID, domain.CollectionItem{Provider: market.Provider, SourceID: market.SourceID, MarketType: market.MarketType})
}

// BuildRequests 按触发时间选出到期的频率与各数据集生效的那份，构造每个频率一次的采集请求。
func (p *Plan) BuildRequests(spaceID, functionName, requestID string, scheduled time.Time, dnsRoutes map[string]sources.DNSResolution) ([]PlannedRequest, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" || strings.TrimSpace(functionName) == "" {
		return nil, errors.New("space and function identity are required")
	}
	scheduled = scheduled.UTC().Truncate(time.Minute)
	frequencies := make([]string, 0, len(p.Datasets))
	seenFrequency := make(map[string]struct{}, len(p.Datasets))
	for _, dataset := range p.Datasets {
		if _, ok := seenFrequency[dataset.Frequency]; !ok {
			seenFrequency[dataset.Frequency] = struct{}{}
			frequencies = append(frequencies, dataset.Frequency)
		}
	}
	sort.Strings(frequencies)
	var requests []PlannedRequest
	for _, frequencyName := range frequencies {
		frequency, err := marketdata.ParseFrequency(frequencyName)
		if err != nil {
			return nil, err
		}
		period, due := frequencyDue(frequency, scheduled)
		if !due {
			continue
		}
		planned, ok, err := p.buildFrequencyRequest(spaceID, functionName, requestID, frequencyName, frequency, period)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		planned.Request.DNSRoutes = dnsRoutes
		requests = append(requests, planned)
	}
	return requests, nil
}

func (p *Plan) buildFrequencyRequest(spaceID, functionName, requestID, frequencyName string, frequency marketdata.Frequency, period time.Time) (PlannedRequest, bool, error) {
	periodText := period.UTC().Format(time.RFC3339Nano)
	var targets []domain.WriteTarget
	subjects := make(map[string]struct{})
	lateness := 0
	firstDataset := ""
	for _, dataset := range p.Datasets {
		if dataset.Frequency != frequencyName {
			continue
		}
		selected, ok := dataset.SelectCopy(period)
		if !ok {
			continue
		}
		if firstDataset == "" {
			firstDataset = dataset.DatasetID
		}
		if lateness == 0 || dataset.LatenessS < lateness {
			lateness = dataset.LatenessS
		}
		outputFields := ""
		if len(dataset.OutputFields) > 0 {
			raw, err := json.Marshal(dataset.OutputFields)
			if err != nil {
				return PlannedRequest{}, false, err
			}
			outputFields = string(raw)
		}
		for _, subject := range selected.Subjects {
			subjects[subject.ID] = struct{}{}
			instanceID := planInstanceID(frequencyName, subject.ID)
			targets = append(targets, domain.WriteTarget{
				ID: dataset.DatasetID + "|" + frequencyName + "|" + subject.ID, SpaceID: spaceID, InstanceID: instanceID, TaskID: dataset.DatasetID,
				DatasetID: dataset.DatasetID, OutputFields: outputFields, SeriesIndex: subject.Index, SeriesHash: selected.Hash, ExpectedCount: selected.Count,
				Frequency: frequencyName, TargetDataTime: periodText,
			})
		}
	}
	if len(targets) == 0 {
		return PlannedRequest{}, false, nil
	}
	ids := make([]string, 0, len(subjects))
	for id := range subjects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]domain.CollectionItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, domain.CollectionItem{
			InstanceID: planInstanceID(frequencyName, id), SubjectID: id, Symbol: p.ExchangeSymbol(id), TargetDataTime: periodText, BarLimit: 1,
			RequirePeriodCommit: true, Provider: p.Market.Provider, SourceID: p.Market.SourceID, MarketID: p.Market.MarketID,
			InstrumentType: p.Market.InstrumentType, MarketType: p.Market.MarketType, DataType: "kline", DatasetID: firstDataset, Frequency: frequencyName,
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	request := Request{
		BatchID: fmt.Sprintf("%s|%s|%d", functionName, frequencyName, period.Unix()), BatchKind: domain.BatchKindRealtime, SpaceID: spaceID,
		MarketID: p.Market.MarketID, InstrumentType: p.Market.InstrumentType, DatasetID: firstDataset, Frequency: frequencyName,
		Provider: p.Market.Provider, SourceID: p.Market.SourceID, MarketType: p.Market.MarketType, NodeID: functionName, FunctionName: functionName,
		RequestID: requestID, RequirePeriodCommit: true, WriteIDByContent: true, Concurrency: p.Fetch.Concurrency, Items: items, Targets: targets,
	}
	bindRequestTargetPeriodIdentity(&request)
	deadline := frequency.BarEnd(period.UTC()).Add(time.Duration(lateness) * time.Second)
	return PlannedRequest{Request: request, Frequency: frequencyName, Period: period.UTC(), StorageDeadline: deadline}, true, nil
}

func planInstanceID(frequency, subjectID string) string { return frequency + "|" + subjectID }

// StaggerDelay 返回按函数名哈希得到的确定性错峰延迟。
func (p *Plan) StaggerDelay(functionName string) time.Duration {
	if p.Fetch.StaggerMS <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(functionName))
	return time.Duration(int(sum[0])<<8|int(sum[1])) % time.Duration(p.Fetch.StaggerMS+1) * time.Millisecond
}

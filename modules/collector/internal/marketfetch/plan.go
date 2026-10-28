package marketfetch

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/packages/wirecompress"
)

// PlanEnvName 是承载采集计划的 SCF 环境变量：zstd 字典压缩后再 base64 的 JSON。
const PlanEnvName = "MOOX_MARKET_FETCH_PLAN"

const (
	maxPlanEncodedBytes = 64 << 10
	maxPlanJSONBytes    = 1 << 20
)

// Plan 是 Collector 下发给一个函数的采集计划。函数只认“市场 + 标的 + 频率”，
// 以及结果写到哪些数据集；不知道数据集的规则。
type Plan struct {
	V int `json:"v"`
	// Executor 是计划的执行端：空表示 SCF 函数，stream 是 WS 推送，local 是本地执行端（见 PlanExecutor*）。
	Executor         string            `json:"executor,omitempty"`
	Market           PlanMarket        `json:"market"`
	Fetch            PlanFetch         `json:"fetch"`
	SymbolExceptions map[string]string `json:"symbol_exceptions,omitempty"`
	// Pairs 是交易对与交易所 symbol 不同的标的（币安永续合约的 pair）：WS 按 pair 订阅、按 symbol 写入。只出现在推送执行端的计划里。
	Pairs    map[string]string `json:"pairs,omitempty"`
	Datasets []PlanDataset     `json:"datasets"`
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
	// StaggerBaseMS 是错峰的固定起点；StaggerMS 是在起点之后按函数名哈希增加的确定性延迟上限（毫秒）。
	StaggerBaseMS int `json:"stagger_base_ms,omitempty"`
	StaggerMS     int `json:"stagger_ms,omitempty"`
	// StandbyMS 大于 0 时启用备援请求（行情请求超过这么多毫秒还没有结果，就并发再发一个备援请求，先成功的为准），0 表示关闭。
	// StandbyHourOnly 为真时只在整点那一分钟触发的调用里启用。
	StandbyMS       int  `json:"standby_ms,omitempty"`
	StandbyHourOnly bool `json:"standby_hour_only,omitempty"`
	// ConnReuse 为真时，函数进程在多次调用之间复用到行情源的 HTTP 连接（上一分钟留下的空闲连接下一分钟直接用）。
	ConnReuse bool `json:"conn_reuse,omitempty"`
	// NotBeforeMS 是相对收盘的最早取数时刻（毫秒）：错峰等待之后还要等到收盘 + NotBeforeMS 才取数，0 表示不限制。
	NotBeforeMS int `json:"not_before_ms,omitempty"`
}

// FeedLimits 是一次取数的限流与超时（计划的 fetch 段），零值字段用数据源默认或进程环境。
type FeedLimits struct {
	RPS              int `json:"rps,omitempty"`
	Burst            int `json:"burst,omitempty"`
	FeedConcurrency  int `json:"feed_concurrency,omitempty"`
	RequestTimeoutMS int `json:"request_timeout_ms,omitempty"`
	StorageTimeoutMS int `json:"storage_timeout_ms,omitempty"`
}

// Limits 返回计划的限流与超时。
func (f PlanFetch) Limits() *FeedLimits {
	return &FeedLimits{RPS: f.RPS, Burst: f.Burst, FeedConcurrency: f.FeedConcurrency, RequestTimeoutMS: f.RequestTimeoutMS, StorageTimeoutMS: f.StorageTimeoutMS}
}

// MinNotBeforeMS、MaxNotBeforeMS 是最早取数时刻的取值范围：小于 1 秒没有意义，超过 12 秒就挤占了提交与重试的时间。
const (
	MinNotBeforeMS = 1000
	MaxNotBeforeMS = 12000
)

// 计划执行端。SCF 函数的计划不写 executor（函数按未知字段拒绝解码），推送与本地执行端的计划写明自己的类型。
const (
	PlanExecutorSCF       = "scf"
	PlanExecutorStreaming = "stream"
	PlanExecutorLocal     = "local"
)

// MinStandbyMS 是备援延迟的下限：正常请求的首字节在几十到几百毫秒，延迟太短会让几乎所有请求都发出备援。
const MinStandbyMS = 200

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

// planDecoder 解压环境变量里的计划：1MB 与计划 JSON 的解码上限一致，在解压过程中生效。
var planDecoder = wirecompress.NewDecoder(wirecompress.UsePlan, maxPlanJSONBytes)

// EncodePlan 把计划编码为环境变量值：JSON 经 zstd 字典压缩（空间有专用字典就用，否则用默认字典）再 base64。
func EncodePlan(spaceID string, plan *Plan) (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(wirecompress.Encode(wirecompress.UsePlan, spaceID, raw))
	if len(encoded) > maxPlanEncodedBytes {
		return "", fmt.Errorf("encoded plan is %d bytes, limit %d", len(encoded), maxPlanEncodedBytes)
	}
	return encoded, nil
}

// DecodePlan 解码并校验环境变量值。解码不需要空间：帧头里的字典 ID 决定用哪本字典。
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
	raw, err := planDecoder.Decode(compressed)
	if err != nil {
		if errors.Is(err, wirecompress.ErrTooLarge) {
			return nil, errors.New("market fetch plan exceeds the decoded size limit")
		}
		return nil, fmt.Errorf("market fetch plan is not valid zstd: %w", err)
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
	if f.RPS < 0 || f.Burst < 0 || f.FeedConcurrency < 0 || f.RequestTimeoutMS < 0 || f.StorageTimeoutMS < 0 || f.StaggerMS < 0 || f.StaggerMS > 60000 || f.StaggerBaseMS < 0 || f.StaggerBaseMS > 60000 {
		return errors.New("plan fetch limits are out of range")
	}
	if f.StandbyMS != 0 && (f.StandbyMS < MinStandbyMS || f.StandbyMS > MaxStandbyMS) {
		return fmt.Errorf("plan fetch.standby_ms must be 0 or between %d and %d", MinStandbyMS, MaxStandbyMS)
	}
	if f.NotBeforeMS != 0 && (f.NotBeforeMS < MinNotBeforeMS || f.NotBeforeMS > MaxNotBeforeMS) {
		return fmt.Errorf("plan fetch.not_before_ms must be 0 or between %d and %d", MinNotBeforeMS, MaxNotBeforeMS)
	}
	if len(p.Datasets) == 0 {
		return errors.New("plan requires at least one dataset")
	}
	maxItems := MaxPlanItems
	switch p.Executor {
	case "":
	case PlanExecutorStreaming, PlanExecutorLocal:
		maxItems = MaxStreamPlanItems
	default:
		return fmt.Errorf("unsupported plan executor %q", p.Executor)
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
			if len(copy.Subjects) > maxItems {
				return fmt.Errorf("plan datasets[%d].copies[%d] has more than %d subjects", index, copyIndex, maxItems)
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
	// 用 Truncate（以零时刻为基准）对齐，周线落在周一，与 Binance 周线及 alignDown 一致；Unix 取模会落在周四。
	step := frequency.Duration()
	if step <= 0 || !scheduled.Truncate(step).Equal(scheduled) {
		return time.Time{}, false
	}
	return scheduled.Add(-step), true
}

// ExchangeSymbol 返回标的的交易所 symbol：例外表优先，否则 crypto 去掉连字符。
func (p *Plan) ExchangeSymbol(subjectID string) string {
	if symbol, ok := p.SymbolExceptions[subjectID]; ok {
		return symbol
	}
	return strings.ReplaceAll(strings.TrimSpace(subjectID), "-", "")
}

// Pair 返回标的的交易所交易对：没有单独登记时与交易所 symbol 相同。
func (p *Plan) Pair(subjectID string) string {
	if pair, ok := p.Pairs[subjectID]; ok && pair != "" {
		return pair
	}
	return p.ExchangeSymbol(subjectID)
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
		RequestID: requestID, RequirePeriodCommit: true, PlanRequest: true, Concurrency: p.Fetch.Concurrency, Items: items, Targets: targets,
	}
	bindRequestTargetPeriodIdentity(&request)
	deadline := frequency.BarEnd(period.UTC()).Add(time.Duration(lateness) * time.Second)
	return PlannedRequest{Request: request, Frequency: frequencyName, Period: period.UTC(), StorageDeadline: deadline}, true, nil
}

func planInstanceID(frequency, subjectID string) string { return frequency + "|" + subjectID }

// StaggerDelay 返回按函数名哈希得到的确定性错峰延迟。
func (p *Plan) StaggerDelay(functionName string) time.Duration {
	base := time.Duration(p.Fetch.StaggerBaseMS) * time.Millisecond
	if p.Fetch.StaggerMS <= 0 {
		return base
	}
	sum := sha256.Sum256([]byte(functionName))
	return base + time.Duration(int(sum[0])<<8|int(sum[1]))%time.Duration(p.Fetch.StaggerMS+1)*time.Millisecond
}

// StartDelay 返回从函数入口 entry 起到开始取数的等待时间：先错峰，再保证不早于收盘（scheduled）+ NotBeforeMS。
// 定时器本身会晚到，所以“收盘后多少秒”必须按 scheduled 而不是入口时间算。
func (p *Plan) StartDelay(functionName string, entry, scheduled time.Time) time.Duration {
	delay := p.StaggerDelay(functionName)
	if p.Fetch.NotBeforeMS > 0 {
		if wait := scheduled.Add(time.Duration(p.Fetch.NotBeforeMS) * time.Millisecond).Sub(entry); wait > delay {
			delay = wait
		}
	}
	return max(delay, 0)
}

// StandbyFor 返回这次调用使用的备援延迟（毫秒），0 表示不启用。
func (p *Plan) StandbyFor(scheduled time.Time) int {
	if p == nil || p.Fetch.StandbyMS <= 0 {
		return 0
	}
	if p.Fetch.StandbyHourOnly && scheduled.UTC().Minute() != 0 {
		return 0
	}
	return p.Fetch.StandbyMS
}

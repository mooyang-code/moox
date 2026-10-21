package marketfetch

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// DefaultSymbolsPerFunction 是每个定时函数默认负责的标的数（现货与合约相同）。
const DefaultSymbolsPerFunction = 50

// PlanNetwork 是某地域的 SCF 网络：函数绑定该地域内的 VPC，通过私网访问同地域的 Access。
type PlanNetwork struct {
	VPCID    string `yaml:"vpc_id" json:"vpc_id"`
	SubnetID string `yaml:"subnet_id" json:"subnet_id"`
}

// PlanOptions 是计算期望状态所需的配置。
type PlanOptions struct {
	SpaceID string
	// Markets 是每个市场类型使用的地域白名单（市场类型小写，如 spot、swap）。
	Markets            map[string][]string
	SymbolsPerFunction int
	Fetch              PlanFetch
	// MarketFetch 是按市场类型（小写）覆盖的拉取参数，没有的市场用 Fetch。
	MarketFetch map[string]PlanFetch
	// DefaultLatenessS 是任务没有指定允许迟到时间时的空间默认值；MarketLatenessS 按市场类型覆盖（拉取模式）。
	DefaultLatenessS int
	MarketLatenessS  map[string]int
	// Modes 是各数据集（按数据集 ID）的采集方式；StreamLatenessS 是推送方式按频率的截止默认值。
	Modes           map[string]DatasetMode
	StreamLatenessS map[string]int
	// AssumedEgressIPs 是函数出口 IP 个数的估计值，只用于频控估算（保守默认 1）。
	AssumedEgressIPs int
}

// PlanDatasetState 是一个结果数据集在某一时刻的状态：标的集合 hash 与总数等。
type PlanDatasetState struct {
	DatasetID    string   `json:"dataset_id"`
	Frequency    string   `json:"frequency"`
	Market       string   `json:"market"`
	Hash         string   `json:"hash"`
	Count        uint32   `json:"count"`
	LatenessS    int      `json:"lateness_s"`
	OutputFields []string `json:"output_fields,omitempty"`
	// Ingest 是数据集所在的执行端（scf、stream、local），空表示 scf。
	Ingest string `json:"ingest,omitempty"`
}

// IngestOrSCF 返回数据集的执行端，空视为 scf。
func (s PlanDatasetState) IngestOrSCF() string {
	if s.Ingest == "" {
		return PlanExecutorSCF
	}
	return s.Ingest
}

// DesiredDataset 是期望状态里的一个数据集，带完整标的序列（规范顺序，序号即下标）。
type DesiredDataset struct {
	PlanDatasetState
	Series []seriesset.Series
	index  map[string]uint32
}

// PlanFunction 是一个定时函数的状态：所在地域、服务的市场、cron 与计划。
type PlanFunction struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Market string `json:"market"`
	// Executor 为空表示 SCF 函数；stream、local 是推送与本地执行端（没有地域与 cron）。
	Executor string `json:"executor,omitempty"`
	Cron     string `json:"cron"`
	Plan     *Plan  `json:"plan"`
}

// DesiredState 是由任务与标签成员算出的期望状态。
type DesiredState struct {
	Datasets  map[string]*DesiredDataset
	Functions map[string]*PlanFunction
}

func planDatasetKey(datasetID, frequency string) string { return datasetID + "|" + frequency }

func planMarketKey(group TaskGroup) string {
	return strings.Join([]string{group.Provider, group.SourceID, group.MarketType, group.MarketID, group.InstrumentType}, "/")
}

var planNameSanitizer = strings.NewReplacer("_", "", " ", "", ".", "")

// PlanFunctionName 返回第 index 个函数的名称，名称含 PlanFleetMarker。
func PlanFunctionName(spaceID, marketType, region string, index int) string {
	region = strings.TrimPrefix(strings.ToLower(region), "ap-")
	return fmt.Sprintf("moox-fetch%s%s-%s-%s-%03d", PlanFleetMarker, planNameSanitizer.Replace(strings.ToLower(spaceID)),
		planNameSanitizer.Replace(strings.ToLower(marketType)), planNameSanitizer.Replace(region), index)
}

// BuildDesiredState 由合并后的任务组算出期望状态。previous 是已生效状态里的各函数，
// 用于让标的尽量留在原来的函数上，减少不必要的环境变更。
// 每个数据集按采集方式分给执行端：拉取 + SCF 的数据集按每函数标的数分到 SCF 函数；推送的数据集与拉取 + 本地执行端的数据集
// 各自每个市场一项（stream-<空间>-<市场>、local-<空间>-<市场>），包含该市场这类数据集的全部标的。
func BuildDesiredState(opts PlanOptions, groups []TaskGroup, previous map[string]*PlanFunction) (*DesiredState, error) {
	spaceID := strings.ToLower(strings.TrimSpace(opts.SpaceID))
	if spaceID == "" {
		return nil, errors.New("space_id is required")
	}
	defaultSize := opts.SymbolsPerFunction
	if defaultSize <= 0 {
		defaultSize = DefaultSymbolsPerFunction
	}
	state := &DesiredState{Datasets: map[string]*DesiredDataset{}, Functions: map[string]*PlanFunction{}}
	type market struct {
		key    string
		sample TaskGroup
		size   int
		// 按执行端（scf、stream、local）分开的标的全集与数据集。
		subjects   map[string]map[string]struct{}
		datasetIDs map[string][]string
		external   map[string]string
		pairs      map[string]string
	}
	markets := map[string]*market{}
	subjectSets := map[string]map[string]struct{}{}
	for _, group := range groups {
		if strings.TrimSpace(group.DatasetID) == "" || strings.TrimSpace(group.Frequency) == "" {
			return nil, errors.New("task group requires dataset_id and frequency")
		}
		frequency, err := marketdata.ParseFrequency(group.Frequency)
		if err != nil || string(frequency) != group.Frequency {
			return nil, fmt.Errorf("task group %s frequency %q is not canonical", group.DatasetID, group.Frequency)
		}
		key := planMarketKey(group)
		info := markets[key]
		if info == nil {
			info = &market{key: key, sample: group, subjects: map[string]map[string]struct{}{}, datasetIDs: map[string][]string{}, external: map[string]string{}, pairs: map[string]string{}}
			markets[key] = info
		}
		info.size = minPositive(info.size, group.SymbolsPerFunction)
		mode := opts.modeOf(group)
		executor := mode.PlanExecutor()
		if executor == PlanExecutorStream && !marketdata.SupportsStream(frequency) {
			return nil, fmt.Errorf("dataset %s %s cannot be collected by stream", group.DatasetID, group.Frequency)
		}
		datasetKey := planDatasetKey(group.DatasetID, group.Frequency)
		dataset := state.Datasets[datasetKey]
		if dataset == nil {
			dataset = &DesiredDataset{PlanDatasetState: PlanDatasetState{DatasetID: group.DatasetID, Frequency: group.Frequency, Market: key,
				LatenessS: opts.latenessFor(group, mode), OutputFields: normalizeOutputFields(group.OutputFields), Ingest: executor}}
			state.Datasets[datasetKey] = dataset
			subjectSets[datasetKey] = map[string]struct{}{}
			info.datasetIDs[executor] = append(info.datasetIDs[executor], datasetKey)
		} else if dataset.Market != key {
			return nil, fmt.Errorf("dataset %s %s is collected from more than one market", group.DatasetID, group.Frequency)
		}
		if info.subjects[executor] == nil {
			info.subjects[executor] = map[string]struct{}{}
		}
		for _, subject := range group.Subjects {
			subject = strings.ToUpper(strings.TrimSpace(subject))
			if subject == "" {
				continue
			}
			subjectSets[datasetKey][subject] = struct{}{}
			info.subjects[executor][subject] = struct{}{}
			if external := strings.TrimSpace(group.ExternalSymbols[subject]); external != "" {
				info.external[subject] = external
			}
			if pair := strings.TrimSpace(group.Pairs[subject]); pair != "" {
				info.pairs[subject] = pair
			}
		}
	}
	marketKeys := make([]string, 0, len(markets))
	marketTypes := map[string]string{}
	for key, info := range markets {
		// 函数名只含空间、市场类型与地域；同一空间里市场类型相同的不同数据源会让函数名冲突。
		marketType := strings.ToLower(info.sample.MarketType)
		if other, clash := marketTypes[marketType]; clash {
			return nil, fmt.Errorf("market type %s is used by more than one market (%s and %s)", marketType, other, key)
		}
		marketTypes[marketType] = key
		marketKeys = append(marketKeys, key)
	}
	sort.Strings(marketKeys)

	// 先算每个数据集的规范序列、序号与 hash。
	for datasetKey, dataset := range state.Datasets {
		info := markets[dataset.Market]
		tag := PlanSeriesTag(spaceID, PlanMarket{Provider: info.sample.Provider, SourceID: info.sample.SourceID, MarketType: info.sample.MarketType})
		series := make([]seriesset.Series, 0, len(subjectSets[datasetKey]))
		for subject := range subjectSets[datasetKey] {
			series = append(series, seriesset.Series{Provider: info.sample.Provider, SourceID: info.sample.SourceID, MarketType: info.sample.MarketType, SubjectID: subject, SeriesTag: tag})
		}
		if len(series) == 0 {
			delete(state.Datasets, datasetKey)
			continue
		}
		set, err := seriesset.Canonicalize(series)
		if err != nil {
			return nil, fmt.Errorf("dataset %s %s: %w", dataset.DatasetID, dataset.Frequency, err)
		}
		dataset.Series, dataset.Hash, dataset.Count = set.Series, set.Hash, uint32(len(set.Series))
		dataset.index = make(map[string]uint32, len(set.Series))
		for i, item := range set.Series {
			dataset.index[item.SubjectID] = uint32(i)
		}
	}

	for _, key := range marketKeys {
		info := markets[key]
		// newPlan 构造一项计划：subjects 是这一项负责的标的，datasetKeys 是它服务的数据集。
		newPlan := func(executor string, subjects []string, datasetKeys []string) *Plan {
			plan := &Plan{V: 1, Fetch: opts.fetchFor(info.sample.MarketType), Market: PlanMarket{
				MarketID: info.sample.MarketID, InstrumentType: info.sample.InstrumentType, Provider: info.sample.Provider,
				SourceID: info.sample.SourceID, MarketType: info.sample.MarketType,
			}}
			if executor != PlanExecutorSCF {
				plan.Executor = executor
			}
			for _, subject := range subjects {
				derived := strings.ReplaceAll(subject, "-", "")
				if external := info.external[subject]; external != "" && external != derived {
					if plan.SymbolExceptions == nil {
						plan.SymbolExceptions = map[string]string{}
					}
					plan.SymbolExceptions[subject] = external
				}
				// 交易对只下发给推送：SCF 函数按未知字段拒绝解码计划。
				if pair := info.pairs[subject]; executor == PlanExecutorStream && pair != "" && pair != plan.ExchangeSymbol(subject) {
					if plan.Pairs == nil {
						plan.Pairs = map[string]string{}
					}
					plan.Pairs[subject] = pair
				}
			}
			for _, datasetKey := range datasetKeys {
				dataset := state.Datasets[datasetKey]
				if dataset == nil {
					continue
				}
				items := make([]PlanSubject, 0, len(subjects))
				for _, subject := range subjects {
					if index, ok := dataset.index[subject]; ok {
						items = append(items, PlanSubject{ID: subject, Index: index})
					}
				}
				if len(items) == 0 {
					continue
				}
				plan.Datasets = append(plan.Datasets, PlanDataset{
					DatasetID: dataset.DatasetID, Frequency: dataset.Frequency, LatenessS: dataset.LatenessS, OutputFields: dataset.OutputFields,
					Copies: []PlanCopy{{Hash: dataset.Hash, Count: dataset.Count, Subjects: items}},
				})
			}
			return plan
		}
		for _, executor := range []string{PlanExecutorSCF, PlanExecutorStream, PlanExecutorLocal} {
			universe := make([]string, 0, len(info.subjects[executor]))
			for subject := range info.subjects[executor] {
				universe = append(universe, subject)
			}
			sort.Strings(universe)
			if len(universe) == 0 {
				continue
			}
			datasetKeys := append([]string(nil), info.datasetIDs[executor]...)
			sort.Strings(datasetKeys)
			if executor != PlanExecutorSCF {
				name := PlanItemName(executor, spaceID, info.sample.MarketType)
				plan := newPlan(executor, universe, datasetKeys)
				if len(plan.Datasets) > 0 {
					state.Functions[name] = &PlanFunction{Name: name, Market: key, Executor: executor, Plan: plan}
				}
				continue
			}
			regions := opts.Markets[strings.ToLower(info.sample.MarketType)]
			if len(regions) == 0 {
				return nil, fmt.Errorf("no SCF region is configured for market %s in space %s", info.sample.MarketType, spaceID)
			}
			size := info.size
			if size <= 0 {
				size = defaultSize
			}
			fleet := (len(universe) + size - 1) / size
			names := make([]string, fleet)
			regionOf := make(map[string]string, fleet)
			for i := range names {
				region := regions[i%len(regions)]
				names[i] = PlanFunctionName(spaceID, info.sample.MarketType, region, i)
				regionOf[names[i]] = region
			}
			assigned := stableAssign(universe, names, size, previousAssignment(previous, key))
			bySubject := map[string][]string{}
			for subject, name := range assigned {
				bySubject[name] = append(bySubject[name], subject)
			}
			for _, name := range names {
				subjects := bySubject[name]
				if len(subjects) == 0 {
					continue
				}
				sort.Strings(subjects)
				plan := newPlan(executor, subjects, datasetKeys)
				state.Functions[name] = &PlanFunction{Name: name, Region: regionOf[name], Market: key, Cron: planCron(plan), Plan: plan}
			}
		}
	}
	return state, nil
}

// PlanItemName 返回推送或本地执行端在计划里的项名，例如 stream-crypto-swap。
func PlanItemName(executor, spaceID, marketType string) string {
	return fmt.Sprintf("%s-%s-%s", executor, planNameSanitizer.Replace(strings.ToLower(spaceID)), planNameSanitizer.Replace(strings.ToLower(marketType)))
}

// 采集方式。
const (
	IngestStream = "stream"
	IngestPull   = "pull"
)

// DatasetMode 是一个数据集的采集方式与拉取、补采执行端。
type DatasetMode struct {
	Mode     string
	Executor string
}

// PlanExecutor 返回该方式在计划里的执行端：推送是 stream，拉取按执行端是 scf 或 local。
func (m DatasetMode) PlanExecutor() string {
	switch {
	case m.Mode == IngestStream:
		return PlanExecutorStream
	case m.Executor == PlanExecutorLocal:
		return PlanExecutorLocal
	default:
		return PlanExecutorSCF
	}
}

// modeOf 返回任务组的采集方式：库里该数据集的方式（初值见 PlanReconciler.datasetModes），没有时按拉取 + SCF。
func (o PlanOptions) modeOf(group TaskGroup) DatasetMode {
	if mode, ok := o.Modes[group.DatasetID]; ok {
		return mode
	}
	return DatasetMode{Mode: IngestPull, Executor: PlanExecutorSCF}
}

// latenessFor 是数据集截止时间的唯一计算入口：任务值 → 当前采集方式的默认值（推送按频率 1m 5 秒、1h 8 秒；
// 拉取按市场覆盖或空间默认，crypto 现货 15 秒、合约 20 秒）。计划内容与 Storage 属性都只用它。
func (o PlanOptions) latenessFor(group TaskGroup, mode DatasetMode) int {
	if group.AllowedLatenessSeconds > 0 {
		return group.AllowedLatenessSeconds
	}
	lateness := 0
	if mode.PlanExecutor() == PlanExecutorStream {
		lateness = o.StreamLatenessS[group.Frequency]
	} else {
		lateness = o.MarketLatenessS[strings.ToLower(group.MarketType)]
	}
	if lateness <= 0 {
		lateness = o.DefaultLatenessS
	}
	if lateness <= 0 {
		lateness = 60
	}
	return lateness
}

// fetchFor 返回某市场类型的拉取参数：有按市场的覆盖就用覆盖，否则用空间值。
func (o PlanOptions) fetchFor(marketType string) PlanFetch {
	if fetch, ok := o.MarketFetch[strings.ToLower(marketType)]; ok {
		return fetch
	}
	return o.Fetch
}

// previousAssignment 从已生效的函数还原“标的 → 函数名”。
func previousAssignment(previous map[string]*PlanFunction, marketKey string) map[string]string {
	result := map[string]string{}
	for name, function := range previous {
		if function == nil || function.Plan == nil || function.Market != marketKey || function.Executor != "" {
			continue
		}
		for _, dataset := range function.Plan.Datasets {
			if len(dataset.Copies) == 0 {
				continue
			}
			for _, subject := range dataset.Copies[len(dataset.Copies)-1].Subjects {
				result[subject.ID] = name
			}
		}
	}
	return result
}

// stableAssign 把标的分配到函数：先让标的留在原函数（未超容量），其余按顺序填入有空位的函数。
func stableAssign(universe, names []string, capacity int, previous map[string]string) map[string]string {
	valid := make(map[string]struct{}, len(names))
	for _, name := range names {
		valid[name] = struct{}{}
	}
	load := make(map[string]int, len(names))
	assigned := make(map[string]string, len(universe))
	for _, subject := range universe {
		if name, ok := previous[subject]; ok {
			if _, exists := valid[name]; exists && load[name] < capacity {
				assigned[subject] = name
				load[name]++
			}
		}
	}
	for _, subject := range universe {
		if _, done := assigned[subject]; done {
			continue
		}
		for _, name := range names {
			if load[name] < capacity {
				assigned[subject] = name
				load[name]++
				break
			}
		}
	}
	return assigned
}

// planCron 按函数服务的最小频率生成 cron（秒在最前，整数秒）；没有任何活跃标的返回空串，表示停用定时器。
func planCron(plan *Plan) string {
	var minStep time.Duration
	for _, dataset := range plan.Datasets {
		active := false
		for _, copy := range dataset.Copies {
			if len(copy.Subjects) > 0 {
				active = true
			}
		}
		if !active {
			continue
		}
		frequency, err := marketdata.ParseFrequency(dataset.Frequency)
		if err != nil {
			continue
		}
		step := frequency.Duration()
		if step <= 0 {
			step = 24 * time.Hour
		}
		if minStep == 0 || step < minStep {
			minStep = step
		}
	}
	switch {
	case minStep <= 0:
		return ""
	case minStep < time.Hour:
		minutes := int(minStep / time.Minute)
		if minutes <= 1 || 60%minutes != 0 {
			return "0 * * * * * *"
		}
		return fmt.Sprintf("0 */%d * * * * *", minutes)
	default:
		return "0 0 * * * * *"
	}
}

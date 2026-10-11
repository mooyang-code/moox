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
	// DefaultLatenessS 是任务没有指定允许迟到时间时的空间默认值。
	DefaultLatenessS int
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
}

// DesiredDataset 是期望状态里的一个数据集，带完整标的序列（规范顺序，序号即下标）。
type DesiredDataset struct {
	PlanDatasetState
	StartAt int64
	Series  []seriesset.Series
	index   map[string]uint32
}

// PlanFunction 是一个定时函数的状态：所在地域、服务的市场、cron 与计划。
type PlanFunction struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Market string `json:"market"`
	Cron   string `json:"cron"`
	Plan   *Plan  `json:"plan"`
}

// DesiredState 是由任务与标签成员算出的期望状态。
type DesiredState struct {
	Datasets  map[string]*DesiredDataset
	Functions map[string]*PlanFunction
	// StopAt 是正在回退到旧链路的数据集的停止边界（Unix 秒）；这些数据集不在 Datasets 里，按“不再采集”处理。
	StopAt map[string]int64
}

func planDatasetKey(datasetID, frequency string) string { return datasetID + "|" + frequency }

func planMarketKey(group TaskGroup) string {
	return strings.Join([]string{group.Provider, group.SourceID, group.MarketType, group.MarketID, group.InstrumentType}, "/")
}

var planNameSanitizer = strings.NewReplacer("_", "", " ", "", ".", "")

// PlanFunctionName 返回第 index 个函数的名称，名称含 PlanFleetMarker 以便旧链路识别并排除。
func PlanFunctionName(spaceID, marketType, region string, index int) string {
	region = strings.TrimPrefix(strings.ToLower(region), "ap-")
	return fmt.Sprintf("moox-fetch%s%s-%s-%s-%03d", PlanFleetMarker, planNameSanitizer.Replace(strings.ToLower(spaceID)),
		planNameSanitizer.Replace(strings.ToLower(marketType)), planNameSanitizer.Replace(region), index)
}

// BuildDesiredState 由合并后的任务组算出期望状态。previous 是已生效状态里的各函数，
// 用于让标的尽量留在原来的函数上，减少不必要的环境变更。
func BuildDesiredState(opts PlanOptions, groups []TaskGroup, previous map[string]*PlanFunction) (*DesiredState, error) {
	spaceID := strings.ToLower(strings.TrimSpace(opts.SpaceID))
	if spaceID == "" {
		return nil, errors.New("space_id is required")
	}
	defaultSize := opts.SymbolsPerFunction
	if defaultSize <= 0 {
		defaultSize = DefaultSymbolsPerFunction
	}
	state := &DesiredState{Datasets: map[string]*DesiredDataset{}, Functions: map[string]*PlanFunction{}, StopAt: map[string]int64{}}
	type market struct {
		key        string
		sample     TaskGroup
		size       int
		subjects   map[string]struct{}
		external   map[string]string
		datasetIDs []string
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
		if group.StopAt > 0 {
			state.StopAt[planDatasetKey(group.DatasetID, group.Frequency)] = group.StopAt
			continue
		}
		key := planMarketKey(group)
		info := markets[key]
		if info == nil {
			info = &market{key: key, sample: group, subjects: map[string]struct{}{}, external: map[string]string{}}
			markets[key] = info
		}
		info.size = minPositive(info.size, group.SymbolsPerFunction)
		datasetKey := planDatasetKey(group.DatasetID, group.Frequency)
		dataset := state.Datasets[datasetKey]
		if dataset == nil {
			lateness := group.AllowedLatenessSeconds
			if lateness <= 0 {
				lateness = opts.DefaultLatenessS
			}
			if lateness <= 0 {
				lateness = 60
			}
			dataset = &DesiredDataset{PlanDatasetState: PlanDatasetState{DatasetID: group.DatasetID, Frequency: group.Frequency, Market: key, LatenessS: lateness, OutputFields: normalizeOutputFields(group.OutputFields)}}
			state.Datasets[datasetKey] = dataset
			subjectSets[datasetKey] = map[string]struct{}{}
			info.datasetIDs = append(info.datasetIDs, datasetKey)
		} else if dataset.Market != key {
			return nil, fmt.Errorf("dataset %s %s is collected from more than one market", group.DatasetID, group.Frequency)
		}
		dataset.StartAt = minPositive64(dataset.StartAt, group.StartAt)
		for _, subject := range group.Subjects {
			subject = strings.ToUpper(strings.TrimSpace(subject))
			if subject == "" {
				continue
			}
			subjectSets[datasetKey][subject] = struct{}{}
			info.subjects[subject] = struct{}{}
			if external := strings.TrimSpace(group.ExternalSymbols[subject]); external != "" {
				info.external[subject] = external
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
		regions := opts.Markets[strings.ToLower(info.sample.MarketType)]
		if len(regions) == 0 {
			return nil, fmt.Errorf("no SCF region is configured for market %s in space %s", info.sample.MarketType, spaceID)
		}
		size := info.size
		if size <= 0 {
			size = defaultSize
		}
		universe := make([]string, 0, len(info.subjects))
		for subject := range info.subjects {
			universe = append(universe, subject)
		}
		sort.Strings(universe)
		if len(universe) == 0 {
			continue
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
		datasetKeys := append([]string(nil), info.datasetIDs...)
		sort.Strings(datasetKeys)
		for _, name := range names {
			subjects := bySubject[name]
			if len(subjects) == 0 {
				continue
			}
			sort.Strings(subjects)
			plan := &Plan{V: 1, Fetch: opts.Fetch, Market: PlanMarket{
				MarketID: info.sample.MarketID, InstrumentType: info.sample.InstrumentType, Provider: info.sample.Provider,
				SourceID: info.sample.SourceID, MarketType: info.sample.MarketType,
			}}
			for _, subject := range subjects {
				derived := strings.ReplaceAll(subject, "-", "")
				if external := info.external[subject]; external != "" && external != derived {
					if plan.SymbolExceptions == nil {
						plan.SymbolExceptions = map[string]string{}
					}
					plan.SymbolExceptions[subject] = external
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
			state.Functions[name] = &PlanFunction{Name: name, Region: regionOf[name], Market: key, Cron: planCron(plan), Plan: plan}
		}
	}
	return state, nil
}

// previousAssignment 从已生效的函数还原“标的 → 函数名”。
func previousAssignment(previous map[string]*PlanFunction, marketKey string) map[string]string {
	result := map[string]string{}
	for name, function := range previous {
		if function == nil || function.Plan == nil || function.Market != marketKey {
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

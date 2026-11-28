package fetchplan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	timerTriggerQualifier = "$LATEST"
	timerTriggerMessage   = "market_fetch_timer_v1"
	// runtimeConfigBatchSize 是代码包升级每两分钟一次的检查最多起的升级操作数（一批）。
	runtimeConfigBatchSize = 100
)

type taskSource interface {
	ListEnabled(context.Context, string) ([]domain.CollectionTask, error)
}

type datasetSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error)
}

type tagRouteSource interface {
	GetTag(context.Context, string, string) (*storagepb.Tag, error)
}

// GroupBuilder 把启用的 K 线采集任务按标签路由展开成采集组（数据集×频率×路由，含标的与交易所符号）。
// 采集计划对账据此算出期望状态；它不接触云端。
type GroupBuilder struct {
	ResolveSourceID marketfetch.SourceIDResolver
	ResolveSymbol   marketfetch.SymbolResolver
	Tasks           taskSource
	Symbols         datasetSource
	lastGroups      map[string][]TaskGroup
}

func (r *GroupBuilder) groups(ctx context.Context, spaceID string) ([]TaskGroup, error) {
	tasks, err := r.Tasks.ListEnabled(ctx, spaceID)
	if err != nil {
		return nil, fmt.Errorf("list enabled collection tasks: %w", err)
	}
	groups := make([]TaskGroup, 0)
	if r.lastGroups == nil {
		r.lastGroups = make(map[string][]TaskGroup)
	}
	for _, task := range tasks {
		if !strings.EqualFold(strings.TrimSpace(task.DataType), domain.DataTypeKline) {
			continue
		}
		params, parseErr := domain.ParseCollectParams(task.CollectParams, task.DataType)
		if parseErr != nil {
			log.WarnContextf(ctx, "skip collection task=%s during timer reconciliation: parse task: %v", task.TaskID, parseErr)
			continue
		}
		datasetID := taskresult.PersistedResultIDs(task.TaskID, task.ResultDatasetID).DatasetID
		if _, datasetErr := r.Symbols.GetDataset(ctx, spaceID, datasetID); datasetErr != nil {
			log.WarnContextf(ctx, "skip collection task=%s during timer reconciliation: get target dataset %s: %v", task.TaskID, datasetID, datasetErr)
			groups = append(groups, r.cachedGroups(task)...)
			continue
		}

		// task_tags 是采集范围的唯一来源；每个标签固定绑定来源与市场。
		tagIDs := normalizedTaskTagIDs(task.TagIDs)
		if len(tagIDs) == 0 {
			log.WarnContextf(ctx, "skip collection task=%s: task has no tags", task.TaskID)
			continue
		}
		tagSource, ok := r.Symbols.(tagRouteSource)
		if !ok {
			log.WarnContextf(ctx, "skip collection task=%s: metadata source does not expose tag routes", task.TaskID)
			groups = append(groups, r.cachedGroups(task)...)
			continue
		}
		taskGroups := make([]TaskGroup, 0)
		valid := true
		for _, tagID := range tagIDs {
			tag, tagErr := tagSource.GetTag(ctx, spaceID, tagID)
			if tagErr != nil || tag == nil {
				log.WarnContextf(ctx, "skip collection task=%s: load tag=%s: %v", task.TaskID, tagID, tagErr)
				valid = false
				break
			}
			provider := tagattr.Source(tag)
			marketType := tagattr.MarketType(tag)
			if provider == "" || marketType == "" {
				log.WarnContextf(ctx, "skip collection task=%s: tag=%s missing sync.source or market_type attribute", task.TaskID, tagID)
				valid = false
				break
			}
			subjects, subjectErr := r.Symbols.ResolveSubjects(ctx, spaceID, []string{tagID})
			if subjectErr != nil || len(subjects) == 0 {
				log.WarnContextf(ctx, "skip collection task=%s: resolve tag=%s subjects: %v", task.TaskID, tagID, subjectErr)
				valid = false
				break
			}
			routeGroups, routeErr := r.taskGroupsForRoute(ctx, task, params, provider, marketType, subjects)
			if routeErr != nil {
				log.WarnContextf(ctx, "skip collection task=%s tag=%s route: %v", task.TaskID, tagID, routeErr)
				valid = false
				break
			}
			taskGroups = append(taskGroups, routeGroups...)
		}
		if !valid || len(taskGroups) == 0 {
			groups = append(groups, r.cachedGroups(task)...)
			continue
		}
		r.lastGroups[task.TaskID] = append([]TaskGroup(nil), taskGroups...)
		groups = append(groups, taskGroups...)
	}
	return mergeGroups(groups), nil
}

// cachedGroups 返回任务上一次成功展开的采集组，元数据暂时读不到时沿用。标的与路由沿用缓存，采集方式取任务的现值：
// 切换采集方式只改任务，不能因为元数据读不到就按旧方式生成计划、把交接推迟。
func (r *GroupBuilder) cachedGroups(task domain.CollectionTask) []TaskGroup {
	previous := r.lastGroups[task.TaskID]
	groups := make([]TaskGroup, len(previous))
	for i, group := range previous {
		group.Ingest = task.Ingest
		groups[i] = group
	}
	return groups
}

func normalizedTaskTagIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// routeMarket 是任务按一个标签路由（provider、market_type）采集时的市场身份：计划的任务组与补采的路由都用它，
// 两边推出的来源（以及由它决定的 series_tag）才会一致。
func routeMarket(spaceID, provider, marketType string, resolveSourceID marketfetch.SourceIDResolver) (marketfetch.PlanMarket, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	marketType = strings.ToLower(strings.TrimSpace(marketType))
	if provider == "" || marketType == "" {
		return marketfetch.PlanMarket{}, fmt.Errorf("provider and market_type are required")
	}
	marketID, instrumentType, err := marketIdentity(spaceID, "", marketType)
	if err != nil {
		return marketfetch.PlanMarket{}, err
	}
	sourceID := ""
	if resolveSourceID != nil {
		sourceID = resolveSourceID(marketID, provider, instrumentType)
	}
	if sourceID == "" {
		sourceID = provider
	}
	return marketfetch.PlanMarket{MarketID: marketID, InstrumentType: instrumentType, Provider: provider, SourceID: sourceID, MarketType: marketType}, nil
}

func (r *GroupBuilder) taskGroupsForRoute(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams, provider, marketType string, subjects []domain.Subject) ([]TaskGroup, error) {
	route, err := routeMarket(task.SpaceID, provider, marketType, r.ResolveSourceID)
	if err != nil {
		return nil, err
	}
	provider, marketType, marketID, instrumentType, sourceID := route.Provider, route.MarketType, route.MarketID, route.InstrumentType, route.SourceID
	symbolIDs := make([]string, 0, len(subjects))
	externalSymbols := make(map[string]string, len(subjects))
	var pairs map[string]string
	activeSubjectCount := 0
	invalidSubjects := make([]string, 0)
	for _, subject := range subjects {
		activeSubjectCount++
		subjectID := strings.ToUpper(strings.TrimSpace(subject.SubjectID))
		if subjectID == "" {
			continue
		}
		external, symbolErr := marketfetch.ResolveProviderSymbol(r.ResolveSymbol, provider, marketID, marketType, subjectID)
		if symbolErr != nil {
			invalidSubjects = append(invalidSubjects, subjectID)
			continue
		}
		symbolIDs = append(symbolIDs, subjectID)
		externalSymbols[subjectID] = external
		if pair := strings.ToUpper(strings.TrimSpace(subject.Pair)); pair != "" && pair != external {
			if pairs == nil {
				pairs = map[string]string{}
			}
			pairs[subjectID] = pair
		}
	}
	if activeSubjectCount > 0 && len(symbolIDs) == 0 {
		return nil, fmt.Errorf("all active %s subjects are invalid: %s", marketType, strings.Join(invalidSubjects, ","))
	}
	if len(invalidSubjects) > 0 {
		log.WarnContextf(ctx, "skip market subjects without valid external symbols space=%s task=%s skipped=%d subjects=%s", task.SpaceID, task.TaskID, len(invalidSubjects), strings.Join(invalidSubjects, ","))
	}
	if params.Frequency == "" {
		return nil, nil
	}
	datasetID := taskresult.PersistedResultIDs(task.TaskID, task.ResultDatasetID).DatasetID
	return []TaskGroup{{
		Provider: provider, MarketType: marketType, MarketID: marketID, InstrumentType: instrumentType,
		SourceID: sourceID, DatasetID: datasetID,
		Frequency: params.Frequency, OutputFields: append([]string(nil), params.OutputFields...),
		Subjects: symbolIDs, ExternalSymbols: externalSymbols, Pairs: pairs,
		SymbolsPerFunction: params.SymbolsPerFunction, AllowedLatenessSeconds: params.AllowedLatenessSeconds,
		Ingest: task.Ingest,
	}}, nil
}

// TaskSeriesTag 返回 kline 采集任务按标签路由（provider、market_type）写入行时用的 series_tag，
// 与 taskGroupsForRoute 生成的计划使用同一套推导（含 source_id 的解析）。
func TaskSeriesTag(spaceID, provider, marketType string, resolveSourceID marketfetch.SourceIDResolver) (string, error) {
	route, err := routeMarket(spaceID, provider, marketType, resolveSourceID)
	if err != nil {
		return "", err
	}
	return marketfetch.CollectionItemSeriesTag(spaceID, domain.CollectionItem{Provider: route.Provider, SourceID: route.SourceID, MarketType: route.MarketType}), nil
}

// BackfillRoutes 由当前的采集任务推出补采用的来源与参数：市场身份与计划同一套推导（routeMarket），拉取参数按空间的
// 计划配置（PlanOptions.planFetch），输出字段取任务的。交易所符号不在这里：补采按标的时间线取当时的符号。
type BackfillRoutes struct {
	Tasks interface {
		ListKline(ctx context.Context, spaceID, datasetID string) ([]domain.CollectionTask, error)
	}
	Tags            tagRouteSource
	ResolveSourceID marketfetch.SourceIDResolver
	// Options 返回空间的计划配置（与对账器用的同一份）。
	Options func(spaceID string) (PlanOptions, error)
}

// BackfillRoute 返回补采数据集（某个频率）用的计划：Market、Fetch 与只含这个数据集（带输出字段）的 Datasets。
// 没有写这个数据集与频率的采集任务（任务已删除）时 found 为假。
func (r *BackfillRoutes) BackfillRoute(ctx context.Context, spaceID, datasetID, frequency string) (plan *marketfetch.Plan, found bool, err error) {
	if r == nil || r.Tasks == nil || r.Tags == nil || r.Options == nil {
		return nil, false, fmt.Errorf("backfill routes are not initialized")
	}
	tasks, err := r.Tasks.ListKline(ctx, spaceID, datasetID)
	if err != nil {
		return nil, false, fmt.Errorf("list collection tasks of dataset %s: %w", datasetID, err)
	}
	for _, task := range tasks {
		params, parseErr := domain.ParseCollectParams(task.CollectParams, task.DataType)
		if parseErr != nil {
			return nil, false, fmt.Errorf("parse collection task %s: %w", task.TaskID, parseErr)
		}
		if params.Frequency != frequency {
			continue
		}
		tagIDs := normalizedTaskTagIDs(task.TagIDs)
		if len(tagIDs) == 0 {
			return nil, false, fmt.Errorf("collection task %s has no tags", task.TaskID)
		}
		// 任务的标签同属一个数据源与市场（建任务时校验），取第一个即可。
		tag, tagErr := r.Tags.GetTag(ctx, spaceID, tagIDs[0])
		if tagErr != nil || tag == nil {
			return nil, false, fmt.Errorf("load tag %s of collection task %s: %v", tagIDs[0], task.TaskID, tagErr)
		}
		market, routeErr := routeMarket(task.SpaceID, tagattr.Source(tag), tagattr.MarketType(tag), r.ResolveSourceID)
		if routeErr != nil {
			return nil, false, fmt.Errorf("route of collection task %s: %w", task.TaskID, routeErr)
		}
		options, optionsErr := r.Options(spaceID)
		if optionsErr != nil {
			return nil, false, optionsErr
		}
		sample := TaskGroup{Provider: market.Provider, MarketType: market.MarketType, MarketID: market.MarketID, InstrumentType: market.InstrumentType, SourceID: market.SourceID, DatasetID: datasetID, Frequency: frequency}
		executor := task.Ingest
		if executor == "" {
			executor = marketfetch.PlanExecutorSCF
		}
		return &marketfetch.Plan{V: 1, Executor: executor, Market: market, Fetch: options.planFetch(sample), Datasets: []marketfetch.PlanDataset{{
			DatasetID: datasetID, Frequency: frequency, OutputFields: normalizeOutputFields(params.OutputFields),
		}}}, true, nil
	}
	return nil, false, nil
}

// marketIdentity 规范化任务的市场与品种：任务没写品种时由市场的标的规则按标签的 market_type 决定。
func marketIdentity(marketID, instrumentType, marketType string) (string, string, error) {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	instrumentType = strings.ToLower(strings.TrimSpace(instrumentType))
	if instrumentType != "" {
		return marketID, instrumentType, nil
	}
	fallback, err := marketfetch.DefaultInstrumentType(marketID, marketType)
	if err != nil {
		return "", "", err
	}
	return marketID, string(fallback), nil
}

func mergeGroups(groups []TaskGroup) []TaskGroup {
	byKey := make(map[string]TaskGroup)
	for _, group := range groups {
		key := groupKey(group)
		current := byKey[key]
		current.Provider, current.MarketType = group.Provider, group.MarketType
		current.MarketID, current.InstrumentType = group.MarketID, group.InstrumentType
		current.SourceID, current.SeriesTag = group.SourceID, group.SeriesTag
		current.DatasetID, current.Frequency = group.DatasetID, group.Frequency
		current.OutputFields = append([]string(nil), group.OutputFields...)
		current.Subjects = append(current.Subjects, group.Subjects...)
		current.SymbolsPerFunction = minPositive(current.SymbolsPerFunction, group.SymbolsPerFunction)
		current.AllowedLatenessSeconds = minPositive(current.AllowedLatenessSeconds, group.AllowedLatenessSeconds)
		if current.Ingest == "" {
			current.Ingest = group.Ingest
		}
		if group.ExternalSymbols != nil {
			if current.ExternalSymbols == nil {
				current.ExternalSymbols = make(map[string]string)
			}
			for subject, external := range group.ExternalSymbols {
				if previous, exists := current.ExternalSymbols[subject]; exists {
					if previous != external {
						current.ExternalSymbols[subject] = ""
					}
					continue
				}
				current.ExternalSymbols[subject] = external
			}
		}
		for subject, pair := range group.Pairs {
			if current.Pairs == nil {
				current.Pairs = make(map[string]string)
			}
			current.Pairs[subject] = pair
		}
		byKey[key] = current
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]TaskGroup, 0, len(keys))
	for _, key := range keys {
		group := byKey[key]
		group.Subjects = normalizeSubjects(group.Subjects)
		if len(group.Subjects) > 0 {
			result = append(result, group)
		}
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func minPositive(left, right int) int {
	return int(minPositive64(int64(left), int64(right)))
}

func minPositive64(left, right int64) int64 {
	switch {
	case left <= 0:
		return max(right, 0)
	case right <= 0:
		return left
	case right < left:
		return right
	default:
		return left
	}
}

package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	timerTriggerType      = "timer"
	timerTriggerQualifier = "$LATEST"
	timerTriggerMessage   = "market_fetch_timer_v1"
	// runtimeConfigBatchSize 是 FunctionInstance 单个作业最多的项数。
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
	ResolveSourceID func(string, string) string
	ResolveSymbol   SymbolResolver
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
		params, parseErr := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
		if parseErr != nil {
			log.WarnContextf(ctx, "skip collection task=%s during timer reconciliation: parse task: %v", task.TaskID, parseErr)
			continue
		}
		if params.Collector.DataType != "kline" {
			continue
		}
		if _, datasetErr := r.Symbols.GetDataset(ctx, spaceID, params.Target.DatasetID); datasetErr != nil {
			log.WarnContextf(ctx, "skip collection task=%s during timer reconciliation: get target dataset %s: %v", task.TaskID, params.Target.DatasetID, datasetErr)
			if previous := r.lastGroups[task.TaskID]; len(previous) > 0 {
				groups = append(groups, previous...)
			}
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
			if previous := r.lastGroups[task.TaskID]; len(previous) > 0 {
				groups = append(groups, previous...)
			}
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
			provider := strings.ToLower(strings.TrimSpace(tag.GetSource()))
			marketType := strings.ToLower(strings.TrimSpace(tag.GetMarketType()))
			if provider == "" || marketType == "" {
				log.WarnContextf(ctx, "skip collection task=%s: tag=%s missing immutable source/market_type", task.TaskID, tagID)
				valid = false
				break
			}
			subjects, subjectErr := r.Symbols.ResolveSubjects(ctx, spaceID, []string{tagID})
			if subjectErr != nil || len(subjects) == 0 {
				log.WarnContextf(ctx, "skip collection task=%s: resolve tag=%s subjects: %v", task.TaskID, tagID, subjectErr)
				valid = false
				break
			}
			routeGroups, routeErr := r.taskGroupsForRoute(ctx, task, params, provider, marketType, "", subjects)
			if routeErr != nil {
				log.WarnContextf(ctx, "skip collection task=%s tag=%s route: %v", task.TaskID, tagID, routeErr)
				valid = false
				break
			}
			taskGroups = append(taskGroups, routeGroups...)
		}
		if !valid || len(taskGroups) == 0 {
			if previous := r.lastGroups[task.TaskID]; len(previous) > 0 {
				groups = append(groups, previous...)
			}
			continue
		}
		r.lastGroups[task.TaskID] = append([]TaskGroup(nil), taskGroups...)
		groups = append(groups, taskGroups...)
	}
	return mergeGroups(groups), nil
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

func (r *GroupBuilder) taskGroupsForRoute(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams, provider, marketType, sourceID string, subjects []domain.Subject) ([]TaskGroup, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	marketType = strings.ToLower(strings.TrimSpace(marketType))
	if provider == "" || marketType == "" {
		return nil, fmt.Errorf("provider and market_type are required")
	}
	marketID, instrumentType := marketIdentity(firstNonEmpty(params.MarketID, task.SpaceID), params.InstrumentType, params.Target.DatasetID)
	if instrumentType == "" {
		instrumentType = defaultInstrumentTypeForMarket(marketID, marketType)
	}
	if sourceID = strings.TrimSpace(sourceID); sourceID == "" && r.ResolveSourceID != nil {
		sourceID = r.ResolveSourceID(provider, instrumentType)
	}
	if sourceID == "" {
		sourceID = provider
	}
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
		external, symbolErr := resolveProviderSymbol(r.ResolveSymbol, provider, marketID, marketType, subjectID)
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
	result := make([]TaskGroup, 0, len(params.Collector.Intervals))
	for _, frequency := range params.Collector.Intervals {
		result = append(result, TaskGroup{
			Provider: provider, MarketType: marketType, MarketID: marketID, InstrumentType: instrumentType,
			SourceID: sourceID, SeriesTag: params.SeriesTag, DatasetID: params.Target.DatasetID,
			Frequency: frequency, OutputFields: append([]string(nil), params.OutputFields...),
			Subjects: symbolIDs, ExternalSymbols: externalSymbols, Pairs: pairs,
			SymbolsPerFunction: params.SymbolsPerFunction, AllowedLatenessSeconds: params.AllowedLatenessSeconds,
			Ingest: params.Ingest, Executor: params.Executor,
		})
	}
	return result, nil
}

// TaskSeriesTag 返回 kline 采集任务按标签路由（provider、market_type）写入行时用的 series_tag，
// 与 taskGroupsForRoute 生成的计划使用同一套推导（含 source_id 的解析）。
func TaskSeriesTag(spaceID string, params *domain.CollectParams, provider, marketType string, resolveSourceID func(string, string) string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	marketType = strings.ToLower(strings.TrimSpace(marketType))
	marketID, instrumentType := marketIdentity(firstNonEmpty(params.MarketID, spaceID), params.InstrumentType, params.Target.DatasetID)
	if instrumentType == "" {
		instrumentType = defaultInstrumentTypeForMarket(marketID, marketType)
	}
	sourceID := ""
	if resolveSourceID != nil {
		sourceID = resolveSourceID(provider, instrumentType)
	}
	if sourceID == "" {
		sourceID = provider
	}
	return collectionItemSeriesTag(spaceID, domain.CollectionItem{Provider: provider, SourceID: sourceID, MarketType: marketType})
}

func marketIdentity(marketID, instrumentType, datasetID string) (string, string) {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	instrumentType = strings.ToLower(strings.TrimSpace(instrumentType))
	if marketID != "" && instrumentType != "" {
		return marketID, instrumentType
	}
	datasetID = strings.ToLower(strings.TrimSpace(datasetID))
	switch {
	case strings.HasPrefix(datasetID, "dataset_stockcn_index"):
		return "stockcn", "index"
	case strings.HasPrefix(datasetID, "dataset_stockcn_bond"):
		return "stockcn", "convertible_bond"
	case strings.HasPrefix(datasetID, "dataset_stockcn"):
		return "stockcn", "equity"
	default:
		return marketID, instrumentType
	}
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
		if current.Executor == "" {
			current.Executor = group.Executor
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

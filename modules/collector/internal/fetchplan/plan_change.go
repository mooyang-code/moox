package fetchplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"trpc.group/trpc-go/trpc-go/log"
)

// maxSCFEnvironmentBytes 是 SCF 函数环境变量的总上限。
const maxSCFEnvironmentBytes = 4096

// handoffLatenessMaxWait 是一轮对账里为等到交接截止的可改时刻最多等待的时间（对账每分钟一轮，1m 的窗口只有约 35 秒）。
const handoffLatenessMaxWait = 40 * time.Second

func (r *PlanReconciler) planChange(ctx context.Context, latenessOK map[string]bool) error {
	var previousFunctions map[string]*PlanFunction
	var previousDatasets map[string]*PlanDataset
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil {
		return r.fail("store", fmt.Errorf("read applied fetch plan: %w", err))
	}
	if applied != nil {
		content, decodeErr := decodeFetchPlanContent(applied.Plan)
		if decodeErr != nil {
			return r.fail("store", decodeErr)
		}
		previousFunctions, previousDatasets = content.Applied()
	}
	previousStates := activeStates(previousDatasets)
	groups, err := r.Groups.groups(ctx, r.SpaceID)
	if err != nil {
		return r.fail("rules", err)
	}
	managed, err := r.managedTasks(ctx)
	if err != nil {
		return r.fail("rules", err)
	}
	if len(groups) == 0 && len(managed) > 0 {
		// 任务启用但标的暂时取不到：属于临时数据状况，保持现状，不能据此停掉全部函数。
		log.WarnContextf(ctx, "collector fetch plan kept current state space=%s: enabled tasks have no usable kline groups", r.SpaceID)
		r.waiting = "启用的任务暂时取不到标的"
		return nil
	}
	// 排除名单叠加在标签成员之上，放在“标的暂时取不到”的保护之后：全部标的都被排除是操作员的决定，照常生效。
	// 它的变化与标的变更一样生成新计划（已打开的计划先推进完）。
	if r.Exclusions != nil {
		exclusions, err := r.Exclusions.Active(ctx, r.SpaceID, r.now())
		if err != nil {
			return r.fail("store", fmt.Errorf("read subject exclusions: %w", err))
		}
		groups = excludeSubjects(groups, exclusions)
	}
	desired, err := BuildDesiredState(r.Options, groups, previousFunctions)
	if err != nil {
		return r.fail("rules", err)
	}
	// 交接与标的变更分开：有数据集的标的集合在变时，本轮先按已生效的执行端完成标的变更，交接等下一份计划。
	if handoffPending(desired, previousStates) && subjectsChanging(desired, previousStates) {
		if desired, err = BuildDesiredState(r.Options, appliedIngest(groups, previousStates), previousFunctions); err != nil {
			return r.fail("rules", err)
		}
		log.InfoContextf(ctx, "collector fetch plan defers the ingest handoff until the subject change is applied space=%s", r.SpaceID)
	}
	handoff := handoffPending(desired, previousStates)
	if r.holdMissingDatasets(ctx, desired, previousStates, managed) {
		return nil
	}
	if blocked, err := r.waitForStream(ctx, desired, previousFunctions); blocked || err != nil {
		return err
	}
	// 计划里有需要新建的函数时拉长提前量：创建 VPC 函数比改环境慢得多。
	lead := planLeadTime
	if handoff {
		lead = planLeadTimeHandoff
	}
	creates := false
	for name, function := range desired.Functions {
		if previousFunctions[name] == nil && function.Executor == marketfetch.PlanExecutorSCF {
			lead, creates = planLeadTimeWithCreate, true
			break
		}
	}
	if firstStreamPlan(desired, previousFunctions, previousStates) {
		lead = planLeadTimePushOnly
	}
	content, changed, waiting, err := BuildPlanContent(previousFunctions, previousDatasets, desired, PlanTiming{Now: r.now(), Lead: lead, LatenessOK: latenessOK})
	if err != nil {
		return r.fail("rules", err)
	}
	if len(waiting) > 0 {
		r.waiting = strings.Join(waiting, "；")
	}
	if !changed {
		return r.verifyFleet(ctx, scfFunctions(previousFunctions))
	}
	if r.Preflight != nil {
		if err := r.Preflight(ctx); err != nil {
			return r.fail("preflight", err)
		}
	}
	if err := r.validateContent(content); err != nil {
		return r.fail("capacity", err)
	}
	if creates {
		for _, function := range content.Functions {
			if function.Create {
				r.Egress.Arm() // 新建函数的出口 IP 要去重：计划生效后探测一次
				break
			}
		}
	}
	if r.Lateness != nil {
		for _, key := range content.sortedDatasetKeys() {
			dataset := content.Datasets[key]
			// 停止的数据集不改；交接中的数据集由每轮对账到点再改（applyHandoffLateness）：此刻改会让旧执行端最后几个周期按新截止建立。
			if dataset.Stopped || dataset.HandoffFrom() > 0 {
				continue
			}
			if err := r.Lateness.EnsureAllowedLateness(ctx, r.SpaceID, dataset.DatasetID, dataset.LatenessS); err != nil {
				return r.fail("dataset", fmt.Errorf("set allowed lateness of dataset %s: %w", dataset.DatasetID, err))
			}
		}
	}
	progress := &FetchPlanProgress{}
	// 不用登记的时间线行（停止行、只改符号的行等）和计划记录一起落库。
	record, err := r.Store.Create(ctx, r.SpaceID, mustJSON(content), mustJSON(progress), content.planCreatedTimeline(r.SpaceID)...)
	if errors.Is(err, store.ErrFetchPlanOpen) {
		return nil
	}
	if err != nil {
		return r.fail("store", fmt.Errorf("save fetch plan: %w", err))
	}
	log.InfoContextf(ctx, "collector fetch plan created space=%s plan=%d datasets=%d functions=%d", r.SpaceID, record.ID, len(content.Datasets), len(content.Functions))
	return r.advance(ctx, record)
}

// firstStreamPlan 报告这是空间的第一份计划，并且全部由 collector-stream 执行。
func firstStreamPlan(desired *DesiredState, previousFunctions map[string]*PlanFunction, previousDatasets map[string]*PlanDatasetState) bool {
	if len(previousFunctions) != 0 || len(previousDatasets) != 0 || len(desired.Functions) == 0 {
		return false
	}
	for _, function := range desired.Functions {
		if function.Executor != marketfetch.PlanExecutorStreaming && function.Executor != marketfetch.PlanExecutorLocal {
			return false
		}
	}
	return true
}

// waitForStream 在新计划要给 collector-stream 下发新的推送或本地执行项、而它不在线时返回 true：本轮不建计划。
func (r *PlanReconciler) waitForStream(ctx context.Context, desired *DesiredState, previous map[string]*PlanFunction) (bool, error) {
	if r.StreamOnline == nil {
		return false, nil
	}
	needed := false
	for name, function := range desired.Functions {
		if (function.Executor == marketfetch.PlanExecutorStreaming || function.Executor == marketfetch.PlanExecutorLocal) && previous[name] == nil {
			needed = true
			break
		}
	}
	if !needed {
		return false, nil
	}
	online, err := r.StreamOnline(ctx)
	if err != nil {
		return false, r.fail("store", fmt.Errorf("read collector-stream status: %w", err))
	}
	if online {
		return false, nil
	}
	r.waiting = "等 collector-stream 上线"
	log.InfoContextf(ctx, "collector fetch plan waits for collector-stream space=%s", r.SpaceID)
	return true, nil
}

// holdMissingDatasets 对“数据集从期望状态中消失、但对应任务仍启用”做防抖：
// 这通常是标签或元数据暂时取不到，连续多轮仍消失才当作真实变更。
func (r *PlanReconciler) holdMissingDatasets(ctx context.Context, desired *DesiredState, previous map[string]*PlanDatasetState, managed map[string]struct{}) bool {
	if r.goneSeen == nil {
		r.goneSeen = map[string]int{}
	}
	hold := false
	seen := map[string]struct{}{}
	for key, state := range previous {
		if desired.Datasets[key] != nil {
			continue
		}
		if _, stillEnabled := managed[state.DatasetID]; !stillEnabled {
			continue
		}
		seen[key] = struct{}{}
		r.goneSeen[key]++
		if r.goneSeen[key] < planGoneDebounce {
			hold = true
			log.WarnContextf(ctx, "collector fetch plan holds dataset removal space=%s dataset=%s seen=%d", r.SpaceID, key, r.goneSeen[key])
		}
	}
	for key := range r.goneSeen {
		if _, ok := seen[key]; !ok {
			delete(r.goneSeen, key)
		}
	}
	return hold
}

// functionEnvironment 构造受管环境：采集计划、外部入口、超时。
func (r *PlanReconciler) functionEnvironment(region string, plan *marketfetch.Plan) (map[string]string, error) {
	encoded, err := marketfetch.EncodePlan(plan)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(encoded))
	count := 0
	for _, dataset := range plan.Datasets {
		count += len(dataset.Copies[len(dataset.Copies)-1].Subjects)
	}
	environment := map[string]string{
		marketfetch.PlanEnvName:             encoded,
		"MOOX_MARKET_FETCH_ASSIGNMENT_HASH": hex.EncodeToString(sum[:8]),
		"MOOX_MARKET_FETCH_SUBJECT_COUNT":   strconv.Itoa(count),
		"MOOX_FETCH_TIMEOUT_SECONDS":        strconv.Itoa(tencent.CollectorTimerTimeoutSeconds),
	}
	if r.Entry == nil {
		return nil, errors.New("SCF entry environment is required")
	}
	access, err := r.Entry(region)
	if err != nil {
		return nil, err
	}
	if err := tencent.ValidateCollectorEntryEnvironment(access); err != nil {
		return nil, err
	}
	for _, key := range []string{"MOOX_ENTRY_ADDRESS", "MOOX_ENTRY_HOST", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
		if value, ok := access[key]; ok {
			environment[key] = value
		}
	}
	return environment, nil
}

// validateContent 在保存计划前确认每个要下发的函数的 Target 能放进 SCF 环境变量上限（每数据集最多两份由决策表保证）。
func (r *PlanReconciler) validateContent(content *FetchPlanContent) error {
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if !function.Push || function.Target == nil {
			continue
		}
		if !function.IsSCF() {
			if err := function.Target.Validate(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		environment, err := r.functionEnvironment(function.Region, function.Target)
		if err != nil {
			return fmt.Errorf("function %s: %w", name, err)
		}
		merged := map[string]string{"MOOX_CODE_PACKAGE_ID": r.packageID()}
		for key, value := range r.Deploy.BaseEnvironment {
			merged[key] = value
		}
		for key, value := range environment {
			merged[key] = value
		}
		if size := tencent.SCFEnvironmentBytes(merged); size > maxSCFEnvironmentBytes {
			return fmt.Errorf("function %s environment is %d bytes; reduce symbols_per_function", name, size)
		}
	}
	return nil
}

// applyHandoffLateness 每轮改一次交接数据集的截止，不记进度：看已生效状态的数据集，再加上未生效计划的（同一个键以未生效计划为准），
// 凡是有两份、两份截止不同的，到“后一份 From + 旧份截止 + 余量”时调 EnsureAllowedLateness，传入当前期望的截止（幂等，到点后每轮都调）。
// 旧执行端的最后一个周期（后一份 From 之前）到那时已经建立；新执行端的第一个周期在后一份 From 加一个周期之后才建立，要在那之前改成。
// 可改时刻在本轮 handoffLatenessMaxWait 之内就等到那一刻。返回本轮已改成功的数据集：裁掉这类数据集的旧份要以它为前提，
// 改成功之前旧份一直留着，这个状态本身就是“还要改”的记号。调用失败只记失败，不挡住本轮别的数据集。
func (r *PlanReconciler) applyHandoffLateness(ctx context.Context, open *store.FetchPlanRecord) (map[string]bool, error) {
	if r.Lateness == nil {
		return nil, nil
	}
	datasets := map[string]*PlanDataset{}
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil {
		return nil, r.fail("store", fmt.Errorf("read applied fetch plan: %w", err))
	}
	var firstErr error
	for _, record := range []*store.FetchPlanRecord{applied, open} {
		if record == nil {
			continue
		}
		content, err := decodeFetchPlanContent(record.Plan)
		if err != nil {
			return nil, r.fail("store", err)
		}
		for key, dataset := range content.Datasets {
			datasets[key] = dataset
		}
	}
	type pending struct {
		key           string
		due, deadline time.Time
	}
	var items []pending
	for key, dataset := range datasets {
		if len(dataset.Copies) != 2 || dataset.Copies[0].LatenessS == dataset.Copies[1].LatenessS {
			continue
		}
		start := time.Unix(dataset.Copies[1].From, 0).UTC()
		deadline := start.Add(time.Minute)
		if frequency, err := marketdata.ParseFrequency(dataset.Frequency); err == nil {
			deadline = nextBoundary(frequency, start.Add(time.Second))
		}
		items = append(items, pending{key: key, due: start.Add(time.Duration(dataset.Copies[0].LatenessS)*time.Second + copyWindowMargin), deadline: deadline})
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].due.Equal(items[j].due) {
			return items[i].due.Before(items[j].due)
		}
		return items[i].key < items[j].key
	})
	done := map[string]bool{}
	for _, item := range items {
		if wait := item.due.Sub(r.now()); wait > 0 {
			if wait > handoffLatenessMaxWait {
				continue
			}
			if err := r.wait(ctx, wait); err != nil {
				return done, nil
			}
		}
		dataset := datasets[item.key]
		if r.now().After(item.deadline) {
			log.WarnContextf(ctx, "collector fetch plan switches allowed lateness late space=%s dataset=%s: periods from %s may have been created with the old lateness", r.SpaceID, item.key, time.Unix(dataset.Copies[1].From, 0).UTC().Format(time.RFC3339))
		}
		if err := r.Lateness.EnsureAllowedLateness(ctx, r.SpaceID, dataset.DatasetID, dataset.LatenessS); err != nil {
			err = r.fail("dataset", fmt.Errorf("set allowed lateness of dataset %s: %w", dataset.DatasetID, err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		done[item.key] = true
	}
	return done, firstErr
}

// excludeSubjects 从采集组里去掉排除的标的；标的全被排除的组不再采集。
func excludeSubjects(groups []TaskGroup, exclusions []store.SubjectExclusion) []TaskGroup {
	if len(exclusions) == 0 {
		return groups
	}
	result := make([]TaskGroup, 0, len(groups))
	for _, group := range groups {
		excluded := map[string]bool{}
		for _, exclusion := range exclusions {
			if exclusion.Excludes(group.MarketType) {
				excluded[exclusion.SubjectID] = true
			}
		}
		subjects := make([]string, 0, len(group.Subjects))
		for _, subject := range group.Subjects {
			if !excluded[strings.ToUpper(subject)] {
				subjects = append(subjects, subject)
			}
		}
		if len(subjects) == len(group.Subjects) {
			result = append(result, group)
			continue
		}
		if len(subjects) == 0 {
			continue
		}
		group.Subjects = subjects
		group.ExternalSymbols = keepKeys(group.ExternalSymbols, subjects)
		group.Pairs = keepKeys(group.Pairs, subjects)
		result = append(result, group)
	}
	return result
}

func keepKeys(values map[string]string, keys []string) map[string]string {
	if values == nil {
		return nil
	}
	kept := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := values[key]; ok {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// handoffPending 判断期望状态里有已生效的数据集换了执行端。
func handoffPending(desired *DesiredState, previous map[string]*PlanDatasetState) bool {
	for key, dataset := range desired.Datasets {
		if old := previous[key]; old != nil && old.Ingest != dataset.Ingest {
			return true
		}
	}
	return false
}

// subjectsChanging 判断有已生效的数据集标的集合在变。
func subjectsChanging(desired *DesiredState, previous map[string]*PlanDatasetState) bool {
	for key, dataset := range desired.Datasets {
		if old := previous[key]; old != nil && old.Hash != dataset.Hash {
			return true
		}
	}
	return false
}

// appliedIngest 把已生效数据集的任务组还原成它们现在的执行端（推迟交接），新数据集保持任务的采集方式。
func appliedIngest(groups []TaskGroup, previous map[string]*PlanDatasetState) []TaskGroup {
	current := map[string]string{}
	for _, state := range previous {
		current[state.DatasetID] = state.Ingest
	}
	result := append([]TaskGroup(nil), groups...)
	for i := range result {
		if ingest, ok := current[result[i].DatasetID]; ok {
			result[i].Ingest = ingest
		}
	}
	return result
}

// scfFunctions 只保留 SCF 函数（云端校验、修复、频控估算只作用于它们）。
func scfFunctions(functions map[string]*PlanFunction) map[string]*PlanFunction {
	result := make(map[string]*PlanFunction, len(functions))
	for name, function := range functions {
		if function != nil && function.Executor == marketfetch.PlanExecutorSCF {
			result[name] = function
		}
	}
	return result
}

package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

func outcomeFor(plan Plan, loaded LoadResult, computation Computation) Outcome {
	outcome := Outcome{
		Status:         "complete",
		FailedSubjects: uniqueSorted(append(append([]string(nil), plan.UpstreamFailed...), loaded.FailedSubjects...)),
		StageDurations: make(map[string]time.Duration),
	}
	for _, factor := range plan.Factors {
		state, ok := computation.FactorStates[factor.FactorID]
		if !ok {
			state = storageio.FactorState{FactorID: factor.FactorID, Status: "degraded", SourceHash: factor.SpecHash, DefinitionHash: domain.DefinitionHash(factor)}
		}
		state.FailedSubjects = uniqueSorted(state.FailedSubjects)
		outcome.Factors = append(outcome.Factors, state)
		if state.Status != "complete" {
			outcome.Status = "degraded"
		}
	}
	if len(outcome.FailedSubjects) > 0 {
		outcome.Status = "degraded"
	}
	if computation.BudgetHit {
		outcome.Status = "degraded"
	}
	sort.Slice(outcome.Factors, func(i, j int) bool { return outcome.Factors[i].FactorID < outcome.Factors[j].FactorID })
	return outcome
}

func (r *Runner) Report(ctx context.Context, plan Plan, outcome Outcome) error {
	if plan.Mode != ModeLive {
		return nil
	}
	if r == nil || r.store == nil || r.clock == nil {
		return fmt.Errorf("factor Storage store and period clock are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return err
	}
	marker := storageio.PeriodMarker{
		SpaceID: plan.Set.SpaceID, ResultDatasetID: plan.Set.ResultDatasetID,
		SourceDatasetID: plan.Set.SourceDatasetID, Frequency: plan.Set.Freq,
		PeriodTime: target.Unix(), Status: outcome.Status,
		UniverseSubjects: uniqueSorted(plan.Expected), FailedSubjects: uniqueSorted(outcome.FailedSubjects),
		Factors:        append([]storageio.FactorState(nil), outcome.Factors...),
		TriggerEventID: plan.TriggerEventID, ComputedAt: time.Now().UTC(),
	}
	if err := r.store.ReportComputed(ctx, marker); err != nil {
		return fmt.Errorf("report factor period computed: %w", err)
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, plan Plan) (Outcome, error) {
	if r == nil || r.store == nil || r.clock == nil {
		return Outcome{}, errors.New("factor pipeline requires Storage and period clock")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := Outcome{StageDurations: make(map[string]time.Duration)}
	planStarted := time.Now()
	if plan.Mode != ModeLive && plan.Mode != ModeRecalc {
		return Outcome{}, fmt.Errorf("unknown factor pipeline mode %d", plan.Mode)
	}
	if _, err := planTarget(r.clock, plan); err != nil {
		return Outcome{}, err
	}
	outcome.StageDurations["plan"] = time.Since(planStarted)
	loaded := LoadResult{Frames: map[string]*storageio.Frame{}, Available: append([]string(nil), plan.Available...)}
	computation := Computation{Results: map[string]map[string]ItemResult{}, FactorStates: map[string]storageio.FactorState{}}
	for _, factor := range plan.Factors {
		computation.FactorStates[factor.FactorID] = storageio.FactorState{
			FactorID: factor.FactorID, Status: "complete", SourceHash: factor.SpecHash,
			DefinitionHash: domain.DefinitionHash(factor),
		}
	}
	rowsWritten := 0
	var items map[ItemKey]ItemStatus
	if len(plan.Factors) > 0 {
		stage := time.Now()
		var err error
		// 读取之前做内存预检（设计 7.5）：补算超限以资源错误失败；实时周期本期全部因子按计算失败上报，不读取。
		budgetErr := r.precheckMemory(plan)
		if budgetErr != nil && plan.Mode == ModeRecalc {
			return Outcome{StageDurations: outcome.StageDurations}, budgetErr
		}
		if budgetErr != nil {
			log.ErrorContextf(ctx, "factor_memory_budget_exceeded set_id=%s error=%v", plan.Set.SetID, budgetErr)
			computation = r.failAll(plan, budgetErr)
			goto assemble
		}
		loaded, err = r.Load(ctx, plan)
		outcome.StageDurations["load"] = time.Since(stage)
		if err != nil {
			return Outcome{StageDurations: outcome.StageDurations}, err
		}
		// 补算使用每次执行唯一的提交 ID：Storage 对相同提交 ID、相同内容直接返回成功而不再写字段，
		// 补算必须保证「期望重写就一定重写」。普通实时写入保持内容寻址去重。
		if plan.CommitSalt == "" && plan.Mode == ModeRecalc {
			plan.CommitSalt = newCommitSalt()
		}
		stage = time.Now()
		computation, err = r.Compute(ctx, plan, loaded)
		loaded.View.Release() // 读租约只覆盖单次计算（设计 4.1）
		outcome.StageDurations["compute"] = time.Since(stage)
		if err != nil {
			return Outcome{StageDurations: outcome.StageDurations}, err
		}
	assemble:
		stage = time.Now()
		rows, assembled, assemblyErr := r.AssembleWithReport(plan, loaded, computation)
		outcome.StageDurations["assemble"] = time.Since(stage)
		if assemblyErr != nil {
			return Outcome{StageDurations: outcome.StageDurations}, assemblyErr
		}
		items = assembled.Items
		// 先记账本、再写结果与 marker：崩溃后事件重投时，同一条目按唯一键合并，不会丢账。
		if !plan.FillOnly {
			r.recordWarming(ctx, plan, loaded.WarmingSubjects)
			r.recordUnavailable(ctx, plan, loaded)
			// 上游失败的待办是这一期补齐的唯一依据（水位随后前进，不会再回头），记不进账本就不能继续。
			if err := r.recordUpstream(ctx, plan); err != nil {
				return Outcome{StageDurations: outcome.StageDurations}, err
			}
		}
		stage = time.Now()
		target, targetErr := planTarget(r.clock, plan)
		if targetErr != nil {
			return Outcome{StageDurations: outcome.StageDurations}, targetErr
		}
		if writeErr := r.Write(ctx, plan, target, rows, r.cfg.WriteBatchRows); writeErr != nil {
			return Outcome{StageDurations: outcome.StageDurations}, writeErr
		}
		rowsWritten = len(rows)
		outcome.StageDurations["write"] = time.Since(stage)
	} else {
		outcome.StageDurations["load"] = 0
		outcome.StageDurations["compute"] = 0
		outcome.StageDurations["assemble"] = 0
		outcome.StageDurations["write"] = 0
	}
	outcome = mergeOutcomeDurations(outcomeFor(plan, loaded, computation), outcome)
	outcome.RowsWritten = rowsWritten
	if plan.Mode == ModeRecalc {
		outcome.Items = items
	}
	outcome.Warming = len(loaded.WarmingSubjects)
	stage := time.Now()
	if err := r.Report(ctx, plan, outcome); err != nil {
		return outcome, err
	}
	outcome.StageDurations["report"] = time.Since(stage)
	return outcome, nil
}

func mergeOutcomeDurations(outcome, previous Outcome) Outcome {
	for name, duration := range previous.StageDurations {
		outcome.StageDurations[name] = duration
	}
	return outcome
}

// newCommitSalt 生成每次执行唯一的提交 ID 盐。
func newCommitSalt() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf)
}

// recordWarming 把实时周期里因预热中被跳过的 (因子, 标的, 周期) 记入账本：这些结果本期没有算，marker 照常按失败标的上报，
// 之后由空洞恢复按「只填充」补算写入。不记账的话，引擎重启后第一期的结果会永久缺失，1h 及更长周期就是整整一根。
// 截面因子也逐标的记录：恢复按周期分组，用 marker 里冻结的全集重算。写入失败只记日志，不影响周期推进。
func (r *Runner) recordWarming(ctx context.Context, plan Plan, warming []string) {
	if r.cfg.GapSink == nil || plan.Mode != ModeLive || len(warming) == 0 || len(plan.Factors) == 0 {
		return
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		log.ErrorContextf(ctx, "factor_warming_record_failed set_id=%s error=%v", plan.Set.SetID, err)
		return
	}
	hashes := make([]string, len(plan.Factors))
	for i, factor := range plan.Factors {
		hashes[i] = domain.DefinitionHash(factor)
	}
	entries := make([]GapEntry, 0, len(warming)*len(plan.Factors))
	for _, subject := range warming {
		for i, factor := range plan.Factors {
			entries = append(entries, GapEntry{
				SetID: plan.Set.SetID, Subject: subject, Period: target.UTC(), FactorID: factor.FactorID, DefinitionHash: hashes[i],
				TriggerEventID: plan.TriggerEventID, Reason: SkipReasonWarming,
			})
		}
	}
	if err := r.cfg.GapSink.RecordGaps(ctx, entries); err != nil {
		log.ErrorContextf(ctx, "factor_warming_record_failed set_id=%s period=%s subjects=%d error=%v", plan.Set.SetID, target.UTC().Format(time.RFC3339), len(warming), err)
	}
}

// recordUpstream 把实时周期里上游失败（截止时还没到）的 (因子, 标的, 周期) 记入账本：这一期缺失在阈值以内时水位照常前进，
// 补采补齐之后由空洞恢复按「只填充」补算；不记账的话这些标的的这一期永远不会补算（原事件的失败名单已过时，不能重放）。
// 记账失败返回基础设施错误：这一期不写结果、水位不前进，由重投或追赶重试。
func (r *Runner) recordUpstream(ctx context.Context, plan Plan) error {
	if r.cfg.GapSink == nil || plan.Mode != ModeLive || len(plan.UpstreamFailed) == 0 || len(plan.Factors) == 0 {
		return nil
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return err
	}
	entries := make([]GapEntry, 0, len(plan.UpstreamFailed)*len(plan.Factors))
	for _, subject := range plan.UpstreamFailed {
		for _, factor := range plan.Factors {
			entries = append(entries, GapEntry{
				SetID: plan.Set.SetID, Subject: subject, Period: target.UTC(), FactorID: factor.FactorID, DefinitionHash: domain.DefinitionHash(factor),
				TriggerEventID: plan.TriggerEventID, Reason: SkipReasonUpstream,
			})
		}
	}
	if err := r.cfg.GapSink.RecordGaps(ctx, entries); err != nil {
		log.ErrorContextf(ctx, "factor_upstream_record_failed set_id=%s period=%s subjects=%d error=%v", plan.Set.SetID, target.UTC().Format(time.RFC3339), len(plan.UpstreamFailed), err)
		return fmt.Errorf("%w: record upstream failures: %v", storageio.ErrInfra, err)
	}
	return nil
}

// recordUnavailable 把实时周期里同步之后本地仍没有目标周期行（数据还没到、或同步失败）的 (因子, 标的, 周期) 记入账本：
// 补采补齐之后由「只填充」补算补上，与上游失败同一种恢复。记账失败只记日志，不影响周期推进。
func (r *Runner) recordUnavailable(ctx context.Context, plan Plan, loaded LoadResult) {
	if r.cfg.GapSink == nil || plan.Mode != ModeLive || len(plan.Factors) == 0 {
		return
	}
	skip := make(map[string]struct{}, len(plan.UpstreamFailed)+len(loaded.WarmingSubjects))
	for _, subject := range plan.UpstreamFailed {
		skip[subject] = struct{}{}
	}
	for _, subject := range loaded.WarmingSubjects {
		skip[subject] = struct{}{}
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		log.ErrorContextf(ctx, "factor_unavailable_record_failed set_id=%s error=%v", plan.Set.SetID, err)
		return
	}
	var entries []GapEntry
	for _, subject := range loaded.FailedSubjects {
		if _, done := skip[subject]; done {
			continue
		}
		for _, factor := range plan.Factors {
			entries = append(entries, GapEntry{
				SetID: plan.Set.SetID, Subject: subject, Period: target.UTC(), FactorID: factor.FactorID, DefinitionHash: domain.DefinitionHash(factor),
				TriggerEventID: plan.TriggerEventID, Reason: SkipReasonUpstream,
			})
		}
	}
	if len(entries) == 0 {
		return
	}
	if err := r.cfg.GapSink.RecordGaps(ctx, entries); err != nil {
		log.ErrorContextf(ctx, "factor_unavailable_record_failed set_id=%s period=%s entries=%d error=%v", plan.Set.SetID, target.UTC().Format(time.RFC3339), len(entries), err)
	}
}

package seriesreplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runner 编排一次回放：对周期 t（K 线 data_time）依次执行
//  1. 因子引擎 ReplayStep 取因子帧；
//  2. 策略回放求值得到决策；
//  3. 持久化周期记录（决策、下一期策略状态、待发送的 ReplayAdvance）；
//  4. 向 Trade 发 ReplayAdvance 并等确认；
//  5. 带 write_factors 时把因子结果写回；
//  6. 标记本期完成，推进下一期。
//
// 每个阶段有完成标记：中断后重跑，已完成的阶段不重做，缺的阶段按记录补齐（Trade 的推进按周期幂等，同一期重发返回同一条记录）。
type Runner struct {
	Store    *Store
	Factor   FactorAPI
	Strategy StrategyAPI
	Trade    TradeAPI

	// PollInterval 是暂停时检查任务状态的间隔；0 取 2 秒。
	PollInterval time.Duration
	// Progress 在每期完成后调用（可为 nil）。
	Progress func(Run, StepTiming)
	// Logf 记录警告与重连（可为 nil）。
	Logf func(format string, args ...any)
}

// StepTiming 是一期各阶段的耗时（毫秒）。
type StepTiming struct {
	Period   time.Time
	Factor   int64
	Strategy int64
	Trade    int64
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func (r *Runner) poll() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return 2 * time.Second
}

// Run 开始或续跑一次回放，直到完成、暂停后被取消、失败或 ctx 结束；返回任务的最终状态。
// 同一个 replay_id 再次调用就是续跑；规格必须与第一次一致。
func (r *Runner) Run(ctx context.Context, spec Spec) (Run, error) {
	if err := validateSpec(&spec); err != nil {
		return Run{}, err
	}
	from, _ := time.Parse(time.RFC3339, spec.From)
	to, _ := time.Parse(time.RFC3339, spec.To)

	binding, err := r.Strategy.Bind(ctx, spec.InstanceID)
	if err != nil {
		return Run{}, fmt.Errorf("固定策略版本：%w", err)
	}
	run, created, err := r.Store.CreateRun(spec, 0)
	if err != nil {
		return Run{}, err
	}
	if !created {
		if !sameSpec(run.Spec, spec) {
			return run, fmt.Errorf("回放 %s 已存在且规格不同；续跑要用同样的参数，要换参数请换一个 replay_id", spec.ReplayID)
		}
		switch run.Status {
		case StatusCompleted:
			return run, nil
		case StatusCancelled:
			return run, errors.New("回放已被取消，不能续跑")
		}
		// 续跑：策略与因子定义必须与第一次固定下来的一致，否则前后的结果不可比。
		if run.Binding.DSLHash != "" {
			if drift := bindingDrift(run.Binding, binding); drift != "" {
				return run, fmt.Errorf("回放期间策略或因子定义发生了变化（%s），不能续跑，请重新发起回放", drift)
			}
		}
	}
	if err := r.Store.SetStatus(spec.ReplayID, StatusRunning, ""); err != nil {
		return Run{}, err
	}

	open, err := r.Factor.Open(ctx, FactorOpenRequest{
		ReplayID: spec.ReplayID, SetID: spec.SetID, From: spec.From, To: spec.To, ExtraPeriods: binding.PreviousDepth,
		Quote: spec.Quote, WriteFactors: spec.WriteFactors,
	})
	if err != nil {
		return r.fail(spec.ReplayID, fmt.Errorf("建立因子回放会话：%w", err))
	}
	if drift := factorDrift(binding.Factors, open.Factors); drift != "" {
		return r.fail(spec.ReplayID, fmt.Errorf("策略引用的因子与因子集合 %s 里的定义不一致（%s）", spec.SetID, drift))
	}
	if len(open.Excluded) > 0 {
		r.logf("回放 %s：%d 个标的的本地数据不满足回放要求，已排除（例如 %s：%s）", spec.ReplayID, len(open.Excluded), open.Excluded[0].Subject, open.Excluded[0].Reason)
	}
	step := time.Duration(open.StepSecs) * time.Second
	if step <= 0 {
		return r.fail(spec.ReplayID, errors.New("因子引擎没有返回周期时长"))
	}
	total := int(to.Sub(from) / step)
	if err := r.Store.SetBinding(spec.ReplayID, binding, total); err != nil {
		return Run{}, err
	}
	if _, err := r.Trade.CreateSession(ctx, TradeCreateRequest{ReplayID: spec.ReplayID, InitialCash: spec.InitialCash, FeeRate: spec.FeeRate, SlippageBPS: spec.SlippageBPS}); err != nil {
		return r.fail(spec.ReplayID, fmt.Errorf("建立 Trade 纸面账本会话：%w", err))
	}

	done, err := r.Store.CompletedPeriods(spec.ReplayID)
	if err != nil {
		return Run{}, err
	}
	var previous Record
	hasPrevious := false
	for t := from; t.Before(to); t = t.Add(step) {
		if err := r.waitIfPaused(ctx, spec.ReplayID); err != nil {
			return r.finish(spec.ReplayID, err)
		}
		rec, err := r.processPeriod(ctx, spec, binding, step, t, previous, hasPrevious)
		if err != nil {
			return r.finish(spec.ReplayID, err)
		}
		previous, hasPrevious = rec.record, true
		if !rec.alreadyDone {
			done++
		}
		if err := r.Store.SetProgress(spec.ReplayID, done, t.Unix()); err != nil {
			return Run{}, err
		}
		if r.Progress != nil {
			run, _ := r.Store.GetRun(spec.ReplayID)
			r.Progress(run, StepTiming{Period: t, Factor: rec.record.FactorMillis, Strategy: rec.record.StrategyMs, Trade: rec.record.TradeMillis})
		}
	}
	if err := r.Factor.Close(ctx, spec.ReplayID); err != nil {
		r.logf("关闭因子回放会话失败（引擎会按空闲超时自动清理）：%v", err)
	}
	if err := r.Trade.Close(ctx, spec.ReplayID); err != nil {
		r.logf("结束 Trade 回放会话失败：%v", err)
	}
	if err := r.Store.SetStatus(spec.ReplayID, StatusCompleted, ""); err != nil {
		return Run{}, err
	}
	return r.Store.GetRun(spec.ReplayID)
}

var errCancelled = errors.New("回放已被取消")

// finish 把执行中的错误落成任务状态：取消保持取消，其他错误记为失败（可续跑）。
func (r *Runner) finish(id string, err error) (Run, error) {
	if errors.Is(err, errCancelled) {
		run, _ := r.Store.GetRun(id)
		return run, err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("回放被中断（用同样的参数再次运行即可续跑）：%w", err)
	}
	return r.fail(id, err)
}

func (r *Runner) fail(id string, err error) (Run, error) {
	_ = r.Store.SetStatus(id, StatusFailed, err.Error())
	run, _ := r.Store.GetRun(id)
	return run, err
}

// waitIfPaused 在任务暂停时等待，取消时返回 errCancelled。
func (r *Runner) waitIfPaused(ctx context.Context, id string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		run, err := r.Store.GetRun(id)
		if err != nil {
			return err
		}
		switch run.Status {
		case StatusCancelled:
			return errCancelled
		case StatusPaused:
			timer := time.NewTimer(r.poll())
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		default:
			return nil
		}
	}
}

type periodOutcome struct {
	record      Record
	alreadyDone bool
}

// processPeriod 执行一期；已经完成的周期直接返回记录。
func (r *Runner) processPeriod(ctx context.Context, spec Spec, binding StrategyBinding, step time.Duration, t time.Time, previous Record, hasPrevious bool) (periodOutcome, error) {
	existing, err := r.Store.GetRecord(spec.ReplayID, t.Unix())
	if err != nil && !errors.Is(err, ErrNotFound) {
		return periodOutcome{}, err
	}
	if err == nil && existing.Complete {
		return periodOutcome{record: existing, alreadyDone: true}, nil
	}
	rec := existing
	if errors.Is(err, ErrNotFound) {
		// 上一期的策略状态：续跑时从上一期的记录读（它一定已经完成，因为周期按顺序推进）。
		var state json.RawMessage
		switch {
		case hasPrevious:
			state = previous.State
		case t.After(mustParse(spec.From)):
			prior, err := r.Store.GetRecord(spec.ReplayID, t.Add(-step).Unix())
			if err != nil {
				return periodOutcome{}, fmt.Errorf("读取上一期记录：%w", err)
			}
			state = prior.State
		}
		startedFactor := time.Now()
		frame, err := r.factorStep(ctx, spec, binding, t, false)
		if err != nil {
			return periodOutcome{}, err
		}
		factorMs := time.Since(startedFactor).Milliseconds()
		startedStrategy := time.Now()
		decision, err := r.Strategy.Evaluate(ctx, evaluateRequest(spec, binding, t, frame, state))
		if err != nil {
			return periodOutcome{}, fmt.Errorf("策略求值 %s：%w", t.Format(time.RFC3339), err)
		}
		advance := buildAdvance(spec.ReplayID, t.Add(step), decision, frame)
		rec = Record{ReplayID: spec.ReplayID, Period: t.Unix(), Decision: decision, State: decision.State, Advance: advance, FactorMillis: factorMs, StrategyMs: time.Since(startedStrategy).Milliseconds()}
		if err := r.Store.SaveRecord(rec); err != nil {
			return periodOutcome{}, err
		}
		if rec, err = r.Store.GetRecord(spec.ReplayID, t.Unix()); err != nil {
			return periodOutcome{}, err
		}
	}
	if !rec.TradeAcked {
		started := time.Now()
		if _, err := r.Trade.Advance(ctx, rec.Advance); err != nil {
			return periodOutcome{}, fmt.Errorf("Trade 推进 %s：%w", t.Format(time.RFC3339), err)
		}
		rec.TradeMillis = time.Since(started).Milliseconds()
		if err := r.Store.MarkTradeAcked(spec.ReplayID, t.Unix(), rec.TradeMillis); err != nil {
			return periodOutcome{}, err
		}
		rec.TradeAcked = true
	}
	if spec.WriteFactors && !rec.FactorWritten {
		if _, err := r.factorStep(ctx, spec, binding, t, true); err != nil {
			return periodOutcome{}, fmt.Errorf("写回因子结果 %s：%w", t.Format(time.RFC3339), err)
		}
		if err := r.Store.MarkFactorWritten(spec.ReplayID, t.Unix()); err != nil {
			return periodOutcome{}, err
		}
	}
	if err := r.Store.MarkComplete(spec.ReplayID, t.Unix()); err != nil {
		return periodOutcome{}, err
	}
	rec.Complete = true
	return periodOutcome{record: rec}, nil
}

// factorStep 取一期的因子帧；因子引擎重启后会话已经不在（404），重新建立会话再取一次。
func (r *Runner) factorStep(ctx context.Context, spec Spec, binding StrategyBinding, t time.Time, write bool) (FactorStepResponse, error) {
	request := FactorStepRequest{ReplayID: spec.ReplayID, Period: t.Format(time.RFC3339), SourceColumns: binding.SourceColumns, WriteFactors: write}
	frame, err := r.Factor.Step(ctx, request)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
		r.logf("回放 %s：因子引擎里的会话不存在（引擎重启？），重新建立", spec.ReplayID)
		if _, openErr := r.Factor.Open(ctx, FactorOpenRequest{ReplayID: spec.ReplayID, SetID: spec.SetID, From: spec.From, To: spec.To, ExtraPeriods: binding.PreviousDepth, Quote: spec.Quote, WriteFactors: spec.WriteFactors}); openErr != nil {
			return FactorStepResponse{}, fmt.Errorf("重新建立因子回放会话：%w", openErr)
		}
		frame, err = r.Factor.Step(ctx, request)
	}
	if err != nil {
		return FactorStepResponse{}, fmt.Errorf("因子引擎 %s：%w", t.Format(time.RFC3339), err)
	}
	return frame, nil
}

func evaluateRequest(spec Spec, binding StrategyBinding, t time.Time, frame FactorStepResponse, state json.RawMessage) StrategyEvaluateRequest {
	rows := make(map[string]StrategyRow, len(frame.Subjects))
	for subject, s := range frame.Subjects {
		rows[subject] = StrategyRow{Source: s.Source, SourcePrev: s.SourcePrev, Factors: s.Factors, FactorsPrev: s.FactorsPrev, AgeBars: s.AgeBars}
	}
	return StrategyEvaluateRequest{
		ReplayID: spec.ReplayID, InstanceID: spec.InstanceID, DSLHash: binding.DSLHash, Period: t.Format(time.RFC3339),
		Rows: rows, FactorStates: frame.States, State: state,
	}
}

// buildAdvance 由决策与报价组装 ReplayAdvance：决策时刻 = K 线起点 + 周期；ok 的决策是 UPDATE_TARGET，跳过是 SKIP。
func buildAdvance(replayID string, decisionAt time.Time, decision StrategyDecision, frame FactorStepResponse) TradeAdvanceRequest {
	quotes := make(map[string]TradeQuote, len(frame.Subjects))
	for subject, s := range frame.Subjects {
		quote := TradeQuote{}
		if s.Close > 0 {
			quote.Mark = decimal(s.Close)
		}
		if s.Exec > 0 {
			quote.Exec = decimal(s.Exec)
		}
		if quote.Mark != "" || quote.Exec != "" {
			quotes[subject] = quote
		}
	}
	out := TradeAdvanceRequest{ReplayID: replayID, Period: decisionAt.UnixMilli(), Quotes: quotes, Decision: TradeDecision{Kind: "SKIP"}}
	if decision.Status == "ok" {
		out.Decision = TradeDecision{Kind: "UPDATE_TARGET", Targets: decision.Targets}
	}
	return out
}

func decimal(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func mustParse(value string) time.Time {
	t, _ := time.Parse(time.RFC3339, value)
	return t
}

func validateSpec(spec *Spec) error {
	spec.ReplayID, spec.SetID, spec.InstanceID = strings.TrimSpace(spec.ReplayID), strings.TrimSpace(spec.SetID), strings.TrimSpace(spec.InstanceID)
	if spec.ReplayID == "" || spec.SetID == "" || spec.InstanceID == "" {
		return errors.New("replay_id、set_id、instance_id 都是必填项")
	}
	from, err := time.Parse(time.RFC3339, spec.From)
	if err != nil {
		return fmt.Errorf("from：%w", err)
	}
	to, err := time.Parse(time.RFC3339, spec.To)
	if err != nil {
		return fmt.Errorf("to：%w", err)
	}
	if !from.Before(to) {
		return errors.New("from 必须早于 to")
	}
	spec.From, spec.To = from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)
	if spec.Quote == "" {
		spec.Quote = "next_open"
	}
	if spec.Quote != "next_open" && spec.Quote != "close" {
		return errors.New("quote 只能是 next_open 或 close")
	}
	return nil
}

func sameSpec(a, b Spec) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

// bindingDrift 比较续跑时重新取得的绑定与第一次固定的绑定。
func bindingDrift(pinned, now StrategyBinding) string {
	if pinned.DSLHash != now.DSLHash {
		return fmt.Sprintf("策略定义 %s → %s", pinned.DSLHash, now.DSLHash)
	}
	for id, hash := range pinned.Factors {
		if now.Factors[id] != hash {
			return fmt.Sprintf("因子 %s 的指纹 %s → %s", id, hash, now.Factors[id])
		}
	}
	return ""
}

// factorDrift 检查策略引用的因子在因子集合里存在且指纹一致。
func factorDrift(strategy map[string]string, set []FactorInfo) string {
	byID := make(map[string]string, len(set))
	for _, f := range set {
		byID[f.FactorID] = f.DefinitionHash
	}
	ids := make([]string, 0, len(strategy))
	for id := range strategy {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		hash, ok := byID[id]
		switch {
		case !ok:
			return fmt.Sprintf("因子 %s 不在集合里", id)
		case hash != strategy[id]:
			return fmt.Sprintf("因子 %s 的指纹 %s（集合）≠ %s（策略）", id, hash, strategy[id])
		}
	}
	return ""
}

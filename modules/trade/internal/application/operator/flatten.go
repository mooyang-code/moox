package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const flattenActionTimeout = 30 * time.Minute

type FlattenCommand struct {
	SpaceID          string
	ActionID         string
	LogicalAccountID string
	// TradingAccountID 非空时只平组合里的这一个执行账户，其他成员的单和仓不动。
	TradingAccountID string
	Reason           string
	// InstrumentID 非空时只处理这个标的（内部标的 ID）：撤单、平仓、剩余统计都只看它，不暂停组合账户。
	InstrumentID string
	// Exchange 非空时只处理组合里属于这个交易所的成员（内部标的 ID 跨交易所同名，混合交易所的组合不能误平另一家）。
	// 只和 InstrumentID 一起用：下架清仓按（交易所、市场、代码）定位标的。
	Exchange string
	// ReduceRatio 在 0 到 1 之间时只减掉这个比例的持仓；为零或 1 表示全部清掉。
	ReduceRatio shared.Decimal
	// DeadlineAt 是下架清仓的截止时间（毫秒，事件中心按下架时间传入）；大于 0 时到点不再下单，动作记 PARTIAL。
	DeadlineAt int64

	// attemptStart 是这一轮执行的起点（毫秒，精确到秒）：动作每次重新进入运行都重新记，下架清仓只统计起点之后的子单是否被拒、是否连续没成交。
	attemptStart int64
}

type RemainingPosition struct {
	InstrumentID string `json:"instrument_id,omitempty"`
	Asset        string `json:"asset,omitempty"`
	Quantity     string `json:"quantity"`
	Reason       string `json:"reason"`
	// final 为真表示重试也平不掉（见 flattenReason）。
	final bool
}

// flattenReason 是一条剩余仓位的原因。final 为真表示同样的条件下重试只会得到同样的结果：
// 无可交易标的、数量不符合交易所规则、零成交就终结的子单、永久校验错误等；没列为终结的一律可重试。
type flattenReason struct {
	text  string
	final bool
}

// flattenFinal 标记终结类错误，本轮只剩这类错误和终结原因时清仓直接结束为部分完成。
type flattenFinal struct{ error }

func (e flattenFinal) Unwrap() error { return e.error }

func isFlattenFinal(err error) bool {
	_, final := err.(flattenFinal)
	return final
}

type FlattenAccountResult struct {
	TradingAccountID string              `json:"trading_account_id"`
	Status           string              `json:"status"`
	ChildOrderIDs    []string            `json:"child_order_ids,omitempty"`
	Remaining        []RemainingPosition `json:"remaining_positions,omitempty"`
	// Dust 是下不了单的现货零头，不算剩余，不阻止完成。
	Dust  []RemainingPosition `json:"dust_positions,omitempty"`
	Error string              `json:"error,omitempty"`
	// Unblocked 为真表示下架清仓发现标的已被解除禁止而提前结束（不算失败）。
	Unblocked bool `json:"unblocked,omitempty"`
	cause     error
	// decided 为真时由 retry 决定动作是否继续；否则按 cause 是否只有业务错误判断。
	decided bool
	retry   bool
	// pacingUntil 是下架清仓下一笔 IOC 最早能下的时间（零值表示没有）：一笔 IOC 发出后记成现在加间隔，间隔未到被挡住时记成间隔结束。
	pacingUntil time.Time
	// pacedOnly 为真表示这一轮只被“间隔未到”挡住、什么都没做。
	pacedOnly bool
	// quotes 是本轮现货清仓拿到的报价（按资产），收尾判断零头时复用，不再逐个请求。
	quotes map[string]shared.Decimal
}

type FlattenResult struct {
	Action   store.OperatorActionRecord `json:"-"`
	Accounts []FlattenAccountResult     `json:"accounts"`
	// AttemptStartedAt 是这一轮执行的起点（毫秒）：被拒和连续未成交的统计只看起点之后创建的子单，
	// 同一个动作号在 FAILED、PARTIAL 之后重试时会重新记，让重试真的重新来过。
	AttemptStartedAt int64 `json:"attempt_started_at,omitempty"`
	// Unblocked 为真表示下架清仓因为标的已解除禁止而提前结束：动作记 COMPLETED，调用方不要重试。
	Unblocked bool `json:"unblocked,omitempty"`
	// ReduceTargets 是减仓发起时写定的各持仓目标数量（键：执行账户|标的 ID），之后各轮都收敛到它。
	ReduceTargets map[string]string `json:"reduce_targets,omitempty"`
	// ReduceDone 是已经算过减仓目标的执行账户：每个账户的目标只在第一次算，之后沿用。
	ReduceDone []string `json:"reduce_done_accounts,omitempty"`
}

type flattenRequest struct {
	LogicalAccountID string `json:"logical_account_id"`
	TradingAccountID string `json:"trading_account_id,omitempty"`
	InstrumentID     string `json:"instrument_id,omitempty"`
	Exchange         string `json:"exchange,omitempty"`
	ReduceRatio      string `json:"reduce_ratio,omitempty"`
	DeadlineAt       int64  `json:"deadline_at,omitempty"`
}

// ErrNotInLogicalAccount 表示执行账户不属于任何组合账户：MooX 只通过组合账户交易，这样的账户没有可平的仓。
var ErrNotInLogicalAccount = errors.New("执行账户不属于任何组合账户")

// FlattenTradingAccount 只平组合账户里的一个执行账户：撤这个账户的单、平这个账户的仓，组合账户照样暂停。
// 同组合只允许一个运行中清仓、锁的短超时、终结与可重试的分类，都和组合清仓相同。
func (s *Service) FlattenTradingAccount(ctx context.Context, command FlattenCommand) (FlattenResult, error) {
	if err := s.validate(); err != nil {
		return FlattenResult{}, err
	}
	if blank(command.TradingAccountID) {
		return FlattenResult{}, ErrInvalidCommand
	}
	logical, _, err := s.Store.FindLogicalAccountMembership(ctx, command.SpaceID, command.TradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return FlattenResult{}, ErrNotInLogicalAccount
	}
	if err != nil {
		return FlattenResult{}, err
	}
	command.LogicalAccountID = logical.LogicalAccountID
	return s.FlattenLogicalAccount(ctx, command)
}

// decodeFlattenRequest 从动作的请求 JSON 恢复清仓命令（工作线程恢复时用）。
func decodeFlattenRequest(action store.OperatorActionRecord) (FlattenCommand, error) {
	var request flattenRequest
	if err := json.Unmarshal([]byte(action.RequestJSON), &request); err != nil {
		return FlattenCommand{}, err
	}
	command := FlattenCommand{
		SpaceID: action.SpaceID, ActionID: action.ActionID,
		LogicalAccountID: action.LogicalAccountID, TradingAccountID: request.TradingAccountID,
		InstrumentID: request.InstrumentID, Exchange: request.Exchange, Reason: action.Reason,
		DeadlineAt: request.DeadlineAt,
	}
	if request.ReduceRatio != "" {
		ratio, err := shared.ParseDecimal(request.ReduceRatio)
		if err != nil {
			return FlattenCommand{}, err
		}
		command.ReduceRatio = ratio
	}
	return command, nil
}

func (s *Service) FlattenLogicalAccount(
	ctx context.Context,
	command FlattenCommand,
) (FlattenResult, error) {
	if err := s.validate(); err != nil {
		return FlattenResult{}, err
	}
	if err := command.validateScope(); err != nil {
		return FlattenResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.flattenTimeout())
	defer cancel()
	requestJSON, err := flattenRequestJSON(command)
	if err != nil {
		return FlattenResult{}, err
	}
	expectedAction := store.OperatorActionRecord{
		SpaceID: command.SpaceID, ActionID: command.ActionID,
		LogicalAccountID: command.LogicalAccountID,
		ActionType:       string(command.actionType()), Reason: strings.TrimSpace(command.Reason),
		RequestJSON: requestJSON, Status: "RUNNING",
	}
	existing, found, err := s.existingAction(ctx, expectedAction)
	if err != nil {
		return FlattenResult{}, err
	}
	if found && existing.Status == string(operatordomain.StatusCompleted) {
		return decodeFlattenResult(existing)
	}
	resuming := found && existing.Status == string(operatordomain.StatusRunning)
	// 同一组合只允许一个运行中的清仓，否则两个清仓会互相撤对方的子单。运行中的清仓多半正持着锁，所以锁前先查一次；
	// 请求的动作本身正在运行（工作线程用原动作号恢复）时不查，否则遗留的两个运行中动作会互相指向对方。
	if !resuming {
		if other, ok, err := s.otherRunningFlatten(ctx, command); err != nil || ok {
			return other, err
		}
	}
	lockCtx, lockCancel := context.WithTimeout(ctx, flattenLockWait)
	unlock, err := s.Store.LockLogicalAccountContext(lockCtx, command.SpaceID, command.LogicalAccountID)
	lockCancel()
	if err != nil {
		if resuming {
			// 运行中的动作由工作线程继续，调用方看到“运行中”即可。
			current, diagnosticErr := s.deferActionLock(ctx, existing, err)
			if _, deferred := diagnosticErr.(submissionDeferredError); deferred {
				diagnosticErr = nil
			}
			return FlattenResult{Action: current}, diagnosticErr
		}
		return FlattenResult{Action: existing}, ErrLogicalAccountBusy
	}
	defer unlock()
	existing, found, err = s.existingAction(ctx, expectedAction)
	if err != nil {
		return FlattenResult{Action: existing}, err
	}
	if found && existing.Status == string(operatordomain.StatusCompleted) {
		return decodeFlattenResult(existing)
	}
	var action store.OperatorActionRecord
	var other *store.OperatorActionRecord
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		// 锁内再查一次，防止两个请求同时通过锁前的检查；恢复自身动作时同样不查。
		if !resuming {
			for _, actionType := range flattenActionTypes {
				running, err := tx.RunningOperatorActionsOfType(command.SpaceID, command.LogicalAccountID, actionType)
				if err != nil {
					return err
				}
				for index := range running {
					if running[index].ActionID != command.ActionID && flattenConflict(running[index], command) {
						other = &running[index]
						return nil
					}
				}
			}
		}
		// 整组清仓和减仓先暂停组合；只平一个标的不动组合的自动执行状态。
		if command.pausesLogicalAccount() {
			if err := tx.SetLogicalAccountAutomation(
				command.SpaceID,
				command.LogicalAccountID,
				"PAUSED",
				strings.TrimSpace(command.Reason),
			); err != nil {
				return err
			}
		}
		var ensureErr error
		action, _, ensureErr = tx.EnsureOperatorAction(expectedAction)
		if ensureErr != nil {
			return ensureErr
		}
		if action.Status != string(operatordomain.StatusRunning) {
			action.Status = string(operatordomain.StatusRunning)
			action.LastError = ""
			return tx.UpdateOperatorAction(action)
		}
		return nil
	})
	if err != nil {
		return FlattenResult{Action: action}, err
	}
	if other != nil {
		result, _, err := runningFlattenFor(*other, command)
		return result, err
	}
	members, err := s.Store.ListLogicalAccountMembers(
		ctx, command.SpaceID, command.LogicalAccountID, true,
	)
	if err != nil {
		return FlattenResult{Action: action}, err
	}
	if target := strings.TrimSpace(command.TradingAccountID); target != "" {
		selected := members[:0]
		for _, member := range members {
			if member.TradingAccountID == target {
				selected = append(selected, member)
			}
		}
		if len(selected) == 0 {
			// 成员在发起之后被移出了组合：没有可平的，按失败结束。
			result := FlattenResult{Action: action}
			return result, errors.Join(ErrNotInLogicalAccount, s.persistFlattenProgress(ctx, &result, string(operatordomain.StatusFailed)))
		}
		members = selected
	}
	if exchangeName := strings.TrimSpace(command.Exchange); exchangeName != "" {
		// 只处理这个交易所的成员；发起之后成员被移出或换了交易所，没有可处理的就按失败结束。
		selected := members[:0]
		for _, member := range members {
			account, accountErr := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
			if accountErr != nil {
				return FlattenResult{Action: action}, accountErr
			}
			if account.Exchange == exchangeName {
				selected = append(selected, member)
			}
		}
		if len(selected) == 0 {
			result := FlattenResult{Action: action}
			return result, errors.Join(ErrNotInLogicalAccount, s.persistFlattenProgress(ctx, &result, string(operatordomain.StatusFailed)))
		}
		members = selected
	}
	// 这一轮执行的起点：运行中的动作被工作线程反复恢复时沿用原来的起点；从 FAILED、PARTIAL 重新进入运行（同一个动作号重试）、
	// 或刚创建时记现在（精确到秒，操作记录的创建时间只到秒）。
	attemptStart := time.Now().Truncate(time.Second).UnixMilli()
	if resuming {
		if previous, _ := decodeFlattenResult(existing); previous.AttemptStartedAt > 0 {
			attemptStart = previous.AttemptStartedAt
		} else if !existing.CreatedAt.IsZero() {
			attemptStart = existing.CreatedAt.UnixMilli()
		}
	}
	command.attemptStart = attemptStart
	result := FlattenResult{
		Action:           action,
		AttemptStartedAt: attemptStart,
		Accounts:         make([]FlattenAccountResult, 0, len(members)),
	}
	scope := flattenScope{instrumentID: strings.TrimSpace(command.InstrumentID), reducing: command.reducing()}
	if command.actionType() == operatordomain.ActionDelistFlatten {
		scope.stillBlocked = func(ctx context.Context, tradingAccountID string) (bool, error) {
			return s.delistStillBlocked(ctx, command.SpaceID, tradingAccountID, scope.instrumentID)
		}
	}
	if scope.instrumentID != "" {
		// 只平一个标的时，标的要能在被选中的成员里解析到：解析不到不能静默当作平完了。
		if err := s.requireInstrumentInMembers(ctx, command, members); err != nil {
			return result, errors.Join(err, s.persistFlattenProgress(ctx, &result, string(operatordomain.StatusFailed)))
		}
	}
	if scope.reducing {
		// 减仓的目标数量每个账户只算一次：在账户撤单、确认、同步之后算好并落库，再下子单；
		// 之后各轮（包括 FAILED、PARTIAL 后用同一个动作号重放和重启恢复）都沿用，不会再减一次。
		var decodeErr error
		if found {
			if previous, _ := decodeFlattenResult(existing); len(previous.ReduceDone) > 0 || len(previous.ReduceTargets) > 0 {
				result.ReduceTargets, result.ReduceDone = previous.ReduceTargets, previous.ReduceDone
			}
		}
		scope.targets, decodeErr = decodeTargets(result.ReduceTargets)
		if decodeErr != nil {
			return result, decodeErr
		}
		for _, accountID := range result.ReduceDone {
			scope.targets[scopeKey(accountID, "")] = shared.Zero()
		}
		targets := scope.targets
		scope.ensureTargets = func(ctx context.Context, tradingAccountID string, settings fundsettings.Settings) error {
			marker := scopeKey(tradingAccountID, "")
			if _, done := targets[marker]; done {
				return nil
			}
			computed, err := s.reduceTargetsForAccount(ctx, command, tradingAccountID, settings)
			if err != nil {
				return err
			}
			for key, value := range computed {
				targets[key] = value
			}
			targets[marker] = shared.Zero()
			result.ReduceTargets = encodeTargets(targets)
			result.ReduceDone = appendUnique(result.ReduceDone, tradingAccountID)
			return s.persistFlattenProgress(ctx, &result, "RUNNING")
		}
	}
	// 过了截止时间（下架时间）：标的已经下架，不再下单，把动作按部分完成收尾；上一轮记下的各账户进度保留。
	if s.pastDeadline(command) {
		if resuming {
			if previous, _ := decodeFlattenResult(existing); len(previous.Accounts) > 0 {
				result.Accounts = previous.Accounts
			}
		}
		if err := s.persistFlattenProgress(ctx, &result, string(operatordomain.StatusPartial)); err != nil {
			return result, err
		}
		if command.actionType() == operatordomain.ActionDelistFlatten && s.Wake != nil {
			s.Wake()
		}
		return result, nil
	}
	// 预算从收到请求算起，等锁用掉的时间要扣除，加上收尾不能超过网关的调用超时。
	budgetEnd, _ := ctx.Deadline()
	executable := 0
	var sharedErrors []error
	retry := false
	for index, member := range members {
		// 每个账户分到剩余时间的一份，前面的账户耗尽预算时后面的账户仍能执行。
		deadline := time.Now().Add(time.Until(budgetEnd) / time.Duration(len(members)-index))
		accountResult, wasExecutable := s.flattenAccount(
			ctx, command, scope, member.TradingAccountID, deadline,
		)
		// 本账户的预算到点时，正在做的数据库或接口调用会带着原样的超时错误回来：那是预算用完，不是共享的故障，
		// 记成本账户的预算错误（可重试），不能当作共享错误让整次调用失败。
		if accountResult.cause != nil && errors.Is(accountResult.cause, context.DeadlineExceeded) && !time.Now().Before(deadline) {
			accountResult.cause = &orderapp.AccountExecutionError{TradingAccountID: member.TradingAccountID, Operation: "flatten_budget", Err: accountResult.cause}
		}
		if wasExecutable {
			executable++
		}
		needsRetry := accountResult.retry
		if !accountResult.decided {
			needsRetry = accountResult.cause != nil && !submissionBusinessError(accountResult.cause)
		}
		if accountResult.cause != nil {
			if accountResult.Error == "" {
				accountResult.Error = accountResult.cause.Error()
			}
			if !allSubmissionErrors(accountResult.cause, func(err error) bool {
				return submissionAccountError(err) || submissionBusinessError(err) || isFlattenFinal(err)
			}) {
				sharedErrors = append(sharedErrors, accountResult.cause)
			}
		}
		if needsRetry {
			retry = true
			if accountResult.Status == "COMPLETED" {
				accountResult.Status = "RUNNING"
			}
		}
		result.Accounts = append(result.Accounts, accountResult)
		result.Unblocked = result.Unblocked || accountResult.Unblocked
		if err := s.persistFlattenProgress(ctx, &result, "RUNNING"); err != nil {
			return result, errors.Join(errors.Join(sharedErrors...), err)
		}
	}
	status := flattenStatus(result.Accounts, executable)
	if retry {
		status = "RUNNING"
		// 账户一直异常时清仓不能永远运行，否则恢复自动执行会被挡住。剩余持仓已写入结果，要继续就重新发起。
		if s.roundOlderThan(result, flattenActionTimeout) || s.pastDeadline(command) {
			status = "PARTIAL"
		}
	}
	if err := s.persistFlattenProgress(ctx, &result, status); err != nil {
		return result, errors.Join(errors.Join(sharedErrors...), err)
	}
	// 下架清仓结束（完成、部分完成、失败）后唤醒目标：已收敛、已阻塞的目标不被定时器扫描，
	// 不唤醒的话周期长的策略要等下一份目标才恢复（解除禁止提前结束时尤其如此）。
	if status != string(operatordomain.StatusRunning) && command.actionType() == operatordomain.ActionDelistFlatten && s.Wake != nil {
		s.Wake()
	}
	return result, errors.Join(sharedErrors...)
}

// reasonArbitrageHedgeLeg 是只平一个标的而它正是套利底仓对冲腿时的剩余原因。
const reasonArbitrageHedgeLeg = "套利底仓的永续对冲腿，不自动平仓，需要人工处理"

// pastDeadline 报告下架清仓是否已过截止时间（下架时间）。
func (s *Service) pastDeadline(command FlattenCommand) bool {
	if command.DeadlineAt <= 0 {
		return false
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return now.UnixMilli() >= command.DeadlineAt
}

// roundOlderThan 判断这一轮执行（从 AttemptStartedAt 起）是否已经超过期限：同一个动作号在 FAILED、PARTIAL 之后重试要重新计时，
// 不能按动作创建时间算（创建很久之后的重试一次只跑一轮就记 PARTIAL）；没有起点（旧记录）时按动作创建时间。
func (s *Service) roundOlderThan(result FlattenResult, age time.Duration) bool {
	if result.AttemptStartedAt <= 0 {
		return s.actionOlderThan(result.Action, age)
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return now.Sub(time.UnixMilli(result.AttemptStartedAt)) >= age
}

// errWaitingBalance：账户在等余额确认，本轮暂停下单，属可重试。
var errWaitingBalance = errors.New("账户等待余额确认，本轮暂停下单")

// flattenLockWait 是清仓等组合账户锁的时长。锁是轮询抢占，目标执行器一整轮都持锁，太短实盘下容易报“正忙”。
const flattenLockWait = 2500 * time.Millisecond

// ErrLogicalAccountBusy 表示组合账户锁被占用，这次清仓没有执行。
var ErrLogicalAccountBusy = errors.New("组合账户正忙，稍后再发起清仓")

// otherRunningFlatten 返回同一组合里另一个运行中的清仓。
func (s *Service) otherRunningFlatten(ctx context.Context, command FlattenCommand) (FlattenResult, bool, error) {
	running, err := s.Store.ListRunningOperatorActions(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		return FlattenResult{}, false, err
	}
	for _, action := range running {
		if action.ActionID != command.ActionID && flattenConflict(action, command) {
			return runningFlattenFor(action, command)
		}
	}
	return FlattenResult{}, false, nil
}

// flattenActionTypes 是清仓这一族动作：整组清仓、按比例减仓、单标的下架清仓。
var flattenActionTypes = []string{
	string(operatordomain.ActionFlatten), string(operatordomain.ActionReduce), string(operatordomain.ActionDelistFlatten),
}

// flattenConflict 报告另一个运行中的动作是否和这次请求冲突：整组清仓和减仓与这一族的任何运行中动作冲突；
// 单标的清仓只和整组动作、同一交易所同一标的的单标的清仓冲突，不同标的的单标的清仓不互相冲突。
func flattenConflict(other store.OperatorActionRecord, command FlattenCommand) bool {
	if !slices.Contains(flattenActionTypes, other.ActionType) {
		return false
	}
	if command.actionType() != operatordomain.ActionDelistFlatten || other.ActionType != string(operatordomain.ActionDelistFlatten) {
		return true
	}
	var request flattenRequest
	_ = json.Unmarshal([]byte(other.RequestJSON), &request)
	return request.InstrumentID == strings.TrimSpace(command.InstrumentID) && request.Exchange == strings.TrimSpace(command.Exchange)
}

// runningFlattenFor 决定另一个运行中清仓能不能代表这次请求：整组清仓覆盖所有成员，同一账户的清仓覆盖这个账户，
// 返回它的进度；只平别的成员的清仓不覆盖这次请求，返回“组合账户正忙”，否则前端会把那次的完成当成本账户平完了。
func runningFlattenFor(other store.OperatorActionRecord, command FlattenCommand) (FlattenResult, bool, error) {
	var request flattenRequest
	_ = json.Unmarshal([]byte(other.RequestJSON), &request)
	// 下架清仓遇到别的动作号正在平同一个标的：返回忙，不返回别人的动作（新号没有创建，调用方稍后再试）。
	// 演练（只看模拟盘）和真实下架同时平同一个模拟盘组合时会遇到。
	if command.actionType() == operatordomain.ActionDelistFlatten {
		return FlattenResult{}, true, ErrLogicalAccountBusy
	}
	sameKind := other.ActionType == string(command.actionType()) && request.InstrumentID == strings.TrimSpace(command.InstrumentID) &&
		request.Exchange == strings.TrimSpace(command.Exchange) && request.ReduceRatio == reduceRatioText(command)
	if sameKind && (request.TradingAccountID == "" || request.TradingAccountID == strings.TrimSpace(command.TradingAccountID)) {
		return runningFlattenResult(other), true, nil
	}
	return FlattenResult{Action: other}, true, ErrLogicalAccountBusy
}

// runningFlattenResult 返回运行中清仓已落库的进度；诊断信息不当作这次调用的错误。
func runningFlattenResult(action store.OperatorActionRecord) FlattenResult {
	result, _ := decodeFlattenResult(action)
	result.Action = action
	return result
}

// reduceRatioText 是命令里减仓比例写进请求 JSON 的文本；整组清仓为空。
func reduceRatioText(command FlattenCommand) string {
	if command.reducing() {
		return command.ReduceRatio.String()
	}
	return ""
}

func flattenRequestJSON(command FlattenCommand) (string, error) {
	if blank(command.SpaceID) || blank(command.ActionID) ||
		blank(command.LogicalAccountID) || blank(command.Reason) {
		return "", ErrInvalidCommand
	}
	request := flattenRequest{
		LogicalAccountID: command.LogicalAccountID,
		TradingAccountID: strings.TrimSpace(command.TradingAccountID),
		InstrumentID:     strings.TrimSpace(command.InstrumentID),
		Exchange:         strings.TrimSpace(command.Exchange),
		DeadlineAt:       command.DeadlineAt,
	}
	request.ReduceRatio = reduceRatioText(command)
	data, err := json.Marshal(request)
	return string(data), err
}

func (s *Service) flattenAccount(
	ctx context.Context,
	command FlattenCommand,
	scope flattenScope,
	tradingAccountID string,
	deadline time.Time,
) (FlattenAccountResult, bool) {
	result := FlattenAccountResult{
		TradingAccountID: tradingAccountID,
		Status:           "RUNNING",
	}
	recoveryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ctx = recoveryCtx
	if err := ctx.Err(); err != nil {
		result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_budget", Err: err})
		return result, false
	}
	// 资金设置每个账户每轮只读一次，平仓、剩余统计、减仓目标共用：读不出来（排除字段坏了、数据库忙）
	// 就不下单，不能悄悄按默认设置把底仓的对冲腿平掉。
	settings, err := s.fundSettingsOf(ctx, command.SpaceID, tradingAccountID)
	if err != nil {
		result.Status = "FAILED"
		result.Error = err.Error()
		result.cause = err
		if errors.Is(err, store.ErrFundSettingsUnreadable) {
			// 设置坏了重试也一样：直接以失败结束，不让动作一直 RUNNING、operator 每秒重试 30 分钟。
			result.cause = flattenFinal{err}
			result.decided, result.retry = true, false
		}
		return result, false
	}
	scope.settings = settings
	if s.stopIfUnblocked(ctx, command, scope, tradingAccountID, &result) {
		return result, true
	}
	if err := s.syncLight(ctx, tradingAccountID); err != nil {
		result.Status = "FAILED"
		result.Error = err.Error()
		result.cause = err
		return result, false
	}
	canceling, err := s.hasCancelableOrders(ctx, command.SpaceID, tradingAccountID, command.ActionID, scope)
	if err != nil {
		result.Status = "FAILED"
		result.Error = err.Error()
		result.cause = err
		return result, false
	}
	if err := s.cancelAccountOrders(
		ctx,
		command.SpaceID,
		tradingAccountID,
		command.ActionID,
		scope,
	); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil, &result, scope,
		)
		return result, false
	}
	if err := s.syncAfterCancel(ctx, tradingAccountID, canceling); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil, &result, scope,
		)
		return result, false
	}
	if err := s.confirmAccountCancellations(
		ctx, command.SpaceID, tradingAccountID, command.ActionID, scope,
	); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil, &result, scope,
		)
		return result, false
	}
	if err := s.recoverFlattenChildren(ctx, command, tradingAccountID, &result); err != nil {
		result.addError(err)
		result.Remaining = s.remainingForAccount(ctx, command.SpaceID, tradingAccountID, nil, &result, scope)
		return result, false
	}
	account, err := s.Store.GetTradingAccount(
		ctx, command.SpaceID, tradingAccountID,
	)
	if err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.cause = err
		return result, false
	}
	if scope.reducing && scope.ensureTargets != nil {
		if err := scope.ensureTargets(ctx, tradingAccountID, scope.settings); err != nil {
			result.addError(err)
			result.Status = "PARTIAL"
			result.Remaining = s.remainingForAccount(ctx, command.SpaceID, tradingAccountID, nil, &result, scope)
			return result, false
		}
	}
	attempts := s.flattenAttempts()
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_budget", Err: err})
			break
		}
		if attempt > 0 && !time.Now().Before(deadline) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_budget", Err: context.DeadlineExceeded})
			break
		}
		// 剩下的预算连发一笔单的余量都不够：这一轮到此为止，下一次调用接着来（不空转同步）。
		if attempt > 0 && time.Until(deadline) < flattenNewOrderReserve {
			break
		}
		// 上一笔 IOC 刚结束、两笔之间的间隔没到：直接睡到间隔结束再往下做，中间不同步。睡醒后还要等一个轮间等待、留一个发单余量，
		// 这些加起来超过本账户的预算就不睡了，结束这一轮。必须先睡再查名单：睡眠期间解除禁止，睡醒后不能再下一笔。
		if until := result.pacingUntil; attempt > 0 && !until.IsZero() {
			result.pacingUntil = time.Time{}
			if until.Add(s.retryWait()).Add(flattenNewOrderReserve).After(deadline) {
				break
			}
			if err := sleepUntil(ctx, until); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, &orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_wait", Err: err})
				break
			}
		}
		if attempt > 0 && s.stopIfUnblocked(ctx, command, scope, tradingAccountID, &result) {
			return result, true
		}
		if attempt > 0 {
			if err := s.waitFlattenRetry(ctx); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, &orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_wait", Err: err})
				break
			}
			if err := s.syncLight(ctx, tradingAccountID); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			canceling, err := s.hasCancelableOrders(ctx, command.SpaceID, tradingAccountID, command.ActionID, scope)
			if err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			if err := s.cancelAccountOrders(
				ctx, command.SpaceID, tradingAccountID, command.ActionID, scope,
			); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			if err := s.syncAfterCancel(ctx, tradingAccountID, canceling); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			if err := s.confirmAccountCancellations(
				ctx, command.SpaceID, tradingAccountID, command.ActionID, scope,
			); err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
			account, err = s.Store.GetTradingAccount(
				ctx, command.SpaceID, tradingAccountID,
			)
			if err != nil {
				result.Error = err.Error()
				result.cause = errors.Join(result.cause, err)
				break
			}
		}
		// 每轮只看本轮的错误：前几轮留下的“子单在途”之类不能让只剩终结原因的清仓继续空转。
		result.Error = ""
		result.cause = nil
		result.Dust = nil
		result.quotes = nil
		result.pacedOnly = false
		reasons := make(map[string]flattenReason)
		switch {
		case len(account.Markets) == 0:
			result.addError(flattenFinal{errors.New("账户没有开通任何市场")})
		case !execution.InTradingSession(s.Exchanges, account.Exchange, time.Now()):
			result.addError(flattenFinal{errors.New("当前不在交易时段")})
		default:
			// 持仓来自余额的市场（现货、股票）和来自持仓表的市场（合约）各清各的。
			if account.HasBalanceMarket() {
				s.flattenSpot(ctx, command, scope, account, &result, reasons)
			}
			if account.HasPositionMarket() {
				s.flattenSwap(ctx, command, scope, account, &result, reasons)
			}
		}
		if ctx.Err() != nil {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "flatten_deadline", Err: ctx.Err()})
			break
		}
		// 只被“IOC 间隔未到”挡住的这一轮什么都没做，不做收尾同步（下一轮睡醒后再同步）。
		if !result.pacedOnly {
			if err := s.syncLight(ctx, tradingAccountID); err != nil {
				result.Error = joinError(result.Error, err.Error())
				result.cause = errors.Join(result.cause, err)
			}
		}
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, reasons, &result, scope,
		)
		if err := s.confirmFlattenChildren(ctx, command, tradingAccountID, &result); err != nil {
			result.addError(err)
		}
		if result.Error == "" && len(result.Remaining) == 0 {
			if submissionAccountError(result.cause) {
				result.cause = nil
			}
			result.Status = "COMPLETED"
			result.decided = true
			return result, true
		}
		if !flattenRoundRetryable(result) {
			result.Status = "PARTIAL"
			result.decided = true
			return result, true
		}
	}
	// 走到这里是预算用完或本轮遇到可重试的问题，交给下一次运行继续。
	result.Status = "PARTIAL"
	result.decided, result.retry = true, true
	return result, true
}

// flattenRoundRetryable 判断本轮结束后还值不值得重试：剩余仓位有可重试原因，或本轮有非终结的错误。
func flattenRoundRetryable(result FlattenAccountResult) bool {
	for _, remaining := range result.Remaining {
		if !remaining.final {
			return true
		}
	}
	return !allSubmissionErrors(result.cause, isFlattenFinal)
}

func (s *Service) recoverFlattenChildren(ctx context.Context, command FlattenCommand, accountID string, result *FlattenAccountResult) error {
	orders, err := s.Store.ListActiveOrdersForAccount(ctx, command.SpaceID, accountID)
	if err != nil {
		return err
	}
	for _, child := range orders {
		if child.OwnerType != string(orderdomain.OwnerOperator) || child.OwnerID != command.ActionID || orderdomain.State(child.State).Terminal() {
			continue
		}
		result.ChildOrderIDs = appendUnique(result.ChildOrderIDs, child.OrderID)
		switch orderdomain.State(child.State) {
		case orderdomain.Pending:
			_, err = s.Orders.Submit(ctx, command.SpaceID, child.OrderID)
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			_, err = s.Orders.ResolveUnknown(ctx, command.SpaceID, child.OrderID)
		case orderdomain.Canceling, orderdomain.CancelUnknown:
			_, err = s.Orders.RecoverCancel(ctx, command.SpaceID, child.OrderID)
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return &orderapp.AccountExecutionError{TradingAccountID: accountID, Operation: "flatten_recovery", Err: ctx.Err()}
		}
	}
	return s.confirmFlattenChildren(ctx, command, accountID, result)
}

func (s *Service) confirmFlattenChildren(ctx context.Context, command FlattenCommand, accountID string, result *FlattenAccountResult) error {
	orders, err := s.Store.ListActiveOrdersForAccount(ctx, command.SpaceID, accountID)
	if err != nil {
		return err
	}
	for _, child := range orders {
		if child.OwnerType == string(orderdomain.OwnerOperator) && child.OwnerID == command.ActionID && !orderdomain.State(child.State).Terminal() {
			result.ChildOrderIDs = appendUnique(result.ChildOrderIDs, child.OrderID)
			return &orderapp.AccountExecutionError{TradingAccountID: accountID, Operation: "flatten_confirmation", Err: fmt.Errorf("flatten child %s remains %s", child.OrderID, child.State)}
		}
	}
	return nil
}

// hasCancelableOrders 报告清仓前是否有要撤的单：有撤单时撤后要读全部挂单确认，否则轻量同步就够。
func (s *Service) hasCancelableOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	scope flattenScope,
) (bool, error) {
	records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return false, err
	}
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() || !scope.orderInScope(current) ||
			current.OwnerType == string(orderdomain.OwnerOperator) && current.OwnerID == actionID {
			continue
		}
		return true, nil
	}
	return false, nil
}

// syncAfterCancel 撤过单时读全部挂单确认；没撤单时紧接着的那次轻量同步不会带来新信息，直接跳过。
func (s *Service) syncAfterCancel(ctx context.Context, tradingAccountID string, canceled bool) error {
	if canceled {
		return s.Syncer.SyncAccount(ctx, tradingAccountID)
	}
	return nil
}

// flattenNewOrderReserve 是剩余预算不足时不再发新单的余量，留给结果落库和收尾同步。
const flattenNewOrderReserve = time.Second

// waitFlattenReady 在每笔清仓子单前等账户就绪：实盘每笔成交都会关闭准入，等轻量同步确认余额后才能下下一笔。
// 等待中的未就绪不算错误；预算不够就返回 false，结束本轮，由下一轮继续。
func (s *Service) waitFlattenReady(ctx context.Context, spaceID, tradingAccountID string) bool {
	for {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < flattenNewOrderReserve {
			return false
		}
		account, err := s.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
		if err != nil {
			return false
		}
		if account.Ready {
			return true
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (s *Service) cancelAccountOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	scope flattenScope,
) error {
	records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	var cancelErrors []error
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() || !scope.orderInScope(current) ||
			current.OwnerType == string(orderdomain.OwnerOperator) &&
				current.OwnerID == actionID {
			continue
		}
		if err := s.stopOrder(ctx, current); err != nil {
			cancelErrors = append(cancelErrors, fmt.Errorf(
				"cancel order %s: %w",
				current.OrderID,
				err,
			))
		}
	}
	return errors.Join(cancelErrors...)
}

func (s *Service) confirmAccountCancellations(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	scope flattenScope,
) error {
	records, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return err
	}
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() || !scope.orderInScope(current) ||
			current.OwnerType == string(orderdomain.OwnerOperator) &&
				current.OwnerID == actionID {
			continue
		}
		return fmt.Errorf(
			"%w: order %s remains %s",
			ErrCancelUnconfirmed,
			current.OrderID,
			current.State,
		)
	}
	return nil
}

func (s *Service) flattenSwap(
	ctx context.Context,
	command FlattenCommand,
	scope flattenScope,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]flattenReason,
) {
	positions, err := s.Store.ListPositions(
		ctx, command.SpaceID, account.TradingAccountID, exchange.SymbolKey{},
	)
	if err != nil {
		result.addError(err)
		return
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	index := newInstrumentIndex(instruments, account.SettlementAsset)
	settings := scope.settings
	for _, position := range positions {
		quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
		if parseErr != nil {
			result.addError(parseErr)
			continue
		}
		if quantity.IsZero() {
			continue
		}
		symbolKey := exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)
		key := symbolKey.String()
		instrument, found := index.bySymbol[symbolKey]
		if !found {
			// 标的表里都没有它：收窄范围时无从判断是不是范围内的，跳过；整组清仓记终结原因。
			if !scope.narrow() {
				reasons[key] = flattenReason{text: "持仓没有对应的可交易标的", final: true}
			}
			continue
		}
		if !scope.includes(instrument.InstrumentID) {
			continue
		}
		// 套利底仓的永续对冲腿不参与清仓，与执行器一致：平掉它会留下没对冲的底仓现货。
		// 但只平这一个标的而它正是对冲腿（下架的恰好是对冲腿）时不能静默当作平完：记一条终结原因，
		// 动作以部分完成结束（剩余统计里报出终结原因）并告警，由人决定怎么处理。
		if settings.IsArbitrageBase(instrument.BaseAsset) {
			if scope.instrumentID != "" {
				log.Warnf("交易关注项：下架清仓的标的 %s 是套利底仓的永续对冲腿，不自动平仓，需要人工处理（账户 %s）",
					instrument.InstrumentID, account.TradingAccountID)
			}
			continue
		}
		// 减仓只处理登记过的持仓，并只减到目标数量；整组清仓的目标是零。
		target, inScope := scope.target(account.TradingAccountID, instrument.InstrumentID)
		if !inScope {
			continue
		}
		quantity = excess(quantity, target)
		if quantity.IsZero() {
			continue
		}
		// 范围内但已经不可交易（例如下架）：记终结原因，不空转。
		if !exposure.Tradable(instrument, account.SettlementAsset) {
			reasons[key] = flattenReason{text: "持仓没有对应的可交易标的", final: true}
			continue
		}
		// 永续平仓单 reduceOnly，不查最小名义金额，只有数量不符合交易所规则才平不掉。
		if reason := quantityRuleReason(quantity.Abs(), instrument); reason != "" {
			reasons[key] = flattenReason{text: reason, final: true}
			continue
		}
		side := exchange.SideSell
		if quantity.IsNegative() {
			side = exchange.SideBuy
		}
		if !s.waitFlattenReady(ctx, command.SpaceID, account.TradingAccountID) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_wait_ready", Err: errWaitingBalance})
			return
		}
		if err := s.placeOrContinueFlattenChild(
			ctx, command, account, instrument, side, quantity.Abs(), result,
		); err != nil {
			reasons[key] = flattenReason{text: err.Error(), final: isFlattenFinal(err)}
			if !result.notePacing(err) { // 间隔未到只是等一等，不是错误
				result.addError(err)
			}
		}
	}
}

func (s *Service) flattenSpot(
	ctx context.Context,
	command FlattenCommand,
	scope flattenScope,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]flattenReason,
) {
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	managed, err := s.Store.ManagedAssets(ctx, command.SpaceID, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	index := newInstrumentIndex(instruments, account.SettlementAsset)
	for _, balance := range account.SpotBalances() {
		if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
			continue
		}
		key := "asset:" + balance.Asset
		total, err := decimalOrZero(balance.Total)
		if err != nil {
			result.addError(err)
			continue
		}
		if total.Cmp(shared.Zero()) <= 0 {
			continue
		}
		instrument, found := index.spot[balance.Asset]
		if !found {
			if !scope.narrow() {
				reasons[key] = flattenReason{text: "资产没有对应结算资产的标的", final: true}
			}
			continue
		}
		if !scope.includes(instrument.InstrumentID) {
			continue
		}
		// 减仓只卖出超出目标数量的部分，整组清仓的目标是零。
		target, inScope := scope.target(account.TradingAccountID, instrument.InstrumentID)
		if !inScope {
			continue
		}
		total = excess(total, target)
		if total.Cmp(shared.Zero()) <= 0 {
			continue
		}
		if !exposure.Tradable(instrument, account.SettlementAsset) {
			reasons[key] = flattenReason{text: "资产没有对应结算资产的标的", final: true}
			continue
		}
		quote, quoteErr := s.Prices.LatestPrice(
			ctx, account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol),
		)
		price := shared.Zero()
		if quoteErr == nil {
			price = quote.Price
			if result.quotes == nil {
				result.quotes = make(map[string]shared.Decimal)
			}
			result.quotes[balance.Asset] = price
		}
		// 零头按总量判断，不算剩余，由剩余统计列进 Dust。
		if exposure.SpotDust(total, price, instrument) {
			continue
		}
		if quoteErr != nil {
			reasons[key] = flattenReason{text: quoteErr.Error()}
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_quote", Err: quoteErr})
			continue
		}
		available, err := decimalOrZero(balance.Available)
		if err != nil {
			result.addError(err)
			continue
		}
		step, _, ruleErr := baseQuantityRules(instrument)
		if ruleErr != nil {
			reasons[key] = flattenReason{text: ruleErr.Error(), final: true}
			result.addError(flattenFinal{ruleErr})
			continue
		}
		// 总量不是零头而可用余额卖不出去，说明还有余额被挂单占着，下一轮再试。
		// 减仓只卖超出目标数量的部分（此时 total 已经是超出部分）。
		if available.Cmp(total) > 0 {
			available = total
		}
		quantity := exposure.FloorToStep(available, step)
		if quantityRuleReason(quantity, instrument) != "" || exposure.SpotDust(quantity, price, instrument) {
			reasons[key] = flattenReason{text: "可用余额不足，部分余额仍被挂单占用"}
			continue
		}
		if !s.waitFlattenReady(ctx, command.SpaceID, account.TradingAccountID) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_wait_ready", Err: errWaitingBalance})
			return
		}
		if err := s.placeOrContinueFlattenChildWithQuote(
			ctx, command, account, instrument,
			exchange.SideSell, quantity, quote, result,
		); err != nil {
			reasons[key] = flattenReason{text: err.Error(), final: isFlattenFinal(err)}
			if !result.notePacing(err) {
				result.addError(err)
			}
		}
	}
}

func (s *Service) placeOrContinueFlattenChild(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	instrument store.InstrumentRecord,
	side exchange.Side,
	quantity shared.Decimal,
	result *FlattenAccountResult,
) error {
	quote, err := s.Prices.LatestPrice(
		ctx, account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol),
	)
	if err != nil {
		return &orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_quote", Err: err}
	}
	return s.placeOrContinueFlattenChildWithQuote(
		ctx, command, account, instrument, side, quantity, quote, result,
	)
}

func (s *Service) placeOrContinueFlattenChildWithQuote(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	instrument store.InstrumentRecord,
	side exchange.Side,
	quantity shared.Decimal,
	quote Quote,
	result *FlattenAccountResult,
) error {
	// 下架清仓用伪市价单（限价 + IOC，滑点 3%）：每一轮按最新报价重新下，零成交的 IOC 单不算终结，
	// 子单号带上第几次尝试，同一个动作号重试时不会撞上上一次已结束的子单。
	delist := command.actionType() == operatorDelistFlatten
	var history delistHistory
	if delist {
		var historyErr error
		history, historyErr = s.loadDelistHistory(ctx, command, account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
		if historyErr != nil {
			return historyErr
		}
	}
	attempt := history.finished
	clientOrderID := flattenClientOrderIDForSpec(
		command.ActionID,
		account.TradingAccountID,
		exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol),
		side,
		quantity,
		attempt,
	)
	existing, found, err := s.flattenChild(
		ctx, command.SpaceID, account.TradingAccountID,
		command.ActionID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol), clientOrderID,
	)
	if err != nil {
		return err
	}
	if found {
		result.ChildOrderIDs = appendUnique(
			result.ChildOrderIDs, existing.OrderID,
		)
		switch orderdomain.State(existing.State) {
		case orderdomain.Pending:
			_, err = s.Orders.Submit(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			_, err = s.Orders.ResolveUnknown(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Canceling, orderdomain.CancelUnknown:
			_, err = s.Orders.RecoverCancel(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Rejected, orderdomain.Canceled,
			orderdomain.PartiallyCanceled, orderdomain.Expired:
			ended := manualFailureError{message: fmt.Sprintf("existing flatten child ended %s", existing.State), cause: ErrInvalidCommand}
			// 零成交就终结的子单，同一单号重试只会得到同样结果；部分成交的等持仓同步后按新数量换单号再下。
			if filled, parseErr := decimalOrZero(existing.FilledQuantity); parseErr == nil && filled.IsZero() {
				return flattenFinal{ended}
			}
			return ended
		default:
			return nil
		}
	}
	if delist {
		// 要下新的一笔了：被拒绝、连续没成交封顶、离上一笔结束太近都先不下。
		if err := s.checkDelistAttempt(history); err != nil {
			return err
		}
	}
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: account.TradingAccountID,
			ClientOrderID:    clientOrderID,
			InstrumentID:     instrument.InstrumentID, Type: exchange.OrderTypeMarket,
			Side: side, Quantity: quantity,
		},
		ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
		ReducePositionOnly: exchange.RuleFor(instrument.MarketType).ReduceOnly,
		Owner: orderdomain.OrderOwner{
			Type: orderdomain.OwnerOperator, OwnerID: command.ActionID,
			LogicalAccountID: command.LogicalAccountID,
		},
	}
	spec.PositionSide = exchange.RuleFor(instrument.MarketType).PositionSide
	if delist {
		limit := delistLimitPrice(quote.Price, side, instrument.PriceTick)
		spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, exchange.FillPolicyIOC, &limit
	}
	placed, err := s.Orders.Place(ctx, command.SpaceID, spec)
	if err != nil {
		// 资金不足（预留不足时也返回它）和只减仓被拒可能只是持仓、余额还没同步，按可重试处理。
		if orderapp.IsPermanentValidationError(err) &&
			!errors.Is(err, orderapp.ErrInsufficientFunds) && !errors.Is(err, orderapp.ErrReduceOnly) {
			return flattenFinal{err}
		}
		return err
	}
	result.ChildOrderIDs = appendUnique(
		result.ChildOrderIDs, string(placed.ID),
	)
	_, err = s.Orders.Submit(ctx, command.SpaceID, string(placed.ID))
	// IOC 单发出后马上就结束了：下一笔最早时间直接记成现在加间隔，下一轮先睡再同步，不用再多跑一轮被间隔挡住的空轮。
	if err == nil && delist {
		if interval := s.delistAttemptInterval(); interval > 0 {
			if until := time.Now().Add(interval); until.After(result.pacingUntil) {
				result.pacingUntil = until
			}
		}
	}
	return err
}

func (s *Service) flattenChild(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	key exchange.SymbolKey,
	clientOrderID string,
) (store.OrderRecord, bool, error) {
	records, err := s.Store.ListOrdersForLane(
		ctx, spaceID, tradingAccountID, key,
	)
	if err != nil {
		return store.OrderRecord{}, false, err
	}
	var active []store.OrderRecord
	var matching *store.OrderRecord
	for _, current := range records {
		if current.OwnerType == string(orderdomain.OwnerOperator) &&
			current.OwnerID == actionID {
			if !orderdomain.State(current.State).Terminal() {
				active = append(active, current)
			}
			if current.ClientOrderID == clientOrderID {
				value := current
				matching = &value
			}
		}
	}
	if len(active) > 1 {
		return store.OrderRecord{}, false,
			fmt.Errorf("multiple active flatten children exist for %s", key)
	}
	if len(active) == 1 {
		return active[0], true, nil
	}
	if matching != nil {
		return *matching, true, nil
	}
	return store.OrderRecord{}, false, nil
}

func (s *Service) remainingForAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	reasons map[string]flattenReason,
	result *FlattenAccountResult,
	scope flattenScope,
) []RemainingPosition {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	account, err := s.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		result.addError(err)
		return []RemainingPosition{{
			Quantity: "unknown", Reason: err.Error(),
		}}
	}
	var remaining []RemainingPosition
	instruments, listErr := s.Store.ListInstrumentsForAccount(ctx, tradingAccountID)
	if listErr != nil {
		result.addError(listErr)
		return []RemainingPosition{{Quantity: "unknown", Reason: listErr.Error()}}
	}
	// 标的表索引和平仓用同一份：持仓所属标的按（市场，代码）查表，不用持仓记录里可能对不上的标的 ID。
	index := newInstrumentIndex(instruments, account.SettlementAsset)
	settings := scope.settings
	if account.HasPositionMarket() {
		positions, listErr := s.Store.ListPositions(
			ctx, spaceID, tradingAccountID, exchange.SymbolKey{},
		)
		if listErr != nil {
			result.addError(listErr)
			return []RemainingPosition{{
				Quantity: "unknown", Reason: listErr.Error(),
			}}
		}
		for _, position := range positions {
			quantity, parseErr := decimalOrZero(position.SignedQuantity)
			if parseErr != nil {
				result.addError(parseErr)
				continue
			}
			if quantity.IsZero() {
				continue
			}
			instrumentID := ""
			if instrument, found := index.bySymbol[exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)]; found {
				if settings.IsArbitrageBase(instrument.BaseAsset) {
					// 对冲腿不算整组清仓和减仓的剩余；只平这个标的而它正是对冲腿时，要作为剩余报出来。
					if scope.instrumentID == "" || !scope.includes(instrument.InstrumentID) {
						continue
					}
					remaining = append(remaining, RemainingPosition{
						InstrumentID: instrument.InstrumentID, Quantity: quantity.String(), Reason: reasonArbitrageHedgeLeg, final: true,
					})
					continue
				}
				instrumentID = instrument.InstrumentID
			} else if scope.narrow() {
				continue // 标的表里没有它，收窄范围时不算范围内
			} else {
				instrumentID = position.ExchangeSymbol
			}
			if !scope.includes(instrumentID) {
				continue
			}
			target, inScope := scope.target(tradingAccountID, instrumentID)
			if !inScope {
				continue
			}
			// 减仓时剩余指超出目标数量的部分。
			quantity = excess(quantity, target)
			if quantity.IsZero() {
				continue
			}
			reason := reasonFor(reasons, exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol).String(), "position remains after final sync")
			remaining = append(remaining, RemainingPosition{
				InstrumentID: instrumentID, Quantity: quantity.String(), Reason: reason.text, final: reason.final,
			})
		}
	}
	if account.HasBalanceMarket() {
		managed, managedErr := s.Store.ManagedAssets(ctx, spaceID, tradingAccountID)
		if managedErr != nil {
			result.addError(managedErr)
			return nil
		}
		result.Dust = nil
		for _, balance := range account.SpotBalances() {
			if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
				continue
			}
			quantity, parseErr := decimalOrZero(balance.Total)
			if parseErr != nil {
				result.addError(parseErr)
				continue
			}
			if quantity.Cmp(shared.Zero()) <= 0 {
				continue
			}
			instrument, found := index.spot[balance.Asset]
			if !found && scope.narrow() {
				continue // 没有对应的现货标的，收窄范围时不算范围内
			}
			if found {
				if !scope.includes(instrument.InstrumentID) {
					continue
				}
				target, inScope := scope.target(tradingAccountID, instrument.InstrumentID)
				if !inScope {
					continue
				}
				quantity = excess(quantity, target)
				if quantity.Cmp(shared.Zero()) <= 0 {
					continue
				}
				// 只用本轮已拿到的报价；没有报价时只按最小数量判断，收尾不再发请求。
				if exposure.Tradable(instrument, account.SettlementAsset) &&
					exposure.SpotDust(quantity, result.quotes[balance.Asset], instrument) {
					result.Dust = append(result.Dust, RemainingPosition{
						Asset: balance.Asset, Quantity: quantity.String(), Reason: "零头，低于交易所最小数量或最小金额",
					})
					continue
				}
			}
			reason := reasonFor(
				reasons, "asset:"+balance.Asset, "asset remains after final sync",
			)
			remaining = append(remaining, RemainingPosition{
				Asset: balance.Asset, Quantity: quantity.String(), Reason: reason.text, final: reason.final,
			})
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].InstrumentID+remaining[i].Asset <
			remaining[j].InstrumentID+remaining[j].Asset
	})
	return remaining
}

// notePacing 记下“间隔未到”要等到的时间；err 不是这个情况返回 false。
func (r *FlattenAccountResult) notePacing(err error) bool {
	var pacing delistPacingError
	if !errors.As(err, &pacing) {
		return false
	}
	if pacing.until.After(r.pacingUntil) {
		r.pacingUntil = pacing.until
	}
	r.pacedOnly = true
	return true
}

func (r *FlattenAccountResult) addError(err error) {
	r.Error = joinError(r.Error, err.Error())
	r.cause = errors.Join(r.cause, err)
}

func (s *Service) persistFlattenProgress(
	ctx context.Context,
	result *FlattenResult,
	status string,
) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	data, err := json.Marshal(struct {
		Accounts         []FlattenAccountResult `json:"accounts"`
		Unblocked        bool                   `json:"unblocked,omitempty"`
		AttemptStartedAt int64                  `json:"attempt_started_at,omitempty"`
		ReduceTargets    map[string]string      `json:"reduce_targets,omitempty"`
		ReduceDone       []string               `json:"reduce_done_accounts,omitempty"`
	}{Accounts: result.Accounts, Unblocked: result.Unblocked, AttemptStartedAt: result.AttemptStartedAt, ReduceTargets: result.ReduceTargets, ReduceDone: result.ReduceDone})
	if err != nil {
		return err
	}
	raw := string(data)
	action := result.Action
	action.Status = status
	action.ResultJSON = &raw
	action.LastError = ""
	for _, account := range result.Accounts {
		if account.Error != "" {
			action.LastError = joinError(
				action.LastError,
				account.TradingAccountID+": "+account.Error,
			)
		}
	}
	if err := s.updateAction(ctx, action); err != nil {
		return err
	}
	result.Action = action
	return nil
}

func decodeFlattenResult(action store.OperatorActionRecord) (FlattenResult, error) {
	result := FlattenResult{Action: action}
	if action.ResultJSON == nil {
		if action.LastError == "" {
			return result, nil
		}
		return result, errors.New(action.LastError)
	}
	var persisted struct {
		Accounts         []FlattenAccountResult `json:"accounts"`
		Unblocked        bool                   `json:"unblocked,omitempty"`
		AttemptStartedAt int64                  `json:"attempt_started_at,omitempty"`
		ReduceTargets    map[string]string      `json:"reduce_targets,omitempty"`
		ReduceDone       []string               `json:"reduce_done_accounts,omitempty"`
	}
	if err := json.Unmarshal([]byte(*action.ResultJSON), &persisted); err != nil {
		return result, err
	}
	result.Accounts, result.Unblocked, result.AttemptStartedAt = persisted.Accounts, persisted.Unblocked, persisted.AttemptStartedAt
	result.ReduceTargets, result.ReduceDone = persisted.ReduceTargets, persisted.ReduceDone
	return result, nil
}

func flattenStatus(accounts []FlattenAccountResult, executable int) string {
	if executable == 0 {
		return string(operatordomain.StatusFailed)
	}
	for _, account := range accounts {
		if account.Status != "COMPLETED" {
			return string(operatordomain.StatusPartial)
		}
	}
	return string(operatordomain.StatusCompleted)
}

// flattenClientOrderIDForSpec 生成清仓子单的客户端订单号：固定 32 位（"mf" + 30 位十六进制），所有交易所都不超过
// （OKX 的 clOrdId 上限 32 位）。下架清仓的第几次尝试并进哈希输入，不能再往后面拼后缀把号拼长。
func flattenClientOrderIDForSpec(
	actionID string,
	accountID string,
	key exchange.SymbolKey,
	side exchange.Side,
	quantity shared.Decimal,
	attempt int,
) string {
	sum := sha256.Sum256([]byte(
		actionID + "\x00" + accountID + "\x00" + key.String() + "\x00" +
			string(side) + "\x00" + quantity.String() + "\x00" + strconv.Itoa(attempt),
	))
	return "mf" + hex.EncodeToString(sum[:15])
}

func (s *Service) flattenAttempts() int {
	if s.FlattenMaxAttempts > 0 {
		return s.FlattenMaxAttempts
	}
	return int(^uint(0) >> 1)
}

// retryWait 是清仓一轮和下一轮之间的等待时间（负数表示不等，零用默认 200 毫秒）。
func (s *Service) retryWait() time.Duration {
	switch {
	case s.FlattenRetryInterval < 0:
		return 0
	case s.FlattenRetryInterval == 0:
		return 200 * time.Millisecond
	}
	return s.FlattenRetryInterval
}

func (s *Service) waitFlattenRetry(ctx context.Context) error {
	interval := s.FlattenRetryInterval
	if interval < 0 {
		return nil
	}
	if interval == 0 {
		interval = 200 * time.Millisecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) flattenTimeout() time.Duration {
	if s.FlattenTimeout > 0 {
		return s.FlattenTimeout
	}
	// 要小于网关给清仓接口的 12 秒调用超时（服务目录 method_timeouts_ms），未完成的部分由工作线程继续。
	return 8 * time.Second
}

func quantityRuleReason(
	quantity shared.Decimal,
	instrument store.InstrumentRecord,
) string {
	step, minimum, err := baseQuantityRules(instrument)
	if err != nil {
		return err.Error()
	}
	if quantity.Cmp(shared.Zero()) <= 0 ||
		quantity.Cmp(minimum) < 0 ||
		!quantity.Div(step).IsInteger() {
		return "数量低于最小值或不符合步长"
	}
	return ""
}

func baseQuantityRules(
	instrument store.InstrumentRecord,
) (shared.Decimal, shared.Decimal, error) {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, shared.Decimal{},
			errors.New("invalid exchange quantity step")
	}
	minimum, err := decimalOrZero(instrument.MinExchangeQuantity)
	if err != nil || minimum.IsNegative() {
		return shared.Decimal{}, shared.Decimal{},
			errors.New("invalid minimum exchange quantity")
	}
	if exchange.RuleFor(instrument.MarketType).ContractValue {
		contractValue, parseErr := shared.ParseDecimal(instrument.ContractValue)
		if parseErr != nil || contractValue.Cmp(shared.Zero()) <= 0 {
			return shared.Decimal{}, shared.Decimal{},
				errors.New("invalid SWAP contract value")
		}
		step = step.Mul(contractValue)
		minimum = minimum.Mul(contractValue)
	}
	return step, minimum, nil
}

func decimalOrZero(raw string) (shared.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return shared.Zero(), nil
	}
	return shared.ParseDecimal(raw)
}

func reasonFor(values map[string]flattenReason, key, fallback string) flattenReason {
	if reason := values[key]; reason.text != "" {
		return reason
	}
	return flattenReason{text: fallback}
}

func joinError(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	return current + "; " + next
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

// operatorDelistFlatten 是下架清仓的动作类型的简写。
const operatorDelistFlatten = operatordomain.ActionDelistFlatten

const (
	// delistMaxAttempts 是下架清仓每个标的连续几笔 IOC 单一股都没成交就记终结原因，交给人看，不无限下单。
	delistMaxAttempts = 20
)

// delistSlippage 是下架清仓伪市价单的滑点：买入向上、卖出向下 3%。
var delistSlippage = shared.MustDecimal("0.03")

// delistLimitPrice 返回伪市价的限价：参考价 ×（1 ± 3%），买入向上、卖出向下取整到价格精度；价格精度不明时不取整。
func delistLimitPrice(reference shared.Decimal, side exchange.Side, priceTick string) shared.Decimal {
	one := shared.MustDecimal("1")
	tick, tickErr := shared.ParseDecimal(priceTick)
	validTick := tickErr == nil && tick.Cmp(shared.Zero()) > 0
	if side == exchange.SideBuy {
		price := reference.Mul(one.Add(delistSlippage))
		if validTick {
			floored := exposure.FloorToStep(price, tick)
			if floored.Cmp(price) != 0 {
				price = floored.Add(tick)
			} else {
				price = floored
			}
		}
		return price
	}
	price := reference.Mul(one.Sub(delistSlippage))
	if validTick {
		price = exposure.FloorToStep(price, tick)
	}
	return price
}

// delistPacingError 表示离上一笔 IOC 单结束太近，要等到 until 才能下下一笔：不算终结，也不是错误。
// 轮内循环见到它不能空转同步（每轮两次轻量同步，对币安现货加永续要发 3 个接口，会打满 IP 限频），直接睡到 until 再继续。
type delistPacingError struct{ until time.Time }

func (e delistPacingError) Error() string {
	return "下架清仓的限价单刚结束，间隔几秒再下下一笔"
}

// delistHistory 是下架清仓在一个标的上的子单历史。
type delistHistory struct {
	// finished 是本动作在这个标的上已结束的子单总数（不分起点）：并进子单号的哈希，保证每笔的号不同。
	finished int
	// lastRejected、rejectReason 说明这一轮（起点之后）最近一笔是否被交易所拒绝；unfilled 是这一轮最近连续一股都没成交的笔数。
	lastRejected bool
	rejectReason string
	unfilled     int
	// lastFinishedAt 是最近一笔结束的时间（任何一轮），用来控制两笔 IOC 之间的最小间隔。
	lastFinishedAt time.Time
}

// loadDelistHistory 读本动作在这个标的上已结束的子单。被拒和连续未成交只统计这一轮起点之后创建的子单：
// 同一个动作号在 FAILED、PARTIAL 之后重试会重新记起点，让重试真的重新来过；起点之前的子单只影响号和间隔。
func (s *Service) loadDelistHistory(ctx context.Context, command FlattenCommand, tradingAccountID string, key exchange.SymbolKey) (delistHistory, error) {
	records, err := s.Store.ListOrdersForLane(ctx, command.SpaceID, tradingAccountID, key)
	if err != nil {
		return delistHistory{}, err
	}
	var finished []store.OrderRecord
	for _, current := range records {
		if current.OwnerType == string(orderdomain.OwnerOperator) && current.OwnerID == command.ActionID &&
			orderdomain.State(current.State).Terminal() {
			finished = append(finished, current)
		}
	}
	sort.SliceStable(finished, func(i, j int) bool {
		if !finished[i].CreatedAt.Equal(finished[j].CreatedAt) {
			return finished[i].CreatedAt.Before(finished[j].CreatedAt)
		}
		return finished[i].OrderID < finished[j].OrderID
	})
	history := delistHistory{finished: len(finished)}
	var thisRound []store.OrderRecord
	for _, current := range finished {
		at := current.CreatedAt
		if current.FinishedAt > 0 {
			at = time.UnixMilli(current.FinishedAt)
		}
		if at.After(history.lastFinishedAt) {
			history.lastFinishedAt = at
		}
		// 起点取到秒、订单创建时间也只到秒（向下取整）：同一秒内创建的子单算这一轮的，否则这一轮刚下的单会被漏数。
		// 代价是上一轮结束后不到一秒就用同一个动作号重试，会看到上一轮同一秒的被拒单（事件中心至少隔 30 秒才重试，碰不到）。
		if current.CreatedAt.UnixMilli() >= command.attemptStart {
			thisRound = append(thisRound, current)
		}
	}
	if n := len(thisRound); n > 0 && thisRound[n-1].State == string(orderdomain.Rejected) {
		history.lastRejected, history.rejectReason = true, thisRound[n-1].RejectReason
	}
	for index := len(thisRound) - 1; index >= 0; index-- {
		filled, parseErr := decimalOrZero(thisRound[index].FilledQuantity)
		if parseErr != nil || filled.Cmp(shared.Zero()) > 0 {
			break
		}
		history.unfilled++
	}
	return history, nil
}

// checkDelistAttempt 判断能不能再下一笔：最近一笔被交易所拒绝（同样的单再发还是拒绝），或这一轮连续 delistMaxAttempts 笔 IOC 一股都没成交
// （部分成交说明在推进，不算）是终结错误，这一轮停下；同一个动作号重试会重新来过。两笔之间至少隔 delistAttemptInterval。
func (s *Service) checkDelistAttempt(history delistHistory) error {
	if history.lastRejected {
		return flattenFinal{fmt.Errorf("下架清仓的订单被交易所拒绝：%s", history.rejectReason)}
	}
	if history.unfilled >= delistMaxAttempts {
		return flattenFinal{fmt.Errorf("下架清仓连续 %d 次限价单（滑点 3%%）一股都没成交，需要人工处理", history.unfilled)}
	}
	if interval := s.delistAttemptInterval(); interval > 0 && !history.lastFinishedAt.IsZero() && time.Since(history.lastFinishedAt) < interval {
		return delistPacingError{until: history.lastFinishedAt.Add(interval)}
	}
	return nil
}

// defaultDelistAttemptInterval 是两笔下架清仓 IOC 单之间的最小间隔：薄盘口下一笔打完马上再打只会在同一个价位上空转。
const defaultDelistAttemptInterval = 3 * time.Second

func (s *Service) delistAttemptInterval() time.Duration {
	switch {
	case s.DelistAttemptInterval < 0:
		return 0
	case s.DelistAttemptInterval == 0:
		return defaultDelistAttemptInterval
	}
	return s.DelistAttemptInterval
}

// sleepUntil 睡到指定时间，ctx 结束时提前返回。
func sleepUntil(ctx context.Context, until time.Time) error {
	wait := time.Until(until)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

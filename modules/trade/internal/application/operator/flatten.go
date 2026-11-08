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
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
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
	// unblocking 为真表示下架清仓发现标的已解除禁止，正在等本动作的撤单确认：过了截止时间也不能把它压成 PARTIAL。
	unblocking bool
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
	// FinishingStatus 非空表示动作已经定了要以这个状态结束（PARTIAL、FAILED 等），只是本动作还有子单在解析或丢弃失败：
	// 动作先保持运行，之后每次恢复只收拾子单、不再下单，收拾完才落成这个状态。
	FinishingStatus string `json:"finishing_status,omitempty"`
	// FinishingSince 是进入收尾的时间（毫秒）：满 flattenFinishingTimeout 仍收拾不完就不再等，照常落成 FinishingStatus，
	// 没收拾掉的子单记进 ManualCheckOrderIDs。落库保存，重启后照样从这个时间算。
	FinishingSince int64 `json:"finishing_since,omitempty"`
	// ManualCheckOrderIDs 是动作结束时没能收拾掉的子单（收尾超时仍解析不出、交易所确认标的不存在），
	// 动作不再管它们，要人工到交易所核对。
	ManualCheckOrderIDs []string `json:"manual_check_child_order_ids,omitempty"`
}

// flattenProgress 是清仓进度落库的 JSON（t_operator_actions.c_result_json）。
type flattenProgress struct {
	Accounts            []FlattenAccountResult `json:"accounts"`
	Unblocked           bool                   `json:"unblocked,omitempty"`
	AttemptStartedAt    int64                  `json:"attempt_started_at,omitempty"`
	ReduceTargets       map[string]string      `json:"reduce_targets,omitempty"`
	ReduceDone          []string               `json:"reduce_done_accounts,omitempty"`
	FinishingStatus     string                 `json:"finishing_status,omitempty"`
	FinishingSince      int64                  `json:"finishing_since,omitempty"`
	ManualCheckOrderIDs []string               `json:"manual_check_child_order_ids,omitempty"`
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
	var previous FlattenResult
	if found {
		previous, _ = decodeFlattenResult(existing)
	}
	// 上一次已经定了结局、只差收拾子单：这次只收拾，不再下单。上一轮记下的各账户进度、进入收尾的时间保留。
	if resuming && previous.FinishingStatus != "" {
		result.Accounts, result.Unblocked = previous.Accounts, previous.Unblocked
		result.FinishingSince = previous.FinishingSince
		_, err := s.finishFlatten(ctx, command, members, &result, previous.FinishingStatus)
		return result, err
	}
	// 过了截止时间（下架时间）：标的已经下架，不再下单，把动作按部分完成收尾；上一轮记下的各账户进度保留（同一个动作号重试也保留）。
	// 先看是否已解除禁止：解除了就走正常流程，由 stopIfUnblocked 撤单后记 COMPLETED 并带“已解除禁止”标记。
	if s.pastDeadline(command) && !s.unblockedEverywhere(ctx, command, scope, members) {
		if len(previous.Accounts) > 0 {
			result.Accounts = previous.Accounts
		}
		_, err := s.finishFlatten(ctx, command, members, &result, string(operatordomain.StatusPartial))
		return result, err
	}
	// 预算从收到请求算起，等锁用掉的时间要扣除，加上收尾不能超过网关的调用超时。
	budgetEnd, _ := ctx.Deadline()
	executable := 0
	var sharedErrors []error
	retry := false
	// stillRetrying 为真表示有账户要重试、而且不是“已解除禁止、等撤单确认”的：过了截止时间就该停了。
	stillRetrying := false
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
			stillRetrying = stillRetrying || !accountResult.unblocking
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
		// 过了截止时间也停，但只剩“已解除禁止、等撤单确认”的账户时不停，让它们记 COMPLETED。
		if s.roundOlderThan(result, flattenActionTimeout) || s.pastDeadline(command) && stillRetrying {
			status = "PARTIAL"
		}
	}
	if _, err := s.finishFlatten(ctx, command, members, &result, status); err != nil {
		return result, errors.Join(errors.Join(sharedErrors...), err)
	}
	return result, errors.Join(sharedErrors...)
}

// unblockedEverywhere 报告下架清仓的标的是否已解除禁止：标的表里有这个标的的成员上都不在名单里了（至少有一个这样的成员）。
// 不是下架清仓、查名单失败都算没解除。
func (s *Service) unblockedEverywhere(ctx context.Context, command FlattenCommand, scope flattenScope, members []store.LogicalAccountMemberRecord) bool {
	if scope.stillBlocked == nil {
		return false
	}
	present := 0
	for _, member := range members {
		blocked, found, err := s.delistBlockState(ctx, command.SpaceID, member.TradingAccountID, scope.instrumentID)
		if err != nil || found && blocked {
			return false
		}
		if found {
			present++
		}
	}
	return present > 0
}

// flattenFinishingTimeout 是收尾（只收拾子单、不再下单）的上限：从进入收尾算起，满了仍收拾不完就不再等。
const flattenFinishingTimeout = 30 * time.Minute

// finishFlatten 把清仓进度落成 status。动作要离开运行（完成、部分完成、失败）时先收拾本动作还没结束的子单：
// 收拾不完（子单还在解析、丢弃失败、没能列全）就先保持运行并记下要结束成什么、什么时候进入的收尾，之后每次恢复只收拾、不下单，
// 收拾完才落成 status。收尾满 flattenFinishingTimeout 仍收拾不完，或者子单收拾不了（交易所确认标的不存在），
// 照样落成 status，这些子单记进结果的 ManualCheckOrderIDs 并记关注项，要人工核对。返回实际落库的状态。
//
// 收拾子单是尽力而为：期间遇到的任何错误（解析、丢弃失败，预算用完，列子单失败）都只记在本动作上，写进 last_error，
// 不作为调用错误返回，不影响 Trade 的整体就绪；没收拾掉的子单统一受收尾上限约束。数据库真坏了时，落库进度本身会失败并返回，
// 其他工作线程也会报出来，故障照样看得见。
func (s *Service) finishFlatten(
	ctx context.Context,
	command FlattenCommand,
	members []store.LogicalAccountMemberRecord,
	result *FlattenResult,
	status string,
) (string, error) {
	note := ""
	if status != string(operatordomain.StatusRunning) {
		outcome := s.settleFlattenChildren(ctx, command, members)
		if (outcome.incomplete || len(outcome.unsettled) > 0) && result.FinishingSince == 0 {
			result.FinishingSince = s.clock().UnixMilli()
		}
		timedOut := result.FinishingSince != 0 && s.clock().Sub(time.UnixMilli(result.FinishingSince)) >= flattenFinishingTimeout
		if (outcome.incomplete || len(outcome.unsettled) > 0) && !timedOut {
			result.ManualCheckOrderIDs = outcome.manualCheck
			result.FinishingStatus = status
			running := string(operatordomain.StatusRunning)
			return running, s.persistFlattenProgressNote(ctx, result, running, outcome.errorText())
		}
		if outcome.incomplete {
			// 收尾到上限仍没能列全子单：没列出来的尽量另列一次（其中待提交的顺手丢弃，释放资金预留），一起交给人工。
			outcome.unsettled = append(outcome.unsettled, s.flattenChildrenLeft(ctx, command, members, outcome)...)
		}
		result.ManualCheckOrderIDs = append(outcome.manualCheck, outcome.unsettled...)
		if len(result.ManualCheckOrderIDs) > 0 || outcome.incomplete {
			detail := fmt.Sprintf("组合 %s 的动作 %s：%d 笔子单没收拾掉，需人工核对", command.LogicalAccountID, command.ActionID, len(result.ManualCheckOrderIDs))
			if outcome.incomplete {
				detail = fmt.Sprintf("组合 %s 的动作 %s：收尾满 30 分钟仍没能列全执行账户 %s 的子单，%d 笔子单没收拾掉，需人工核对",
					command.LogicalAccountID, command.ActionID, strings.Join(outcome.unlisted, ","), len(result.ManualCheckOrderIDs))
			}
			telemetry.Note(telemetry.AttentionFlattenOrdersUnsettled, detail, s.clock())
			log.WarnContextf(ctx, "交易关注项：%s（%s）：%s", detail, strings.Join(result.ManualCheckOrderIDs, ","), outcome.errorText())
		}
		result.FinishingStatus, result.FinishingSince = "", 0
		note = outcome.errorText()
	}
	if err := s.persistFlattenProgressNote(ctx, result, status, note); err != nil {
		return status, err
	}
	// 下架清仓结束（完成、部分完成、失败）后唤醒目标：已收敛、已阻塞的目标不被定时器扫描，
	// 不唤醒的话周期长的策略要等下一份目标才恢复（解除禁止提前结束时尤其如此）。
	if status != string(operatordomain.StatusRunning) && command.actionType() == operatordomain.ActionDelistFlatten && s.Wake != nil {
		s.Wake()
	}
	return status, nil
}

// settleLocalTimeout 是收拾子单时只读写本地库的一步（列一个账户的子单、丢弃一笔待提交子单）的时限。
// 这些步骤另起上下文（不受本次调用剩余预算影响），每一步各用各的时限。
var settleLocalTimeout = 5 * time.Second

// settleMinResolveTime 是解析一笔结果未知子单至少要分到的时间；剩余预算不够时，没轮到的留到下次恢复。
const settleMinResolveTime = 2 * time.Second

// settleFlattenChildren 在动作结束前收拾本动作还没结束的子单，不让它们在动作结束后没人管。尽力而为，错误只记进结果，分三步：
//  1. 只读库，逐个账户列出本动作的子单。某个账户列不出来时跳过它、记 incomplete（结果不全），其余账户照常；
//  2. 待提交的逐笔丢弃（记为已撤销，从没发到交易所，释放资金预留），每笔另起短时限。某个账户等账户锁超时一次，
//     它余下的待提交子单这一轮不再试（锁被占着，再等也是白等）；服务关闭（调用上下文被取消）时跳过这一步。
//     子单已经不是待提交（例如在控制台手动撤掉了）算已收拾；
//  3. 逐笔解析提交中、结果未知的子单（要问交易所），解析后退回待提交的同样丢弃。本次剩余预算平均分给还要尝试的子单，
//     每笔至少 settleMinResolveTime，放不下的留到下次；某个账户这一轮已失败过，它余下的子单这一轮不再试。
//     挂单中、撤单中的不动，它们自己会结束。
//
// unsettled 是这次没收拾掉、下次恢复还能再试的；manualCheck 是收拾不了的（交易所确认标的不存在），不阻塞收尾，要人工核对。
func (s *Service) settleFlattenChildren(ctx context.Context, command FlattenCommand, members []store.LogicalAccountMemberRecord) settleOutcome {
	var outcome settleOutcome
	var pending, unknown []store.OrderRecord
	for _, member := range members {
		records, err := s.listFlattenChildren(ctx, command, member.TradingAccountID)
		if err != nil {
			outcome.incomplete = true
			outcome.unlisted = append(outcome.unlisted, member.TradingAccountID)
			outcome.errs = append(outcome.errs, err)
			continue
		}
		for _, child := range records {
			switch orderdomain.State(child.State) {
			case orderdomain.Pending:
				pending = append(pending, child)
			case orderdomain.Submitting, orderdomain.SubmitUnknown:
				unknown = append(unknown, child)
			}
		}
	}

	lockBusy := make(map[string]struct{})
	for _, child := range pending {
		if _, busy := lockBusy[child.TradingAccountID]; busy || errors.Is(ctx.Err(), context.Canceled) {
			outcome.unsettled = append(outcome.unsettled, child.OrderID)
			continue
		}
		if errors.Is(s.discardChild(ctx, command, child, &outcome), errDiscardLockBusy) {
			lockBusy[child.TradingAccountID] = struct{}{}
		}
	}

	// 第 2 步等锁超时的账户，解析同样要等这把锁，这一轮也不再试。
	failedAccounts := make(map[string]struct{}, len(lockBusy))
	for accountID := range lockBusy {
		failedAccounts[accountID] = struct{}{}
	}
	for i, child := range unknown {
		if _, failed := failedAccounts[child.TradingAccountID]; failed {
			outcome.unsettled = append(outcome.unsettled, child.OrderID)
			continue
		}
		itemCtx, cancelItem := ctx, context.CancelFunc(func() {})
		if deadline, ok := ctx.Deadline(); ok {
			left := time.Until(deadline)
			share := max(left/time.Duration(remainingTries(unknown[i:], failedAccounts)), settleMinResolveTime)
			if left < share {
				// 剩余预算放不下一笔：没轮到的留给下次恢复，到收尾上限时交给人工。
				outcome.unsettled = append(outcome.unsettled, orderIDs(unknown[i:])...)
				outcome.errs = append(outcome.errs, fmt.Errorf("本次收拾的时间用完，还有 %d 笔子单没解析", len(unknown)-i))
				break
			}
			itemCtx, cancelItem = context.WithTimeout(ctx, share)
		}
		resolved, resolveErr := s.Orders.ResolveUnknown(itemCtx, command.SpaceID, child.OrderID)
		cancelItem()
		if resolveErr != nil {
			if childUnsettleable(resolveErr) {
				outcome.manualCheck = append(outcome.manualCheck, child.OrderID)
			} else {
				outcome.unsettled = append(outcome.unsettled, child.OrderID)
				failedAccounts[child.TradingAccountID] = struct{}{}
			}
			outcome.errs = append(outcome.errs, fmt.Errorf("解析子单 %s：%w", child.OrderID, resolveErr))
			continue
		}
		switch resolved.State {
		case orderdomain.Pending:
			_ = s.discardChild(ctx, command, child, &outcome)
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			outcome.unsettled = append(outcome.unsettled, child.OrderID)
		}
	}
	return outcome
}

// remainingTries 数 children 里所属账户这一轮还没失败过、还会去解析的子单（至少 1）。
func remainingTries(children []store.OrderRecord, failedAccounts map[string]struct{}) int {
	count := 0
	for _, child := range children {
		if _, failed := failedAccounts[child.TradingAccountID]; !failed {
			count++
		}
	}
	return max(count, 1)
}

// listFlattenChildren 只读库，列出一个账户上本动作还没结束的子单，另起短时限的上下文（不受调用方预算影响）。
func (s *Service) listFlattenChildren(ctx context.Context, command FlattenCommand, tradingAccountID string) ([]store.OrderRecord, error) {
	listCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleLocalTimeout)
	defer cancel()
	records, err := s.Store.ListActiveOrdersForAccount(listCtx, command.SpaceID, tradingAccountID)
	if err != nil {
		return nil, fmt.Errorf("列执行账户 %s 的子单：%w", tradingAccountID, err)
	}
	var children []store.OrderRecord
	for _, child := range records {
		if child.OwnerType == string(orderdomain.OwnerOperator) && child.OwnerID == command.ActionID {
			children = append(children, child)
		}
	}
	return children, nil
}

// errDiscardLockBusy 表示丢弃子单时等交易账户锁没等到。
var errDiscardLockBusy = errors.New("等交易账户锁超时")

// discardChild 丢弃一笔待提交的子单，另起短时限（要等交易账户锁）。子单已经不是待提交算已收拾；
// 其余失败记进 unsettled 和错误说明，下次再试，并返回错误（等账户锁没等到时返回 errDiscardLockBusy）。
func (s *Service) discardChild(ctx context.Context, command FlattenCommand, child store.OrderRecord, outcome *settleOutcome) error {
	discardCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleLocalTimeout)
	defer cancel()
	_, discardErr := s.Orders.DiscardPending(discardCtx, command.SpaceID, child.OrderID)
	if discardErr == nil || errors.Is(discardErr, orderdomain.ErrInvalidTransition) {
		return nil
	}
	outcome.unsettled = append(outcome.unsettled, child.OrderID)
	outcome.errs = append(outcome.errs, fmt.Errorf("丢弃子单 %s：%w", child.OrderID, discardErr))
	var accountErr *orderapp.AccountExecutionError
	if errors.As(discardErr, &accountErr) && accountErr.Operation == orderapp.OperationDiscardPendingLock {
		return errDiscardLockBusy
	}
	return discardErr
}

// orderIDs 取一组订单的单号。
func orderIDs(records []store.OrderRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.OrderID)
	}
	return ids
}

// flattenChildrenLeft 在收尾到上限、子单没能列全时，尽量另列一次本动作还没结束、又不在 outcome 里的子单，交给人工核对。
// 待提交的顺手丢弃（从没发到交易所，释放资金预留），丢弃成功的不再交给人工；列不出来的账户跳过，只写日志。
func (s *Service) flattenChildrenLeft(ctx context.Context, command FlattenCommand, members []store.LogicalAccountMemberRecord, outcome settleOutcome) []string {
	seen := make(map[string]struct{}, len(outcome.unsettled)+len(outcome.manualCheck))
	for _, id := range append(append([]string(nil), outcome.unsettled...), outcome.manualCheck...) {
		seen[id] = struct{}{}
	}
	var left []string
	for _, member := range members {
		records, err := s.listFlattenChildren(ctx, command, member.TradingAccountID)
		if err != nil {
			log.WarnContextf(ctx, "动作 %s 收尾到上限，%v", command.ActionID, err)
			continue
		}
		for _, child := range records {
			state := orderdomain.State(child.State)
			if state != orderdomain.Pending && state != orderdomain.Submitting && state != orderdomain.SubmitUnknown {
				// 挂单中、撤单中的自己会结束，平时收尾也不管它们。
				continue
			}
			if _, found := seen[child.OrderID]; found {
				continue
			}
			seen[child.OrderID] = struct{}{}
			if state == orderdomain.Pending {
				var discarded settleOutcome
				if s.discardChild(ctx, command, child, &discarded) == nil {
					continue
				}
				log.WarnContextf(ctx, "动作 %s 收尾到上限，%s", command.ActionID, discarded.errorText())
			}
			left = append(left, child.OrderID)
		}
	}
	return left
}

// settleOutcome 是一次收拾子单的结果。
type settleOutcome struct {
	unsettled   []string
	manualCheck []string
	errs        []error
	// incomplete 表示有账户的子单没列出来（结果不全），unlisted 是这些账户。
	incomplete bool
	unlisted   []string
}

// errorText 是写进动作 last_error 的收拾失败原因；没有错误时为空。
func (o settleOutcome) errorText() string {
	if len(o.errs) == 0 {
		return ""
	}
	return errors.Join(o.errs...).Error()
}

// childUnsettleable 报告子单解析失败是否属于等多久都收拾不了的：只有交易所确认标的不存在（币安 -1121、OKX 51001，标的已下架）。
// 取不到账户的执行通道（没有会话）不算：重启后会话还没建好时只是暂时的，当场放行会把本可解析的子单丢给人工；
// 它和凭证失效、超时、限流一样按收尾上限处理。
func childUnsettleable(err error) bool {
	return exchange.IsInstrumentNotFound(err)
}

// clock 返回当前时间（测试可以替换 Now）。
func (s *Service) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
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

// errDelistDeadlinePassed：已过下架时间（截止时间），不再下单也不再提交待提交的子单，属终结原因。
var errDelistDeadlinePassed = errors.New("已过下架时间，不再下单")

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
		canOrder, sessionReason := execution.CanOrder(s.Exchanges, account.Exchange, time.Now())
		switch {
		case len(account.Markets) == 0:
			result.addError(flattenFinal{errors.New("账户没有开通任何市场")})
		case !canOrder:
			result.addError(flattenFinal{errors.New(sessionReason)})
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
			// 过了下架时间，待提交的子单不再发出，直接丢弃。
			if s.pastDeadline(command) {
				_, err = s.Orders.DiscardPending(ctx, command.SpaceID, child.OrderID)
				break
			}
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
		err := s.stopOrder(ctx, current)
		if exchange.IsInstrumentNotFound(err) {
			// 交易所确认标的不存在（已下架）：这笔单撤不掉也查不到（多半是之前动作留下的结果未知子单）。不算撤单失败，
			// 撤单后的同步（syncAfterCancel，此时一定有要撤的单）会把它按过期结束并记关注项，随后的撤单确认就过了。
			continue
		}
		if err != nil {
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
		balanceTotal := total
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
		var quantity shared.Decimal
		if exchange.RuleFor(instrument.MarketType).BoardLot {
			// 整手市场（A 股）：和目标执行器同一个卖出规则，卖出全部可卖数量时零股一次卖完；只因 T+1 卖不出的部分
			// （当日买入 = 总数 − 冻结 − 可卖）这一轮不再重试，原因写明。
			quantity, _ = targetapp.SellQuantity(instrument, total, available)
			if quantity.IsZero() {
				locked, _ := decimalOrZero(balance.Locked)
				bought := balanceTotal.Sub(locked).Sub(available)
				if later, _ := targetapp.SellQuantity(instrument, total, available.Add(bought)); exchange.RuleFor(instrument.MarketType).T1 &&
					bought.Cmp(shared.Zero()) > 0 && !later.IsZero() {
					reasons[key] = flattenReason{text: "当日买入，次日可卖", final: true}
					continue
				}
				reasons[key] = flattenReason{text: "可用余额不足，部分余额仍被挂单占用"}
				continue
			}
		} else {
			// 总量不是零头而可用余额卖不出去，说明还有余额被挂单占着，下一轮再试。
			// 减仓只卖超出目标数量的部分（此时 total 已经是超出部分）。
			if available.Cmp(total) > 0 {
				available = total
			}
			quantity = exposure.FloorToStep(available, step)
			if quantityRuleReason(quantity, instrument) != "" || exposure.SpotDust(quantity, price, instrument) {
				reasons[key] = flattenReason{text: "可用余额不足，部分余额仍被挂单占用"}
				continue
			}
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
	// stockIOC：A 股没有市价单，清仓、减仓也发限价 IOC；子单号和下架清仓一样带上第几次尝试，零成交的单结束后换号再下。
	stockIOC := exchange.RuleFor(instrument.MarketType).PriceLimits
	var history delistHistory
	if delist || stockIOC {
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
			if s.pastDeadline(command) {
				if _, err = s.Orders.DiscardPending(ctx, command.SpaceID, existing.OrderID); err != nil {
					return err
				}
				return flattenFinal{errDelistDeadlinePassed}
			}
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
			ended := manualFailureError{message: fmt.Sprintf("上一笔清仓子单已结束（%s）", existing.State), cause: ErrInvalidCommand}
			// 零成交就终结的子单，同一单号重试只会得到同样结果；部分成交的等持仓同步后按新数量换单号再下。
			if filled, parseErr := decimalOrZero(existing.FilledQuantity); parseErr == nil && filled.IsZero() {
				return flattenFinal{ended}
			}
			return ended
		default:
			return nil
		}
	}
	// 每笔新单之前都看截止时间：一轮里前面的单耗掉时间，过了下架时间后面的就不再下。
	if s.pastDeadline(command) {
		return flattenFinal{errDelistDeadlinePassed}
	}
	if delist {
		// 要下新的一笔了：被拒绝、连续没成交封顶、离上一笔结束太近都先不下。
		if err := s.checkDelistAttempt(history); err != nil {
			return err
		}
	} else if stockIOC {
		if err := checkStockFlattenAttempt(history, side, quote); err != nil {
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
	if delist || exchange.RuleFor(instrument.MarketType).PriceLimits {
		// 3% 滑点可能超出交易所的价格偏离限制（币安 PERCENT_PRICE、A 股价格笼子），和目标执行器的伪市价单用同一个夹价函数，免得被拒。
		// A 股没有市价单：清仓同样发限价 IOC，再截到涨跌停价之间。
		limit := targetapp.ClampToPriceBand(delistLimitPrice(quote.Price, side, instrument.PriceTick), targetapp.CageBasis(quote, side), side, instrument)
		limit = targetapp.ClampToPriceLimits(limit, quote)
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
	return s.persistFlattenProgressNote(ctx, result, status, "")
}

// persistFlattenProgressNote 落库清仓进度；note 非空时追加到 last_error（收尾时子单解析、丢弃失败的原因）。
func (s *Service) persistFlattenProgressNote(
	ctx context.Context,
	result *FlattenResult,
	status string,
	note string,
) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	data, err := json.Marshal(flattenProgress{
		Accounts: result.Accounts, Unblocked: result.Unblocked, AttemptStartedAt: result.AttemptStartedAt,
		ReduceTargets: result.ReduceTargets, ReduceDone: result.ReduceDone, FinishingStatus: result.FinishingStatus,
		FinishingSince: result.FinishingSince, ManualCheckOrderIDs: result.ManualCheckOrderIDs,
	})
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
	if note != "" {
		action.LastError = joinError(action.LastError, "收拾子单："+note)
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
	var persisted flattenProgress
	if err := json.Unmarshal([]byte(*action.ResultJSON), &persisted); err != nil {
		return result, err
	}
	result.Accounts, result.Unblocked, result.AttemptStartedAt = persisted.Accounts, persisted.Unblocked, persisted.AttemptStartedAt
	result.ReduceTargets, result.ReduceDone = persisted.ReduceTargets, persisted.ReduceDone
	result.FinishingStatus, result.FinishingSince = persisted.FinishingStatus, persisted.FinishingSince
	result.ManualCheckOrderIDs = persisted.ManualCheckOrderIDs
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
		// 休市前一刻发出、休市时被模拟撮合撤掉的单不是一次真正的尝试，下个时段继续。
		if thisRound[index].RejectReason == paper.OutOfSessionReason {
			continue
		}
		history.unfilled++
	}
	return history, nil
}

// stockFlattenMaxUnfilled 是 A 股清仓、减仓在一个标的上连续零成交的 IOC 上限，到了就按终结处理、要人工看。
const stockFlattenMaxUnfilled = 20

// checkStockFlattenAttempt 判断 A 股清仓、减仓这一笔能不能下：
//   - 停牌、卖出时跌停（现价在跌停价且没有买一）：一股都卖不出，按可重试处理，下一轮再看，由清仓动作的 30 分钟期限兜底；
//     和“当日买入，次日可卖”不同，后者在当天怎么重试都卖不了，按终结处理；
//   - 最近一笔被拒绝，或这一轮连续 stockFlattenMaxUnfilled 笔零成交（休市时被撤掉的不算）：终结。
func checkStockFlattenAttempt(history delistHistory, side exchange.Side, quote Quote) error {
	switch {
	case quote.Halted:
		return manualFailureError{message: "停牌，复牌后再卖", cause: ErrInvalidCommand}
	case side == exchange.SideSell && quote.LimitDown.Cmp(shared.Zero()) > 0 && quote.Price.Cmp(quote.LimitDown) <= 0 &&
		quote.Bid.Cmp(shared.Zero()) <= 0:
		return manualFailureError{message: "跌停，没有买一，打开后再卖", cause: ErrInvalidCommand}
	case side == exchange.SideBuy && quote.LimitUp.Cmp(shared.Zero()) > 0 && quote.Price.Cmp(quote.LimitUp) >= 0 &&
		quote.Ask.Cmp(shared.Zero()) <= 0:
		return manualFailureError{message: "涨停，没有卖一，打开后再买", cause: ErrInvalidCommand}
	case history.lastRejected:
		return flattenFinal{fmt.Errorf("清仓的订单被拒绝：%s", history.rejectReason)}
	case history.unfilled >= stockFlattenMaxUnfilled:
		return flattenFinal{fmt.Errorf("清仓连续 %d 笔限价单一股都没成交，需要人工处理", history.unfilled)}
	}
	return nil
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

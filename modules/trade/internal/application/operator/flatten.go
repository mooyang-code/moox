package operator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
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

// FlattenTradingAccount 只平组合账户里的一个执行账户：撤这个账户的单、平这个账户的仓，组合账户照样暂停（自主下单组合不暂停）。
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
		RequestJSON: requestJSON, Status: string(operatordomain.StatusRunning),
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
	// 已有动作的进度只解码这一次：起点、减仓目标、收尾状态都从它取。
	var previous FlattenResult
	if found {
		previous, _ = decodeFlattenResult(existing)
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
		// 自主下单组合没有自动执行，和撤单一样不暂停，也就不改写暂停原因。
		if command.pausesLogicalAccount() {
			logical, err := tx.GetLogicalAccount(command.SpaceID, command.LogicalAccountID)
			if err != nil {
				return err
			}
			if logical.ControlMode != "MANUAL" {
				if err := tx.SetLogicalAccountAutomation(
					command.SpaceID,
					command.LogicalAccountID,
					"PAUSED",
					strings.TrimSpace(command.Reason),
				); err != nil {
					return err
				}
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
		if previous.AttemptStartedAt > 0 {
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
		if len(previous.ReduceDone) > 0 || len(previous.ReduceTargets) > 0 {
			result.ReduceTargets, result.ReduceDone = previous.ReduceTargets, previous.ReduceDone
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
			return s.persistFlattenProgress(ctx, &result, string(operatordomain.StatusRunning))
		}
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
			if accountResult.Status == string(operatordomain.StatusCompleted) {
				accountResult.Status = string(operatordomain.StatusRunning)
			}
		}
		result.Accounts = append(result.Accounts, accountResult)
		result.Unblocked = result.Unblocked || accountResult.Unblocked
		if err := s.persistFlattenProgress(ctx, &result, string(operatordomain.StatusRunning)); err != nil {
			return result, errors.Join(errors.Join(sharedErrors...), err)
		}
	}
	status := flattenStatus(result.Accounts, executable)
	if retry {
		status = string(operatordomain.StatusRunning)
		// 账户一直异常时清仓不能永远运行，否则恢复自动执行会被挡住。剩余持仓已写入结果，要继续就重新发起。
		// 过了截止时间也停，但只剩“已解除禁止、等撤单确认”的账户时不停，让它们记 COMPLETED。
		if s.roundOlderThan(result, flattenActionTimeout) || s.pastDeadline(command) && stillRetrying {
			status = string(operatordomain.StatusPartial)
		}
	}
	if _, err := s.finishFlatten(ctx, command, members, &result, status); err != nil {
		return result, errors.Join(errors.Join(sharedErrors...), err)
	}
	return result, errors.Join(sharedErrors...)
}

// clock 返回当前时间（测试可以替换 Now）。
func (s *Service) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// roundOlderThan 判断这一轮执行（从 AttemptStartedAt 起）是否已经超过期限：同一个动作号在 FAILED、PARTIAL 之后重试要重新计时，
// 不能按动作创建时间算（创建很久之后的重试一次只跑一轮就记 PARTIAL）。
func (s *Service) roundOlderThan(result FlattenResult, age time.Duration) bool {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return now.Sub(time.UnixMilli(result.AttemptStartedAt)) >= age
}

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
		if account.Status != string(operatordomain.StatusCompleted) {
			return string(operatordomain.StatusPartial)
		}
	}
	return string(operatordomain.StatusCompleted)
}

func (s *Service) flattenTimeout() time.Duration {
	if s.FlattenTimeout > 0 {
		return s.FlattenTimeout
	}
	// 要小于网关给清仓接口的 12 秒调用超时（服务目录 method_timeouts_ms），未完成的部分由工作线程继续。
	return 8 * time.Second
}

func decimalOrZero(raw string) (shared.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return shared.Zero(), nil
	}
	return shared.ParseDecimal(raw)
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

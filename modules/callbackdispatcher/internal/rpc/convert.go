package rpc

import (
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	pb "github.com/mooyang-code/moox/modules/callbackdispatcher/proto/callbackdispatchergen"
)

var statusToPB = map[string]pb.ExecutionStatus{
	store.StatusPending:   pb.ExecutionStatus_EXECUTION_STATUS_PENDING,
	store.StatusRunning:   pb.ExecutionStatus_EXECUTION_STATUS_RUNNING,
	store.StatusSucceeded: pb.ExecutionStatus_EXECUTION_STATUS_SUCCEEDED,
	store.StatusFailed:    pb.ExecutionStatus_EXECUTION_STATUS_FAILED,
	store.StatusCanceled:  pb.ExecutionStatus_EXECUTION_STATUS_CANCELED,
}

var statusFromPB = func() map[pb.ExecutionStatus]string {
	result := make(map[pb.ExecutionStatus]string, len(statusToPB))
	for status, value := range statusToPB {
		result[value] = status
	}
	return result
}()

func actionTypeToPB(actionType string) pb.ActionType {
	switch actionType {
	case store.ActionNotifyURL:
		return pb.ActionType_ACTION_TYPE_NOTIFY_URL
	case store.ActionSystem:
		return pb.ActionType_ACTION_TYPE_SYSTEM
	}
	return pb.ActionType_ACTION_TYPE_UNSPECIFIED
}

func systemActionToPB(systemAction string) pb.SystemAction {
	if systemAction == store.SystemActionSellDelistedHoldings {
		return pb.SystemAction_SYSTEM_ACTION_SELL_DELISTED_HOLDINGS
	}
	return pb.SystemAction_SYSTEM_ACTION_UNSPECIFIED
}

func delistKindToPB(kind string) pb.DelistStepKind {
	switch kind {
	case store.DelistKindBlock:
		return pb.DelistStepKind_DELIST_STEP_KIND_BLOCK
	case store.DelistKindFlatten:
		return pb.DelistStepKind_DELIST_STEP_KIND_FLATTEN
	case store.DelistKindVerify:
		return pb.DelistStepKind_DELIST_STEP_KIND_VERIFY
	}
	return pb.DelistStepKind_DELIST_STEP_KIND_UNSPECIFIED
}

func scopeToPB(scope []store.ScopeFilter) []*pb.ScopeFilter {
	result := make([]*pb.ScopeFilter, 0, len(scope))
	for _, filter := range scope {
		result = append(result, &pb.ScopeFilter{Field: filter.Field, Values: append([]string(nil), filter.Values...)})
	}
	return result
}

// secretHint 只给出验证密钥的末 4 位。
func secretHint(secret string) string {
	runes := []rune(secret)
	if len(runes) <= 4 {
		return ""
	}
	return string(runes[len(runes)-4:])
}

func ruleToPB(rule store.Rule, last *store.LastExecution) *pb.EventRule {
	result := &pb.EventRule{
		RuleId: rule.RuleID, Name: rule.Name, EventName: rule.EventName, Scope: scopeToPB(rule.Scope),
		ActionType: actionTypeToPB(rule.ActionType), NotifyUrl: rule.NotifyURL, AllowPrivateAddress: rule.AllowPrivate,
		SecretHint: secretHint(rule.Secret), SystemAction: systemActionToPB(rule.SystemAction), System: rule.System, Enabled: rule.Enabled,
		CreatedAtMs: rule.CreatedAt, UpdatedAtMs: rule.UpdatedAt,
	}
	if last != nil {
		result.LastExecution = &pb.RuleLastExecution{Status: statusToPB[last.Status], AtMs: last.At}
	}
	return result
}

func executionToPB(execution store.Execution) *pb.EventExecution {
	return &pb.EventExecution{
		ExecutionId: execution.ExecutionID, RuleId: execution.RuleID, RuleName: execution.RuleName,
		ActionType: actionTypeToPB(execution.ActionType), EventId: execution.EventID, EventName: execution.EventName,
		SpaceId: execution.SpaceID, SubjectId: execution.SubjectID, Status: statusToPB[execution.Status],
		Attempts: uint32(execution.Attempts), LastError: execution.LastError, OccurredAtMs: execution.OccurredAt,
		CreatedAtMs: execution.CreatedAt, FinishedAtMs: execution.FinishedAt, DurationMs: execution.DurationMS,
		NextRunAtMs: execution.NextRunAt, Acknowledged: execution.Acknowledged,
	}
}

func attemptToPB(attempt store.Attempt) *pb.DeliveryAttempt {
	return &pb.DeliveryAttempt{
		Attempt: uint32(attempt.Attempt), AtMs: attempt.At, HttpStatus: int32(attempt.HTTPStatus),
		DurationMs: attempt.DurationMS, ResponseSnippet: attempt.ResponseSnippet, Error: attempt.Error,
	}
}

func delistStepToPB(action store.DelistAction) *pb.DelistStep {
	return &pb.DelistStep{
		Round: uint32(action.Round), Kind: delistKindToPB(action.Kind), SpaceId: action.SpaceID, LogicalAccountId: action.LogicalAccountID,
		Exchange: action.Exchange, OperatorActionId: action.OperatorActionID, Status: action.Status, Attempts: uint32(action.Attempts),
		Result: action.Result, Error: action.Error, UpdatedAtMs: action.UpdatedAt,
	}
}

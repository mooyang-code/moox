package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	pb "github.com/mooyang-code/moox/modules/callbackdispatcher/proto/callbackdispatchergen"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/schema"
	"github.com/stretchr/testify/require"
)

const delistEvent = "event.collector.instrument.delist_scheduled"

var testNow = time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)

type fakeSender struct {
	result  delivery.Result
	request delivery.Request
	target  delivery.Target
}

func (f *fakeSender) Send(_ context.Context, target delivery.Target, request delivery.Request) delivery.Result {
	f.target, f.request = target, request
	return f.result
}

type cancelHandler struct{ err error }

func (cancelHandler) Step(context.Context, store.Execution, time.Time) (action.Outcome, error) {
	return action.Outcome{Status: store.StatusSucceeded}, nil
}
func (h cancelHandler) OnCancel(context.Context, store.Execution) error { return h.err }

func newService(t *testing.T) (*Service, *fakeSender) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "dispatcher.db"))
	require.NoError(t, err)
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.EnsureSystemRule(context.Background(), SystemRule(testNow)))
	sender := &fakeSender{result: delivery.Result{Class: delivery.Success, StatusCode: 200, Duration: 15 * time.Millisecond, Snippet: "ok"}}
	return &Service{Store: s, Sender: sender, Actions: action.NewRegistry(), Now: func() time.Time { return testNow }}, sender
}

func createRule(t *testing.T, s *Service, mutate func(*pb.CreateEventRuleReq)) *pb.CreateEventRuleRsp {
	t.Helper()
	req := &pb.CreateEventRuleReq{
		Name: "通知我", EventName: delistEvent, NotifyUrl: "https://example.com/hook",
		Scope: []*pb.ScopeFilter{{Field: "exchange", Values: []string{"binance"}}},
	}
	if mutate != nil {
		mutate(req)
	}
	rsp, err := s.CreateEventRule(context.Background(), req)
	require.NoError(t, err)
	return rsp
}

func TestSubscribableEventsCatalog(t *testing.T) {
	s, _ := newService(t)
	rsp, err := s.ListSubscribableEvents(context.Background(), &pb.ListSubscribableEventsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	names := map[string]*pb.SubscribableEvent{}
	for _, event := range rsp.Events {
		names[event.EventName] = event
	}
	require.Contains(t, names, delistEvent)
	require.NotContains(t, names, "event.trade.target.weight_requested", "工作队列里的事件不开放")
	require.NotContains(t, names, "event.storage.dataset.rows.upserted")
	var fields []string
	for _, field := range names[delistEvent].ScopeFields {
		fields = append(fields, field.Field)
	}
	require.Equal(t, []string{"exchange", "market", "exchange_symbol"}, fields)
}

func TestCreateRuleReturnsSecretOnlyOnce(t *testing.T) {
	s, _ := newService(t)
	created := createRule(t, s, nil)
	require.Equal(t, pb.ErrorCode_SUCCESS, created.RetInfo.Code, created.RetInfo.Msg)
	require.True(t, strings.HasPrefix(created.SigningSecret, "whsec_"))
	require.Len(t, created.SigningSecret, len("whsec_")+64)
	require.True(t, created.Rule.Enabled, "不带 enabled 时默认启用")
	require.Equal(t, created.SigningSecret[len(created.SigningSecret)-4:], created.Rule.SecretHint)
	require.NotContains(t, created.Rule.String(), created.SigningSecret)

	got, err := s.GetEventRule(context.Background(), &pb.GetEventRuleReq{RuleId: created.Rule.RuleId})
	require.NoError(t, err)
	require.Equal(t, created.Rule.SecretHint, got.Rule.SecretHint)
	require.NotContains(t, got.String(), created.SigningSecret)

	list, err := s.ListEventRules(context.Background(), &pb.ListEventRulesReq{})
	require.NoError(t, err)
	require.Len(t, list.Rules, 2)
	require.True(t, list.Rules[0].System, "系统规则排在前面")
	require.NotContains(t, list.String(), created.SigningSecret)
	require.EqualValues(t, 2, list.PageResult.Total)
}

func TestCreateRuleValidation(t *testing.T) {
	s, _ := newService(t)
	disabled := false
	for name, mutate := range map[string]func(*pb.CreateEventRuleReq){
		"名称为空":    func(r *pb.CreateEventRuleReq) { r.Name = "  " },
		"名称太长":    func(r *pb.CreateEventRuleReq) { r.Name = strings.Repeat("长", 101) },
		"事件不在目录里": func(r *pb.CreateEventRuleReq) { r.EventName = "event.trade.target.weight_requested" },
		"事件不存在":   func(r *pb.CreateEventRuleReq) { r.EventName = "event.nope" },
		"字段不属于这个事件": func(r *pb.CreateEventRuleReq) {
			r.Scope = []*pb.ScopeFilter{{Field: "space_id", Values: []string{"crypto"}}}
		},
		"取值为空": func(r *pb.CreateEventRuleReq) { r.Scope = []*pb.ScopeFilter{{Field: "exchange"}} },
		"交易所不在选项里": func(r *pb.CreateEventRuleReq) {
			r.Scope = []*pb.ScopeFilter{{Field: "exchange", Values: []string{"kraken"}}}
		},
		"字段重复": func(r *pb.CreateEventRuleReq) {
			r.Scope = []*pb.ScopeFilter{{Field: "exchange", Values: []string{"binance"}}, {Field: "exchange", Values: []string{"okx"}}}
		},
		"地址为空":      func(r *pb.CreateEventRuleReq) { r.NotifyUrl = "" },
		"地址不是 http": func(r *pb.CreateEventRuleReq) { r.NotifyUrl = "ftp://example.com/x" },
		"地址内嵌密码":    func(r *pb.CreateEventRuleReq) { r.NotifyUrl = "https://user:pw@example.com/x" },
	} {
		rsp := createRule(t, s, mutate)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code, name)
		require.NotEmpty(t, rsp.RetInfo.Msg, name)
		require.Empty(t, rsp.SigningSecret, name)
	}
	ok := createRule(t, s, func(r *pb.CreateEventRuleReq) {
		r.Enabled = &disabled
		r.Scope = []*pb.ScopeFilter{{Field: "exchange", Values: []string{"binance", "okx", "binance"}}, {Field: "market", Values: []string{"SWAP"}}}
	})
	require.Equal(t, pb.ErrorCode_SUCCESS, ok.RetInfo.Code, ok.RetInfo.Msg)
	require.False(t, ok.Rule.Enabled)
	require.Equal(t, []string{"binance", "okx"}, ok.Rule.Scope[0].Values, "重复取值去重")
}

func TestSystemRuleCanOnlyBeToggled(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	name, enabled, url := "改名", false, "https://example.com"
	for name, req := range map[string]*pb.UpdateEventRuleReq{
		"改名":   {RuleId: SystemRuleID, Name: &name},
		"改范围":  {RuleId: SystemRuleID, Scope: &pb.ScopeFilters{}},
		"改地址":  {RuleId: SystemRuleID, NotifyUrl: &url},
		"改内网":  {RuleId: SystemRuleID, AllowPrivateAddress: &enabled},
		"又改又停": {RuleId: SystemRuleID, Name: &name, Enabled: &enabled},
	} {
		rsp, err := s.UpdateEventRule(ctx, req)
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code, name)
	}
	rsp, err := s.UpdateEventRule(ctx, &pb.UpdateEventRuleReq{RuleId: SystemRuleID, Enabled: &enabled})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code, rsp.RetInfo.Msg)
	require.False(t, rsp.Rule.Enabled)
	require.True(t, rsp.Rule.System)

	deleted, err := s.DeleteEventRule(ctx, &pb.DeleteEventRuleReq{RuleId: SystemRuleID})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, deleted.RetInfo.Code)

	// 用户不能新建系统动作规则：创建接口只产生通知地址规则。
	created := createRule(t, s, nil)
	require.Equal(t, pb.ActionType_ACTION_TYPE_NOTIFY_URL, created.Rule.ActionType)
	require.False(t, created.Rule.System)
}

func TestUpdateOnlyChangesGivenFields(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	created := createRule(t, s, nil)
	disabled := false
	rsp, err := s.UpdateEventRule(ctx, &pb.UpdateEventRuleReq{RuleId: created.Rule.RuleId, Enabled: &disabled})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code, rsp.RetInfo.Msg)
	require.False(t, rsp.Rule.Enabled)
	require.Equal(t, "通知我", rsp.Rule.Name, "只给 enabled 不能清掉名称")
	require.Equal(t, "https://example.com/hook", rsp.Rule.NotifyUrl)
	require.Len(t, rsp.Rule.Scope, 1)

	newName, newURL, allow := "新名字", "https://example.org/new", true
	rsp, err = s.UpdateEventRule(ctx, &pb.UpdateEventRuleReq{RuleId: created.Rule.RuleId, Name: &newName, NotifyUrl: &newURL, AllowPrivateAddress: &allow, Scope: &pb.ScopeFilters{}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code, rsp.RetInfo.Msg)
	require.Equal(t, newName, rsp.Rule.Name)
	require.Equal(t, newURL, rsp.Rule.NotifyUrl)
	require.True(t, rsp.Rule.AllowPrivateAddress)
	require.Empty(t, rsp.Rule.Scope, "带上空的范围表示不限")
	require.False(t, rsp.Rule.Enabled)
	require.Equal(t, created.Rule.SecretHint, rsp.Rule.SecretHint, "验证密钥不变")

	bad := "javascript:alert(1)"
	rsp, err = s.UpdateEventRule(ctx, &pb.UpdateEventRuleReq{RuleId: created.Rule.RuleId, NotifyUrl: &bad})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
	got, err := s.GetEventRule(ctx, &pb.GetEventRuleReq{RuleId: created.Rule.RuleId})
	require.NoError(t, err)
	require.Equal(t, newURL, got.Rule.NotifyUrl, "校验失败不写入")

	missing, err := s.UpdateEventRule(ctx, &pb.UpdateEventRuleReq{RuleId: "none", Enabled: &disabled})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, missing.RetInfo.Code)
}

func TestTestEventRuleSendsSampleAndWritesNoRecord(t *testing.T) {
	ctx := context.Background()
	s, sender := newService(t)
	created := createRule(t, s, nil)
	rsp, err := s.TestEventRule(ctx, &pb.TestEventRuleReq{RuleId: created.Rule.RuleId})
	require.NoError(t, err)
	require.True(t, rsp.Success)
	require.EqualValues(t, 200, rsp.HttpStatus)
	require.EqualValues(t, 15, rsp.DurationMs)
	require.Equal(t, "ok", rsp.ResponseSnippet)
	require.Equal(t, created.SigningSecret, sender.target.Secret)
	require.Equal(t, "https://example.com/hook", sender.target.URL)
	require.True(t, strings.HasPrefix(sender.request.EventID, "test-"))
	require.Contains(t, sender.request.PayloadJSON, "FOOUSDT")
	list, err := s.ListEventExecutions(ctx, &pb.ListEventExecutionsReq{})
	require.NoError(t, err)
	require.Empty(t, list.Executions)

	sender.result = delivery.Result{Class: delivery.Permanent, Err: errors.New("通知地址被出站防护拒绝")}
	rsp, err = s.TestEventRule(ctx, &pb.TestEventRuleReq{RuleId: created.Rule.RuleId})
	require.NoError(t, err)
	require.False(t, rsp.Success)
	require.Zero(t, rsp.HttpStatus)
	require.Contains(t, rsp.ResponseSnippet, "出站防护")

	system, err := s.TestEventRule(ctx, &pb.TestEventRuleReq{RuleId: SystemRuleID})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, system.RetInfo.Code)
}

func seedExecution(t *testing.T, s *Service, id, ruleID, action, status string) {
	t.Helper()
	require.NoError(t, s.Store.InTx(context.Background(), func(tx *store.Tx) error {
		execution := store.Execution{
			ExecutionID: id, RuleID: ruleID, RuleName: "规则", ActionType: action, EventID: "event-" + id, EventName: delistEvent,
			SpaceID: "mooxsys", SubjectID: "binance:SPOT:FOOUSDT", OccurredAt: 1, PayloadJSON: `{"exchange":"binance"}`,
			Status: status, NextRunAt: 5, CreatedAt: testNow.UnixMilli(), UpdatedAt: 1,
		}
		if action == store.ActionSystem && status == store.StatusPending {
			execution.DedupeKey = ruleID + "|binance:SPOT:FOOUSDT"
		}
		if status == store.StatusFailed || status == store.StatusSucceeded || status == store.StatusCanceled {
			execution.NextRunAt = 0
		}
		return tx.InsertExecution(execution)
	}))
}

func TestExecutionLifecycleRetryAckCancel(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	created := createRule(t, s, nil)
	seedExecution(t, s, "failed", created.Rule.RuleId, store.ActionNotifyURL, store.StatusFailed)
	seedExecution(t, s, "pending", created.Rule.RuleId, store.ActionNotifyURL, store.StatusPending)
	seedExecution(t, s, "done", created.Rule.RuleId, store.ActionNotifyURL, store.StatusSucceeded)

	// 筛选
	failedOnly, err := s.ListEventExecutions(ctx, &pb.ListEventExecutionsReq{Status: pb.ExecutionStatus_EXECUTION_STATUS_FAILED})
	require.NoError(t, err)
	require.Len(t, failedOnly.Executions, 1)
	require.Equal(t, "failed", failedOnly.Executions[0].ExecutionId)
	byRule, err := s.ListEventExecutions(ctx, &pb.ListEventExecutionsReq{RuleId: created.Rule.RuleId})
	require.NoError(t, err)
	require.Len(t, byRule.Executions, 3)

	// 规则列表带最近一次执行
	rules, err := s.ListEventRules(ctx, &pb.ListEventRulesReq{})
	require.NoError(t, err)
	for _, rule := range rules.Rules {
		if rule.RuleId == created.Rule.RuleId {
			require.NotNil(t, rule.LastExecution)
		}
	}

	// 确认：只对失败的记录
	ack, err := s.AcknowledgeEventExecution(ctx, &pb.AcknowledgeEventExecutionReq{ExecutionId: "failed"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, ack.RetInfo.Code)
	require.True(t, ack.Execution.Acknowledged)
	ack, err = s.AcknowledgeEventExecution(ctx, &pb.AcknowledgeEventExecutionReq{ExecutionId: "done"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, ack.RetInfo.Code)

	// 重新执行：只对失败的记录
	retried, err := s.RetryEventExecution(ctx, &pb.RetryEventExecutionReq{ExecutionId: "failed"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, retried.RetInfo.Code, retried.RetInfo.Msg)
	require.Equal(t, pb.ExecutionStatus_EXECUTION_STATUS_PENDING, retried.Execution.Status)
	require.False(t, retried.Execution.Acknowledged)
	again, err := s.RetryEventExecution(ctx, &pb.RetryEventExecutionReq{ExecutionId: "failed"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, again.RetInfo.Code)

	// 取消：只对没结束的记录
	canceled, err := s.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "pending"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, canceled.RetInfo.Code, canceled.RetInfo.Msg)
	require.Equal(t, pb.ExecutionStatus_EXECUTION_STATUS_CANCELED, canceled.Execution.Status)
	done, err := s.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "done"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, done.RetInfo.Code)

	missing, err := s.GetEventExecution(ctx, &pb.GetEventExecutionReq{ExecutionId: "none"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, missing.RetInfo.Code)
}

func TestExecutionDetailIncludesAttemptsAndDelistSteps(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	seedExecution(t, s, "sys-exec", SystemRuleID, store.ActionSystem, store.StatusPending)
	require.NoError(t, s.Store.UpsertDelistAction(ctx, store.DelistAction{
		ExecutionID: "sys-exec", Round: 1, Kind: store.DelistKindFlatten, SpaceID: "crypto", LogicalAccountID: "la-1", Exchange: "binance",
		OperatorActionID: "op-1", Status: store.DelistSucceeded, Attempts: 2, Result: "已平仓", UpdatedAt: 7,
	}))
	rsp, err := s.GetEventExecution(ctx, &pb.GetEventExecutionReq{ExecutionId: "sys-exec"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Equal(t, `{"exchange":"binance"}`, rsp.Detail.PayloadJson)
	require.Len(t, rsp.Detail.DelistSteps, 1)
	step := rsp.Detail.DelistSteps[0]
	require.Equal(t, pb.DelistStepKind_DELIST_STEP_KIND_FLATTEN, step.Kind)
	require.Equal(t, "op-1", step.OperatorActionId)
	require.EqualValues(t, 2, step.Attempts)
}

func TestCancelSystemExecutionRunsHandlerCleanupFirst(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)

	// 没注册处理器（C6 之前）：直接取消。
	seedExecution(t, s, "no-handler", SystemRuleID, store.ActionSystem, store.StatusPending)
	rsp, err := s.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "no-handler"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code, rsp.RetInfo.Msg)

	// 善后（解除禁止）失败：不取消，用户可以重试。
	s.Actions.Register(store.SystemActionSellDelistedHoldings, cancelHandler{err: errors.New("Trade 不可达")})
	seedExecution(t, s, "cleanup-fails", SystemRuleID, store.ActionSystem, store.StatusPending)
	rsp, err = s.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "cleanup-fails"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
	require.NotContains(t, rsp.RetInfo.Msg, "Trade 不可达", "内部错误不原样返回")
	got, err := s.GetEventExecution(ctx, &pb.GetEventExecutionReq{ExecutionId: "cleanup-fails"})
	require.NoError(t, err)
	require.Equal(t, pb.ExecutionStatus_EXECUTION_STATUS_PENDING, got.Detail.Execution.Status)

	s.Actions.Register(store.SystemActionSellDelistedHoldings, cancelHandler{})
	rsp, err = s.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "cleanup-fails"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code, rsp.RetInfo.Msg)
}

func TestDeleteRuleCancelsItsPendingExecutions(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	created := createRule(t, s, nil)
	seedExecution(t, s, "pending", created.Rule.RuleId, store.ActionNotifyURL, store.StatusPending)
	rsp, err := s.DeleteEventRule(ctx, &pb.DeleteEventRuleReq{RuleId: created.Rule.RuleId})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	got, err := s.GetEventExecution(ctx, &pb.GetEventExecutionReq{ExecutionId: "pending"})
	require.NoError(t, err)
	require.Equal(t, pb.ExecutionStatus_EXECUTION_STATUS_CANCELED, got.Detail.Execution.Status)
	again, err := s.DeleteEventRule(ctx, &pb.DeleteEventRuleReq{RuleId: created.Rule.RuleId})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, again.RetInfo.Code)
}

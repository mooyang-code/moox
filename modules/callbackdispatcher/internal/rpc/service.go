// Package rpc 实现 CallbackDispatcherService：管理台「事件中心」与 moox-cli 用的事件规则、执行记录接口。
package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/catalog"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/dispatch"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	pb "github.com/mooyang-code/moox/modules/callbackdispatcher/proto/callbackdispatchergen"
	"trpc.group/trpc-go/trpc-go/log"
)

// SystemRuleID 是服务自带的系统规则：标的即将下架时，卖出策略持有的这个币。
const SystemRuleID = "system-sell-delisted-holdings"

// SystemRule 返回服务自带的系统规则；首次启动时写入库，默认启用，用户可以停用但不能删除、不能修改。
func SystemRule(now time.Time) store.Rule {
	return store.Rule{
		RuleID: SystemRuleID, Name: "标的即将下架时，卖出策略持有的这个币",
		EventName:  "event.collector.instrument.delist_scheduled",
		ActionType: store.ActionSystem, SystemAction: store.SystemActionSellDelistedHoldings, System: true, Enabled: true,
		CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
	}
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
	maxNameRunes    = 100
	maxScopeValues  = 100
	maxRules        = 200
)

// Service 是 CallbackDispatcherService 的实现。
type Service struct {
	Store   *store.Store
	Sender  dispatch.Sender
	Actions *action.Registry
	Now     func() time.Time
}

var _ pb.CallbackDispatcherServiceService = (*Service)(nil)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func okRet() *pb.RetInfo { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS} }

func failRet(code pb.ErrorCode, message string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: message}
}

func invalid(message string) *pb.RetInfo { return failRet(pb.ErrorCode_INVALID_PARAM, message) }

// internalRet 把内部错误记进日志，对外只给通用说明，避免把数据库细节带给调用方。
func internalRet(ctx context.Context, what string, err error) *pb.RetInfo {
	log.ErrorContextf(ctx, "%s: %v", what, err)
	return failRet(pb.ErrorCode_INNER_ERR, what+"，请查看事件中心日志")
}

func pageParams(page *pb.Page) (int, int) {
	number, size := int(page.GetPage()), int(page.GetSize())
	if number <= 0 {
		number = 1
	}
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return number, size
}

func pageResult(page, size int, total int64) *pb.PageResult {
	return &pb.PageResult{Page: uint32(page), Size: uint32(size), Total: uint32(total), HasMore: int64(page*size) < total}
}

// ListSubscribableEvents 返回可配置规则的事件目录。
func (s *Service) ListSubscribableEvents(_ context.Context, _ *pb.ListSubscribableEventsReq) (*pb.ListSubscribableEventsRsp, error) {
	rsp := &pb.ListSubscribableEventsRsp{RetInfo: okRet()}
	for _, entry := range catalog.Subscribable() {
		item := &pb.SubscribableEvent{EventName: entry.Name(), Version: entry.Event.Version(), Title: entry.Title, Description: entry.Description}
		for _, field := range entry.Fields {
			item.ScopeFields = append(item.ScopeFields, &pb.ScopeField{Field: field.Key, Title: field.Title, Options: append([]string(nil), field.Options...)})
		}
		rsp.Events = append(rsp.Events, item)
	}
	return rsp, nil
}

// ListEventRules 列出规则。
func (s *Service) ListEventRules(ctx context.Context, req *pb.ListEventRulesReq) (*pb.ListEventRulesRsp, error) {
	page, size := pageParams(req.GetPage())
	rules, total, err := s.Store.ListRules(ctx, (page-1)*size, size)
	if err != nil {
		return &pb.ListEventRulesRsp{RetInfo: internalRet(ctx, "读取事件规则失败", err)}, nil
	}
	ids := make([]string, 0, len(rules))
	for _, rule := range rules {
		ids = append(ids, rule.RuleID)
	}
	last, err := s.Store.LastExecutions(ctx, ids)
	if err != nil {
		return &pb.ListEventRulesRsp{RetInfo: internalRet(ctx, "读取规则的最近一次执行失败", err)}, nil
	}
	rsp := &pb.ListEventRulesRsp{RetInfo: okRet(), PageResult: pageResult(page, size, total)}
	for _, rule := range rules {
		var latest *store.LastExecution
		if value, ok := last[rule.RuleID]; ok {
			latest = &value
		}
		rsp.Rules = append(rsp.Rules, ruleToPB(rule, latest))
	}
	return rsp, nil
}

// GetEventRule 返回一条规则。
func (s *Service) GetEventRule(ctx context.Context, req *pb.GetEventRuleReq) (*pb.GetEventRuleRsp, error) {
	if strings.TrimSpace(req.GetRuleId()) == "" {
		return &pb.GetEventRuleRsp{RetInfo: invalid("rule_id 不能为空")}, nil
	}
	rule, err := s.Store.GetRule(ctx, req.GetRuleId())
	if errors.Is(err, store.ErrNotFound) {
		return &pb.GetEventRuleRsp{RetInfo: failRet(pb.ErrorCode_NOT_FOUND, "规则不存在")}, nil
	}
	if err != nil {
		return &pb.GetEventRuleRsp{RetInfo: internalRet(ctx, "读取事件规则失败", err)}, nil
	}
	last, err := s.Store.LastExecutions(ctx, []string{rule.RuleID})
	if err != nil {
		return &pb.GetEventRuleRsp{RetInfo: internalRet(ctx, "读取规则的最近一次执行失败", err)}, nil
	}
	var latest *store.LastExecution
	if value, ok := last[rule.RuleID]; ok {
		latest = &value
	}
	return &pb.GetEventRuleRsp{RetInfo: okRet(), Rule: ruleToPB(rule, latest)}, nil
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("规则名称不能为空")
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", fmt.Errorf("规则名称不能超过 %d 个字", maxNameRunes)
	}
	return name, nil
}

// normalizeScope 按事件目录校验并整理限定范围：字段必须是这个事件可限定的，取值不能为空、不能重复，
// 有固定选项的字段（交易所、市场）取值必须在选项里。
func normalizeScope(entry catalog.Entry, scope []*pb.ScopeFilter) ([]store.ScopeFilter, error) {
	result := make([]store.ScopeFilter, 0, len(scope))
	seen := map[string]bool{}
	for _, filter := range scope {
		field := strings.TrimSpace(filter.GetField())
		var definition *catalog.Field
		for i := range entry.Fields {
			if entry.Fields[i].Key == field {
				definition = &entry.Fields[i]
			}
		}
		if definition == nil {
			return nil, fmt.Errorf("事件“%s”不能按 %q 限定范围", entry.Title, field)
		}
		if seen[field] {
			return nil, fmt.Errorf("%s 重复出现，多个取值请放在同一项里", definition.Title)
		}
		seen[field] = true
		if len(filter.GetValues()) == 0 || len(filter.GetValues()) > maxScopeValues {
			return nil, fmt.Errorf("%s 至少填一个取值，最多 %d 个", definition.Title, maxScopeValues)
		}
		values := make([]string, 0, len(filter.GetValues()))
		valueSeen := map[string]bool{}
		for _, raw := range filter.GetValues() {
			value := strings.TrimSpace(raw)
			if value == "" || len(value) > 256 {
				return nil, fmt.Errorf("%s 的取值不能为空，也不能太长", definition.Title)
			}
			if len(definition.Options) != 0 && !contains(definition.Options, value) {
				return nil, fmt.Errorf("%s 的取值 %q 不在可选范围 %s 内", definition.Title, value, strings.Join(definition.Options, "、"))
			}
			if !valueSeen[value] {
				valueSeen[value] = true
				values = append(values, value)
			}
		}
		result = append(result, store.ScopeFilter{Field: field, Values: values})
	}
	return result, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func newSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(raw), nil
}

// CreateEventRule 新建通知地址规则，验证密钥只在返回里完整出现这一次。
func (s *Service) CreateEventRule(ctx context.Context, req *pb.CreateEventRuleReq) (*pb.CreateEventRuleRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.CreateEventRuleRsp, error) {
		return &pb.CreateEventRuleRsp{RetInfo: info}, nil
	}
	name, err := validateName(req.GetName())
	if err != nil {
		return fail(invalid(err.Error()))
	}
	entry, ok := catalog.Lookup(strings.TrimSpace(req.GetEventName()))
	if !ok {
		return fail(invalid("触发事件不在可配置的事件里，用 ListSubscribableEvents 查看"))
	}
	scope, err := normalizeScope(entry, req.GetScope())
	if err != nil {
		return fail(invalid(err.Error()))
	}
	notifyURL := strings.TrimSpace(req.GetNotifyUrl())
	if err := delivery.ValidateURL(notifyURL); err != nil {
		return fail(invalid(err.Error()))
	}
	_, total, err := s.Store.ListRules(ctx, 0, 1)
	if err != nil {
		return fail(internalRet(ctx, "读取事件规则失败", err))
	}
	if total >= maxRules {
		return fail(failRet(pb.ErrorCode_CONFLICT, fmt.Sprintf("规则最多 %d 条，请先删掉不用的", maxRules)))
	}
	secret, err := newSecret()
	if err != nil {
		return fail(internalRet(ctx, "生成验证密钥失败", err))
	}
	now := s.now().UnixMilli()
	rule := store.Rule{
		RuleID: uuid.NewString(), Name: name, EventName: entry.Name(), Scope: scope, ActionType: store.ActionNotifyURL,
		NotifyURL: notifyURL, AllowPrivate: req.GetAllowPrivateAddress(), Secret: secret,
		Enabled: req.Enabled == nil || req.GetEnabled(), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Store.CreateRule(ctx, rule); err != nil {
		return fail(internalRet(ctx, "保存事件规则失败", err))
	}
	return &pb.CreateEventRuleRsp{RetInfo: okRet(), Rule: ruleToPB(rule, nil), SigningSecret: secret}, nil
}

// UpdateEventRule 只改请求里带了的字段；系统规则只允许改启停。
func (s *Service) UpdateEventRule(ctx context.Context, req *pb.UpdateEventRuleReq) (*pb.UpdateEventRuleRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.UpdateEventRuleRsp, error) {
		return &pb.UpdateEventRuleRsp{RetInfo: info}, nil
	}
	if strings.TrimSpace(req.GetRuleId()) == "" {
		return fail(invalid("rule_id 不能为空"))
	}
	var problem error
	rule, err := s.Store.UpdateRule(ctx, req.GetRuleId(), s.now().UnixMilli(), func(rule *store.Rule) error {
		if rule.System {
			if req.Name != nil || req.Scope != nil || req.NotifyUrl != nil || req.AllowPrivateAddress != nil {
				problem = errors.New("系统规则只能启用或停用，不能修改其他内容")
				return problem
			}
		}
		if req.Name != nil {
			name, err := validateName(req.GetName())
			if err != nil {
				problem = err
				return err
			}
			rule.Name = name
		}
		if req.Scope != nil {
			entry, ok := catalog.Lookup(rule.EventName)
			if !ok {
				problem = errors.New("规则的触发事件已经不在可配置的事件里")
				return problem
			}
			scope, err := normalizeScope(entry, req.GetScope().GetItems())
			if err != nil {
				problem = err
				return err
			}
			rule.Scope = scope
		}
		if req.NotifyUrl != nil {
			notifyURL := strings.TrimSpace(req.GetNotifyUrl())
			if err := delivery.ValidateURL(notifyURL); err != nil {
				problem = err
				return err
			}
			rule.NotifyURL = notifyURL
		}
		if req.AllowPrivateAddress != nil {
			rule.AllowPrivate = req.GetAllowPrivateAddress()
		}
		if req.Enabled != nil {
			rule.Enabled = req.GetEnabled()
		}
		return nil
	})
	switch {
	case problem != nil:
		return fail(invalid(problem.Error()))
	case errors.Is(err, store.ErrNotFound):
		return fail(failRet(pb.ErrorCode_NOT_FOUND, "规则不存在"))
	case err != nil:
		return fail(internalRet(ctx, "保存事件规则失败", err))
	}
	return &pb.UpdateEventRuleRsp{RetInfo: okRet(), Rule: ruleToPB(rule, nil)}, nil
}

// DeleteEventRule 删除通知地址规则；系统规则不能删除。
func (s *Service) DeleteEventRule(ctx context.Context, req *pb.DeleteEventRuleReq) (*pb.DeleteEventRuleRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.DeleteEventRuleRsp, error) {
		return &pb.DeleteEventRuleRsp{RetInfo: info}, nil
	}
	rule, err := s.Store.GetRule(ctx, req.GetRuleId())
	if errors.Is(err, store.ErrNotFound) {
		return fail(failRet(pb.ErrorCode_NOT_FOUND, "规则不存在"))
	}
	if err != nil {
		return fail(internalRet(ctx, "读取事件规则失败", err))
	}
	if rule.System {
		return fail(invalid("系统规则不能删除，只能停用"))
	}
	if err := s.Store.DeleteRule(ctx, rule.RuleID, s.now().UnixMilli()); err != nil {
		return fail(internalRet(ctx, "删除事件规则失败", err))
	}
	return &pb.DeleteEventRuleRsp{RetInfo: okRet()}, nil
}

// TestEventRule 用一条示例事件向通知地址发一次，不写执行记录。
func (s *Service) TestEventRule(ctx context.Context, req *pb.TestEventRuleReq) (*pb.TestEventRuleRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.TestEventRuleRsp, error) { return &pb.TestEventRuleRsp{RetInfo: info}, nil }
	rule, err := s.Store.GetRule(ctx, req.GetRuleId())
	if errors.Is(err, store.ErrNotFound) {
		return fail(failRet(pb.ErrorCode_NOT_FOUND, "规则不存在"))
	}
	if err != nil {
		return fail(internalRet(ctx, "读取事件规则失败", err))
	}
	if rule.ActionType != store.ActionNotifyURL {
		return fail(invalid("只有通知地址规则能发测试"))
	}
	entry, ok := catalog.Lookup(rule.EventName)
	if !ok {
		return fail(invalid("规则的触发事件已经不在可配置的事件里"))
	}
	payload, err := dispatch.SampleJSON(entry)
	if err != nil {
		return fail(internalRet(ctx, "生成示例事件失败", err))
	}
	now := s.now()
	result := s.Sender.Send(ctx, delivery.Target{URL: rule.NotifyURL, Secret: rule.Secret, AllowPrivate: rule.AllowPrivate}, delivery.Request{
		RuleID: rule.RuleID, EventID: "test-" + uuid.NewString(), EventName: entry.Name(), EventVersion: entry.Event.Version(),
		SpaceID: "mooxsys", SubjectID: "test", OccurredAt: now, PayloadJSON: payload,
	})
	rsp := &pb.TestEventRuleRsp{RetInfo: okRet(), HttpStatus: int32(result.StatusCode), DurationMs: result.Duration.Milliseconds(), ResponseSnippet: result.Snippet, Success: result.Class == delivery.Success}
	if result.Err != nil {
		rsp.ResponseSnippet = result.Err.Error()
		if result.Snippet != "" {
			rsp.ResponseSnippet += "：" + result.Snippet
		}
	}
	return rsp, nil
}

// ListEventExecutions 列出执行记录。
func (s *Service) ListEventExecutions(ctx context.Context, req *pb.ListEventExecutionsReq) (*pb.ListEventExecutionsRsp, error) {
	filter := store.ExecutionFilter{RuleID: strings.TrimSpace(req.GetRuleId())}
	if req.GetStatus() != pb.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED {
		status, ok := statusFromPB[req.GetStatus()]
		if !ok {
			return &pb.ListEventExecutionsRsp{RetInfo: invalid("状态不合法")}, nil
		}
		filter.Status = status
	}
	page, size := pageParams(req.GetPage())
	executions, total, err := s.Store.ListExecutions(ctx, filter, (page-1)*size, size)
	if err != nil {
		return &pb.ListEventExecutionsRsp{RetInfo: internalRet(ctx, "读取执行记录失败", err)}, nil
	}
	rsp := &pb.ListEventExecutionsRsp{RetInfo: okRet(), PageResult: pageResult(page, size, total)}
	for _, execution := range executions {
		rsp.Executions = append(rsp.Executions, executionToPB(execution))
	}
	return rsp, nil
}

// GetEventExecution 返回执行记录的详情：事件内容、每次尝试、下架处理的各轮步骤。
func (s *Service) GetEventExecution(ctx context.Context, req *pb.GetEventExecutionReq) (*pb.GetEventExecutionRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.GetEventExecutionRsp, error) {
		return &pb.GetEventExecutionRsp{RetInfo: info}, nil
	}
	execution, err := s.Store.GetExecution(ctx, req.GetExecutionId())
	if errors.Is(err, store.ErrNotFound) {
		return fail(failRet(pb.ErrorCode_NOT_FOUND, "执行记录不存在"))
	}
	if err != nil {
		return fail(internalRet(ctx, "读取执行记录失败", err))
	}
	attempts, err := s.Store.ListAttempts(ctx, execution.ExecutionID)
	if err != nil {
		return fail(internalRet(ctx, "读取通知尝试失败", err))
	}
	steps, err := s.Store.ListDelistActions(ctx, execution.ExecutionID)
	if err != nil {
		return fail(internalRet(ctx, "读取下架处理步骤失败", err))
	}
	detail := &pb.EventExecutionDetail{Execution: executionToPB(execution), PayloadJson: execution.PayloadJSON}
	for _, attempt := range attempts {
		detail.Attempts = append(detail.Attempts, attemptToPB(attempt))
	}
	for _, step := range steps {
		detail.DelistSteps = append(detail.DelistSteps, delistStepToPB(step))
	}
	return &pb.GetEventExecutionRsp{RetInfo: okRet(), Detail: detail}, nil
}

// RetryEventExecution 重新执行失败的记录。
func (s *Service) RetryEventExecution(ctx context.Context, req *pb.RetryEventExecutionReq) (*pb.RetryEventExecutionRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.RetryEventExecutionRsp, error) {
		return &pb.RetryEventExecutionRsp{RetInfo: info}, nil
	}
	execution, err := s.Store.RetryExecution(ctx, req.GetExecutionId(), s.now().UnixMilli())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fail(failRet(pb.ErrorCode_NOT_FOUND, "执行记录不存在"))
	case errors.Is(err, store.ErrConflict):
		return fail(failRet(pb.ErrorCode_CONFLICT, "只有失败的记录能重新执行；同一标的已有未结束的下架处理时也不能重新执行"))
	case err != nil:
		return fail(internalRet(ctx, "重新执行失败", err))
	}
	return &pb.RetryEventExecutionRsp{RetInfo: okRet(), Execution: executionToPB(execution)}, nil
}

// CancelEventExecution 取消还没结束的执行；对系统动作先让处理器善后（下架：解除禁止买入），善后失败则不取消。
func (s *Service) CancelEventExecution(ctx context.Context, req *pb.CancelEventExecutionReq) (*pb.CancelEventExecutionRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.CancelEventExecutionRsp, error) {
		return &pb.CancelEventExecutionRsp{RetInfo: info}, nil
	}
	execution, err := s.Store.GetExecution(ctx, req.GetExecutionId())
	if errors.Is(err, store.ErrNotFound) {
		return fail(failRet(pb.ErrorCode_NOT_FOUND, "执行记录不存在"))
	}
	if err != nil {
		return fail(internalRet(ctx, "读取执行记录失败", err))
	}
	if execution.Status != store.StatusPending && execution.Status != store.StatusRunning {
		return fail(failRet(pb.ErrorCode_CONFLICT, "这条记录已经结束，不能取消"))
	}
	if execution.ActionType == store.ActionSystem {
		rule, err := s.Store.GetRule(ctx, execution.RuleID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fail(internalRet(ctx, "读取事件规则失败", err))
		}
		if handler, ok := s.Actions.Handler(rule.SystemAction); ok {
			if err := handler.OnCancel(ctx, execution); err != nil {
				log.ErrorContextf(ctx, "取消系统动作 %s 的善后失败: %v", execution.ExecutionID, err)
				return fail(failRet(pb.ErrorCode_INNER_ERR, "取消前的善后（解除禁止买入）失败，记录没有取消，请稍后重试"))
			}
		}
	}
	if _, err := s.Store.CancelExecution(ctx, execution.ExecutionID, s.now().UnixMilli()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fail(failRet(pb.ErrorCode_CONFLICT, "这条记录已经结束，不能取消"))
		}
		return fail(internalRet(ctx, "取消执行失败", err))
	}
	canceled, err := s.Store.GetExecution(ctx, execution.ExecutionID)
	if err != nil {
		return fail(internalRet(ctx, "读取执行记录失败", err))
	}
	return &pb.CancelEventExecutionRsp{RetInfo: okRet(), Execution: executionToPB(canceled)}, nil
}

// AcknowledgeEventExecution 确认失败的记录已处理。
func (s *Service) AcknowledgeEventExecution(ctx context.Context, req *pb.AcknowledgeEventExecutionReq) (*pb.AcknowledgeEventExecutionRsp, error) {
	fail := func(info *pb.RetInfo) (*pb.AcknowledgeEventExecutionRsp, error) {
		return &pb.AcknowledgeEventExecutionRsp{RetInfo: info}, nil
	}
	err := s.Store.AcknowledgeExecution(ctx, req.GetExecutionId(), s.now().UnixMilli())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fail(failRet(pb.ErrorCode_NOT_FOUND, "执行记录不存在"))
	case errors.Is(err, store.ErrConflict):
		return fail(failRet(pb.ErrorCode_CONFLICT, "只有失败的记录需要确认"))
	case err != nil:
		return fail(internalRet(ctx, "确认失败", err))
	}
	execution, err := s.Store.GetExecution(ctx, req.GetExecutionId())
	if err != nil {
		return fail(internalRet(ctx, "读取执行记录失败", err))
	}
	return &pb.AcknowledgeEventExecutionRsp{RetInfo: okRet(), Execution: executionToPB(execution)}, nil
}

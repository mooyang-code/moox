package tencentscf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
	"trpc.group/trpc-go/trpc-go/log"
)

const defaultTimerQualifier = "$LATEST"

type TimerTriggerRequest struct {
	FunctionRef
	Name      string
	Cron      string
	Enabled   bool
	Qualifier string
	Message   string
}

type TimerTriggerInfo struct {
	Name            string
	Type            string
	Cron            string
	Enabled         bool
	AvailableStatus string
	Qualifier       string
	Message         string
}

// GetTimerTrigger is a read-only provider lookup used by CloudNode's health
// refresh. It intentionally does not repair drift; Collector/Monitor should
// be able to report a manually disabled or deleted trigger before the next
// reconciliation decides to apply the desired state.
func (c *Client) GetTimerTrigger(ctx context.Context, ref FunctionRef, name string) (*TimerTriggerInfo, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("timer trigger name is required")
	}
	item, err := c.findTimerTrigger(ctx, ref, name)
	if err != nil || item == nil {
		return nil, err
	}
	return timerTriggerInfoFromSDK(item), nil
}

func (c *Client) EnsureTimerTrigger(ctx context.Context, req TimerTriggerRequest) (*TimerTriggerInfo, error) {
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Cron) == "" {
		return nil, fmt.Errorf("timer trigger name and cron are required")
	}
	current, err := c.findTimerTrigger(ctx, req.FunctionRef, req.Name)
	if err != nil {
		return nil, err
	}
	qualifier := firstNonEmpty(req.Qualifier, defaultTimerQualifier)
	message := req.Message
	if current == nil {
		return c.createTimerTrigger(ctx, req, qualifier, message)
	}
	if deref(current.Type) != "timer" {
		return nil, fmt.Errorf("trigger %q exists with type %q", req.Name, deref(current.Type))
	}
	currentCron := normalizeTimerCron(current.TriggerDesc)
	currentEnabled := current.Enable != nil && *current.Enable != 0
	currentQualifier := deref(current.Qualifier)
	currentMessage := deref(current.CustomArgument)
	if currentCron == req.Cron && currentEnabled == req.Enabled && currentQualifier == qualifier && currentMessage == message {
		readback, readbackErr := c.waitForTimerTrigger(ctx, req.FunctionRef, req, qualifier, message)
		if readbackErr != nil {
			return nil, readbackErr
		}
		info := timerTriggerInfoFromSDK(readback)
		if err := validateTimerTriggerInfo(info, req, qualifier, message); err != nil {
			return nil, err
		}
		return info, nil
	}
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] update timer trigger function=%s trigger=%s", req.FunctionName, req.Name)
	client, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	update := scf.NewUpdateTriggerRequest()
	update.FunctionName = common.StringPtr(req.FunctionName)
	update.TriggerName = common.StringPtr(req.Name)
	update.Type = common.StringPtr("timer")
	update.TriggerDesc = common.StringPtr(req.Cron)
	update.Enable = common.StringPtr(timerEnable(req.Enabled))
	update.Qualifier = common.StringPtr(qualifier)
	update.Namespace = common.StringPtr(req.Namespace)
	update.CustomArgument = common.StringPtr(message)
	if err := c.call(ctx, req.Region, "UpdateTrigger", func(ctx context.Context) error {
		_, callErr := client.UpdateTriggerWithContext(ctx, update)
		return callErr
	}); err != nil {
		return nil, err
	}
	updated, err := c.waitForTimerTrigger(ctx, req.FunctionRef, req, qualifier, message)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, fmt.Errorf("timer trigger %q disappeared after update", req.Name)
	}
	info := timerTriggerInfoFromSDK(updated)
	if err := validateTimerTriggerInfo(info, req, qualifier, message); err != nil {
		return nil, err
	}
	return info, nil
}

func (c *Client) DeleteTimerTrigger(ctx context.Context, req TimerTriggerRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return nil
	}
	current, err := c.findTimerTrigger(ctx, req.FunctionRef, req.Name)
	if err != nil || current == nil {
		return err
	}
	client, err := c.newClient(req.Region)
	if err != nil {
		return err
	}
	request := scf.NewDeleteTriggerRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.TriggerName = common.StringPtr(req.Name)
	request.Type = common.StringPtr("timer")
	request.Namespace = common.StringPtr(req.Namespace)
	request.Qualifier = common.StringPtr(firstNonEmpty(req.Qualifier, defaultTimerQualifier))
	return c.call(ctx, req.Region, "DeleteTrigger", func(ctx context.Context) error {
		_, callErr := client.DeleteTriggerWithContext(ctx, request)
		return callErr
	})
}

func (c *Client) createTimerTrigger(ctx context.Context, req TimerTriggerRequest, qualifier, message string) (*TimerTriggerInfo, error) {
	log.InfoContextf(ctx, "[CloudNode-TencentSCF] create timer trigger function=%s trigger=%s", req.FunctionName, req.Name)
	client, err := c.newClient(req.Region)
	if err != nil {
		return nil, err
	}
	request := scf.NewCreateTriggerRequest()
	request.FunctionName = common.StringPtr(req.FunctionName)
	request.TriggerName = common.StringPtr(req.Name)
	request.Type = common.StringPtr("timer")
	request.TriggerDesc = common.StringPtr(req.Cron)
	request.Namespace = common.StringPtr(req.Namespace)
	request.Qualifier = common.StringPtr(qualifier)
	request.Enable = common.StringPtr(timerEnable(req.Enabled))
	request.CustomArgument = common.StringPtr(message)
	if err := c.call(ctx, req.Region, "CreateTrigger", func(ctx context.Context) error {
		_, callErr := client.CreateTriggerWithContext(ctx, request)
		return callErr
	}); err != nil {
		return nil, err
	}
	created, err := c.waitForTimerTrigger(ctx, req.FunctionRef, req, qualifier, message)
	if err != nil {
		return nil, err
	}
	if created == nil {
		return nil, fmt.Errorf("timer trigger %q was not returned after creation", req.Name)
	}
	info := timerTriggerInfoFromSDK(created)
	if err := validateTimerTriggerInfo(info, req, qualifier, message); err != nil {
		return nil, err
	}
	return info, nil
}

// validateTimerTriggerInfo is deliberately strict. A successful Tencent
// update request is not enough: CloudNode persists the assignment only after
// the provider readback matches the requested schedule and is Available.
func validateTimerTriggerInfo(info *TimerTriggerInfo, req TimerTriggerRequest, qualifier, message string) error {
	if info == nil {
		return fmt.Errorf("timer trigger %q readback is empty", req.Name)
	}
	if info.Name != req.Name || !strings.EqualFold(strings.TrimSpace(info.Type), "timer") || info.Cron != req.Cron || info.Enabled != req.Enabled || info.Qualifier != qualifier || info.Message != message {
		return fmt.Errorf("timer trigger %q readback does not match requested state", req.Name)
	}
	if !strings.EqualFold(strings.TrimSpace(info.AvailableStatus), "available") {
		return fmt.Errorf("timer trigger %q is not available: status=%q", req.Name, info.AvailableStatus)
	}
	return nil
}

// waitForTimerTrigger is kept small and bounded. Tencent may expose the
// trigger list one short poll behind Create/Update; callers can use it before
// strict validation without turning a control-plane retry into a long job.
// timerConfirmBudget 是更新或创建定时器后等待云端读回一致的总时限；
// timerConfirmBackoff 是各次轮询之间的间隔，用尽后不再拉长。
// 轮询走 ListTriggers（官方限频 20 次/秒），固定 200ms 轮询时每个函数最多约 25 次调用。
var (
	timerConfirmBudget  = 5 * time.Second
	timerConfirmBackoff = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
)

// waitForTimerTrigger 等待云端定时器读回与请求一致，返回最后一次读到的触发器。
// 总时限由局部 ctx 约束，并传给每次查询：取令牌、限频重试和 SDK 请求都不会越过预算。
func (c *Client) waitForTimerTrigger(ctx context.Context, ref FunctionRef, req TimerTriggerRequest, qualifier, message string) (*scf.TriggerInfo, error) {
	return pollTimerTrigger(ctx, func(ctx context.Context) (*scf.TriggerInfo, error) {
		return c.findTimerTrigger(ctx, ref, req.Name)
	}, req, qualifier, message)
}

func pollTimerTrigger(ctx context.Context, find func(context.Context) (*scf.TriggerInfo, error), req TimerTriggerRequest, qualifier, message string) (*scf.TriggerInfo, error) {
	localDeadline := limiterNow().Add(timerConfirmBudget)
	// 父 ctx 的截止时间更早时，超时属于调用方预算不足，不能当成本地预算耗尽而吞掉。
	parentDeadline, hasParentDeadline := ctx.Deadline()
	parentTighter := hasParentDeadline && parentDeadline.Before(localDeadline)
	pollCtx, cancel := context.WithDeadline(ctx, localDeadline)
	defer cancel()
	var last *scf.TriggerInfo
	for attempt := 0; ; attempt++ {
		item, err := find(pollCtx)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return nil, ctx.Err()
			case !parentTighter && (pollCtx.Err() != nil || errors.Is(err, ErrRateLimitWaitExceedsDeadline)):
				// 本地预算耗尽：返回最后一次读到的结果，由调用方校验。
				return last, nil
			default:
				return nil, err
			}
		}
		if item != nil {
			last = item
			if validateTimerTriggerInfo(timerTriggerInfoFromSDK(item), req, qualifier, message) == nil {
				return item, nil
			}
		}
		delay := timerConfirmBackoff[min(attempt, len(timerConfirmBackoff)-1)]
		if !limiterNow().Add(delay).Before(localDeadline) {
			return last, nil
		}
		if err := limiterSleep(pollCtx, delay); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return last, nil
		}
	}
}

func (c *Client) findTimerTrigger(ctx context.Context, ref FunctionRef, name string) (*scf.TriggerInfo, error) {
	client, err := c.newClient(ref.Region)
	if err != nil {
		return nil, err
	}
	for offset := uint64(0); ; offset += 100 {
		request := scf.NewListTriggersRequest()
		request.FunctionName = common.StringPtr(ref.FunctionName)
		request.Namespace = common.StringPtr(ref.Namespace)
		request.Offset = common.Uint64Ptr(offset)
		request.Limit = common.Uint64Ptr(100)
		var response *scf.ListTriggersResponse
		if err := c.call(ctx, ref.Region, "ListTriggers", func(ctx context.Context) (callErr error) {
			response, callErr = client.ListTriggersWithContext(ctx, request)
			return callErr
		}); err != nil {
			return nil, err
		}
		if response.Response == nil {
			return nil, nil
		}
		for _, item := range response.Response.Triggers {
			if item != nil && deref(item.TriggerName) == name {
				return item, nil
			}
		}
		total := derefUint64(response.Response.TotalCount)
		if len(response.Response.Triggers) < 100 || total > 0 && offset+uint64(len(response.Response.Triggers)) >= total {
			return nil, nil
		}
	}
}

func timerTriggerInfoFromSDK(item *scf.TriggerInfo) *TimerTriggerInfo {
	if item == nil {
		return nil
	}
	return &TimerTriggerInfo{
		Name:            deref(item.TriggerName),
		Type:            deref(item.Type),
		Cron:            normalizeTimerCron(item.TriggerDesc),
		Enabled:         item.Enable != nil && *item.Enable != 0,
		AvailableStatus: deref(item.AvailableStatus),
		Qualifier:       deref(item.Qualifier),
		Message:         deref(item.CustomArgument),
	}
}

func normalizeTimerCron(raw *string) string {
	value := strings.TrimSpace(deref(raw))
	if value == "" {
		return ""
	}
	var object struct {
		Cron string `json:"cron"`
	}
	if json.Unmarshal([]byte(value), &object) == nil && strings.TrimSpace(object.Cron) != "" {
		return strings.TrimSpace(object.Cron)
	}
	return value
}

func timerEnable(enabled bool) string {
	if enabled {
		return "OPEN"
	}
	return "CLOSE"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func derefUint64(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

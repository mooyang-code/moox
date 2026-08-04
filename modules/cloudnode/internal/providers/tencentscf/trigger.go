package tencentscf

import (
	"context"
	"encoding/json"
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
	if _, err := client.UpdateTriggerWithContext(ctx, update); err != nil {
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
	_, err = client.DeleteTriggerWithContext(ctx, request)
	return err
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
	if _, err := client.CreateTriggerWithContext(ctx, request); err != nil {
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
func (c *Client) waitForTimerTrigger(ctx context.Context, ref FunctionRef, req TimerTriggerRequest, qualifier, message string) (*scf.TriggerInfo, error) {
	deadline := time.Now().Add(5 * time.Second)
	var last *scf.TriggerInfo
	for {
		item, err := c.findTimerTrigger(ctx, ref, req.Name)
		if err != nil || item == nil {
			if err != nil {
				return nil, err
			}
		} else {
			last = item
			if validateTimerTriggerInfo(timerTriggerInfoFromSDK(item), req, qualifier, message) == nil {
				return item, nil
			}
		}
		if time.Now().After(deadline) {
			return last, nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
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
		response, err := client.ListTriggersWithContext(ctx, request)
		if err != nil {
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

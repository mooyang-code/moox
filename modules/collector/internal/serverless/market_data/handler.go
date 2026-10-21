package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	timerTriggerName    = "moox-market-fetch-timer"
	timerTriggerMessage = "market_fetch_timer_v1"
)

type Handler struct {
	NewMarketFetch func() *marketfetch.Handler
}

func NewHandler() *Handler {
	return &Handler{NewMarketFetch: marketwiring.NewHandler}
}

func RegisterCloudFunction() {
	cloudfunction.Start(NewHandler().HandleRequest)
}

func (h *Handler) HandleRequest(ctx context.Context, raw json.RawMessage) (response interface{}, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.ErrorContextf(ctx, "market_data_scf_panic error=%v stack=%s", recovered, debug.Stack())
			response = failure("internal_error", "SCF handler panic recovered")
			err = nil
		}
	}()
	spaceID := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")))
	if spaceID == "" {
		return failure("invalid_space", "MOOX_SPACE_ID is required"), nil
	}
	functionName := strings.TrimSpace(os.Getenv("MOOX_SCF_FUNCTION_NAME"))
	requestID := ""
	if function, _ := functioncontext.FromContext(ctx); function != nil {
		requestID = function.RequestID
		if strings.TrimSpace(function.FunctionName) != "" {
			functionName = function.FunctionName
		}
	}
	fetch := h.NewMarketFetch
	if fetch == nil {
		fetch = marketwiring.NewHandler
	}
	runtimeHandler := fetch()
	if runtimeHandler == nil {
		return failure("internal_error", "market fetch handler is not configured"), nil
	}
	invocationMetrics, metricsErr := marketfetch.NewInvocationMetrics(functionName, spaceID)
	if metricsErr != nil {
		log.WarnContextf(ctx, "market_data_metrics_setup_failed error=%v", metricsErr)
	} else {
		runtimeHandler.Metrics = invocationMetrics.Metrics
		runtimeHandler.MetricsReporter = invocationMetrics.Reporter
	}
	if reporter := newItemReporter(ctx, functionName); reporter != nil {
		runtimeHandler.Reporter = reporter
	}
	return Execute(ctx, raw, Env{SpaceID: spaceID, FunctionName: functionName, RequestID: requestID, Handler: runtimeHandler, AllowTimer: true})
}

// Env 是一次执行的上下文：云函数入口从函数环境与运行时填写，本地执行端由进程配置填写。
type Env struct {
	SpaceID      string
	FunctionName string
	// RequestID 是运行时提供的请求 ID，事件没有带时使用。
	RequestID string
	// Handler 是已装配好的处理器（存储、指标、上报由入口决定）。
	Handler *marketfetch.Handler
	// AllowTimer 为真时接受定时器事件（只有云函数入口：计划在函数环境变量里）。
	AllowTimer bool
}

// Execute 解码事件并按 action 分发；云函数入口与本地执行端入口都调它。
func Execute(ctx context.Context, raw json.RawMessage, env Env) (*model.Response, error) {
	if env.Handler == nil {
		return failure("internal_error", "market fetch handler is not configured"), nil
	}
	var event model.CloudFunctionEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return failure("invalid_event", fmt.Sprintf("decode event: %v", err)), nil
	}
	if event.RequestID == "" {
		event.RequestID = env.RequestID
	}
	timerEnvelope := event.Type != "" || event.TriggerName != "" || event.Message != ""
	var scheduled time.Time
	if timerEnvelope {
		if !env.AllowTimer {
			return failure("invalid_event", "timer events are only accepted by cloud functions"), nil
		}
		var err error
		if scheduled, err = validateTimerEvent(event); err != nil {
			return failure("invalid_timer_event", err.Error()), nil
		}
		if event.Action != "" && event.Action != model.EventActionMarketFetch {
			return failure("invalid_timer_event", "timer event action must be market_fetch"), nil
		}
		event.Action = model.EventActionMarketFetch
		event.Source = "tencent_timer"
	}
	handler := *env.Handler
	handler.SpaceID = env.SpaceID
	switch event.Action {
	case model.EventActionMarketFetch:
		if timerEnvelope {
			// 定时触发：按环境里的采集计划与事件的计划时间执行，周期由计划时间决定。
			return handler.HandlePlanTimer(ctx, event.RequestID, env.FunctionName, scheduled)
		}
		// 手动调用与补采：只写行，不涉及周期状态。
		return handler.HandleWithFunctionName(ctx, event, env.FunctionName)
	case model.EventActionEgressProbe:
		if env.SpaceID == "stockcn" {
			return marketfetch.StockEgressIdentityProbe(ctx)
		}
		return marketwiring.EgressProbe(ctx, "binance", "spot")
	default:
		return failure("unknown_event_type", "unsupported market_data SCF action"), nil
	}
}

// validateTimerEvent 校验定时事件并返回事件携带的计划时间。缺失或非法的时间直接拒绝，
// 不回退到当前时间：延迟投递或重试的旧事件不能冒充当前周期。
func validateTimerEvent(event model.CloudFunctionEvent) (time.Time, error) {
	if !strings.EqualFold(strings.TrimSpace(event.Type), "Timer") {
		return time.Time{}, fmt.Errorf("timer event type must be Timer")
	}
	if strings.TrimSpace(event.TriggerName) != timerTriggerName {
		return time.Time{}, fmt.Errorf("timer trigger name is not recognized")
	}
	if strings.TrimSpace(event.Message) != timerTriggerMessage {
		return time.Time{}, fmt.Errorf("timer trigger message is not recognized")
	}
	if strings.TrimSpace(event.Time) == "" {
		return time.Time{}, fmt.Errorf("timer event time is required")
	}
	scheduled, err := time.Parse(time.RFC3339, strings.TrimSpace(event.Time))
	if err != nil {
		return time.Time{}, fmt.Errorf("timer event time is invalid: %w", err)
	}
	return scheduled.UTC(), nil
}

func failure(code, message string) *model.Response {
	return &model.Response{Success: false, Message: code + ": " + message, Timestamp: time.Now().UTC()}
}

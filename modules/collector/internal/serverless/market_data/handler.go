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
	return &Handler{NewMarketFetch: marketfetch.NewHandler}
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
	var event model.CloudFunctionEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return failure("invalid_event", fmt.Sprintf("decode event: %v", err)), nil
	}
	timerMode := strings.ToLower(strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MODE")))
	timerConfigured := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SUBJECTS")) != "" || timerMode != ""
	timerEnvelope := event.Type != "" || event.TriggerName != "" || event.Message != ""
	if timerEnvelope {
		if err := validateTimerEvent(event); err != nil {
			return failure("invalid_timer_event", err.Error()), nil
		}
		if timerConfigured {
			expectedAction := timerAction(timerMode)
			if event.Action == "" {
				event.Action = expectedAction
			} else if event.Action != expectedAction {
				return failure("invalid_timer_mode", "timer action does not match MOOX_MARKET_FETCH_MODE"), nil
			}
		} else if event.Action == "" {
			event.Action = timerAction(timerMode)
		}
		event.Source = "tencent_timer"
		event.StorageRPCGatewayTarget = os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET")
	}
	functionName := strings.TrimSpace(os.Getenv("MOOX_SCF_FUNCTION_NAME"))
	if function, _ := functioncontext.FromContext(ctx); function != nil {
		if event.RequestID == "" {
			event.RequestID = function.RequestID
		}
		if strings.TrimSpace(function.FunctionName) != "" {
			functionName = function.FunctionName
		}
	}
	if event.StorageRPCGatewayTarget == "" {
		event.StorageRPCGatewayTarget = os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET")
	}
	fetch := h.NewMarketFetch
	if fetch == nil {
		fetch = marketfetch.NewHandler
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
	switch event.Action {
	case model.EventActionMarketFetch, model.EventActionInstrumentSnapshot:
		if timerConfigured && isTimerAction(event.Action) {
			if !timerEnvelope && len(event.Data) == 0 {
				return failure("invalid_timer_event", "timer event identity or action data is required"), nil
			}
			if timerEnvelope {
				return runtimeHandler.HandleTimerAt(ctx, event.RequestID, functionName, timerNow(event))
			}
			return runtimeHandler.HandleWithFunctionNameWithoutCompletion(ctx, event, functionName)
		}
		return runtimeHandler.HandleWithFunctionName(ctx, event, functionName)
	case model.EventActionEgressProbe:
		if spaceID == "stock_cn" {
			return marketfetch.StockEgressIdentityProbe(ctx)
		}
		return marketfetch.EgressProbe(ctx, "binance", "spot")
	default:
		return failure("unknown_event_type", "unsupported market_data SCF action"), nil
	}
}

func timerNow(event model.CloudFunctionEvent) time.Time {
	now := time.Now().UTC()
	if parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(event.Time)); parseErr == nil {
		return parsed.UTC()
	}
	return now
}

func timerAction(mode string) model.EventAction {
	if strings.EqualFold(strings.TrimSpace(mode), "instrument_snapshot") {
		return model.EventActionInstrumentSnapshot
	}
	return model.EventActionMarketFetch
}

func isTimerAction(action model.EventAction) bool {
	return action == model.EventActionMarketFetch || action == model.EventActionInstrumentSnapshot
}

func validateTimerEvent(event model.CloudFunctionEvent) error {
	if !strings.EqualFold(strings.TrimSpace(event.Type), "Timer") {
		return fmt.Errorf("timer event type must be Timer")
	}
	if strings.TrimSpace(event.TriggerName) != timerTriggerName {
		return fmt.Errorf("timer trigger name is not recognized")
	}
	if strings.TrimSpace(event.Message) != timerTriggerMessage {
		return fmt.Errorf("timer trigger message is not recognized")
	}
	if strings.TrimSpace(event.Time) == "" {
		return fmt.Errorf("timer event time is required")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(event.Time)); err != nil {
		return fmt.Errorf("timer event time is invalid: %w", err)
	}
	return nil
}

func failure(code, message string) *model.Response {
	return &model.Response{Success: false, Message: code + ": " + message, Timestamp: time.Now().UTC()}
}

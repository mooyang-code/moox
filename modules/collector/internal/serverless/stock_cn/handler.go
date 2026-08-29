package stockcn

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
	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	spaceID             = "stock_cn"
	timerTriggerName    = "moox-market-fetch-timer"
	timerTriggerMessage = "market_fetch_timer_v1"
)

type Handler struct {
	NewMarketFetch       func() *marketfetch.Handler
	NewInstrumentStorage func(string, string) (marketfetch.InstrumentStorage, error)
	NewReporter          func() (clsreporter.Reporter, time.Duration, error)
}

func NewHandler() *Handler {
	return &Handler{
		NewMarketFetch:       marketfetch.NewHandler,
		NewInstrumentStorage: marketfetch.NewStockInstrumentStorage,
		NewReporter: func() (clsreporter.Reporter, time.Duration, error) {
			cfg, enabled, err := clsreporter.ConfigFromEnv(os.Getenv)
			if err != nil || !enabled {
				return clsreporter.Noop(), 0, err
			}
			reporter, err := clsreporter.New(cfg)
			return reporter, cfg.Timeout, err
		},
	}
}

func RegisterCloudFunction() { cloudfunction.Start(NewHandler().HandleRequest) }

func (h *Handler) HandleRequest(ctx context.Context, raw json.RawMessage) (response interface{}, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.ErrorContextf(ctx, "stock_cn_scf_panic error=%v stack=%s", recovered, debug.Stack())
			response = failure("internal_error", "SCF handler panic recovered")
			err = nil
		}
	}()
	if strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")) != spaceID {
		return failure("invalid_space", "MOOX_SPACE_ID must be stock_cn"), nil
	}
	var event model.CloudFunctionEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return failure("invalid_event", fmt.Sprintf("decode event: %v", err)), nil
	}
	timerConfigured := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SUBJECTS")) != ""
	if timerConfigured && (event.Action == "" || event.Action == model.EventActionMarketFetch) {
		if err := validateTimerEvent(event); err != nil {
			return failure("invalid_timer_event", err.Error()), nil
		}
	}
	function, _ := functioncontext.FromContext(ctx)
	functionName := strings.TrimSpace(os.Getenv("MOOX_SCF_FUNCTION_NAME"))
	if function != nil {
		if event.RequestID == "" {
			event.RequestID = function.RequestID
		}
		if strings.TrimSpace(function.FunctionName) != "" {
			functionName = function.FunctionName
		}
	}
	reporter, timeout, reportErr := h.NewReporter()
	if reportErr != nil {
		reporter, timeout = clsreporter.Noop(), 0
	}
	defer func() {
		if timeout <= 0 || ctx.Err() != nil {
			return
		}
		flushCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		_ = reporter.Flush(flushCtx)
	}()
	if timerConfigured && event.Action == "" {
		event.Action = model.EventActionMarketFetch
		event.Source = "tencent_timer"
		event.StorageRPCGatewayTarget = os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET")
	}
	switch event.Action {
	case model.EventActionMarketFetch:
		handler := h.NewMarketFetch()
		handler.Reporter = reporter
		handler.CLSReserve = timeout
		if timerConfigured {
			timerNow := time.Now().UTC()
			if parsed, parseErr := time.Parse(time.RFC3339, event.Time); parseErr == nil {
				timerNow = parsed.UTC()
			}
			return handler.HandleTimerAt(ctx, event.RequestID, functionName, timerNow)
		}
		return handler.HandleWithFunctionName(ctx, event, functionName)
	case model.EventActionEgressProbe:
		return marketfetch.StockEgressProbe(ctx)
	case model.EventActionInstrumentSnapshot:
		storageTarget := strings.TrimSpace(event.StorageRPCGatewayTarget)
		if storageTarget == "" {
			return failure("invalid_instrument_snapshot", "storage_rpc_gateway_target is required"), nil
		}
		storage, storageErr := h.NewInstrumentStorage(storageTarget, "stock_cn_instrument_snapshot")
		if storageErr != nil {
			return nil, storageErr
		}
		pipeline, pipelineErr := marketfetch.NewStockInstrumentPipeline(storage)
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		result, pipelineErr := pipeline.Execute(ctx, marketfetch.InstrumentPipelineRequest{RequestID: event.RequestID, SnapshotAt: time.Now().UTC()})
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		return &model.Response{Success: true, Message: "succeeded", Data: result, RequestID: event.RequestID, Timestamp: time.Now().UTC()}, nil
	default:
		return failure("unknown_event_type", "unsupported stock_cn SCF action"), nil
	}
}

func validateTimerEvent(event model.CloudFunctionEvent) error {
	if !strings.EqualFold(strings.TrimSpace(event.Type), "Timer") || strings.TrimSpace(event.TriggerName) != timerTriggerName || strings.TrimSpace(event.Message) != timerTriggerMessage {
		return fmt.Errorf("timer event identity is not recognized")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(event.Time)); err != nil {
		return fmt.Errorf("timer event time is invalid: %w", err)
	}
	return nil
}

func failure(code, message string) *model.Response {
	return &model.Response{Success: false, Message: code + ": " + message, Timestamp: time.Now().UTC()}
}

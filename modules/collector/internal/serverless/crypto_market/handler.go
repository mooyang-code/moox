// Package cryptomarket contains the short-lived crypto market SCF runtime.
package cryptomarket

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

const spaceID = "crypto_market"

// Handler accepts only bounded crypto market actions and has no resident work.
type Handler struct {
	NewMarketFetch func() *marketfetch.Handler
	NewReporter    func() (clsreporter.Reporter, time.Duration, error)
}

func NewHandler() *Handler {
	return &Handler{
		NewMarketFetch: marketfetch.NewHandler,
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
			log.ErrorContextf(ctx, "crypto_market_scf_panic error=%v stack=%s", recovered, debug.Stack())
			response = failure("internal_error", "SCF handler panic recovered")
			err = nil
		}
	}()
	if strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")) != spaceID {
		return failure("invalid_space", "MOOX_SPACE_ID must be crypto_market"), nil
	}
	var event model.CloudFunctionEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return failure("invalid_event", fmt.Sprintf("decode event: %v", err)), nil
	}
	function, _ := functioncontext.FromContext(ctx)
	if event.RequestID == "" && function != nil {
		event.RequestID = function.RequestID
	}
	reporter, timeout, reportErr := h.NewReporter()
	if reportErr != nil {
		log.ErrorContextf(ctx, "cls_reporter_init_failed error=%q", reportErr)
		reporter = clsreporter.Noop()
		timeout = 0
	}
	functionName := strings.TrimSpace(os.Getenv("MOOX_SCF_FUNCTION_NAME"))
	region := ""
	if function != nil {
		functionName = firstNonEmpty(function.FunctionName, functionName)
		region = function.TencentcloudRegion
	}
	reporter = staticFieldsReporter{Reporter: reporter, Fields: map[string]string{
		"function_name": functionName,
		"region":        region,
	}}
	defer func() {
		if timeout <= 0 {
			return
		}
		flushCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if flushErr := reporter.Flush(flushCtx); flushErr != nil {
			log.ErrorContextf(ctx, "cls_report_flush_failed request_id=%s error=%q", event.RequestID, flushErr)
		}
	}()
	switch event.Action {
	case model.EventActionMarketFetch:
		handler := h.NewMarketFetch()
		handler.Reporter = reporter
		handler.CLSReserve = timeout
		return handler.Handle(ctx, event)
	case model.EventActionEgressProbe:
		return marketfetch.EgressProbe(ctx, "binance", "spot")
	default:
		return failure("unknown_event_type", "unsupported crypto market SCF action"), nil
	}
}

func failure(code, message string) *model.Response {
	return &model.Response{Success: false, Message: code + ": " + message, Timestamp: time.Now().UTC()}
}

type staticFieldsReporter struct {
	clsreporter.Reporter
	Fields map[string]string
}

func (r staticFieldsReporter) Report(entry clsreporter.Entry) {
	fields := make(map[string]string, len(r.Fields)+len(entry.Fields))
	for key, value := range r.Fields {
		if value != "" {
			fields[key] = value
		}
	}
	for key, value := range entry.Fields {
		if _, fixed := r.Fields[key]; !fixed && (value != "" || fields[key] == "") {
			fields[key] = value
		}
	}
	entry.Fields = fields
	r.Reporter.Report(entry)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

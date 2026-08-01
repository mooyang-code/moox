package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// Handler is the short-lived SCF action handler. Dependencies are built per
// invocation so there is no resident worker, timer, or job lease in SCF.
type Handler struct {
	NewStorage func(string, string) (Storage, error)
	Publish    func(context.Context, Request, proto.Message) error
	Now        func() time.Time
}

func NewHandler() *Handler {
	return &Handler{NewStorage: func(target, market string) (Storage, error) { return binance.NewBatchStorage(target, market) }, Publish: publishCompletion}
}

func (h *Handler) Handle(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	if h == nil {
		return nil, fmt.Errorf("market fetch handler is nil")
	}
	var req Request
	if err := decodeRequest(event.Data, &req); err != nil {
		return &model.Response{Success: false, Message: err.Error(), RequestID: event.RequestID, Timestamp: time.Now().UTC()}, nil
	}
	if req.RequestID == "" {
		req.RequestID = event.RequestID
	}
	if expectedSpaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")); expectedSpaceID == "" {
		return &model.Response{Success: false, Message: "MOOX_SPACE_ID is required", RequestID: req.RequestID, Timestamp: time.Now().UTC()}, nil
	} else if req.SpaceID != expectedSpaceID {
		return &model.Response{Success: false, Message: fmt.Sprintf("market_fetch space_id %q does not match function space %q", req.SpaceID, expectedSpaceID), RequestID: req.RequestID, Timestamp: time.Now().UTC()}, nil
	}
	if req.Concurrency == 0 {
		req.Concurrency = envInt("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", envInt("MOOX_MARKET_FETCH_MAX_INFLIGHT", DefaultConcurrency))
	}
	budgetCtx, cancel := executionContext(ctx)
	defer cancel()
	storageTarget := runtimeapp.GetStorageRPCGatewayTarget()
	storage, err := h.NewStorage(storageTarget, req.MarketType)
	if err != nil {
		return nil, err
	}
	reserve := time.Duration(envInt("MOOX_FETCH_COMMIT_RESERVE_MS", envInt("MOOX_MARKET_FETCH_COMMIT_RESERVE_MS", 2000))) * time.Millisecond
	storageReserve := reserve * 3 / 4
	if storageReserve <= 0 {
		storageReserve = reserve
	}
	executor := &Executor{Klines: binance.NewKlineCollector(), Catchup: binance.NewKlineCollector(), Symbols: binance.NewSymbolCollector(), Storage: storage, Now: h.Now, CommitReserve: reserve, StorageReserve: storageReserve}
	payload, err := executor.Execute(budgetCtx, req)
	if err != nil {
		return nil, err
	}
	publishReserve := reserve - storageReserve
	if publishReserve <= 0 {
		publishReserve = reserve
	}
	publishCtx, publishCancel := context.WithTimeout(budgetCtx, publishReserve)
	defer publishCancel()
	if err := h.Publish(publishCtx, req, payload); err != nil {
		// Do not turn a successful Storage write into a false success: the
		// caller retries the invocation and the event publisher is idempotent
		// on batch_id.
		return nil, fmt.Errorf("publish market fetch completion: %w", err)
	}
	return &model.Response{Success: payload.GetStatus() == "succeeded" || payload.GetStatus() == "partial_failed", Message: payload.GetStatus(), Data: payload, RequestID: req.RequestID, Timestamp: time.Now().UTC()}, nil
}

func executionContext(parent context.Context) (context.Context, context.CancelFunc) {
	seconds := envInt("MOOX_FETCH_TIMEOUT_SECONDS", envInt("MOOX_MARKET_FETCH_TIMEOUT_SECONDS", 10))
	budget := time.Duration(seconds) * time.Second
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		budget = time.Millisecond
	}
	return context.WithTimeout(parent, budget)
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func decodeRequest(data map[string]interface{}, request *Request) error {
	if len(data) == 0 {
		return fmt.Errorf("market_fetch data is required")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode market_fetch data: %w", err)
	}
	if len(raw) > 128*1024 {
		return fmt.Errorf("market_fetch data exceeds 128KB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return fmt.Errorf("decode market_fetch data: %w", err)
	}
	return request.validate()
}

func publishCompletion(ctx context.Context, req Request, payload proto.Message) error {
	if strings.TrimSpace(req.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(nil, "moox-collector-market-fetch"))
	if err != nil {
		return err
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return err
	}
	subjectID := strings.TrimSpace(req.DatasetID)
	if subjectID == "" {
		subjectID = req.BatchID
	}
	_, err = publisher.Publish(ctx, events.MarketFetchBatchCompleted, payload, events.PublishOptions{EventID: req.BatchID, OccurredAt: time.Now().UTC(), SpaceID: req.SpaceID, SubjectID: subjectID})
	if err != nil {
		log.ErrorContextf(ctx, "publish market fetch completion failed: batch_id=%s err=%v", req.BatchID, err)
	}
	return err
}

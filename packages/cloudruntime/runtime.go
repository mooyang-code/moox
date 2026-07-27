// Package cloudruntime contains the generic CloudNode SCF job execution boundary.
package cloudruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultHTTPTimeout = 8 * time.Second
	normalRetryDelay   = time.Second
	reportSuccess      = 1
	reportFailed       = 2
	errorRetryable     = 1
	errorPermanent     = 2
	jobStatusSuccess   = 3
	jobStatusFailed    = 4
)

type Config struct {
	ServiceGatewayTarget string
	SpaceID              string
	NodeID               string
	Auth                 AuthConfig
	HTTPTimeout          time.Duration
}

type JobItem struct {
	SpaceID   string
	JobID     string
	JobItemID string
	JobType   string
	Params    map[string]any
	ExecuteAt time.Time
	Consumer  string
	MessageID string
	// DeliveryCount is runtime metadata for lifecycle correlation. It is set by
	// ExecuteJobItem and is not part of the CloudNode status request.
	DeliveryCount uint64
	MaxDeliver    int
}

type Result struct{ Summary map[string]any }

type Handler interface {
	Execute(context.Context, JobItem) (Result, error)
}

type HandlerFunc func(context.Context, JobItem) (Result, error)

func (fn HandlerFunc) Execute(ctx context.Context, item JobItem) (Result, error) {
	return fn(ctx, item)
}

type ErrorKind string

const (
	ErrorKindRetryable ErrorKind = "retryable"
	ErrorKindPermanent ErrorKind = "permanent"
)

type runtimeError struct {
	kind ErrorKind
	code string
	err  error
}

func (e *runtimeError) Error() string {
	if e.err == nil {
		return e.code
	}
	return e.err.Error()
}
func (e *runtimeError) Unwrap() error { return e.err }

func Retryable(err error, code string) error {
	return &runtimeError{kind: ErrorKindRetryable, code: normalizeErrorCode(code, "RETRYABLE_ERROR"), err: err}
}
func Permanent(err error, code string) error {
	return &runtimeError{kind: ErrorKindPermanent, code: normalizeErrorCode(code, "PERMANENT_ERROR"), err: err}
}

type registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

var globalRegistry = &registry{handlers: map[string]Handler{}}

func Register(jobType string, handler Handler) { globalRegistry.register(jobType, handler) }
func (r *registry) register(jobType string, handler Handler) {
	jobType = strings.TrimSpace(jobType)
	if jobType == "" || handler == nil {
		panic("cloudruntime: job_type and handler are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[jobType]; exists {
		panic("cloudruntime: duplicate handler for job_type " + jobType)
	}
	r.handlers[jobType] = handler
}
func (r *registry) get(jobType string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[strings.TrimSpace(jobType)]
	return handler, ok
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type reportRequest struct {
	SpaceID       string         `json:"space_id"`
	NodeID        string         `json:"node_id"`
	JobItemID     string         `json:"job_item_id"`
	Status        int            `json:"status"`
	ErrorKind     int            `json:"error_kind"`
	ErrorCode     string         `json:"error_code"`
	ErrorMessage  string         `json:"error_message"`
	ResultSummary map[string]any `json:"result_summary"`
	DurationMS    int64          `json:"duration_ms"`
}

func (cfg *Config) Validate() error {
	cfg.ServiceGatewayTarget = normalizeGatewayTarget(cfg.ServiceGatewayTarget)
	cfg.SpaceID = strings.TrimSpace(cfg.SpaceID)
	cfg.NodeID = strings.TrimSpace(cfg.NodeID)
	if cfg.ServiceGatewayTarget == "" || cfg.SpaceID == "" || cfg.NodeID == "" {
		return fmt.Errorf("cloud runtime requires service_gateway_target, space_id and node_id")
	}
	if _, err := (cloudjobqueue.Identity{SpaceID: cfg.SpaceID, JobType: "validation"}).ConsumerName(); err != nil {
		return err
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	return nil
}

// ExecuteJobItem reports a terminal state before selecting ACK or TERM.
// Retryable non-final failures are intentionally not reported as terminal.
func ExecuteJobItem(ctx context.Context, cfg Config, item JobItem, deliveryCount uint64, maxDeliver int) jetstream.HandlerResult {
	item.DeliveryCount = deliveryCount
	item.MaxDeliver = maxDeliver
	if err := cfg.Validate(); err != nil {
		logCloudJob(ctx, cloudJobLogFields{
			Event: "collector_job_done", Config: cfg, Item: item, DeliveryCount: deliveryCount,
			Status: "failed", ErrorCode: "INVALID_RUNTIME_CONFIG", Err: err,
		}, true)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if deliveryCount > 1 {
		decision, terminal, err := terminalRedeliveryDecision(ctx, cfg, item)
		if err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: normalRetryDelay, Err: err}
		}
		if terminal {
			return jetstream.HandlerResult{Decision: decision}
		}
	}
	started := time.Now()
	result, execErr := invokeHandler(ctx, item)
	duration := time.Since(started)

	kind, code := classifyError(execErr)
	status := "success"
	if execErr != nil {
		status = "failed"
	}
	logCloudJob(ctx, cloudJobLogFields{
		Event: "collector_job_done", Config: cfg, Item: item, DeliveryCount: deliveryCount,
		Status: status, Duration: duration, ErrorCode: code, Err: execErr,
	}, execErr != nil)
	if execErr != nil && kind == errorRetryable && maxDeliver > 0 && deliveryCount < uint64(maxDeliver) {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: normalRetryDelay, Err: execErr}
	}

	req := reportRequest{
		SpaceID: cfg.SpaceID, NodeID: cfg.NodeID, JobItemID: item.JobItemID,
		Status: reportSuccess, ResultSummary: normalizeMap(result.Summary), DurationMS: duration.Milliseconds(),
	}
	decision := jetstream.ACK
	if execErr != nil {
		req.Status = reportFailed
		req.ErrorKind = kind
		req.ErrorCode = code
		req.ErrorMessage = execErr.Error()
		decision = jetstream.TERM
	}
	reportErr := reportJobItem(ctx, cfg, req)
	reportLogErr := execErr
	reportErrorCode := code
	if reportErr != nil {
		reportLogErr = reportErr
		reportErrorCode = "CLOUDNODE_REPORT_FAILED"
	}
	logCloudJob(ctx, cloudJobLogFields{
		Event: "collector_job_cloudnode_reported", Config: cfg, Item: item, DeliveryCount: deliveryCount,
		Status: status, Duration: duration, ErrorCode: reportErrorCode, Err: reportLogErr,
	}, reportLogErr != nil)
	if reportErr != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: normalRetryDelay, Err: reportErr}
	}
	return jetstream.HandlerResult{Decision: decision, Err: execErr}
}

func terminalRedeliveryDecision(
	ctx context.Context,
	cfg Config,
	item JobItem,
) (jetstream.HandlerDecision, bool, error) {
	request := struct {
		SpaceID   string `json:"space_id"`
		JobItemID string `json:"job_item_id"`
	}{SpaceID: cfg.SpaceID, JobItemID: item.JobItemID}
	var response struct {
		RetInfo *retInfo `json:"ret_info"`
		Item    *struct {
			Status json.RawMessage `json:"status"`
		} `json:"item"`
	}
	if err := postService(ctx, cfg, "cloudnode", "GetJobItem", request, &response); err != nil {
		return jetstream.RETRY, false, fmt.Errorf("get redelivered job item: %w", err)
	}
	if response.RetInfo == nil || response.RetInfo.Code != 0 || response.Item == nil {
		message := ""
		if response.RetInfo != nil {
			message = response.RetInfo.Msg
		}
		return jetstream.RETRY, false, fmt.Errorf("get redelivered job item rejected: %s", message)
	}
	status, err := decodeJobItemStatus(response.Item.Status)
	if err != nil {
		return jetstream.RETRY, false, err
	}
	switch status {
	case jobStatusSuccess:
		return jetstream.ACK, true, nil
	case jobStatusFailed:
		return jetstream.TERM, true, nil
	default:
		return jetstream.RETRY, false, nil
	}
}

func decodeJobItemStatus(raw json.RawMessage) (int, error) {
	var numeric int
	if err := json.Unmarshal(raw, &numeric); err == nil {
		return numeric, nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return 0, fmt.Errorf("decode job item status: %w", err)
	}
	switch name {
	case "JOB_ITEM_STATUS_SUCCESS":
		return jobStatusSuccess, nil
	case "JOB_ITEM_STATUS_FAILED":
		return jobStatusFailed, nil
	default:
		return 0, nil
	}
}

func invokeHandler(ctx context.Context, item JobItem) (result Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = Permanent(fmt.Errorf("panic: %v", recovered), "HANDLER_PANIC")
		}
	}()
	handler, ok := globalRegistry.get(item.JobType)
	if !ok {
		return Result{}, Permanent(fmt.Errorf("handler not found for job_type %s", item.JobType), "HANDLER_NOT_FOUND")
	}
	return handler.Execute(ctx, item)
}

func reportJobItem(ctx context.Context, cfg Config, request reportRequest) error {
	var response struct {
		RetInfo *retInfo `json:"ret_info"`
	}
	if err := postService(ctx, cfg, "cloudnode", "ReportJobItemStatus", request, &response); err != nil {
		return fmt.Errorf("report job item: %w", err)
	}
	if response.RetInfo == nil || response.RetInfo.Code != 0 {
		message := ""
		if response.RetInfo != nil {
			message = response.RetInfo.Msg
		}
		return fmt.Errorf("report job item rejected: %s", message)
	}
	return nil
}

func classifyError(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var runtimeErr *runtimeError
	if errors.As(err, &runtimeErr) {
		if runtimeErr.kind == ErrorKindRetryable {
			return errorRetryable, runtimeErr.code
		}
		return errorPermanent, runtimeErr.code
	}
	return errorPermanent, "HANDLER_ERROR"
}

type cloudJobLogFields struct {
	Event         string
	Config        Config
	Item          JobItem
	DeliveryCount uint64
	Status        string
	Duration      time.Duration
	ErrorCode     string
	Err           error
}

func (fields cloudJobLogFields) String() string {
	return fmt.Sprintf(
		"event=%s space_id=%s job_id=%s job_item_id=%s task_id=%s job_type=%s "+
			"runtime_code_package_id=%s node_id=%s consumer=%s message_id=%s "+
			"delivery_count=%d execute_at=%s "+
			"dataset_id=%s subject_id=%s symbol=%s interval=%s status=%s "+
			"duration_ms=%d error_code=%s error=%s",
		quotedLogValue(fields.Event),
		quotedLogValue(fields.Item.SpaceID),
		quotedLogValue(fields.Item.JobID),
		quotedLogValue(fields.Item.JobItemID),
		quotedLogValue(paramString(fields.Item.Params, "task_id")),
		quotedLogValue(fields.Item.JobType),
		quotedLogValue(os.Getenv("MOOX_CODE_PACKAGE_ID")),
		quotedLogValue(fields.Config.NodeID),
		quotedLogValue(fields.Item.Consumer),
		quotedLogValue(fields.Item.MessageID),
		fields.DeliveryCount,
		quotedLogValue(formatLogTime(fields.Item.ExecuteAt)),
		quotedLogValue(paramString(fields.Item.Params, "dataset_id")),
		quotedLogValue(paramString(fields.Item.Params, "subject_id")),
		quotedLogValue(paramString(fields.Item.Params, "symbol")),
		quotedLogValue(paramString(fields.Item.Params, "interval")),
		quotedLogValue(fields.Status),
		fields.Duration.Milliseconds(),
		quotedLogValue(fields.ErrorCode),
		quotedLogValue(errorString(fields.Err)),
	)
}

func logCloudJob(ctx context.Context, fields cloudJobLogFields, failed bool) {
	if failed {
		log.ErrorContextf(ctx, "%s", fields.String())
		return
	}
	log.InfoContextf(ctx, "%s", fields.String())
}

func quotedLogValue(value string) string {
	return strconv.Quote(strings.TrimSpace(value))
}

func formatLogTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func paramString(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return value
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Join(strings.Fields(err.Error()), " ")
	const maxErrorBytes = 256
	if len(value) > maxErrorBytes {
		value = value[:maxErrorBytes]
	}
	return value
}

func normalizeMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func normalizeErrorCode(value, fallback string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func postService(ctx context.Context, cfg Config, module, method string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	auth := normalizeAuthConfig(cfg.Auth)
	url := fmt.Sprintf("%s/api/service/%s/%s", cfg.ServiceGatewayTarget, module, method)
	req, err := newSignedRequestWithNormalizedAuth(ctx, http.MethodPost, url, raw, auth)
	if err != nil {
		return err
	}
	client, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: cfg.HTTPTimeout, CAFile: auth.CAFile, CAPEMBase64: auth.CAPEMBase64})
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	rawResponse, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("service request failed with status %d", response.StatusCode)
	}
	if err := json.Unmarshal(rawResponse, out); err != nil {
		return fmt.Errorf("decode service response: %w", err)
	}
	return nil
}

func normalizeGatewayTarget(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

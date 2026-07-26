// Package cloudruntime contains the generic CloudNode SCF job execution boundary.
package cloudruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	reportSuccess      = 1
	reportFailed       = 2
	errorRetryable     = 1
	errorPermanent     = 2
)

type Config struct {
	ServiceGatewayTarget string
	SpaceID              string
	NodeID               string
	CodePackageID        string
	Auth                 AuthConfig
	HTTPTimeout          time.Duration
}

type JobItem struct {
	SpaceID       string
	JobID         string
	JobItemID     string
	JobType       string
	CodePackageID string
	Params        map[string]any
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
	cfg.CodePackageID = strings.TrimSpace(cfg.CodePackageID)
	if cfg.ServiceGatewayTarget == "" || cfg.SpaceID == "" || cfg.NodeID == "" || cfg.CodePackageID == "" {
		return fmt.Errorf("cloud runtime requires service_gateway_target, space_id, node_id and code_package_id")
	}
	if _, err := (cloudjobqueue.Identity{SpaceID: cfg.SpaceID, CodePackageID: cfg.CodePackageID, JobType: "validation"}).ConsumerName(); err != nil {
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
	if err := cfg.Validate(); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	started := time.Now()
	result, execErr := invokeHandler(ctx, item)
	duration := time.Since(started)
	logCompletion(ctx, cfg, item, deliveryCount, duration, execErr)

	kind, code := classifyError(execErr)
	if execErr != nil && kind == errorRetryable && maxDeliver > 0 && deliveryCount < uint64(maxDeliver) {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: execErr}
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
	if err := reportJobItem(ctx, cfg, req); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: decision, Err: execErr}
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

func logCompletion(ctx context.Context, cfg Config, item JobItem, deliveryCount uint64, duration time.Duration, execErr error) {
	status := "success"
	if execErr != nil {
		status = "failed"
	}
	log.InfoContextf(ctx, "collector_job_done space_id=%s job_item_id=%s node_id=%s job_type=%s delivery_count=%d status=%s duration_ms=%d error=%q",
		item.SpaceID, item.JobItemID, cfg.NodeID, item.JobType, deliveryCount, status, duration.Milliseconds(), errorString(execErr))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
		return fmt.Errorf("service request failed with status %d: %s", response.StatusCode, string(rawResponse))
	}
	if err := json.Unmarshal(rawResponse, out); err != nil {
		return fmt.Errorf("decode service response: %w", err)
	}
	return nil
}

func normalizeGatewayTarget(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

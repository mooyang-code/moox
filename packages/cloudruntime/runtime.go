// Package cloudruntime contains the generic CloudNode SCF runtime loop shared by workload modules.
package cloudruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/servicegateway"

	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultHTTPTimeout     = 8 * time.Second
	defaultJobItemLimit    = 8
	defaultProtocolVersion = "cloudnode-jobitem-v1"

	jobItemReportStatusSuccess = 1
	jobItemReportStatusFailed  = 2

	jobItemErrorKindRetryable = 1
	jobItemErrorKindPermanent = 2
)

// Config describes the MooX control plane and node capabilities for an SCF runtime.
type Config struct {
	ServiceGatewayTarget string
	ServerIP             string
	ServerPort           int
	SpaceID              string
	NodeID               string
	SupportedJobTypes    []string
	Limit                int
	RuntimeVersion       string
	ProtocolVersion      string
	Auth                 AuthConfig
	HTTPTimeout          time.Duration
}

// JobItem is a CloudNode async execution unit.
type JobItem struct {
	SpaceID       string
	JobID         string
	JobItemID     string
	JobType       string
	CodePackageID string
	Params        map[string]any
	AttemptNo     int
}

// Result is the execution summary reported back to CloudNode.
type Result struct {
	Summary map[string]any
}

// Handler executes one CloudNode JobItem.
type Handler interface {
	Execute(context.Context, JobItem) (Result, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(context.Context, JobItem) (Result, error)

func (fn HandlerFunc) Execute(ctx context.Context, item JobItem) (Result, error) {
	return fn(ctx, item)
}

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

func (e *runtimeError) Unwrap() error {
	return e.err
}

// ErrorKind classifies whether a failed JobItem should be retried by CloudNode.
type ErrorKind string

const (
	ErrorKindRetryable ErrorKind = "retryable"
	ErrorKindPermanent ErrorKind = "permanent"
)

// Retryable marks an execution error as retryable.
func Retryable(err error, code string) error {
	return &runtimeError{kind: ErrorKindRetryable, code: normalizeErrorCode(code, "RETRYABLE_ERROR"), err: err}
}

// Permanent marks an execution error as permanent.
func Permanent(err error, code string) error {
	return &runtimeError{kind: ErrorKindPermanent, code: normalizeErrorCode(code, "PERMANENT_ERROR"), err: err}
}

type registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

var globalRegistry = &registry{handlers: map[string]Handler{}}

var logCompletion = func(ctx context.Context, line string) {
	log.InfoContextf(ctx, "%s", line)
}

// Register adds a JobItem handler for jobType.
func Register(jobType string, handler Handler) {
	globalRegistry.register(jobType, handler)
}

func (r *registry) register(jobType string, handler Handler) {
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		panic("cloudruntime: job_type is required")
	}
	if handler == nil {
		panic("cloudruntime: handler is required")
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

func resetRegistryForTest() {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.handlers = map[string]Handler{}
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type pollJobItemsRequest struct {
	SpaceID           string   `json:"space_id"`
	NodeID            string   `json:"node_id"`
	SupportedJobTypes []string `json:"supported_job_types"`
	Limit             int      `json:"limit"`
	RuntimeVersion    string   `json:"runtime_version"`
	ProtocolVersion   string   `json:"protocol_version"`
}

type polledJobItem struct {
	SpaceID       string         `json:"space_id"`
	JobID         string         `json:"job_id"`
	JobItemID     string         `json:"job_item_id"`
	JobType       string         `json:"job_type"`
	CodePackageID string         `json:"code_package_id"`
	Params        map[string]any `json:"params"`
	AttemptNo     int            `json:"attempt_no"`
}

type pollJobItemsResponse struct {
	RetInfo *retInfo        `json:"ret_info"`
	Items   []polledJobItem `json:"items"`
}

type reportJobItemStatusRequest struct {
	SpaceID       string         `json:"space_id"`
	NodeID        string         `json:"node_id"`
	JobItemID     string         `json:"job_item_id"`
	AttemptNo     int            `json:"attempt_no"`
	Status        int            `json:"status"`
	ErrorKind     int            `json:"error_kind"`
	ErrorCode     string         `json:"error_code"`
	ErrorMessage  string         `json:"error_message"`
	ResultSummary map[string]any `json:"result_summary"`
	DurationMS    int64          `json:"duration_ms"`
}

// Run polls CloudNode JobItems, dispatches them through registered handlers, and reports final status.
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	items, err := pollJobItems(ctx, cfg)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		log.DebugContextf(ctx, "[CloudRuntime] no cloud job items")
		return nil
	}
	for _, item := range items {
		executeJobItem(ctx, cfg, item)
	}
	return nil
}

func (cfg *Config) validate() error {
	cfg.ServiceGatewayTarget = normalizeGatewayTarget(cfg.ServiceGatewayTarget)
	cfg.ServerIP = strings.TrimSpace(cfg.ServerIP)
	cfg.SpaceID = strings.TrimSpace(cfg.SpaceID)
	cfg.NodeID = strings.TrimSpace(cfg.NodeID)
	if cfg.ServiceGatewayTarget == "" && cfg.ServerIP != "" && cfg.ServerPort > 0 {
		cfg.ServiceGatewayTarget = fmt.Sprintf("http://%s:%d", cfg.ServerIP, cfg.ServerPort)
	}
	if cfg.ServiceGatewayTarget == "" || cfg.SpaceID == "" || cfg.NodeID == "" {
		return fmt.Errorf("cloud runtime requires service_gateway_target, space_id and node_id")
	}
	cfg.SupportedJobTypes = compactStrings(cfg.SupportedJobTypes)
	if len(cfg.SupportedJobTypes) == 0 {
		return fmt.Errorf("cloud runtime supported_job_types is required")
	}
	if cfg.Limit <= 0 {
		cfg.Limit = defaultJobItemLimit
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	if strings.TrimSpace(cfg.ProtocolVersion) == "" {
		cfg.ProtocolVersion = defaultProtocolVersion
	}
	return nil
}

func pollJobItems(ctx context.Context, cfg Config) ([]JobItem, error) {
	reqBody := pollJobItemsRequest{
		SpaceID:           cfg.SpaceID,
		NodeID:            cfg.NodeID,
		SupportedJobTypes: cfg.SupportedJobTypes,
		Limit:             cfg.Limit,
		RuntimeVersion:    strings.TrimSpace(cfg.RuntimeVersion),
		ProtocolVersion:   strings.TrimSpace(cfg.ProtocolVersion),
	}
	var rsp pollJobItemsResponse
	if err := postService(ctx, cfg, "cloudnode", "PollJobItems", reqBody, &rsp); err != nil {
		return nil, err
	}
	if rsp.RetInfo == nil || rsp.RetInfo.Code != 0 {
		msg := ""
		if rsp.RetInfo != nil {
			msg = rsp.RetInfo.Msg
		}
		return nil, fmt.Errorf("poll job items failed: %s", msg)
	}
	out := make([]JobItem, 0, len(rsp.Items))
	for _, item := range rsp.Items {
		out = append(out, JobItem{
			SpaceID:       firstNonEmpty(item.SpaceID, cfg.SpaceID),
			JobID:         item.JobID,
			JobItemID:     item.JobItemID,
			JobType:       item.JobType,
			CodePackageID: item.CodePackageID,
			Params:        normalizeMap(item.Params),
			AttemptNo:     item.AttemptNo,
		})
	}
	return out, nil
}

func executeJobItem(ctx context.Context, cfg Config, item JobItem) {
	start := time.Now()
	var result Result
	var execErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			execErr = Permanent(fmt.Errorf("panic: %v", recovered), "HANDLER_PANIC")
		}
		duration := time.Since(start)
		status := jobItemReportStatusSuccess
		if execErr != nil {
			status = jobItemReportStatusFailed
		}
		logCompletion(ctx, jobCompletionLogLine(cfg, item, status, duration, execErr))
		reportJobItem(ctx, cfg, item, result, execErr, duration)
	}()
	handler, ok := globalRegistry.get(item.JobType)
	if !ok {
		execErr = Permanent(fmt.Errorf("handler not found for job_type %s", item.JobType), "HANDLER_NOT_FOUND")
		return
	}
	result, execErr = handler.Execute(ctx, item)
}

func jobCompletionLogLine(cfg Config, item JobItem, status int, duration time.Duration, execErr error) string {
	errorMessage := ""
	if execErr != nil {
		errorMessage = execErr.Error()
	}
	fields := []struct {
		key   string
		value string
		quote bool
	}{
		{key: "space_id", value: firstNonEmpty(item.SpaceID, cfg.SpaceID)},
		{key: "task_id", value: firstNonEmpty(stringParam(item.Params, "task_id"), taskIDFromJobItemID(item.JobItemID))},
		{key: "job_item_id", value: item.JobItemID},
		{key: "node_id", value: cfg.NodeID},
		{key: "job_type", value: item.JobType},
		{key: "attempt_no", value: strconv.Itoa(item.AttemptNo)},
		{key: "symbol", value: stringParam(item.Params, "symbol")},
		{key: "interval", value: stringParam(item.Params, "interval")},
		{key: "status", value: jobItemStatusText(status)},
		{key: "duration_ms", value: strconv.FormatInt(duration.Milliseconds(), 10)},
		{key: "error", value: errorMessage, quote: true},
	}
	var b strings.Builder
	b.WriteString("collector_job_done")
	for _, field := range fields {
		b.WriteByte(' ')
		b.WriteString(field.key)
		b.WriteByte('=')
		if field.quote {
			b.WriteString(strconv.Quote(field.value))
			continue
		}
		b.WriteString(logValue(field.value))
	}
	return b.String()
}

func jobItemStatusText(status int) string {
	if status == jobItemReportStatusFailed {
		return "failed"
	}
	return "success"
}

func taskIDFromJobItemID(jobItemID string) string {
	jobItemID = strings.TrimSpace(jobItemID)
	if jobItemID == "" {
		return ""
	}
	if before, _, ok := strings.Cut(jobItemID, ":"); ok {
		return strings.TrimSpace(before)
	}
	return jobItemID
}

func stringParam(params map[string]any, key string) string {
	if len(params) == 0 {
		return ""
	}
	raw, ok := params[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func logValue(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\r\n\"") {
		return strconv.Quote(value)
	}
	return value
}

func reportJobItem(ctx context.Context, cfg Config, item JobItem, result Result, execErr error, duration time.Duration) {
	reqBody := reportJobItemStatusRequest{
		SpaceID:       cfg.SpaceID,
		NodeID:        cfg.NodeID,
		JobItemID:     item.JobItemID,
		AttemptNo:     item.AttemptNo,
		Status:        jobItemReportStatusSuccess,
		ResultSummary: normalizeMap(result.Summary),
		DurationMS:    duration.Milliseconds(),
	}
	if execErr != nil {
		kind, code := classifyError(execErr)
		reqBody.Status = jobItemReportStatusFailed
		reqBody.ErrorKind = kind
		reqBody.ErrorCode = code
		reqBody.ErrorMessage = execErr.Error()
	}
	var rsp struct {
		RetInfo *retInfo `json:"ret_info"`
	}
	if err := postService(ctx, cfg, "cloudnode", "ReportJobItemStatus", reqBody, &rsp); err != nil {
		log.WarnContextf(ctx, "[CloudRuntime] report job item failed job_item_id=%s err=%v", item.JobItemID, err)
		return
	}
	if rsp.RetInfo == nil || rsp.RetInfo.Code != 0 {
		msg := ""
		if rsp.RetInfo != nil {
			msg = rsp.RetInfo.Msg
		}
		log.WarnContextf(ctx, "[CloudRuntime] report job item rejected job_item_id=%s msg=%s", item.JobItemID, msg)
	}
}

func classifyError(err error) (int, string) {
	var runtimeErr *runtimeError
	if errors.As(err, &runtimeErr) {
		if runtimeErr.kind == ErrorKindRetryable {
			return jobItemErrorKindRetryable, runtimeErr.code
		}
		return jobItemErrorKindPermanent, runtimeErr.code
	}
	return jobItemErrorKindPermanent, "HANDLER_ERROR"
}

func postService(ctx context.Context, cfg Config, module string, method string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/service/%s/%s", cfg.ServiceGatewayTarget, module, method)
	req, err := newSignedRequestWithContext(ctx, http.MethodPost, url, raw, cfg.Auth)
	if err != nil {
		return err
	}
	httpClient, err := servicegateway.NewClient(cfg.HTTPTimeout)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("request %s failed status=%d body=%s", url, resp.StatusCode, string(respBody))
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func normalizeGatewayTarget(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "http://" + raw
}

func normalizeErrorCode(code string, fallback string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return fallback
	}
	return code
}

func normalizeMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	return values
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

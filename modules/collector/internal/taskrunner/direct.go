// Package taskrunner adapts JetStream Job Execution Queue deliveries to collector workloads.
package taskrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/executor"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

var registerHandlersOnce sync.Once

type queueBinding struct {
	consumer   queueConsumer
	name       string
	subject    string
	subjectID  string
	jobType    string
	maxDeliver int
}

type queueConsumer interface {
	Fetch(context.Context, int) ([]*jetstream.Delivery, error)
	Close() error
	MaxDeliver() int
}

const directFetchMaxWait = 500 * time.Millisecond

type roundRobinConsumer struct {
	bindings   []queueBinding
	next       int
	stopOnIdle bool
}

func (c *roundRobinConsumer) Fetch(ctx context.Context, _ int) ([]*jetstream.Delivery, error) {
	if len(c.bindings) == 0 {
		return nil, jetstream.ErrClosed
	}
	for offset := 0; offset < len(c.bindings); offset++ {
		index := (c.next + offset) % len(c.bindings)
		deliveries, err := c.bindings[index].consumer.Fetch(ctx, 1)
		if len(deliveries) > 0 {
			c.next = (index + 1) % len(c.bindings)
			return deliveries, err
		}
		if err != nil && !errors.Is(err, nats.ErrTimeout) {
			return nil, err
		}
	}
	if c.stopOnIdle {
		return nil, jetstream.ErrClosed
	}
	return nil, nats.ErrTimeout
}

func (c *roundRobinConsumer) Close() error {
	var joined error
	for _, binding := range c.bindings {
		joined = errors.Join(joined, binding.consumer.Close())
	}
	return joined
}

// Run waits for the first complete keepalive and consumes jobs until ctx ends.
func Run(ctx context.Context) error {
	if err := runtimeapp.WaitForReadiness(ctx); err != nil {
		return err
	}
	return run(ctx, false)
}

// RunOnce binds the same queues as Run but exits after one empty fetch round.
func RunOnce(ctx context.Context) error {
	return run(ctx, true)
}

func run(ctx context.Context, stopOnIdle bool) error {
	spaceID := runtimeSpaceID()
	nodeID, _ := runtimeapp.GetNodeInfo()
	gatewayTarget := runtimeapp.GetServiceGatewayTarget()
	if spaceID == "" || nodeID == "" || gatewayTarget == "" {
		return fmt.Errorf("job execution requires MOOX_SPACE_ID, node_id and service gateway target")
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(nil, "collector-cloudjob-worker"))
	if err != nil {
		return err
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	jobTypes := jobs.SupportedJobTypes()
	bindings := make([]queueBinding, 0, len(jobTypes))
	for _, jobType := range jobTypes {
		identity := cloudjobqueue.Identity{SpaceID: spaceID, JobType: jobType}
		name, nameErr := identity.ConsumerName()
		if nameErr != nil {
			return nameErr
		}
		subjectID, subjectErr := identity.SubjectID()
		if subjectErr != nil {
			return subjectErr
		}
		subject, subjectErr := registry.RenderSubject(events.CloudJobExecutionRequested, spaceID, subjectID)
		if subjectErr != nil {
			return subjectErr
		}
		consumer, bindErr := events.BindSubjectConsumer(ctx, client, registry, events.SubjectConsumerConfig{
			ConsumerConfig: events.ConsumerConfig{
				Name: name, Event: events.CloudJobExecutionRequested, FetchMaxWait: directFetchMaxWait,
				DeliverDecodeErrors: true,
			},
			SpaceID: spaceID, SubjectID: subjectID,
		})
		if errors.Is(bindErr, jetstream.ErrConsumerNotFound) {
			continue
		}
		if bindErr != nil {
			return bindErr
		}
		bindings = append(bindings, queueBinding{
			consumer: consumer, name: name, subject: subject, subjectID: subjectID,
			jobType: jobType, maxDeliver: consumer.MaxDeliver(),
		})
	}
	if len(bindings) == 0 {
		return fmt.Errorf("no active job execution queue")
	}
	registerCollectorHandlers()
	auth := runtimeapp.GetServiceAuthConfig()
	runtimeCfg := nodeRuntime.Config{
		ServiceGatewayTarget: gatewayTarget, SpaceID: spaceID, NodeID: nodeID,
		Auth: nodeRuntime.AuthConfig{
			AccessKey: auth.AccessKey, SecretKey: auth.SecretKey, TargetNode: auth.TargetNode,
			CAFile: auth.CAFile, CAPEMBase64: auth.CAPEMBase64, ExpireSec: auth.ExpireSec,
		},
	}
	roundRobin := &roundRobinConsumer{bindings: bindings, stopOnIdle: stopOnIdle}
	defer roundRobin.Close()
	handler := jetstream.DeliveryHandlerFunc(func(handleCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		return handleDelivery(handleCtx, registry, bindings, runtimeCfg, delivery)
	})
	actionReporter := &jobActionReporter{
		registry: registry, bindings: bindings, spaceID: spaceID, nodeID: nodeID,
	}
	return jetstream.NewRunner(roundRobin, handler, jetstream.RunnerConfig{
		BatchSize: 1, InProgressInterval: 0,
		ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			reportTransportError(ctx, nodeID, err)
		}),
		ActionReporter: actionReporter,
	}).Run(ctx)
}

func handleDelivery(ctx context.Context, registry *events.Registry, bindings []queueBinding, cfg nodeRuntime.Config, delivery *jetstream.Delivery) jetstream.HandlerResult {
	return handleDeliveryAt(ctx, registry, bindings, cfg, delivery, time.Now)
}

func handleDeliveryAt(
	ctx context.Context,
	registry *events.Registry,
	bindings []queueBinding,
	cfg nodeRuntime.Config,
	delivery *jetstream.Delivery,
	now func() time.Time,
) jetstream.HandlerResult {
	receivedFields := baseDeliveryLogFields(delivery, cfg.NodeID)

	if delivery == nil {
		err := jetstream.ErrInvalidDelivery
		receivedFields.Event = "collector_job_received"
		writeJobLog(ctx, receivedFields, false)
		receivedFields.Event = "collector_job_rejected"
		receivedFields.Decision = "TERM"
		receivedFields.ErrorCode = "INVALID_DELIVERY"
		receivedFields.Err = err
		writeJobLog(ctx, receivedFields, true)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	var binding *queueBinding
	for i := range bindings {
		if bindings[i].name == delivery.Consumer {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil || delivery.Subject != binding.subject {
		err := fmt.Errorf("job execution queue identity mismatch")
		receivedFields.Event = "collector_job_received"
		writeJobLog(ctx, receivedFields, false)
		receivedFields.Event = "collector_job_rejected"
		receivedFields.Decision = "TERM"
		receivedFields.ErrorCode = "QUEUE_IDENTITY_MISMATCH"
		receivedFields.Err = err
		writeJobLog(ctx, receivedFields, true)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	decoded := events.DecodeDelivery(registry, delivery)
	payload, ok := decoded.Payload.(*cloudjobpb.JobExecutionRequested)
	if decoded.Err != nil || !ok || decoded.Message.GetSpaceId() != cfg.SpaceID ||
		decoded.Message.GetSubjectId() != binding.subjectID ||
		payload.GetJobType() != binding.jobType {
		err := fmt.Errorf("invalid job execution delivery: %v", decoded.Err)
		receivedFields.Event = "collector_job_received"
		writeJobLog(ctx, receivedFields, false)
		receivedFields.Event = "collector_job_rejected"
		receivedFields.Decision = "TERM"
		receivedFields.ErrorCode = "INVALID_DELIVERY"
		receivedFields.Err = err
		writeJobLog(ctx, receivedFields, true)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	fields := validatedDeliveryLogFields(registry, bindings, cfg.SpaceID, cfg.NodeID, delivery)
	fields.Event = "collector_job_received"
	writeJobLog(ctx, fields, false)
	executeAt, err := requestedExecutionTime(payload)
	if err != nil {
		fields.Event = "collector_job_rejected"
		fields.Decision = "TERM"
		fields.ErrorCode = "INVALID_EXECUTE_AT"
		fields.Err = err
		writeJobLog(ctx, fields, true)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if decision := executeAtDecision(executeAt, now().UTC()); decision.Decision == jetstream.RETRY {
		fields.Event = "collector_job_deferred"
		fields.Decision = "RETRY"
		fields.Delay = decision.Delay
		fields.Status = "deferred"
		writeJobLog(ctx, fields, false)
		return decision
	}
	fields.Event = "collector_job_started"
	fields.Decision = "EXECUTE"
	fields.Status = "running"
	writeJobLog(ctx, fields, false)
	item := nodeRuntime.JobItem{
		SpaceID: decoded.Message.GetSpaceId(), JobID: payload.GetJobId(), JobItemID: payload.GetJobItemId(),
		JobType: payload.GetJobType(), Params: payload.GetParams().AsMap(), ExecuteAt: executeAt,
		Consumer: delivery.Consumer, MessageID: delivery.RawMessageID,
	}
	return nodeRuntime.ExecuteJobItem(ctx, cfg, item, delivery.DeliveryCount, binding.maxDeliver)
}

func requestedExecutionTime(payload *cloudjobpb.JobExecutionRequested) (time.Time, error) {
	if payload.GetExecuteAt() == nil {
		return time.Time{}, nil
	}
	if err := payload.GetExecuteAt().CheckValid(); err != nil {
		return time.Time{}, fmt.Errorf("invalid execute_at: %w", err)
	}
	return payload.GetExecuteAt().AsTime(), nil
}

func executeAtDecision(executeAt, now time.Time) jetstream.HandlerResult {
	if executeAt.IsZero() || !now.Before(executeAt) {
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}
	return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: executeAt.Sub(now)}
}

func registerCollectorHandlers() {
	registerHandlersOnce.Do(func() {
		for _, jobType := range jobs.SupportedJobTypes() {
			nodeRuntime.Register(jobType, nodeRuntime.HandlerFunc(executeCollectorJobItem))
		}
	})
}

func runtimeSpaceID() string {
	return strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
}

func executeCollectorJobItem(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	execCtx, cancel := context.WithTimeout(ctx, 105*time.Second)
	defer cancel()
	taskEvent, err := taskEventFromJobItem(item)
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "INVALID_JOB_ITEM")
	}
	result, err := executor.ExecuteTaskImmediately(execCtx, taskEvent)
	summary := map[string]any{}
	if strings.TrimSpace(result) != "" {
		_ = json.Unmarshal([]byte(result), &summary)
	}
	if err != nil {
		return nodeRuntime.Result{Summary: summary}, nodeRuntime.Retryable(err, "COLLECT_FAILED")
	}
	return nodeRuntime.Result{Summary: summary}, nil
}

func taskEventFromJobItem(item nodeRuntime.JobItem) (*model.TaskExecuteEvent, error) {
	payload := item.Params
	taskID := firstString(stringValue(payload, "task_id"), item.JobItemID)
	dataType := strings.ToLower(firstString(stringValue(payload, "data_type"), "kline"))
	market := strings.ToLower(firstString(stringValue(payload, "market"), "spot"))
	symbol := stringValue(payload, "symbol")
	subjectID := firstString(stringValue(payload, "subject_id"), symbol)
	datasetID := stringValue(payload, "dataset_id")
	interval := stringValue(payload, "interval")
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	if strings.TrimSpace(item.JobItemID) == "" {
		return nil, fmt.Errorf("job_item_id is required")
	}
	if datasetID == "" {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if dataType != "symbol" && symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if dataType != "symbol" && interval == "" {
		interval = "1m"
	}
	intervals := []string{interval}
	if dataType == "symbol" {
		intervals = []string{""}
	}
	return &model.TaskExecuteEvent{
		SpaceID: stringValue(payload, "space_id"), DatasetID: datasetID,
		TaskID: taskID, JobItemID: item.JobItemID, DataType: dataType,
		DataSource: firstString(stringValue(payload, "exchange"), "binance"), Market: market,
		InstType: strings.ToUpper(market), SubjectID: subjectID, Symbol: symbol, Intervals: intervals,
		Immediate: true, Live: boolValue(payload, "live"),
	}, nil
}

func boolValue(payload map[string]any, key string) bool {
	value, ok := payload[key].(bool)
	return ok && value
}
func stringValue(payload map[string]any, key string) string {
	if value, ok := payload[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

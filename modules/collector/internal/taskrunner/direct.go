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
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
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
	bindings []queueBinding
	next     int
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
	return nil, jetstream.ErrClosed
}

func (c *roundRobinConsumer) Close() error {
	var joined error
	for _, binding := range c.bindings {
		joined = errors.Join(joined, binding.consumer.Close())
	}
	return joined
}

// RunJobItems binds existing per-route consumers, executes available work, then exits.
func RunJobItems(ctx context.Context) error {
	spaceID := runtimeSpaceID()
	codePackageID := strings.TrimSpace(os.Getenv("MOOX_CODE_PACKAGE_ID"))
	nodeID, _ := runtimeapp.GetNodeInfo()
	nodeID = firstString(nodeID, os.Getenv("MOOX_RUNTIME_NODE_ID"))
	gatewayTarget := runtimeapp.GetServiceGatewayTarget()
	if spaceID == "" || codePackageID == "" || nodeID == "" || gatewayTarget == "" {
		return fmt.Errorf("job execution requires space_id, code_package_id, node_id and service gateway target")
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
	jobTypes := []string{jobs.JobTypeCollectKline, jobs.JobTypeCollectSymbol}
	bindings := make([]queueBinding, 0, len(jobTypes))
	for _, jobType := range jobTypes {
		identity := cloudjobqueue.Identity{SpaceID: spaceID, CodePackageID: codePackageID, JobType: jobType}
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
		ServiceGatewayTarget: gatewayTarget, SpaceID: spaceID, NodeID: nodeID, CodePackageID: codePackageID,
		Auth: nodeRuntime.AuthConfig{
			AccessKey: auth.AccessKey, SecretKey: auth.SecretKey, TargetNode: auth.TargetNode,
			CAFile: auth.CAFile, CAPEMBase64: auth.CAPEMBase64, ExpireSec: auth.ExpireSec,
		},
	}
	roundRobin := &roundRobinConsumer{bindings: bindings}
	defer roundRobin.Close()
	handler := jetstream.DeliveryHandlerFunc(func(handleCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		return handleDelivery(handleCtx, registry, bindings, runtimeCfg, delivery)
	})
	return jetstream.NewRunner(roundRobin, handler, jetstream.RunnerConfig{BatchSize: 1, InProgressInterval: 0}).Run(ctx)
}

func handleDelivery(ctx context.Context, registry *events.Registry, bindings []queueBinding, cfg nodeRuntime.Config, delivery *jetstream.Delivery) jetstream.HandlerResult {
	var binding *queueBinding
	for i := range bindings {
		if bindings[i].name == delivery.Consumer {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil || delivery.Subject != binding.subject {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("job execution queue identity mismatch")}
	}
	decoded := events.DecodeDelivery(registry, delivery)
	payload, ok := decoded.Payload.(*cloudjobpb.JobExecutionRequested)
	if decoded.Err != nil || !ok || decoded.Message.GetSpaceId() != cfg.SpaceID ||
		decoded.Message.GetSubjectId() != binding.subjectID ||
		payload.GetCodePackageId() != cfg.CodePackageID || payload.GetJobType() != binding.jobType {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("invalid job execution delivery: %v", decoded.Err)}
	}
	item := nodeRuntime.JobItem{
		SpaceID: decoded.Message.GetSpaceId(), JobID: payload.GetJobId(), JobItemID: payload.GetJobItemId(),
		JobType: payload.GetJobType(), CodePackageID: payload.GetCodePackageId(), Params: payload.GetParams().AsMap(),
	}
	return nodeRuntime.ExecuteJobItem(ctx, cfg, item, delivery.DeliveryCount, binding.maxDeliver)
}

func registerCollectorHandlers() {
	registerHandlersOnce.Do(func() {
		nodeRuntime.Register(jobs.JobTypeCollectKline, nodeRuntime.HandlerFunc(executeCollectorJobItem))
		nodeRuntime.Register(jobs.JobTypeCollectSymbol, nodeRuntime.HandlerFunc(executeCollectorJobItem))
	})
}

func runtimeSpaceID() string {
	if value := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")); value != "" {
		return value
	}
	binding, err := binance.ResolveStorageBinding(binance.InstTypeSPOT)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(binding.SpaceID)
}

func executeCollectorJobItem(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	execCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
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
	interval := stringValue(payload, "interval")
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
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
		SpaceID: stringValue(payload, "space_id"), TaskID: taskID, DataType: dataType,
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

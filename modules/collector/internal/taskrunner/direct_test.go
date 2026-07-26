package taskrunner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/executor"
	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type directTestActionReporterFunc func(
	context.Context,
	*jetstream.Delivery,
	jetstream.HandlerResult,
	error,
)

func (f directTestActionReporterFunc) ReportAction(
	ctx context.Context,
	delivery *jetstream.Delivery,
	result jetstream.HandlerResult,
	err error,
) {
	f(ctx, delivery, result, err)
}

func TestCollectorWorkloadTimeout(t *testing.T) {
	if collectorWorkloadTimeout != 100*time.Second {
		t.Fatalf("collectorWorkloadTimeout = %v, want 100s", collectorWorkloadTimeout)
	}
}

func TestEventBusConfigLoadsRoleCredentialFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, []byte("test-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(dir, "cloudnode-worker.yaml")
	if err := os.WriteFile(credentialPath, []byte(
		"version: 1\nurls: [tls://eventbus.example:4222]\nusername: cloudnode-worker\ntoken: worker-token\nca_file: ca.pem\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_EVENTBUS_CREDENTIAL_FILE", credentialPath)

	cfg, err := eventBusConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.URLs) != 1 || cfg.URLs[0] != "tls://eventbus.example:4222" {
		t.Fatalf("URLs = %v", cfg.URLs)
	}
	if cfg.Username != "cloudnode-worker" || cfg.Password != "worker-token" {
		t.Fatalf("credentials = %q/%q", cfg.Username, cfg.Password)
	}
	if cfg.TLSCAFile != caPath {
		t.Fatalf("TLSCAFile = %q, want %q", cfg.TLSCAFile, caPath)
	}
}

func TestEventBusConfigRejectsInvalidRoleCredentialFile(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "cloudnode-worker.yaml")
	if err := os.WriteFile(credentialPath, []byte("username: cloudnode-worker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_EVENTBUS_CREDENTIAL_FILE", credentialPath)

	if _, err := eventBusConfig(); err == nil {
		t.Fatal("eventBusConfig() accepted an invalid role credential file")
	}
}

func TestCollectorErrorCodeDistinguishesInstanceReportFailure(t *testing.T) {
	reportErr := fmt.Errorf("%w: gateway unavailable", executor.ErrTaskInstanceReportFailed)
	if got := collectorErrorCode(reportErr); got != "TASK_INSTANCE_REPORT_FAILED" {
		t.Fatalf("report error code = %q", got)
	}
	if got := collectorErrorCode(errors.New("binance collect failed")); got != "COLLECT_FAILED" {
		t.Fatalf("collect error code = %q", got)
	}
}

type fakeQueueConsumer struct {
	results []fakeFetchResult
}

type fakeFetchResult struct {
	deliveries []*jetstream.Delivery
	err        error
}

type directTestExecution struct {
	itemID string
	at     time.Time
}

type directTestAction struct {
	itemID        string
	decision      jetstream.HandlerDecision
	delay         time.Duration
	deliveryCount uint64
	err           error
}

func (f *fakeQueueConsumer) Fetch(context.Context, int) ([]*jetstream.Delivery, error) {
	if len(f.results) == 0 {
		return nil, nats.ErrTimeout
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.deliveries, result.err
}
func (*fakeQueueConsumer) Close() error    { return nil }
func (*fakeQueueConsumer) MaxDeliver() int { return 3 }

func TestRoundRobinConsumerDoesNotStarveBindings(t *testing.T) {
	if directFetchMaxWait != 500*time.Millisecond {
		t.Fatalf("directFetchMaxWait = %s", directFetchMaxWait)
	}
	first := &fakeQueueConsumer{results: []fakeFetchResult{
		{deliveries: []*jetstream.Delivery{{Consumer: "first-1"}}},
		{deliveries: []*jetstream.Delivery{{Consumer: "first-2"}}},
	}}
	second := &fakeQueueConsumer{results: []fakeFetchResult{
		{deliveries: []*jetstream.Delivery{{Consumer: "second-1"}}},
	}}
	consumer := &roundRobinConsumer{bindings: []queueBinding{{consumer: first}, {consumer: second}}}
	for index, want := range []string{"first-1", "second-1", "first-2"} {
		rows, err := consumer.Fetch(context.Background(), 1)
		if err != nil || len(rows) != 1 || rows[0].Consumer != want {
			t.Fatalf("fetch %d = %+v, %v; want %s", index, rows, err, want)
		}
	}
}

func TestRoundRobinConsumerReturnsDeliveryAndErrorTogether(t *testing.T) {
	wantErr := errors.New("fetch failed after delivery")
	binding := &fakeQueueConsumer{results: []fakeFetchResult{{
		deliveries: []*jetstream.Delivery{{Consumer: "one"}},
		err:        wantErr,
	}}}
	consumer := &roundRobinConsumer{bindings: []queueBinding{{consumer: binding}}}
	rows, err := consumer.Fetch(context.Background(), 1)
	if len(rows) != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("fetch = %+v, %v", rows, err)
	}
}

func TestRoundRobinConsumerResidentModeKeepsWaitingAfterEmptyRound(t *testing.T) {
	consumer := &roundRobinConsumer{bindings: []queueBinding{
		{consumer: &fakeQueueConsumer{}},
		{consumer: &fakeQueueConsumer{}},
	}}
	rows, err := consumer.Fetch(context.Background(), 1)
	if len(rows) != 0 || !errors.Is(err, nats.ErrTimeout) {
		t.Fatalf("fetch = %+v, %v", rows, err)
	}
}

func TestRoundRobinConsumerDiagnosticModeStopsAfterEmptyRound(t *testing.T) {
	consumer := &roundRobinConsumer{
		bindings:   []queueBinding{{consumer: &fakeQueueConsumer{}}},
		stopOnIdle: true,
	}
	rows, err := consumer.Fetch(context.Background(), 1)
	if len(rows) != 0 || !errors.Is(err, jetstream.ErrClosed) {
		t.Fatalf("fetch = %+v, %v", rows, err)
	}
}

func TestExecuteAtDecision(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		executeAt time.Time
		decision  jetstream.HandlerDecision
		delay     time.Duration
	}{
		{name: "missing executes immediately", decision: jetstream.ACK},
		{name: "past executes immediately", executeAt: now.Add(-time.Second), decision: jetstream.ACK},
		{name: "equal executes immediately", executeAt: now, decision: jetstream.ACK},
		{name: "future retries until due", executeAt: now.Add(17 * time.Second), decision: jetstream.RETRY, delay: 17 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := executeAtDecision(test.executeAt, now)
			if result.Decision != test.decision || result.Delay != test.delay {
				t.Fatalf("executeAtDecision() = %+v, want decision=%v delay=%s", result, test.decision, test.delay)
			}
		})
	}
}

func TestTaskEventFromJobItemCarriesJobItemID(t *testing.T) {
	event, err := taskEventFromJobItem(nodeRuntime.JobItem{
		SpaceID:       "crypto",
		JobItemID:     "item-123",
		DeliveryCount: 4,
		Params: map[string]any{
			"space_id":   "crypto",
			"dataset_id": "symbols-custom",
			"task_id":    "task-1",
			"data_type":  "symbol",
			"exchange":   "binance",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.JobItemID != "item-123" {
		t.Fatalf("job item id = %q, want item-123", event.JobItemID)
	}
	if event.DeliveryCount != 4 {
		t.Fatalf("delivery count = %d, want 4", event.DeliveryCount)
	}
	if event.DatasetID != "symbols-custom" {
		t.Fatalf("dataset id = %q, want symbols-custom", event.DatasetID)
	}
}

func TestTaskEventFromJobItemRequiresJobItemID(t *testing.T) {
	_, err := taskEventFromJobItem(nodeRuntime.JobItem{
		SpaceID: "crypto",
		Params: map[string]any{
			"space_id":   "crypto",
			"dataset_id": "symbols-custom",
			"task_id":    "task-1",
			"data_type":  "symbol",
			"exchange":   "binance",
		},
	})
	if err == nil || err.Error() != "job_item_id is required" {
		t.Fatalf("taskEventFromJobItem() error = %v, want required job_item_id", err)
	}
}

func TestTaskEventFromJobItemRequiresDatasetID(t *testing.T) {
	_, err := taskEventFromJobItem(nodeRuntime.JobItem{
		SpaceID:   "crypto",
		JobItemID: "item-123",
		Params: map[string]any{
			"space_id":  "crypto",
			"task_id":   "task-1",
			"data_type": "symbol",
			"exchange":  "binance",
		},
	})
	if err == nil || err.Error() != "dataset_id is required" {
		t.Fatalf("taskEventFromJobItem() error = %v, want required dataset_id", err)
	}
}

func TestHandleDeliveryAtDefersFutureJobWithoutExecuting(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	jobType := "test.execute-at.future"
	itemID := "future-1"
	identity := cloudjobqueue.Identity{SpaceID: "crypto", JobType: jobType}
	consumerName, err := identity.ConsumerName()
	if err != nil {
		t.Fatal(err)
	}
	subjectID, err := identity.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := registry.RenderSubject(events.CloudJobExecutionRequested, "crypto", subjectID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := registry.MarshalMessage(events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: "job-1", JobItemId: itemID, JobType: jobType,
		ExecuteAt: timestamppb.New(now.Add(20 * time.Second)),
	}, events.PublishOptions{
		EventID: itemID, SpaceID: "crypto", SubjectID: subjectID, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	executed := false
	nodeRuntime.Register(jobType, nodeRuntime.HandlerFunc(func(context.Context, nodeRuntime.JobItem) (nodeRuntime.Result, error) {
		executed = true
		return nodeRuntime.Result{}, nil
	}))
	binding := queueBinding{
		name: consumerName, subject: subject, subjectID: subjectID, jobType: jobType, maxDeliver: 3,
	}
	result := handleDeliveryAt(context.Background(), registry, []queueBinding{binding}, nodeRuntime.Config{
		ServiceGatewayTarget: "http://127.0.0.1:1", SpaceID: "crypto", NodeID: "node-1",
	}, &jetstream.Delivery{
		Consumer: consumerName, Subject: subject, RawData: raw, RawMessageID: itemID,
		ContentType: events.ContentType, DeliveryCount: 1,
	}, func() time.Time { return now })

	if result.Decision != jetstream.RETRY || result.Delay != 20*time.Second {
		t.Fatalf("result = %+v", result)
	}
	if executed {
		t.Fatal("future job executed before execute_at")
	}
}

func TestDirectWorkerJetStreamAckRetryAndTerm(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{
		Name: "MOOX_CLOUDNODE_EXEC", Subjects: []string{"moox.cloudnode.job.execution.requested.v1.>"},
		Storage: nats.MemoryStorage, Retention: nats.WorkQueuePolicy,
	})

	ctx := context.Background()
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "direct-worker-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}

	reportsFail := false
	reports := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reports++
		if reportsFail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer gateway.Close()
	cfg := nodeRuntime.Config{
		ServiceGatewayTarget: gateway.URL, SpaceID: "crypto", NodeID: "scf-1",
		Auth: nodeRuntime.AuthConfig{AccessKey: "collector", SecretKey: "secret", TargetNode: "node-1"},
	}

	nodeRuntime.Register("test.direct.success", nodeRuntime.HandlerFunc(func(context.Context, nodeRuntime.JobItem) (nodeRuntime.Result, error) {
		return nodeRuntime.Result{Summary: map[string]any{"rows": 1}}, nil
	}))
	success := bindDirectTestQueue(t, ctx, client, registry, "test.direct.success")
	publishDirectTestJob(t, ctx, publisher, success, "success-1")
	delivery := fetchDirectTestDelivery(t, ctx, success.consumer)
	result := handleDelivery(ctx, registry, []queueBinding{success}, cfg, delivery)
	if result.Decision != jetstream.ACK {
		t.Fatalf("success decision = %+v", result)
	}
	if err := jetstream.ApplyHandlerResult(ctx, delivery, result); err != nil {
		t.Fatal(err)
	}

	nodeRuntime.Register("test.direct.retry", nodeRuntime.HandlerFunc(func(context.Context, nodeRuntime.JobItem) (nodeRuntime.Result, error) {
		return nodeRuntime.Result{}, nodeRuntime.Retryable(errors.New("temporary"), "TEMP")
	}))
	retry := bindDirectTestQueue(t, ctx, client, registry, "test.direct.retry")
	publishDirectTestJob(t, ctx, publisher, retry, "retry-1")
	first := fetchDirectTestDelivery(t, ctx, retry.consumer)
	firstResult := handleDelivery(ctx, registry, []queueBinding{retry}, cfg, first)
	if firstResult.Decision != jetstream.RETRY || first.DeliveryCount != 1 {
		t.Fatalf("first retry = delivery=%+v result=%+v", first, firstResult)
	}
	firstResult.Delay = 0
	if err := jetstream.ApplyHandlerResult(ctx, first, firstResult); err != nil {
		t.Fatal(err)
	}
	last := fetchDirectTestDelivery(t, ctx, retry.consumer)
	lastResult := handleDelivery(ctx, registry, []queueBinding{retry}, cfg, last)
	if lastResult.Decision != jetstream.TERM || last.DeliveryCount != 2 {
		t.Fatalf("last retry = delivery=%+v result=%+v", last, lastResult)
	}
	if err := jetstream.ApplyHandlerResult(ctx, last, lastResult); err != nil {
		t.Fatal(err)
	}

	nodeRuntime.Register("test.direct.report", nodeRuntime.HandlerFunc(func(context.Context, nodeRuntime.JobItem) (nodeRuntime.Result, error) {
		return nodeRuntime.Result{}, nil
	}))
	reportQueue := bindDirectTestQueue(t, ctx, client, registry, "test.direct.report")
	publishDirectTestJob(t, ctx, publisher, reportQueue, "report-1")
	reportsFail = true
	reportFirst := fetchDirectTestDelivery(t, ctx, reportQueue.consumer)
	reportResult := handleDelivery(ctx, registry, []queueBinding{reportQueue}, cfg, reportFirst)
	if reportResult.Decision != jetstream.RETRY {
		t.Fatalf("report failure decision = %+v", reportResult)
	}
	reportResult.Delay = 0
	if err := jetstream.ApplyHandlerResult(ctx, reportFirst, reportResult); err != nil {
		t.Fatal(err)
	}
	reportsFail = false
	reportLast := fetchDirectTestDelivery(t, ctx, reportQueue.consumer)
	reportResult = handleDelivery(ctx, registry, []queueBinding{reportQueue}, cfg, reportLast)
	if reportResult.Decision != jetstream.ACK || reportLast.DeliveryCount != 2 {
		t.Fatalf("report recovery = delivery=%+v result=%+v", reportLast, reportResult)
	}
	if err := jetstream.ApplyHandlerResult(ctx, reportLast, reportResult); err != nil {
		t.Fatal(err)
	}
	if reports < 4 {
		t.Fatalf("reports = %d, want at least 4", reports)
	}
}

func TestDirectWorkerJetStreamExecuteAtTiming(t *testing.T) {
	server := testkit.Start(t)
	server.AddStream(t, &nats.StreamConfig{
		Name: "MOOX_CLOUDNODE_EXEC", Subjects: []string{"moox.cloudnode.job.execution.requested.v1.>"},
		Storage: nats.MemoryStorage, Retention: nats.WorkQueuePolicy,
	})

	ctx := context.Background()
	client, err := jetstream.Connect(ctx, jetstream.Config{
		URLs: []string{server.URL()}, Name: "direct-worker-execute-at",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	t.Cleanup(gateway.Close)
	cfg := nodeRuntime.Config{
		ServiceGatewayTarget: gateway.URL, SpaceID: "crypto", NodeID: "scf-timing",
		Auth: nodeRuntime.AuthConfig{AccessKey: "collector", SecretKey: "secret", TargetNode: "node-1"},
	}

	executions := make(chan directTestExecution, 4)
	jobType := fmt.Sprintf("test.direct.execute-at.timing.%d", time.Now().UnixNano())
	nodeRuntime.Register(jobType, nodeRuntime.HandlerFunc(func(_ context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
		executions <- directTestExecution{itemID: item.JobItemID, at: time.Now()}
		return nodeRuntime.Result{}, nil
	}))
	binding := bindDirectTestQueue(t, ctx, client, registry, jobType)

	actions := make(chan directTestAction, 4)
	runnerCtx, cancelRunner := context.WithCancel(ctx)
	runnerDone := make(chan error, 1)
	runnerStopped := false
	go func() {
		runnerDone <- jetstream.NewRunner(
			binding.consumer,
			jetstream.DeliveryHandlerFunc(func(handleCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
				return handleDelivery(handleCtx, registry, []queueBinding{binding}, cfg, delivery)
			}),
			jetstream.RunnerConfig{
				BatchSize: 1,
				ActionReporter: directTestActionReporterFunc(func(
					_ context.Context,
					delivery *jetstream.Delivery,
					result jetstream.HandlerResult,
					err error,
				) {
					actions <- directTestAction{
						itemID: delivery.RawMessageID, decision: result.Decision,
						delay: result.Delay, deliveryCount: delivery.DeliveryCount, err: err,
					}
				}),
			},
		).Run(runnerCtx)
	}()
	t.Cleanup(func() {
		cancelRunner()
		if runnerStopped {
			return
		}
		select {
		case <-runnerDone:
		case <-time.After(2 * time.Second):
		}
	})

	publishDirectTestJobAt(t, ctx, publisher, binding, "timing-missing", nil)
	assertDirectTestExecution(t, executions, "timing-missing")
	assertDirectTestAction(t, actions, "timing-missing", jetstream.ACK, 1)

	past := time.Now().Add(-time.Second)
	publishDirectTestJobAt(t, ctx, publisher, binding, "timing-past", timestamppb.New(past))
	assertDirectTestExecution(t, executions, "timing-past")
	assertDirectTestAction(t, actions, "timing-past", jetstream.ACK, 1)

	due := time.Now().Add(300 * time.Millisecond)
	publishDirectTestJobAt(t, ctx, publisher, binding, "timing-future", timestamppb.New(due))
	firstFuture := assertDirectTestAction(t, actions, "timing-future", jetstream.RETRY, 1)
	if firstFuture.delay <= 0 || firstFuture.delay > 300*time.Millisecond {
		t.Fatalf("future retry delay = %s, want (0, 300ms]", firstFuture.delay)
	}

	futureExecution := assertDirectTestExecution(t, executions, "timing-future")
	if futureExecution.at.Before(due) {
		t.Fatalf("future workload executed at %s before due time %s", futureExecution.at, due)
	}
	assertDirectTestAction(t, actions, "timing-future", jetstream.ACK, 2)

	cancelRunner()
	select {
	case err := <-runnerDone:
		runnerStopped = true
		if err != nil {
			t.Fatalf("Runner.Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Runner.Run() did not stop after cancellation")
	}
}

func assertDirectTestExecution(t *testing.T, executions <-chan directTestExecution, itemID string) directTestExecution {
	t.Helper()
	select {
	case got := <-executions:
		if got.itemID != itemID {
			t.Fatalf("executed item = %+v, want %s", got, itemID)
		}
		return got
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for workload %s", itemID)
		return directTestExecution{}
	}
}

func assertDirectTestAction(
	t *testing.T,
	actions <-chan directTestAction,
	itemID string,
	decision jetstream.HandlerDecision,
	deliveryCount uint64,
) directTestAction {
	t.Helper()
	select {
	case got := <-actions:
		if got.itemID != itemID || got.decision != decision || got.deliveryCount != deliveryCount || got.err != nil {
			t.Fatalf(
				"action = %+v, want item=%s decision=%v delivery_count=%d with no transport error",
				got, itemID, decision, deliveryCount,
			)
		}
		return got
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for action %s", itemID)
		return directTestAction{}
	}
}

func bindDirectTestQueue(t *testing.T, ctx context.Context, client *jetstream.Client, registry *events.Registry, jobType string) queueBinding {
	t.Helper()
	identity := cloudjobqueue.Identity{SpaceID: "crypto", JobType: jobType}
	name, _ := identity.ConsumerName()
	subjectID, _ := identity.SubjectID()
	subject, _ := registry.RenderSubject(events.CloudJobExecutionRequested, "crypto", subjectID)
	cfg := events.SubjectConsumerConfig{
		ConsumerConfig: events.ConsumerConfig{
			Name: name, Event: events.CloudJobExecutionRequested, AckWait: time.Second, MaxDeliver: 2,
			MaxAckPending: 1, FetchMaxWait: time.Second, DeliverDecodeErrors: true,
		},
		SpaceID: "crypto", SubjectID: subjectID,
	}
	if _, err := events.EnsureSubjectConsumer(ctx, client, registry, cfg); err != nil {
		t.Fatal(err)
	}
	consumer, err := events.BindSubjectConsumer(ctx, client, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	return queueBinding{consumer: consumer, name: name, subject: subject, subjectID: subjectID, jobType: jobType, maxDeliver: 2}
}

func publishDirectTestJob(t *testing.T, ctx context.Context, publisher *events.Publisher, binding queueBinding, itemID string) {
	t.Helper()
	publishDirectTestJobAt(t, ctx, publisher, binding, itemID, nil)
}

func publishDirectTestJobAt(
	t *testing.T,
	ctx context.Context,
	publisher *events.Publisher,
	binding queueBinding,
	itemID string,
	executeAt *timestamppb.Timestamp,
) {
	t.Helper()
	_, err := publisher.Publish(ctx, events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: "job-1", JobItemId: itemID, JobType: binding.jobType, ExecuteAt: executeAt,
	}, events.PublishOptions{EventID: itemID, SpaceID: "crypto", SubjectID: binding.subjectID, OccurredAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
}

func fetchDirectTestDelivery(t *testing.T, ctx context.Context, consumer queueConsumer) *jetstream.Delivery {
	t.Helper()
	deliveries, err := consumer.Fetch(ctx, 1)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("fetch deliveries=%v err=%v", deliveries, err)
	}
	return deliveries[0]
}

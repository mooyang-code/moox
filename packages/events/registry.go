package events

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Event 表示一个由代码声明的业务事件契约。
type Event struct {
	name       string
	version    uint32
	stream     string
	owner      string
	newPayload func() proto.Message
	validate   EventValidator
}

func (e Event) Name() string    { return e.name }
func (e Event) Version() uint32 { return e.version }
func (e Event) Stream() string  { return e.stream }
func (e Event) Owner() string   { return e.owner }
func (e Event) NewPayload() proto.Message {
	if e.newPayload == nil {
		return nil
	}
	return e.newPayload()
}
func (e Event) PayloadFullName() protoreflect.FullName {
	if payload := e.NewPayload(); payload != nil {
		return payload.ProtoReflect().Descriptor().FullName()
	}
	return ""
}
func (e Event) Validate(message *eventpb.EventMessage, payload proto.Message) error {
	if e.validate == nil {
		return fmt.Errorf("event %s validator is nil", eventKey(e))
	}
	return e.validate(message, payload)
}

var builtInEvents []Event

const ObservabilityFilterSubject = "moox.event.observability.>"

func ObservabilityStreamName() string {
	return ObservabilityMetricsSnapshotReported.Stream()
}

func declareEvent(name string, version uint32, stream, owner string, newPayload func() proto.Message, validate EventValidator) Event {
	event := Event{name: name, version: version, stream: stream, owner: owner, newPayload: newPayload, validate: validate}
	builtInEvents = append(builtInEvents, event)
	return event
}

var (
	CloudJobExecutionRequested = declareEvent("event.cloudnode.job.execution.requested", 1, "MOOX_CLOUDNODE_EXEC", "cloudnode", func() proto.Message {
		return &cloudjobpb.JobExecutionRequested{}
	}, validateCloudJobExecutionRequested)
	ObservabilityMetricsSnapshotReported = declareEvent("event.observability.metrics.snapshot.reported", 1, "MOOX_OBSERVABILITY", "service", func() proto.Message {
		return &metricspb.MetricReport{}
	}, validateObservabilityMetricsSnapshotReported)
	ObservabilityHostSnapshotReported = declareEvent("event.observability.host.snapshot.reported", 1, "MOOX_OBSERVABILITY", "hostagent", func() proto.Message {
		return &hostmetricpb.HostMetric{}
	}, validateObservabilityHostSnapshotReported)
	ObservabilityHealthCheckReported = declareEvent("event.observability.health.check.reported", 1, "MOOX_OBSERVABILITY", "watchdog", func() proto.Message {
		return &observabilitypb.HealthCheckReport{}
	}, validateObservabilityHealthCheckReported)
	DatasetRowsUpserted = declareEvent("event.storage.dataset.rows.upserted", 2, "MOOX_STORAGE", "storage", func() proto.Message {
		return &storagepb.DatasetRowsUpserted{}
	}, validateDatasetRowsUpserted)
	DatasetPeriodCollected = declareEvent("event.storage.dataset.period.collected", 1, "MOOX_STORAGE", "storage", func() proto.Message {
		return &storagepb.DatasetPeriodCollected{}
	}, validateDatasetPeriodCollected)
	ViewSourcePeriodReady = declareEvent("event.storage.view.source_period.ready", 1, "MOOX_STORAGE", "storage", func() proto.Message {
		return &storagepb.ViewSourcePeriodReady{}
	}, validateViewSourcePeriodReady)
	FactorPeriodComputed = declareEvent("event.storage.dataset.factor_period.computed", 1, "MOOX_STORAGE", "storage", func() proto.Message {
		return &storagepb.FactorPeriodComputed{}
	}, validateFactorPeriodComputed)
	ViewFactorPeriodReady = declareEvent("event.storage.view.factor_period.ready", 1, "MOOX_STORAGE", "storage", func() proto.Message {
		return &storagepb.ViewFactorPeriodReady{}
	}, validateViewFactorPeriodReady)
	DatasetSyncPoint = declareEvent("event.storage.dataset.sync_point", 1, "MOOX_STORAGE", "storage", func() proto.Message {
		return &storagepb.DatasetSyncPoint{}
	}, validateDatasetSyncPoint)
	MarketFetchBatchCompleted = declareEvent("event.market.fetch.batch.completed", 1, "MOOX_MARKET_FETCH", "collector", func() proto.Message {
		return &marketfetchpb.MarketFetchBatchCompleted{}
	}, validateMarketFetchBatchCompleted)
	LogicalAccountTargetRequested = declareEvent("event.trade.target.requested", 1, "MOOX_TRADE", "strategy", func() proto.Message {
		return &tradeeventpb.LogicalAccountTargetRequested{}
	}, validateLogicalAccountTargetRequested)
	LogicalAccountTargetWeightRequested = declareEvent("event.trade.target.weight_requested", 1, "MOOX_TRADE", "strategy", func() proto.Message {
		return &tradeeventpb.LogicalAccountTargetWeightRequested{}
	}, validateLogicalAccountTargetWeightRequested)
)

type Registry struct {
	byKey map[string]Event
}

var defaultRegistry struct {
	once sync.Once
	reg  *Registry
	err  error
}

func DefaultRegistry() (*Registry, error) {
	defaultRegistry.once.Do(func() {
		defaultRegistry.reg, defaultRegistry.err = newRegistry(builtInEvents)
	})
	return defaultRegistry.reg, defaultRegistry.err
}

func newRegistry(events []Event) (*Registry, error) {
	r := &Registry{byKey: make(map[string]Event, len(events))}
	payloads := make(map[protoreflect.FullName]string, len(events))
	subjects := make(map[string]string, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.Name()) == "" || event.Version() == 0 || strings.TrimSpace(event.Stream()) == "" || strings.TrimSpace(event.Owner()) == "" || event.NewPayload() == nil || event.validate == nil {
			return nil, fmt.Errorf("event declaration %q is incomplete", eventKey(event))
		}
		if !strings.HasPrefix(event.Name(), "event.") {
			return nil, fmt.Errorf("event name %q must start with event.", event.Name())
		}
		key := eventKey(event)
		if _, exists := r.byKey[key]; exists {
			return nil, fmt.Errorf("duplicate event %s", key)
		}
		payloadName := event.PayloadFullName()
		if previous, exists := payloads[payloadName]; exists {
			return nil, fmt.Errorf("events %s and %s share payload %s", previous, key, payloadName)
		}
		family := eventFamily(event)
		if previous, exists := subjects[family]; exists {
			return nil, fmt.Errorf("events %s and %s share subject %s", previous, key, family)
		}
		r.byKey[key] = event
		payloads[payloadName] = key
		subjects[family] = key
	}
	return r, nil
}

func (r *Registry) Validate() error {
	if r == nil || len(r.byKey) == 0 {
		return fmt.Errorf("event registry is empty")
	}
	_, err := newRegistry(r.Events())
	return err
}

func (r *Registry) Lookup(name string, version uint32) (Event, bool) {
	if r == nil {
		return Event{}, false
	}
	event, ok := r.byKey[eventKeyParts(name, version)]
	return event, ok
}

func (r *Registry) Events() []Event {
	if r == nil {
		return nil
	}
	out := make([]Event, 0, len(r.byKey))
	for _, event := range r.byKey {
		out = append(out, event)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name() != out[j].Name() {
			return out[i].Name() < out[j].Name()
		}
		return out[i].Version() < out[j].Version()
	})
	return out
}

func (r *Registry) RenderSubject(event Event, spaceID, subjectID string) (string, error) {
	if _, ok := r.Lookup(event.Name(), event.Version()); !ok {
		return "", fmt.Errorf("event %s is not registered", eventKey(event))
	}
	template, err := NewSubjectTemplate(eventSubject(event))
	if err != nil {
		return "", err
	}
	return template.Render(spaceID, subjectID)
}

func (r *Registry) FamilyPattern(event Event) (string, error) {
	if _, ok := r.Lookup(event.Name(), event.Version()); !ok {
		return "", fmt.Errorf("event %s is not registered", eventKey(event))
	}
	return eventFamily(event), nil
}

// SpacePattern returns the subject family for exactly one space. It is useful
// for consumers which own all routes of an event type within one tenant, while
// keeping completions from other spaces out of their durable consumer.
func (r *Registry) SpacePattern(event Event, spaceID string) (string, error) {
	if _, ok := r.Lookup(event.Name(), event.Version()); !ok {
		return "", fmt.Errorf("event %s is not registered", eventKey(event))
	}
	spaceToken, err := jetstream.EncodeSubjectToken(spaceID)
	if err != nil {
		return "", fmt.Errorf("encode space_id: %w", err)
	}
	return strings.ReplaceAll(strings.ReplaceAll(eventSubject(event), "<space>", spaceToken), "<subject>", ">"), nil
}

func eventSubject(event Event) string {
	return fmt.Sprintf("moox.%s.v%d.<space>.<subject>", event.Name(), event.Version())
}

func eventFamily(event Event) string {
	return fmt.Sprintf("moox.%s.v%d.>", event.Name(), event.Version())
}

func eventKey(event Event) string {
	return eventKeyParts(event.Name(), event.Version())
}

func eventKeyParts(name string, version uint32) string {
	return fmt.Sprintf("%s@%d", name, version)
}

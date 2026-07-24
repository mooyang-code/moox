package events

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/dlqpb"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/events/tradingpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/strategyeventpb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gopkg.in/yaml.v3"
)

//go:embed registry/events.yaml
var defaultRegistryYAML embed.FS

type EventType struct {
	name    string
	version uint32
}

// Name returns the governed event name. EventType intentionally keeps its
// representation private so producers can only use the vocabulary declared by
// this package instead of manufacturing unregistered event types.
func (e EventType) Name() string { return e.name }

// Version returns the governed event schema version.
func (e EventType) Version() uint32 { return e.version }

var (
	TickReceived                 = EventType{name: "market.tick.received", version: 1}
	MarketKlineClosed            = EventType{name: "market.kline.closed", version: 1}
	TradingSignal                = EventType{name: "trading.signal", version: 1}
	DatasetRowsUpserted          = EventType{name: "storage.dataset.rows.upserted", version: 1}
	MetricsHostReported          = EventType{name: "metrics.host.reported", version: 1}
	MetricsSnapshotReported      = EventType{name: "metrics.snapshot.reported", version: 1}
	DLQMessageRejected           = EventType{name: "dlq.message.rejected", version: 1}
	StrategyOutputAccepted       = EventType{name: "strategy.output.accepted", version: 1}
	TradeOrderIntentCreated      = EventType{name: "trade.order.intent.created", version: 1}
	TradeOrderStateChanged       = EventType{name: "trade.order.state.changed", version: 1}
	TradeExecutionSliceReady     = EventType{name: "trade.execution.slice.ready", version: 1}
	TradeFillReceived            = EventType{name: "trade.fill.received", version: 1}
	TradeRebalanceRequested      = EventType{name: "trade.rebalance.requested", version: 1}
	TradeRebalanceCompleted      = EventType{name: "trade.rebalance.completed", version: 1}
	TradeReconciliationRequested = EventType{name: "trade.reconciliation.requested", version: 1}
	CloudJobExecutionRequested   = EventType{name: "cloudnode.job.execution.requested", version: 1}
	TradeOrderAcknowledged       = EventType{name: "trade.order.acknowledged", version: 1}
	TradeOrderSubmitUnknown      = EventType{name: "trade.order.submit.unknown", version: 1}
)

// AllEventTypes is the compile-time event vocabulary used by producers. The
// architecture gate verifies every entry remains present in the YAML registry
// so a new producer constant cannot silently bypass governed subjects.
var AllEventTypes = []EventType{
	TickReceived, MarketKlineClosed, TradingSignal, DatasetRowsUpserted,
	MetricsHostReported, MetricsSnapshotReported, DLQMessageRejected,
	StrategyOutputAccepted, TradeOrderIntentCreated, TradeOrderStateChanged,
	TradeExecutionSliceReady, TradeFillReceived, TradeRebalanceRequested,
	TradeRebalanceCompleted, TradeReconciliationRequested,
	CloudJobExecutionRequested, TradeOrderAcknowledged, TradeOrderSubmitUnknown,
}

var defaultRegistry struct {
	once sync.Once
	reg  *Registry
	err  error
}

type EventSchema struct {
	Name         string                `yaml:"name"`
	Version      uint32                `yaml:"version"`
	Payload      protoreflect.FullName `yaml:"payload"`
	Subject      string                `yaml:"subject"`
	Stream       string                `yaml:"stream"`
	PartitionKey string                `yaml:"partition_key"`
	Owner        string                `yaml:"owner"`
}

// EventTypeFromSchema creates the lookup key used by registry consumers when
// iterating the governed schema catalog. Producers should use the named
// vocabulary values above instead of constructing EventType dynamically.
func EventTypeFromSchema(schema EventSchema) EventType {
	return EventType{name: schema.Name, version: schema.Version}
}

type registryFile struct {
	Version uint32        `yaml:"version"`
	Events  []EventSchema `yaml:"events"`
}

type Registry struct {
	byKey    map[string]EventSchema
	payloads map[protoreflect.FullName]func() proto.Message
	subjects map[string]SubjectTemplate
}

func DefaultRegistry() (*Registry, error) {
	defaultRegistry.once.Do(func() {
		raw, err := defaultRegistryYAML.ReadFile("registry/events.yaml")
		if err != nil {
			defaultRegistry.err = fmt.Errorf("read embedded event registry: %w", err)
			return
		}
		defaultRegistry.reg, defaultRegistry.err = NewRegistry(raw)
	})
	return defaultRegistry.reg, defaultRegistry.err
}

func NewRegistry(raw []byte) (*Registry, error) {
	var file registryFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse event registry: %w", err)
	}
	if file.Version == 0 {
		return nil, fmt.Errorf("event registry version must be positive")
	}
	if len(file.Events) == 0 {
		return nil, fmt.Errorf("event registry must contain events")
	}
	r := &Registry{
		byKey:    make(map[string]EventSchema, len(file.Events)),
		payloads: payloadFactories(),
		subjects: make(map[string]SubjectTemplate, len(file.Events)),
	}
	for _, spec := range file.Events {
		if err := validateSchema(spec); err != nil {
			return nil, err
		}
		if _, ok := r.payloads[spec.Payload]; !ok {
			return nil, fmt.Errorf("event %q payload %q is not registered", spec.Name, spec.Payload)
		}
		key := eventKey(EventType{name: spec.Name, version: spec.Version})
		if _, ok := r.byKey[key]; ok {
			return nil, fmt.Errorf("duplicate event %s", key)
		}
		r.byKey[key] = spec
		template, err := NewSubjectTemplate(spec.Subject)
		if err != nil {
			return nil, fmt.Errorf("event %q subject: %w", spec.Name, err)
		}
		r.subjects[key] = template
	}
	return r, nil
}

// Schemas returns a stable snapshot of all registered event schemas.
// EventBus uses it to validate topology coverage without maintaining a second
// hard-coded list of governed events.
func (r *Registry) Schemas() []EventSchema {
	if r == nil {
		return nil
	}
	out := make([]EventSchema, 0, len(r.byKey))
	for _, spec := range r.byKey {
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func validateSchema(spec EventSchema) error {
	if strings.TrimSpace(spec.Name) == "" || strings.ContainsAny(spec.Name, " \t\r\n") {
		return fmt.Errorf("event name %q is invalid", spec.Name)
	}
	if spec.Version == 0 {
		return fmt.Errorf("event %q version must be positive", spec.Name)
	}
	if strings.TrimSpace(string(spec.Payload)) == "" {
		return fmt.Errorf("event %q payload is required", spec.Name)
	}
	if strings.TrimSpace(spec.Stream) == "" {
		return fmt.Errorf("event %q stream is required", spec.Name)
	}
	if strings.TrimSpace(spec.PartitionKey) == "" {
		return fmt.Errorf("event %q partition_key is required", spec.Name)
	}
	if strings.TrimSpace(spec.Owner) == "" {
		return fmt.Errorf("event %q owner is required", spec.Name)
	}
	if _, err := NewSubjectTemplate(spec.Subject); err != nil {
		return fmt.Errorf("event %q subject: %w", spec.Name, err)
	}
	return nil
}

func payloadFactories() map[protoreflect.FullName]func() proto.Message {
	return map[protoreflect.FullName]func() proto.Message{
		"trpc.moox.cloudjob.JobExecutionRequested":        func() proto.Message { return &cloudjobpb.JobExecutionRequested{} },
		"trpc.moox.market.Tick":                           func() proto.Message { return &marketpb.Tick{} },
		"trpc.moox.market.KlineClosed":                    func() proto.Message { return &marketpb.KlineClosed{} },
		"trpc.moox.trading.TradingSignal":                 func() proto.Message { return &tradingpb.TradingSignal{} },
		"trpc.moox.storage.event.DatasetRowsUpserted":     func() proto.Message { return &storagepb.DatasetRowsUpserted{} },
		"trpc.moox.hostagent.HostMetric":                  func() proto.Message { return &hostmetricpb.HostMetric{} },
		"trpc.moox.metrics.MetricReport":                  func() proto.Message { return &metricspb.MetricReport{} },
		"trpc.moox.dlq.RejectedMessage":                   func() proto.Message { return &dlqpb.RejectedMessage{} },
		"trpc.moox.strategy.event.StrategyOutputAccepted": func() proto.Message { return &strategyeventpb.StrategyOutputAccepted{} },
		"trpc.moox.trade.event.OrderSnapshot":             func() proto.Message { return &tradeeventpb.OrderSnapshot{} },
		"trpc.moox.trade.event.FillReceived":              func() proto.Message { return &tradeeventpb.FillReceived{} },
		"trpc.moox.trade.event.ReconciliationRequested":   func() proto.Message { return &tradeeventpb.ReconciliationRequested{} },
		"trpc.moox.trade.event.RebalanceRequested":        func() proto.Message { return &tradeeventpb.RebalanceRequested{} },
		"trpc.moox.trade.event.RebalanceCompleted":        func() proto.Message { return &tradeeventpb.RebalanceCompleted{} },
	}
}

func (r *Registry) Schema(event EventType) (EventSchema, bool) {
	if r == nil {
		return EventSchema{}, false
	}
	spec, ok := r.byKey[eventKey(event)]
	return spec, ok
}

func (r *Registry) PayloadFactory(name protoreflect.FullName) (func() proto.Message, bool) {
	if r == nil {
		return nil, false
	}
	factory, ok := r.payloads[name]
	return factory, ok
}

// RenderSubject renders a registered event's subject using the cached
// validated template.
func (r *Registry) RenderSubject(event EventType, spaceID, subjectID string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("event registry is nil")
	}
	template, ok := r.subjects[eventKey(event)]
	if !ok {
		return "", fmt.Errorf("event %s is not registered", eventKey(event))
	}
	return template.Render(spaceID, subjectID)
}

// FamilyPattern returns the governed wildcard subject for an event. Keeping
// this derivation with the validated subject template prevents topology code
// from having to duplicate placeholder parsing rules.
func (r *Registry) FamilyPattern(event EventType) (string, error) {
	if r == nil {
		return "", fmt.Errorf("event registry is nil")
	}
	template, ok := r.subjects[eventKey(event)]
	if !ok {
		return "", fmt.Errorf("event %s is not registered", eventKey(event))
	}
	return template.FamilyPattern(), nil
}

func (r *Registry) Validate() error {
	if r == nil || len(r.byKey) == 0 {
		return fmt.Errorf("event registry is empty")
	}
	return nil
}

func eventKey(event EventType) string {
	return fmt.Sprintf("%s@%d", strings.TrimSpace(event.name), event.version)
}

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
	Name    string
	Version uint32
}

var (
	TickReceived                 = EventType{Name: "market.tick.received", Version: 1}
	MarketKlineClosed            = EventType{Name: "market.kline.closed", Version: 1}
	TradingSignal                = EventType{Name: "trading.signal", Version: 1}
	DatasetRowsUpserted          = EventType{Name: "storage.dataset.rows.upserted", Version: 1}
	MetricsHostReported          = EventType{Name: "metrics.host.reported", Version: 1}
	MetricsSnapshotReported      = EventType{Name: "metrics.snapshot.reported", Version: 1}
	DLQMessageRejected           = EventType{Name: "dlq.message.rejected", Version: 1}
	StrategyOutputAccepted       = EventType{Name: "strategy.output.accepted", Version: 1}
	TradeOrderIntentCreated      = EventType{Name: "trade.order.intent.created", Version: 1}
	TradeOrderStateChanged       = EventType{Name: "trade.order.state.changed", Version: 1}
	TradeExecutionSliceReady     = EventType{Name: "trade.execution.slice.ready", Version: 1}
	TradeFillReceived            = EventType{Name: "trade.fill.received", Version: 1}
	TradeRebalanceRequested      = EventType{Name: "trade.rebalance.requested", Version: 1}
	TradeRebalanceCompleted      = EventType{Name: "trade.rebalance.completed", Version: 1}
	TradeReconciliationRequested = EventType{Name: "trade.reconciliation.requested", Version: 1}
	CloudJobExecutionRequested   = EventType{Name: "cloudnode.job.execution.requested", Version: 1}
	TradeOrderAcknowledged       = EventType{Name: "trade.order.acknowledged", Version: 1}
	TradeOrderSubmitUnknown      = EventType{Name: "trade.order.submit.unknown", Version: 1}
)

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
		key := eventKey(EventType{Name: spec.Name, Version: spec.Version})
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
	return fmt.Sprintf("%s@%d", strings.TrimSpace(event.Name), event.Version)
}

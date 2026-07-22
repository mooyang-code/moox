package events

import (
	"embed"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/events/storagepb"
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
	MarketTradeReceived = EventType{Name: "market.trade.received", Version: 1}
	MarketKlineClosed   = EventType{Name: "market.kline.closed", Version: 1}
	StorageRowsUpserted = EventType{Name: "storage.rows.upserted", Version: 1}
)

type EventSpec struct {
	Name         string                `yaml:"name"`
	Version      uint32                `yaml:"version"`
	Payload      protoreflect.FullName `yaml:"payload"`
	Subject      string                `yaml:"subject"`
	Stream       string                `yaml:"stream"`
	PartitionKey string                `yaml:"partition_key"`
	Owner        string                `yaml:"owner"`
}

type registryFile struct {
	Version uint32      `yaml:"version"`
	Events  []EventSpec `yaml:"events"`
}

type Registry struct {
	byKey     map[string]EventSpec
	payloads  map[protoreflect.FullName]func() proto.Message
	byPayload map[protoreflect.FullName]EventSpec
}

func DefaultRegistry() (*Registry, error) {
	raw, err := defaultRegistryYAML.ReadFile("registry/events.yaml")
	if err != nil {
		return nil, fmt.Errorf("read embedded event registry: %w", err)
	}
	return NewRegistry(raw)
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
		byKey:     make(map[string]EventSpec, len(file.Events)),
		payloads:  payloadFactories(),
		byPayload: make(map[protoreflect.FullName]EventSpec, len(file.Events)),
	}
	for _, spec := range file.Events {
		if err := validateSpec(spec); err != nil {
			return nil, err
		}
		if _, ok := r.payloads[spec.Payload]; !ok {
			return nil, fmt.Errorf("event %q payload %q is not registered", spec.Name, spec.Payload)
		}
		key := eventKey(EventType{Name: spec.Name, Version: spec.Version})
		if _, ok := r.byKey[key]; ok {
			return nil, fmt.Errorf("duplicate event %s", key)
		}
		if existing, ok := r.byPayload[spec.Payload]; ok && existing.Name != spec.Name {
			return nil, fmt.Errorf("payload %q is assigned to both %q and %q", spec.Payload, existing.Name, spec.Name)
		}
		r.byKey[key] = spec
		r.byPayload[spec.Payload] = spec
	}
	return r, nil
}

func validateSpec(spec EventSpec) error {
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
		"trpc.moox.market.TradeReceived": func() proto.Message { return &marketpb.TradeReceived{} },
		"trpc.moox.market.KlineClosed":   func() proto.Message { return &marketpb.KlineClosed{} },
		"trpc.moox.event.storage.RowsUpserted": func() proto.Message { return &storagepb.RowsUpserted{} },
	}
}

func (r *Registry) Spec(event EventType) (EventSpec, bool) {
	if r == nil {
		return EventSpec{}, false
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

func (r *Registry) EventForPayload(name protoreflect.FullName) (EventSpec, bool) {
	if r == nil {
		return EventSpec{}, false
	}
	spec, ok := r.byPayload[name]
	return spec, ok
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

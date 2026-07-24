package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
	"github.com/mooyang-code/moox/modules/streamcalc/internal/config"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var ErrLateData = errors.New("late event is outside the allowed lateness")

type Writer interface {
	Write(context.Context, aggregate.Bar) error
}

type EventPublisher interface {
	Publish(context.Context, events.EventType, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error)
}

func (p *Processor) Snapshot() aggregate.Snapshot {
	if p == nil || p.aggregator == nil {
		return aggregate.Snapshot{}
	}
	snapshot := p.aggregator.Export()
	p.mu.Lock()
	if len(p.pending) > 0 {
		snapshot.Pending = make(map[string]aggregate.Bar, len(p.pending))
		for id, bar := range p.pending {
			snapshot.Pending[id] = bar
		}
	}
	p.mu.Unlock()
	return snapshot
}

func (p *Processor) Restore(snapshot aggregate.Snapshot) error {
	if p == nil || p.aggregator == nil {
		return fmt.Errorf("processor is not initialized")
	}
	if err := p.aggregator.Restore(snapshot); err != nil {
		return err
	}
	p.mu.Lock()
	p.pending = make(map[string]aggregate.Bar, len(snapshot.Pending))
	for id, bar := range snapshot.Pending {
		p.pending[id] = bar
	}
	p.mu.Unlock()
	return nil
}

type Processor struct {
	aggregator *aggregate.Aggregator
	writer     Writer
	publisher  EventPublisher
	mu         sync.Mutex
	pending    map[string]aggregate.Bar
}

func NewEventProcessor(aggregator *aggregate.Aggregator, publisher EventPublisher) (*Processor, error) {
	if aggregator == nil {
		return nil, fmt.Errorf("aggregator is nil")
	}
	if publisher == nil {
		return nil, fmt.Errorf("event publisher is nil")
	}
	return &Processor{aggregator: aggregator, publisher: publisher, pending: make(map[string]aggregate.Bar)}, nil
}

func NewProcessor(aggregator *aggregate.Aggregator, writer Writer) (*Processor, error) {
	if aggregator == nil {
		return nil, fmt.Errorf("aggregator is nil")
	}
	return &Processor{aggregator: aggregator, writer: writer, pending: make(map[string]aggregate.Bar)}, nil
}

func (p *Processor) Process(ctx context.Context, delivery *events.EventDelivery) error {
	if delivery == nil || delivery.Delivery == nil {
		return fmt.Errorf("event delivery is nil")
	}
	if delivery.Err != nil {
		return delivery.Err
	}
	if delivery.Message == nil {
		return fmt.Errorf("event message is nil")
	}
	if delivery.Message.GetEventName() != events.TickReceived.Name || delivery.Message.GetEventVersion() != events.TickReceived.Version {
		return fmt.Errorf("unexpected streamcalc input event %q@%d", delivery.Message.GetEventName(), delivery.Message.GetEventVersion())
	}
	eventID := delivery.Message.GetEventId()
	var result aggregate.Result
	var err error
	switch input := delivery.Payload.(type) {
	case *marketpb.Tick:
		if delivery.Message.GetSubjectId() != input.GetSymbol() {
			return fmt.Errorf("tick subject_id %q does not match payload symbol %q", delivery.Message.GetSubjectId(), input.GetSymbol())
		}
		result, err = p.aggregator.ApplyTick(eventID, delivery.Message.GetSpaceId(), input)
	default:
		return fmt.Errorf("unexpected streamcalc payload %T", delivery.Payload)
	}
	if p.writer != nil || p.publisher != nil {
		p.mu.Lock()
		pending, ok := p.pending[eventID]
		p.mu.Unlock()
		if ok {
			if err := p.writeClosed(ctx, pending, eventID); err != nil {
				return err
			}
			p.mu.Lock()
			delete(p.pending, eventID)
			p.mu.Unlock()
			return nil
		}
	}
	if err != nil {
		return err
	}
	if result.Duplicate {
		return nil
	}
	if result.Late {
		return ErrLateData
	}
	if !result.Bar.Closed || (p.writer == nil && p.publisher == nil) {
		return nil
	}
	if err := p.writeClosed(ctx, result.Bar, eventID); err != nil {
		p.mu.Lock()
		p.pending[eventID] = result.Bar
		p.mu.Unlock()
		return err
	}
	return nil
}

func (p *Processor) writeClosed(ctx context.Context, bar aggregate.Bar, inputEventID string) error {
	if p.writer != nil {
		return p.writer.Write(ctx, bar)
	}
	if p.publisher == nil {
		return fmt.Errorf("streamcalc output publisher is unavailable")
	}
	targetFrequency, err := config.ParseFrequency(bar.Key.Frequency)
	if err != nil {
		return fmt.Errorf("parse aggregate frequency: %w", err)
	}
	windowEnd := bar.Key.Start.Add(targetFrequency).UTC()
	payload := &marketpb.KlineClosed{Symbol: bar.Key.Subject, Frequency: bar.Key.Frequency, WindowStart: timestamppb.New(bar.Key.Start), WindowEnd: timestamppb.New(windowEnd), Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, QuoteVolume: bar.QuoteVolume, TradeCount: bar.TradeCount, Revision: bar.Revision}
	spaceID := bar.Key.SpaceID
	if spaceID == "" {
		spaceID = "moox_system"
	}
	_, err = p.publisher.Publish(ctx, events.MarketKlineClosed, payload, events.PublishOptions{EventID: inputEventID + ":kline:" + bar.Key.Subject + ":" + bar.Key.Start.UTC().Format(time.RFC3339Nano), OccurredAt: windowEnd.UTC(), SpaceID: spaceID, SubjectID: bar.Key.Subject})
	return err
}

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
)

var ErrLateData = errors.New("late event is outside the allowed lateness")

type Writer interface {
	Write(context.Context, aggregate.Bar) error
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
	mu         sync.Mutex
	pending    map[string]aggregate.Bar
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
	eventID := delivery.Message.GetEventId()
	var result aggregate.Result
	var err error
	switch input := delivery.Payload.(type) {
	case *marketpb.KlineClosed:
		if delivery.Message.GetSubjectId() != input.GetSymbol() {
			return fmt.Errorf("kline subject_id %q does not match payload symbol %q", delivery.Message.GetSubjectId(), input.GetSymbol())
		}
		result, err = p.aggregator.Apply(eventID, delivery.Message.GetSpaceId(), input)
	case *marketpb.Tick:
		if delivery.Message.GetSubjectId() != input.GetSymbol() {
			return fmt.Errorf("tick subject_id %q does not match payload symbol %q", delivery.Message.GetSubjectId(), input.GetSymbol())
		}
		result, err = p.aggregator.ApplyTick(eventID, delivery.Message.GetSpaceId(), input)
	default:
		return fmt.Errorf("unexpected streamcalc payload %T", delivery.Payload)
	}
	if p.writer != nil {
		p.mu.Lock()
		pending, ok := p.pending[eventID]
		p.mu.Unlock()
		if ok {
			if err := p.writer.Write(ctx, pending); err != nil {
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
	if !result.Bar.Closed || p.writer == nil {
		return nil
	}
	if err := p.writer.Write(ctx, result.Bar); err != nil {
		p.mu.Lock()
		p.pending[eventID] = result.Bar
		p.mu.Unlock()
		return err
	}
	return nil
}

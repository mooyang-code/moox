package eventconsumer

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
)

// HandleViewPeriodReady accepts either the source View readiness event used by
// OHLCV-only strategies or the Factor result View readiness event used by
// factor-backed strategies. The envelope is decoded once so the consumer can
// share one durable delivery path for both trigger forms.
func HandleViewPeriodReady(ctx context.Context, delivery *jetstream.Delivery, processor *trigger.Processor) jetstream.HandlerResult {
	if delivery == nil || processor == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Err: err}
	}
	contentType := delivery.ContentType
	if contentType == "" {
		contentType = events.ContentType
	}
	message, payload, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, contentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	switch typed := payload.(type) {
	case *storagepb.ViewFactorPeriodReady:
		return processFactorPeriodReady(ctx, message, typed, processor)
	case *storagepb.ViewSourcePeriodReady:
		return processSourcePeriodReady(ctx, message, typed, processor)
	default:
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("unsupported storage readiness payload %T", payload)}
	}
}

// HandleViewFactorPeriodReady is retained for callers and tests that consume
// only Factor result events. New consumers should use HandleViewPeriodReady.
func HandleViewFactorPeriodReady(ctx context.Context, delivery *jetstream.Delivery, processor *trigger.Processor) jetstream.HandlerResult {
	if delivery == nil || processor == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Err: err}
	}
	message, payload, err := events.DecodeViewFactorPeriodReady(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	return processFactorPeriodReady(ctx, message, payload, processor)
}

func processFactorPeriodReady(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.ViewFactorPeriodReady, processor *trigger.Processor) jetstream.HandlerResult {
	bindingStatuses := make(map[string]string, len(payload.GetBindings()))
	bindingStates := make(map[string]trigger.BindingPeriodState, len(payload.GetBindings()))
	for _, binding := range payload.GetBindings() {
		if binding != nil && binding.GetBindingId() != "" {
			bindingStatuses[binding.GetBindingId()] = binding.GetStatus()
			bindingStates[binding.GetBindingId()] = trigger.BindingPeriodState{
				Status:          binding.GetStatus(),
				SkippedSubjects: append([]string(nil), binding.GetSkippedSubjects()...),
				FailedSubjects:  append([]string(nil), binding.GetFailedSubjects()...),
				SourceHash:      binding.GetSourceHash(),
			}
		}
	}
	storagePeriod := periodTime(payload.GetPeriodTime())
	period, periodErr := input.FromStorageStart("crypto_24x7", payload.GetFrequency(), storagePeriod)
	if periodErr != nil {
		// The event payload does not carry a calendar. Strategy instances may
		// use cn_stock, so preserve the Storage timestamp here and let the
		// instance-specific processor normalize it from its DSL.
		period = input.PeriodBoundaries{StorageStart: storagePeriod, BarEnd: storagePeriod, PreviousStart: storagePeriod}
	}
	return processPeriod(ctx, processor, trigger.PeriodReady{MessageID: message.GetEventId(), EventName: message.GetEventName(), SpaceID: message.GetSpaceId(), ViewID: payload.GetResultViewId(), SourceViewID: payload.GetSourceViewId(), Frequency: payload.GetFrequency(), PeriodTime: storagePeriod, StoragePeriodTime: storagePeriod, BarEndTime: period.BarEnd, Status: payload.GetStatus(), ReadyViewIDs: []string{payload.GetResultViewId()}, SourceIndexID: payload.GetSourceIndexId(), ResultIndexID: payload.GetResultIndexId(), SourceIndexRevision: payload.GetSourceIndexRevision(), ResultIndexRevision: payload.GetResultIndexRevision(), BindingStatuses: bindingStatuses, BindingStates: bindingStates})
}

func processSourcePeriodReady(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.ViewSourcePeriodReady, processor *trigger.Processor) jetstream.HandlerResult {
	storagePeriod := periodTime(payload.GetPeriodTime())
	period, periodErr := input.FromStorageStart("crypto_24x7", payload.GetFrequency(), storagePeriod)
	if periodErr != nil {
		period = input.PeriodBoundaries{StorageStart: storagePeriod, BarEnd: storagePeriod, PreviousStart: storagePeriod}
	}
	return processPeriod(ctx, processor, trigger.PeriodReady{MessageID: message.GetEventId(), EventName: message.GetEventName(), SpaceID: message.GetSpaceId(), ViewID: payload.GetSourceViewId(), SourceViewID: payload.GetSourceViewId(), Frequency: payload.GetFrequency(), PeriodTime: storagePeriod, StoragePeriodTime: storagePeriod, BarEndTime: period.BarEnd, Status: payload.GetStatus(), ReadyViewIDs: []string{payload.GetSourceViewId()}, SourceIndexID: payload.GetActiveIndexId(), SourceIndexRevision: payload.GetActiveIndexRevision()})
}

func processPeriod(ctx context.Context, processor *trigger.Processor, period trigger.PeriodReady) jetstream.HandlerResult {
	if processErr := processor.Handle(ctx, period); processErr != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: processErr}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func periodTime(unixSeconds int64) time.Time {
	return time.Unix(unixSeconds, 0).UTC()
}

package eventconsumer

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
)

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
	message, payload, err := events.DecodeViewDataReadyWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, contentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	return processViewDataReady(ctx, message, payload, processor)
}

func processViewDataReady(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.ViewDataReady, processor *trigger.Processor) jetstream.HandlerResult {
	storagePeriod := periodTime(payload.GetPeriodTime())
	period, periodErr := input.FromStorageStart("crypto_24x7", payload.GetFrequency(), storagePeriod)
	if periodErr != nil {
		period = input.PeriodBoundaries{StorageStart: storagePeriod, BarEnd: storagePeriod, PreviousStart: storagePeriod}
	}
	statuses, states := periodReadyBindings(payload)
	return processPeriod(ctx, processor, trigger.PeriodReady{
		MessageID: message.GetEventId(), EventName: message.GetEventName(), SpaceID: message.GetSpaceId(),
		ViewID: payload.GetViewId(), SourceViewID: payload.GetViewId(), Frequency: payload.GetFrequency(),
		PeriodTime: storagePeriod, StoragePeriodTime: storagePeriod, BarEndTime: period.BarEnd,
		Status: payload.GetStatus(), ReadyViewIDs: []string{payload.GetViewId()},
		CompletionKind: payload.GetCompletionKind(),
		BindingStatuses: statuses, BindingStates: states,
	})
}

func periodReadyBindings(payload *storagepb.ViewDataReady) (map[string]string, map[string]trigger.BindingPeriodState) {
	if payload == nil || len(payload.GetBindings()) == 0 {
		return nil, nil
	}
	statuses := make(map[string]string, len(payload.GetBindings()))
	states := make(map[string]trigger.BindingPeriodState, len(payload.GetBindings()))
	for _, binding := range payload.GetBindings() {
		if binding == nil {
			continue
		}
		id := strings.TrimSpace(binding.GetBindingId())
		if id == "" {
			continue
		}
		statuses[id] = binding.GetStatus()
		states[id] = trigger.BindingPeriodState{
			Status:          binding.GetStatus(),
			SkippedSubjects: append([]string(nil), binding.GetSkippedSubjects()...),
			FailedSubjects:  append([]string(nil), binding.GetFailedSubjects()...),
			SourceHash:      binding.GetSourceHash(),
		}
	}
	if len(statuses) == 0 {
		return nil, nil
	}
	return statuses, states
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

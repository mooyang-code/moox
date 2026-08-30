package eventconsumer

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// HandleViewFactorPeriodReady decodes one Storage readiness event and hands it
// to the stateless trigger processor. The inbox remains the processor's
// responsibility, so redelivery is harmless.
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
	if processErr := processor.Handle(ctx, trigger.PeriodReady{MessageID: message.GetEventId(), EventName: message.GetEventName(), SpaceID: message.GetSpaceId(), ViewID: payload.GetResultViewId(), Frequency: payload.GetFrequency(), PeriodTime: periodTime(payload.GetPeriodTime()), Status: payload.GetStatus(), ReadyViewIDs: []string{payload.GetResultViewId()}, SourceIndexID: payload.GetSourceIndexId(), ResultIndexID: payload.GetResultIndexId(), SourceIndexRevision: payload.GetSourceIndexRevision(), ResultIndexRevision: payload.GetResultIndexRevision(), BindingStatuses: bindingStatuses, BindingStates: bindingStates}); processErr != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: processErr}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func periodTime(unixSeconds int64) time.Time {
	return time.Unix(unixSeconds, 0).UTC()
}

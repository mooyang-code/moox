package stockcn

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRouteTimerEventMapsDedicatedInstrumentTimer(t *testing.T) {
	event := model.CloudFunctionEvent{Type: "Timer", TriggerName: timerTriggerName, Message: timerTriggerMessage, Time: "2026-08-30T00:00:00+08:00"}

	err := routeTimerEvent(&event, false, true)

	require.NoError(t, err)
	require.Equal(t, model.EventActionInstrumentSnapshot, event.Action)
	require.Equal(t, "tencent_timer", event.Source)
}

func TestRouteTimerEventRejectsAmbiguousTimerConfiguration(t *testing.T) {
	event := model.CloudFunctionEvent{Type: "Timer", TriggerName: timerTriggerName, Message: timerTriggerMessage, Time: "2026-08-30T00:00:00+08:00"}

	err := routeTimerEvent(&event, true, true)

	require.ErrorContains(t, err, "both market and instrument")
}

package hostmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
)

func TestEvaluateNilInputsShouldNoop(t *testing.T) {
	var evaluator *AlertEvaluator
	assert.NoError(t, evaluator.Evaluate(context.Background(), "agent-1", "msg-1", &hostmetricpb.HostSnapshot{}, time.Now()))

	evaluator = &AlertEvaluator{}
	assert.NoError(t, evaluator.Evaluate(context.Background(), "agent-1", "msg-1", nil, time.Now()))
}

func TestPositiveDefaultsToOne(t *testing.T) {
	assert.Equal(t, 1, positive(0))
	assert.Equal(t, 3, positive(3))
}

func TestDeterministicEventIDStable(t *testing.T) {
	first := deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered)
	second := deterministicEventID("msg-1", "rule-1", domain.AlertEventTriggered)
	assert.Equal(t, first, second)
	assert.NotEqual(t, first, deterministicEventID("msg-2", "rule-1", domain.AlertEventTriggered))
}

func TestHostThresholdsUsesNetworkDefault(t *testing.T) {
	threshold, recovery := hostThresholds(domain.AlertRule{CheckID: HostMetricNetworkErrors}, HostMetricNetworkErrors)
	assert.Equal(t, 1.0, threshold)
	assert.Equal(t, 1.0, recovery)
}

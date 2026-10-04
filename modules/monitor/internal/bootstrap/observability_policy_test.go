package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestObservabilityConsumerConfigUsesValidatedPolicy(t *testing.T) {
	t.Setenv("MOOX_OBSERVABILITY_DELIVER_POLICY", "new")
	for _, tc := range []struct {
		policy string
		want   nats.DeliverPolicy
	}{{"all", nats.DeliverAllPolicy}, {"new", nats.DeliverNewPolicy}} {
		t.Run(tc.policy, func(t *testing.T) {
			cfg, err := observabilityConsumerConfig(config.ObservabilityConfig{DeliverPolicy: tc.policy})
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.DeliverPolicy)
		})
	}
	_, err := observabilityConsumerConfig(config.ObservabilityConfig{DeliverPolicy: "neww"})
	require.ErrorContains(t, err, "deliver_policy")
}

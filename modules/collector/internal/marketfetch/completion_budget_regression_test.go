package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

type stalledCompletionClient struct{ stall bool }

func (c stalledCompletionClient) PublishRaw(ctx context.Context, _, _ string, _ []byte, _ string) (*jetstream.PublishAck, error) {
	if c.stall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &jetstream.PublishAck{}, nil
}
func (c stalledCompletionClient) Close() error { return nil }

func TestCompletionFirstACKCannotConsumeRetryBudget(t *testing.T) {
	req := validClaimedTimerRequest()
	req.ScheduleID, req.NodeID = "schedule-1", "node-1"
	payload := buildCompletion(req, []domain.ItemResult{successResult(req.Items[0])}, time.Now().UTC(), time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	attempts := 0
	connect := func(ctx context.Context, cfg jetstream.Config) (completionClient, error) {
		attempts++
		require.NoError(t, ctx.Err())
		require.Equal(t, completionConnectTimeout, cfg.ConnectTimeout)
		return stalledCompletionClient{stall: attempts == 1}, nil
	}
	require.NoError(t, publishCompletionWithClient(ctx, req, payload, connect, 50*time.Millisecond, 10*time.Millisecond))
	require.Equal(t, 2, attempts)
	require.NoError(t, ctx.Err(), "the first stalled ACK leaves time for retry and response")
}

func TestPublishCompletionPrefersPackagedCAFile(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_NATS_TLS_CA_FILE", "/var/task/certs/eventbus-ca.pem")
	t.Setenv("MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64", "c3RhbGUtY2E=")
	req := validClaimedTimerRequest()
	req.ScheduleID, req.NodeID = "schedule-1", "node-1"
	payload := buildCompletion(req, []domain.ItemResult{successResult(req.Items[0])}, time.Now().UTC(), time.Millisecond)

	var got jetstream.Config
	connect := func(_ context.Context, cfg jetstream.Config) (completionClient, error) {
		got = cfg
		return stalledCompletionClient{}, nil
	}
	require.NoError(t, publishCompletionWithClient(context.Background(), req, payload, connect, time.Second, 0))
	require.Equal(t, "/var/task/certs/eventbus-ca.pem", got.TLSCAFile)
	require.Empty(t, got.TLSCAPEMBase64)
}

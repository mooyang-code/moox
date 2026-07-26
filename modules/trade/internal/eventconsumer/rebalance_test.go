package eventconsumer

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/assert"
)

func TestHandleRebalanceTermsInvalidDelivery(t *testing.T) {
	result := HandleRebalance(context.Background(), nil, RebalanceOptions{})
	assert.Equal(t, jetstream.TERM, result.Decision)
	assert.ErrorIs(t, result.Err, jetstream.ErrInvalidDelivery)
}

func TestHandleRebalanceTermsMalformedEnvelope(t *testing.T) {
	result := HandleRebalance(context.Background(), &jetstream.Delivery{
		RawData: []byte("malformed"), ContentType: "application/protobuf",
	}, RebalanceOptions{})
	assert.Equal(t, jetstream.TERM, result.Decision)
	assert.Error(t, result.Err)
}

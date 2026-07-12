package registry

import (
	"testing"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
)

func TestParseAckDeliverReplayPolicies(t *testing.T) {
	assert.Equal(t, nats.AckNonePolicy, parseAckPolicy("none"))
	assert.Equal(t, nats.AckAllPolicy, parseAckPolicy("all"))
	assert.Equal(t, nats.AckExplicitPolicy, parseAckPolicy("explicit"))
	assert.Equal(t, nats.DeliverNewPolicy, parseDeliverPolicy("new"))
	assert.Equal(t, nats.DeliverAllPolicy, parseDeliverPolicy("all"))
	assert.Equal(t, nats.ReplayOriginalPolicy, parseReplayPolicy("original"))
	assert.Equal(t, nats.ReplayInstantPolicy, parseReplayPolicy("instant"))
}

func TestEnabledTopics(t *testing.T) {
	cfg := config.Default()
	assert.Greater(t, enabledTopics(cfg), 0)
}

func TestSubjectRemoved(t *testing.T) {
	assert.True(t, subjectRemoved([]string{"a", "b"}, []string{"a"}))
	assert.False(t, subjectRemoved([]string{"a"}, []string{"a", "b"}))
}

func TestSameStrings(t *testing.T) {
	assert.True(t, sameStrings([]string{"a", "b"}, []string{"a", "b"}))
	assert.False(t, sameStrings([]string{"a"}, []string{"b"}))
}

func TestSubjectPatternsOverlapVariants(t *testing.T) {
	assert.True(t, subjectPatternsOverlap("a.>", "a.b.c"))
	assert.False(t, subjectPatternsOverlap("a.b", "a.c"))
}

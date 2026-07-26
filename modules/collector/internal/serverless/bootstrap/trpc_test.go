package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTRPCTimerServiceNames(t *testing.T) {
	assert.Equal(t, []string{
		"trpc.heartbeat.timer",
		"trpc.dnsresolve.timer",
	}, trpcTimerServiceNames())
}

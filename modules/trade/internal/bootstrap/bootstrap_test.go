package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKernelEventBusReady_NoClient_ShouldReturnFalse(t *testing.T) {
	setKernelEventBusClient(nil)
	assert.False(t, kernelEventBusReady())
}

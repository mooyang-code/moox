package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegisterMetricsReporter_NilServer_ShouldNoPanic(t *testing.T) {
	require.NotPanics(t, func() {
		registerMetricsReporter(nil)
	})
}

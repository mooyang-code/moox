package report

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigWithDefaults_FillsMissingFields(t *testing.T) {
	got := (Config{ServiceName: "trade", Interval: 0}).withDefaults()
	assert.Equal(t, DefaultTopic, got.Topic)
	assert.Equal(t, DefaultSpace, got.SpaceID)
	assert.Equal(t, DefaultBusURL, got.EventBusURL)
	assert.Equal(t, 30*time.Second, got.Interval)
	assert.Equal(t, `^.*$`, got.IncludeRegex)
	assert.NotEmpty(t, got.InstanceID)
}

func TestNewHandler_InvalidRegex_ShouldError(t *testing.T) {
	_, err := NewHandler(Config{ServiceName: "trade", IncludeRegex: "("})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "include regex")

	_, err = NewHandler(Config{ServiceName: "trade", ExcludeRegex: "("})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exclude regex")
}

func TestNewHandler_EmptyServiceName_ShouldError(t *testing.T) {
	_, err := NewHandler(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "service name is required")
}

func TestNewID_ShouldReturnNonEmptyBootID(t *testing.T) {
	assert.NotEmpty(t, newID())
	assert.NotEqual(t, newID(), newID())
}

func TestDefaultConfig_TradeService_ShouldUseCentralDefaults(t *testing.T) {
	cfg := DefaultConfig("moox-trade")
	assert.Equal(t, DefaultTopic, cfg.Topic)
	assert.Equal(t, DefaultSpace, cfg.SpaceID)
	assert.Equal(t, "dev", cfg.Version)
	assert.Equal(t, 4*1024*1024, cfg.MaxUncompressedBytes)
}

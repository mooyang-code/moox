package algorithm

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/algorithm/split"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_NewRegistry_ShouldReturnEmptyRegistry(t *testing.T) {
	r := NewRegistry()
	require.NotNil(t, r)
	_, err := r.Split(execution.AlgorithmDescriptor{Name: "single", Version: "1"})
	assert.ErrorIs(t, err, ErrUnknownAlgorithm)
}

func TestRegistry_RegisterSplit_ShouldRegisterAlgorithm(t *testing.T) {
	r := NewRegistry()
	r.RegisterSplit(split.Single{})

	got, err := r.Split(execution.AlgorithmDescriptor{Name: "single", Version: "1"})
	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestRegistry_Split_KnownVersion_ShouldReturnAlgorithm(t *testing.T) {
	r := NewRegistry()
	r.RegisterSplit(split.FixedNotional{})

	got, err := r.Split(execution.AlgorithmDescriptor{Name: "fixed_notional", Version: "1"})
	require.NoError(t, err)
	assert.Equal(t, "fixed_notional", got.Descriptor().Name)
	assert.Equal(t, "1", got.Descriptor().Version)
}

func TestRegistry_Split_UnknownVersion_ShouldReturnError(t *testing.T) {
	r := NewRegistry()
	r.RegisterSplit(split.FixedNotional{})

	_, err := r.Split(execution.AlgorithmDescriptor{Name: "fixed_notional", Version: "2"})
	assert.ErrorIs(t, err, ErrUnknownAlgorithm)
}

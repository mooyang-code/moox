package sources

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCollector struct {
	source, dataType string
}

func (f *fakeCollector) Source() string                                { return f.source }
func (f *fakeCollector) DataType() string                              { return f.dataType }
func (f *fakeCollector) Collect(context.Context, *CollectParams) error { return nil }

func TestCollectorRegistry_RegisterAndGet_ShouldReturnCollector(t *testing.T) {
	r := &CollectorRegistry{collectors: make(map[string]*CollectorDescriptor)}
	desc := &CollectorDescriptor{
		Source:    "test",
		Market:    "spot",
		DataType:  "kline",
		Collector: &fakeCollector{source: "test", dataType: "kline"},
	}
	require.NoError(t, r.Register(desc))

	collector, err := r.Get("test", "spot", "kline")
	require.NoError(t, err)
	assert.Equal(t, "test", collector.Source())
}

func TestCollectorRegistry_RegisterDuplicate_ShouldReturnError(t *testing.T) {
	r := &CollectorRegistry{collectors: make(map[string]*CollectorDescriptor)}
	desc := &CollectorDescriptor{Source: "dup", Market: "spot", DataType: "kline", Collector: &fakeCollector{}}
	require.NoError(t, r.Register(desc))
	err := r.Register(desc)
	assert.Error(t, err)
}

func TestCollectorRegistry_Get_UnknownCollector_ShouldReturnError(t *testing.T) {
	r := &CollectorRegistry{collectors: make(map[string]*CollectorDescriptor)}
	_, err := r.Get("missing", "spot", "kline")
	assert.Error(t, err)
}

func TestCollectorRegistry_GetDataTypes_ShouldDeduplicate(t *testing.T) {
	r := &CollectorRegistry{collectors: make(map[string]*CollectorDescriptor)}
	require.NoError(t, r.Register(&CollectorDescriptor{Source: "a", Market: "spot", DataType: "kline", Collector: &fakeCollector{}}))
	require.NoError(t, r.Register(&CollectorDescriptor{Source: "a", Market: "swap", DataType: "kline", Collector: &fakeCollector{}}))
	require.NoError(t, r.Register(&CollectorDescriptor{Source: "a", Market: "spot", DataType: "symbol", Collector: &fakeCollector{}}))

	types := r.GetDataTypes()
	assert.ElementsMatch(t, []string{"kline", "symbol"}, types)
}

func TestCollectorRegistry_GetRegistry(t *testing.T) {
	assert.NotNil(t, GetRegistry())
}

func TestCollectorBuilder_Register_ShouldDefaultMarketToSpot(t *testing.T) {
	r := &CollectorRegistry{collectors: make(map[string]*CollectorDescriptor)}
	orig := globalRegistry
	globalRegistry = r
	defer func() { globalRegistry = orig }()

	err := NewBuilder().
		Source("builder", "构建器").
		DataType("symbol", "标的").
		Collector(&fakeCollector{source: "builder", dataType: "symbol"}).
		Register()
	require.NoError(t, err)

	collector, err := r.Get("builder", "", "symbol")
	require.NoError(t, err)
	assert.Equal(t, "builder", collector.Source())
}

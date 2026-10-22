package localdata

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// Reset 丢弃内存里的数据源：窗口不再就绪、数据源表为空，常驻内存登记被释放。
func TestResetDropsEverySourceAndReleasesResidency(t *testing.T) {
	d := New(Config{})
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "ds", Freq: "1m"}
	st := &source{}
	st.ready.Store(true)
	d.sources[src] = st
	require.Len(t, d.sources, 1)

	d.Reset()
	require.False(t, st.ready.Load(), "数据源不再就绪")
	require.Empty(t, d.sources)
	d.Reset() // 重复执行无副作用
}

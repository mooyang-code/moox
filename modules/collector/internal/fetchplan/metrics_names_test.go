package fetchplan

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// nameRecorder 记下注册进来的全部指标名（不管有没有样本）。
type nameRecorder struct{ names []string }

var descName = regexp.MustCompile(`fqName: "([^"]+)"`)

func (r *nameRecorder) Register(collector prometheus.Collector) error {
	descs := make(chan *prometheus.Desc, 16)
	go func() {
		collector.Describe(descs)
		close(descs)
	}()
	for desc := range descs {
		if match := descName.FindStringSubmatch(desc.String()); match != nil {
			r.names = append(r.names, match[1])
		}
	}
	return nil
}

func (r *nameRecorder) MustRegister(collectors ...prometheus.Collector) {
	for _, collector := range collectors {
		_ = r.Register(collector)
	}
}

func (r *nameRecorder) Unregister(prometheus.Collector) bool { return true }

// Collector 主进程注册的采集指标名与拆包之前记录的相同（记录文件是在 marketfetch 拆成执行面、控制面之前生成的）。
// 有意增删指标时设 MOOX_UPDATE_GOLDEN=1 重新生成。
func TestControlMetricNamesMatchBaseline(t *testing.T) {
	recorder := &nameRecorder{}
	NewControlMetrics(recorder)
	NewStreamMetrics(recorder)
	sort.Strings(recorder.names)
	got := strings.Join(recorder.names, "\n") + "\n"
	path := filepath.Join("testdata", "control_metric_names.txt")
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(want), got)
}

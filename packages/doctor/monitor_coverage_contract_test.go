package doctor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 监控覆盖契约：Doctor 使用共享的组件目录（packages/servicecatalog/catalog.yaml），Monitor 的数据集健康策略
// （config/setup/dataset-health-policy.yaml）用版本 2 且只含必需的默认值，Collector 与因子模块都登记了数据集指标清单。
// 这些原先由 scripts/test/contract/test-monitor-coverage-contract.sh 里的 Python 检查，现在是普通的 Go 测试。

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

type coverageComponent struct {
	ID     string `yaml:"id"`
	Doctor struct {
		Transport    string   `yaml:"transport"`
		Dependencies []string `yaml:"dependencies"`
	} `yaml:"doctor"`
	Health struct {
		Kind     string `yaml:"kind"`
		Port     int    `yaml:"port"`
		Loopback *bool  `yaml:"loopback"`
	} `yaml:"health"`
}

func TestMonitorCoverageComponentCatalog(t *testing.T) {
	root := repoRoot(t)
	var catalog struct {
		Components []coverageComponent `yaml:"components"`
	}
	if err := yaml.Unmarshal(readFile(t, filepath.Join(root, "packages/servicecatalog/catalog.yaml")), &catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "packages/doctor/components.yaml")); err == nil {
		t.Fatal("Doctor must use the shared component catalog")
	}

	components := map[string]coverageComponent{}
	for _, component := range catalog.Components {
		components[component.ID] = component
	}
	for name, component := range components {
		if !contains([]string{"reporter", "host_snapshot", "health_only"}, component.Doctor.Transport) {
			t.Errorf("%s: unsupported transport %q", name, component.Doctor.Transport)
		}
		for _, dependency := range component.Doctor.Dependencies {
			if _, ok := components[dependency]; !ok {
				t.Errorf("%s: unknown component dependency %q", name, dependency)
			}
		}
		if !contains([]string{"readyz", "https", "none"}, component.Health.Kind) {
			t.Errorf("%s: unsupported health kind %q", name, component.Health.Kind)
		}
	}
	proxy, ok := components["console-proxy"]
	if !ok {
		t.Fatal("console-proxy component is missing")
	}
	if proxy.Doctor.Transport != "health_only" || proxy.Health.Kind != "readyz" || proxy.Health.Port != 19528 || proxy.Health.Loopback == nil || !*proxy.Health.Loopback {
		t.Error("console-proxy diagnostics contract changed")
	}
}

func TestMonitorCoverageDatasetHealthPolicy(t *testing.T) {
	root := repoRoot(t)
	text := string(readFile(t, filepath.Join(root, "config/setup/dataset-health-policy.yaml")))
	var policy struct {
		Version            int `yaml:"version"`
		RealtimeTimeseries struct {
			Defaults map[string]any `yaml:"defaults"`
		} `yaml:"realtime_timeseries"`
	}
	if err := yaml.Unmarshal([]byte(text), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Version != 2 {
		t.Errorf("monitor policy must use version 2, got %d", policy.Version)
	}
	if strings.Contains(text, "crosses_storage_deferred") {
		t.Error("monitor policy still contains crosses_storage_deferred")
	}
	for _, removed := range []string{"canary_subject_id", "market_price_change_ratio", "market_volume_ratio"} {
		if strings.Contains(text, removed) {
			t.Errorf("dataset health policy contains unused field %s", removed)
		}
	}
	var got []string
	for key := range policy.RealtimeTimeseries.Defaults {
		got = append(got, key)
	}
	sort.Strings(got)
	if want := "hour_lateness,minute_lateness,run_missed_intervals,success_missed_intervals"; strings.Join(got, ",") != want {
		t.Errorf("monitor policy defaults mismatch: got=%v", got)
	}
}

func TestMonitorCoverageDatasetMetricsRegistered(t *testing.T) {
	root := repoRoot(t)
	for _, item := range []struct{ module, registration, inventory, constructor string }{
		{"collector", "internal/bootstrap/bootstrap.go", "internal/observability/realtime_inventory.go", "NewRealtimeInventory"},
		{"factor", "internal/bootstrap/metrics_reporter.go", "internal/bootstrap/dataset_observer.go", "newFactorDatasetObserver"},
	} {
		bootstrap := string(readFile(t, filepath.Join(root, "modules", item.module, item.registration)))
		inventory := string(readFile(t, filepath.Join(root, "modules", item.module, item.inventory)))
		if !strings.Contains(bootstrap, "NewDatasetMetrics") || !strings.Contains(bootstrap, item.constructor) {
			t.Errorf("%s: DatasetMetrics inventory is not registered", item.module)
		}
		if !strings.Contains(inventory, "ReplaceExpected") {
			t.Errorf("%s: realtime inventory does not replace expected datasets", item.module)
		}
	}
}

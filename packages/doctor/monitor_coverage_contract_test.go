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
// （modules/monitor/config/app.yaml 的 metrics.dataset_health）只含必需的默认值，Collector 与因子模块都登记了数据集指标清单。

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
		Port int `yaml:"port"`
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
		if !contains([]string{"reporter", "host_snapshot", "delegated"}, component.Doctor.Transport) {
			t.Errorf("%s: unsupported transport %q", name, component.Doctor.Transport)
		}
		for _, dependency := range component.Doctor.Dependencies {
			if _, ok := components[dependency]; !ok {
				t.Errorf("%s: unknown component dependency %q", name, dependency)
			}
		}
		if component.Health.Port <= 0 {
			t.Errorf("%s: missing health port", name)
		}
	}
	admin, ok := components["admin"]
	if !ok {
		t.Fatal("admin component is missing")
	}
	if admin.Doctor.Transport != "reporter" || admin.Health.Port != 11010 {
		t.Error("admin diagnostics contract changed")
	}
}

func TestMonitorCoverageDatasetHealthPolicy(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "config/setup/dataset-health-policy.yaml")); err == nil {
		t.Fatal("独立的 dataset-health-policy.yaml 应已并入 Monitor 的 app.yaml")
	}
	text := string(readFile(t, filepath.Join(root, "modules/monitor/config/app.yaml")))
	var app struct {
		Metrics struct {
			DatasetHealth struct {
				Defaults map[string]any `yaml:"defaults"`
			} `yaml:"dataset_health"`
		} `yaml:"metrics"`
	}
	if err := yaml.Unmarshal([]byte(text), &app); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"canary_subject_id", "market_price_change_ratio", "market_volume_ratio"} {
		if strings.Contains(text, removed) {
			t.Errorf("Monitor 配置包含已废弃字段 %s", removed)
		}
	}
	var got []string
	for key := range app.Metrics.DatasetHealth.Defaults {
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

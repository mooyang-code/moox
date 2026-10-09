package doctor

import (
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

func TestEmbeddedManifestFollowsCatalog(t *testing.T) {
	t.Parallel()

	manifest, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	catalog := servicecatalog.Default()
	if manifest.Checksum == "" || manifest.Checksum != catalog.Checksum() {
		t.Fatalf("清单校验和 %q 应当等于组件目录的 %q", manifest.Checksum, catalog.Checksum())
	}
	if len(manifest.Components) != len(catalog.Components) {
		t.Fatalf("清单有 %d 个组件，组件目录有 %d 个", len(manifest.Components), len(catalog.Components))
	}
	collector, ok := manifest.Component("collector")
	if !ok {
		t.Fatal("清单缺少 collector")
	}
	if collector.Name != "行情采集服务" {
		t.Fatalf("组件名称应当取自组件目录: %+v", collector)
	}
	if collector.Transport != servicecatalog.TransportReporter || collector.Functional != servicecatalog.FunctionalActive ||
		collector.HealthPath != "/readyz" || collector.HealthPort != 11412 {
		t.Fatalf("collector = %+v", collector)
	}
	proxy, ok := manifest.Component("console-proxy")
	if !ok || proxy.HealthKind != servicecatalog.HealthHTTPS || proxy.HealthPath != "" {
		t.Fatalf("控制台代理用 https 探测，没有 /readyz: %+v", proxy)
	}
	for _, component := range manifest.Components {
		if (component.ComponentID == "storage-primary" || component.ComponentID == "storage-view" || component.ComponentID == "storage-node") &&
			component.Functional != servicecatalog.FunctionalDeferred {
			t.Fatalf("Storage 的业务进度暂未接入: %+v", component)
		}
	}
}

func TestManifestChecksumStable(t *testing.T) {
	t.Parallel()

	one, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	two, err := LoadEmbeddedManifest()
	if err != nil {
		t.Fatal(err)
	}
	if one.Checksum == "" || one.Checksum != two.Checksum {
		t.Fatalf("checksums %q and %q", one.Checksum, two.Checksum)
	}
}

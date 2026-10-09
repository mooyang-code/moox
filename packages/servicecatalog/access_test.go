package servicecatalog

import (
	"strings"
	"testing"
)

func TestAccessEndpointForRegion(t *testing.T) {
	compiled, err := Default().Compile(productionDeployment())
	if err != nil {
		t.Fatal(err)
	}
	directory := compiled.Directory
	cases := []struct {
		region string
		want   AccessEndpoint
	}{
		{"ap-nanjing", AccessEndpoint{Address: "10.206.0.5:11004", ID: "access@storage", HostID: "storage", Private: true}},
		{"AP-HONGKONG", AccessEndpoint{Address: "172.19.32.13:11004", ID: "access@compute-1", HostID: "compute-1", Private: true}},
		{"ap-guangzhou", AccessEndpoint{Address: "146.56.196.204:11004", ID: "access@storage", HostID: "storage"}},
		{"", AccessEndpoint{Address: "146.56.196.204:11004", ID: "access@storage", HostID: "storage"}},
	}
	for _, tc := range cases {
		got, err := directory.AccessEndpointForRegion(tc.region, AccessFallbackHostID)
		if err != nil {
			t.Fatalf("地域 %q: %v", tc.region, err)
		}
		if got != tc.want {
			t.Fatalf("地域 %q 选到 %+v，期望 %+v", tc.region, got, tc.want)
		}
	}
}

func TestAccessEndpointForRegionRequiresFallbackAccess(t *testing.T) {
	deployment := productionDeployment()
	for index := range deployment.Placements {
		placement := &deployment.Placements[index]
		if placement.HostID == "storage" && placement.ComponentID == AccessComponentID {
			placement.Enabled = false
		}
	}
	compiled, err := Default().Compile(deployment)
	if err != nil {
		t.Fatal(err)
	}
	// 同地域有外部接入时不需要兜底主机。
	if got, err := compiled.Directory.AccessEndpointForRegion("ap-hongkong", AccessFallbackHostID); err != nil || got.ID != "access@compute-1" {
		t.Fatalf("香港应选 compute-1 的外部接入: %+v, %v", got, err)
	}
	_, err = compiled.Directory.AccessEndpointForRegion("ap-nanjing", AccessFallbackHostID)
	if err == nil || !strings.Contains(err.Error(), "兜底主机 storage 上也没有启用的外部接入") {
		t.Fatalf("兜底主机没有外部接入时应报错，实际 %v", err)
	}
}

func TestAccessPortMatchesCatalog(t *testing.T) {
	component, ok := Default().Component(AccessComponentID)
	if !ok || len(component.Ports) != 1 || component.Ports[0].Port != AccessPort {
		t.Fatalf("组件目录中外部接入的端口必须是 %d", AccessPort)
	}
}

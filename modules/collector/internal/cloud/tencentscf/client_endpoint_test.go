package tencentscf

import "testing"

func TestSCFEndpointForRegion(t *testing.T) {
	if scfRequestTimeoutSeconds != 960 {
		t.Fatalf("SCF provider request timeout = %d, want 960 seconds", scfRequestTimeoutSeconds)
	}
	if got := scfEndpoint("ap-guangzhou"); got != "scf.tencentcloudapi.com" {
		t.Fatalf("standard endpoint = %q", got)
	}
	if got := scfEndpoint("ap-shanghai-fsi"); got != "scf.ap-shanghai-fsi.tencentcloudapi.com" {
		t.Fatalf("financial endpoint = %q", got)
	}
}

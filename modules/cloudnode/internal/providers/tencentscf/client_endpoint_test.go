package tencentscf

import "testing"

func TestSCFEndpointForRegion(t *testing.T) {
	if got := scfEndpoint("ap-guangzhou"); got != "scf.tencentcloudapi.com" {
		t.Fatalf("standard endpoint = %q", got)
	}
	if got := scfEndpoint("ap-shanghai-fsi"); got != "scf.ap-shanghai-fsi.tencentcloudapi.com" {
		t.Fatalf("financial endpoint = %q", got)
	}
}

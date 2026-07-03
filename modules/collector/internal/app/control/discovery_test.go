package control

import "testing"

func TestEndpointTRPCTargetIgnoresHTTPStorageRows(t *testing.T) {
	items := map[string]endpoint{
		"storage_metadata": {
			ServiceName: "storage_metadata",
			Protocol:    "http",
			Host:        "10.0.0.2",
			Port:        20200,
			BaseURL:     "http://10.0.0.2:20200",
			RPCAddress:  "10.0.0.2:20200",
		},
	}

	if got := endpointTRPCTarget(items, "storage_metadata_trpc", "storage_metadata"); got != "" {
		t.Fatalf("endpointTRPCTarget() = %q, want empty for HTTP storage row", got)
	}
}

func TestEndpointTRPCTargetUsesTRPCDeploymentHostPort(t *testing.T) {
	items := map[string]endpoint{
		"storage_metadata_trpc": {
			ServiceName: "storage_metadata_trpc",
			Protocol:    "trpc",
			Host:        "10.0.0.3",
			Port:        20100,
			BaseURL:     "trpc://10.0.0.3:20100",
		},
	}

	if got := endpointTRPCTarget(items, "storage_metadata_trpc"); got != "10.0.0.3:20100" {
		t.Fatalf("endpointTRPCTarget() = %q, want host:port", got)
	}
}

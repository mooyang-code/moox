package proto

import (
	"os"
	"regexp"
	"testing"
)

func TestMonitorPublicRPCSurface(t *testing.T) {
	data, err := os.ReadFile("monitor.proto")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`rpc\s+(\w+)\s*\(`)
	allowed := map[string]bool{"GetHealthOverview": true, "GetNotificationChannel": true, "UpdateNotificationChannel": true, "ListHostAgents": true, "QueryHostMetricHistory": true, "GetDoctorContext": true}
	for _, match := range re.FindAllSubmatch(data, -1) {
		name := string(match[1])
		if !allowed[name] {
			t.Errorf("public RPC %s is not allowed", name)
		}
	}
}

package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestURL_BuildsGatewayEndpoint(t *testing.T) {
	got := URL("127.0.0.1", 8080, "collector", "GetTaskRuleList")
	assert.Equal(t, "http://127.0.0.1:8080/api/service/collector/GetTaskRuleList", got)
}

func TestServiceURL_NormalizesGatewayTarget(t *testing.T) {
	got := ServiceURL("http://gateway:9000/", "collector", "ReportInstanceStatus")
	assert.Equal(t, "http://gateway:9000/api/service/collector/ReportInstanceStatus", got)
}

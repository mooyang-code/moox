package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceURLNormalizesGatewayTarget(t *testing.T) {
	got := ServiceURL("http://gateway:9000/", "collector", "ReportInstanceStatus")
	assert.Equal(t, "http://gateway:9000/api/service/collector/ReportInstanceStatus", got)
}

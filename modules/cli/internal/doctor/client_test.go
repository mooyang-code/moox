package doctor

import (
	"context"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/stretchr/testify/require"
)

func TestNilClientFailsClosed(t *testing.T) {
	var client *Client
	_, err := client.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{})
	require.ErrorContains(t, err, "unavailable")
	_, err = client.ListPlacements(context.Background(), "node-a")
	require.ErrorContains(t, err, "不可用")
}
